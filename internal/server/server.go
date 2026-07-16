// Package server implements the Unix-socket HTTP API.
//
// All error responses are structured and redacted: they never contain SMTP
// credentials, SMTP transcripts, resolved email addresses, or file paths.
// Logs carry request IDs, aliases, states, and sanitized codes only.
package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/kadel/allowmaild/internal/config"
	"github.com/kadel/allowmaild/internal/mailmsg"
	"github.com/kadel/allowmaild/internal/smtpclient"
	"github.com/kadel/allowmaild/internal/store"
)

const maxIdempotencyKeyBytes = 200

// SendFunc performs one SMTP delivery attempt.
type SendFunc func(from, to string, msg []byte) smtpclient.Result

type Server struct {
	cfg    *config.Config
	store  *store.Store
	send   SendFunc
	log    *slog.Logger
	now    func() time.Time
}

func New(cfg *config.Config, st *store.Store, send SendFunc, log *slog.Logger) *Server {
	return &Server{cfg: cfg, store: st, send: send, log: log, now: time.Now}
}

// SetNow overrides the clock (tests only).
func (s *Server) SetNow(now func() time.Time) { s.now = now }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.handleHealth)
	mux.HandleFunc("POST /v1/send", s.handleSend)
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// sendRequest is the exact request schema; any other property is rejected.
type sendRequest struct {
	Recipient      string `json:"recipient"`
	Subject        string `json:"subject"`
	Text           string `json:"text"`
	IdempotencyKey string `json:"idempotency_key"`
}

type errorBody struct {
	Code            string   `json:"code"`
	Message         string   `json:"message"`
	ValidRecipients []string `json:"valid_recipients,omitempty"`
}

type errorResponse struct {
	Error errorBody `json:"error"`
}

// terminalResponse reports a request that reached a terminal state. Duplicate
// keys replay it verbatim from the stored row.
type terminalResponse struct {
	RequestID  string `json:"request_id"`
	Status     string `json:"status"`
	Recipient  string `json:"recipient"`
	MessageID  string `json:"message_id,omitempty"`
	ResultCode string `json:"result_code,omitempty"`
	Detail     string `json:"detail"`
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	req, errMsg := s.decodeSend(w, r)
	if errMsg != "" {
		s.writeError(w, http.StatusBadRequest, "validation", errMsg)
		return
	}
	if msg := s.validateFields(req); msg != "" {
		s.writeError(w, http.StatusBadRequest, "validation", msg)
		return
	}

	recipient, ok := s.cfg.Recipients[req.Recipient]
	if !ok {
		s.writeUnknownRecipient(w)
		return
	}

	subjectHash := sha256hex(req.Subject)
	bodyHash := sha256hex(req.Text)
	requestID := newRequestID()
	now := s.now()

	limits := store.RateLimits{
		GlobalPerHour: s.cfg.Limits.PerHour,
		GlobalPerDay:  s.cfg.Limits.PerDay,
	}
	if recipient.Limits != nil {
		limits.RecipientPerHour = recipient.Limits.PerHour
		limits.RecipientPerDay = recipient.Limits.PerDay
	}

	res, err := s.store.Reserve(r.Context(), store.ReserveParams{
		Key:         req.IdempotencyKey,
		RequestID:   requestID,
		Alias:       req.Recipient,
		SubjectHash: subjectHash,
		BodyHash:    bodyHash,
		Now:         now,
		Limits:      limits,
	})
	if err != nil {
		// Fail closed: if limits or duplicates cannot be evaluated, no send.
		s.log.Error("reserve failed", "request_id", requestID, "alias", req.Recipient)
		s.writeError(w, http.StatusServiceUnavailable, "unavailable", "request store unavailable; request rejected")
		return
	}

	switch res.Kind {
	case store.RateLimited:
		s.log.Info("rate limited", "request_id", requestID, "alias", req.Recipient, "limit", res.Limit)
		s.writeError(w, http.StatusTooManyRequests, "rate_limited",
			"rate limit exceeded: "+res.Limit)
		return
	case store.KeyReuse:
		s.writeError(w, http.StatusConflict, "key_reuse",
			"idempotency_key was already used with different content")
		return
	case store.InFlight:
		s.writeError(w, http.StatusConflict, "in_flight",
			"a request with this idempotency_key is still in progress")
		return
	case store.Replay:
		s.log.Info("replayed", "request_id", res.Existing.RequestID, "alias", res.Existing.Alias,
			"state", res.Existing.State)
		writeJSON(w, http.StatusOK, responseFromRow(res.Existing))
		return
	}

	// Reserved: build the message and make the single SMTP attempt.
	msg, msgID := mailmsg.Build(mailmsg.Input{
		FromAddress: s.cfg.Sender.Address,
		FromName:    s.cfg.Sender.Name,
		ToAddress:   recipient.Address,
		Subject:     req.Subject,
		Body:        req.Text,
		Date:        now,
	})
	result := s.send(s.cfg.Sender.Address, recipient.Address, msg)

	storedMsgID := ""
	if result.Outcome == smtpclient.OutcomeSent {
		storedMsgID = msgID
	}
	if err := s.store.Complete(r.Context(), req.IdempotencyKey, string(result.Outcome),
		result.Code, storedMsgID, s.now()); err != nil {
		// The attempt already happened; report its true outcome. The row
		// stays "sending" and the startup sweep will mark it ambiguous.
		s.log.Error("persisting terminal state failed", "request_id", requestID,
			"alias", req.Recipient, "state", result.Outcome, "code", result.Code)
	}
	s.log.Info("send attempted", "request_id", requestID, "alias", req.Recipient,
		"state", result.Outcome, "code", result.Code)

	writeJSON(w, http.StatusOK, responseFromRow(&store.Row{
		RequestID:  requestID,
		Alias:      req.Recipient,
		State:      string(result.Outcome),
		ResultCode: result.Code,
		MessageID:  storedMsgID,
	}))
}

func responseFromRow(row *store.Row) terminalResponse {
	detail := ""
	switch row.State {
	case store.StateSent:
		detail = "message accepted by the SMTP server"
	case store.StateFailed:
		detail = "delivery failed before the message was accepted; safe to retry with a new idempotency_key"
	case store.StateAmbiguous:
		detail = "delivery uncertain: the message may or may not have been delivered; do not retry automatically"
	}
	return terminalResponse{
		RequestID:  row.RequestID,
		Status:     row.State,
		Recipient:  row.Alias,
		MessageID:  row.MessageID,
		ResultCode: row.ResultCode,
		Detail:     detail,
	}
}

// decodeSend strictly decodes the request body: unknown properties, type
// mismatches, trailing data, and oversized bodies are all validation errors.
func (s *Server) decodeSend(w http.ResponseWriter, r *http.Request) (sendRequest, string) {
	var req sendRequest
	// JSON string escaping can inflate content ~6x; anything larger cannot
	// be a valid request.
	maxLen := int64(6*(s.cfg.Limits.MaxBodyBytes+s.cfg.Limits.MaxSubjectBytes) + 4096)
	body := http.MaxBytesReader(w, r.Body, maxLen)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return req, "request body too large"
		}
		return req, "request body is not a valid send request: " + jsonErrorDetail(err)
	}
	if dec.More() {
		return req, "request body contains trailing data"
	}
	return req, ""
}

// jsonErrorDetail returns a safe description of a JSON decode error without
// echoing request content.
func jsonErrorDetail(err error) string {
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.As(err, &typeErr):
		if typeErr.Field != "" {
			return "field " + typeErr.Field + " has the wrong type"
		}
		return "wrong JSON type"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "empty or truncated JSON"
	default:
		// Unknown-field errors name only the offending property.
		msg := err.Error()
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return msg
	}
}

func (s *Server) validateFields(req sendRequest) string {
	if req.Recipient == "" {
		return "recipient is required"
	}
	if req.Subject == "" {
		return "subject is required"
	}
	if req.Text == "" {
		return "text is required"
	}
	if req.IdempotencyKey == "" {
		return "idempotency_key is required"
	}
	if len(req.IdempotencyKey) > maxIdempotencyKeyBytes {
		return "idempotency_key exceeds the maximum length"
	}
	if !utf8.ValidString(req.Subject) || !utf8.ValidString(req.Text) ||
		!utf8.ValidString(req.IdempotencyKey) || !utf8.ValidString(req.Recipient) {
		return "fields must be valid UTF-8"
	}
	if hasControl(req.IdempotencyKey, false) {
		return "idempotency_key must not contain control characters"
	}
	if len(req.Subject) > s.cfg.Limits.MaxSubjectBytes {
		return "subject exceeds max_subject_bytes"
	}
	if hasControl(req.Subject, false) {
		return "subject must not contain control characters"
	}
	if len(req.Text) > s.cfg.Limits.MaxBodyBytes {
		return "text exceeds max_body_bytes"
	}
	if hasControl(req.Text, true) {
		return "text must not contain control characters other than line feed"
	}
	return ""
}

// hasControl reports whether v contains control characters; allowLF permits
// line feeds (body text) while everything else, including CR, stays banned.
func hasControl(v string, allowLF bool) bool {
	for _, r := range v {
		if r == '\n' && allowLF {
			continue
		}
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func (s *Server) writeUnknownRecipient(w http.ResponseWriter) {
	writeJSON(w, http.StatusBadRequest, errorResponse{Error: errorBody{
		Code:            "unknown_recipient",
		Message:         "unknown recipient alias; recipient must exactly match a configured alias",
		ValidRecipients: s.cfg.AliasNames(),
	}})
}

func (s *Server) writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Error: errorBody{Code: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func sha256hex(v string) string {
	h := sha256.Sum256([]byte(v))
	return hex.EncodeToString(h[:])
}

func newRequestID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf[:])
}
