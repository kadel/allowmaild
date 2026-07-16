package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kadel/allowmaild/internal/config"
	"github.com/kadel/allowmaild/internal/smtpclient"
	"github.com/kadel/allowmaild/internal/store"
)

type sentCall struct {
	from string
	to   string
	msg  []byte
}

// stubSender records delivery attempts and returns a scripted result.
type stubSender struct {
	mu     sync.Mutex
	calls  []sentCall
	result smtpclient.Result
	block  chan struct{} // when non-nil, Send blocks until closed
	entered chan struct{}
}

func (s *stubSender) send(from, to string, msg []byte) smtpclient.Result {
	s.mu.Lock()
	s.calls = append(s.calls, sentCall{from, to, msg})
	block := s.block
	entered := s.entered
	s.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if block != nil {
		<-block
	}
	return s.result
}

func (s *stubSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func testConfig() *config.Config {
	return &config.Config{
		Sender: config.Sender{Address: "sender@example.test", Name: "Sender Name"},
		Recipients: map[string]config.Recipient{
			"self-gmail": {Address: "dest@example.test"},
			"work":       {Address: "work@example.test", Limits: &config.RecipientLimits{PerHour: 1, PerDay: 5}},
		},
		Limits: config.Limits{PerHour: 100, PerDay: 200, MaxSubjectBytes: 200, MaxBodyBytes: 10000},
	}
}

func newTestServer(t *testing.T, cfg *config.Config, stub *stubSender) (http.Handler, *store.Store, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(cfg, st, stub.send, log).Handler(), st, dbPath
}

func post(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/v1/send", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func validBody(key string) string {
	return fmt.Sprintf(`{"recipient":"self-gmail","subject":"Hello","text":"A body line\n","idempotency_key":%q}`, key)
}

func sentStub() *stubSender {
	return &stubSender{result: smtpclient.Result{Outcome: smtpclient.OutcomeSent, Code: "250"}}
}

func TestValidSend(t *testing.T) {
	stub := sentStub()
	h, _, _ := newTestServer(t, testConfig(), stub)

	w := post(t, h, validBody("k1"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["status"] != "sent" || resp["recipient"] != "self-gmail" {
		t.Fatalf("response = %v", resp)
	}
	if resp["request_id"] == "" || resp["message_id"] == "" {
		t.Fatalf("missing ids: %v", resp)
	}

	if stub.count() != 1 {
		t.Fatalf("send calls = %d, want 1", stub.count())
	}
	call := stub.calls[0]
	if call.from != "sender@example.test" || call.to != "dest@example.test" {
		t.Fatalf("envelope = %s -> %s", call.from, call.to)
	}
}

func TestUnknownPropertyRejected(t *testing.T) {
	stub := sentStub()
	h, st, _ := newTestServer(t, testConfig(), stub)

	for _, body := range []string{
		`{"recipient":"self-gmail","subject":"s","text":"t","idempotency_key":"k","cc":"x@example.com"}`,
		`{"recipient":"self-gmail","subject":"s","text":"t","idempotency_key":"k","to":"x@example.com"}`,
		`{"recipient":"self-gmail","subject":"s","text":"t","idempotency_key":"k","from":"x@example.com"}`,
		`{"recipient":"self-gmail","subject":"s","text":"t","idempotency_key":"k","headers":{"Bcc":"x"}}`,
		`{"recipient":"self-gmail","subject":"s","text":"t","idempotency_key":"k","html":"<b>x</b>"}`,
	} {
		w := post(t, h, body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("body %s: status = %d, want 400", body, w.Code)
		}
	}
	if stub.count() != 0 {
		t.Fatalf("send was attempted for rejected request")
	}
	if _, err := st.Get(t.Context(), "k"); err == nil {
		t.Fatal("request row was created for rejected request")
	}
}

func TestUnknownAliasListsAliasesNotAddresses(t *testing.T) {
	h, _, _ := newTestServer(t, testConfig(), sentStub())

	for _, alias := range []string{"SELF-GMAIL", "self-Gmail", "unknown", "self-gmail,attacker@example.com", "self-gmail attacker@example.com"} {
		body := fmt.Sprintf(`{"recipient":%q,"subject":"s","text":"t","idempotency_key":"k"}`, alias)
		w := post(t, h, body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("alias %q: status = %d, want 400", alias, w.Code)
		}
		out := w.Body.String()
		if !strings.Contains(out, "self-gmail") {
			t.Errorf("alias %q: error does not name valid aliases: %s", alias, out)
		}
		if strings.Contains(out, "@example.test") || strings.Contains(out, "dest@") {
			t.Errorf("alias %q: error leaks an address: %s", alias, out)
		}
	}
}

func TestRecipientArrayRejected(t *testing.T) {
	h, _, _ := newTestServer(t, testConfig(), sentStub())
	w := post(t, h, `{"recipient":["self-gmail"],"subject":"s","text":"t","idempotency_key":"k"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestMissingAndEmptyFields(t *testing.T) {
	h, _, _ := newTestServer(t, testConfig(), sentStub())
	cases := []string{
		`{}`,
		`{"recipient":"self-gmail"}`,
		`{"recipient":"","subject":"s","text":"t","idempotency_key":"k"}`,
		`{"recipient":"self-gmail","subject":"","text":"t","idempotency_key":"k"}`,
		`{"recipient":"self-gmail","subject":"s","text":"","idempotency_key":"k"}`,
		`{"recipient":"self-gmail","subject":"s","text":"t","idempotency_key":""}`,
		``,
		`not json`,
	}
	for _, body := range cases {
		if w := post(t, h, body); w.Code != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", body, w.Code)
		}
	}
}

func TestByteLimits(t *testing.T) {
	h, _, _ := newTestServer(t, testConfig(), sentStub())

	big := strings.Repeat("a", 201)
	w := post(t, h, fmt.Sprintf(`{"recipient":"self-gmail","subject":%q,"text":"t","idempotency_key":"k"}`, big))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "max_subject_bytes") {
		t.Fatalf("oversized subject: status=%d body=%s", w.Code, w.Body.String())
	}

	bigText := strings.Repeat("a", 10001)
	w = post(t, h, fmt.Sprintf(`{"recipient":"self-gmail","subject":"s","text":%q,"idempotency_key":"k"}`, bigText))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "max_body_bytes") {
		t.Fatalf("oversized text: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestControlCharacters(t *testing.T) {
	h, _, _ := newTestServer(t, testConfig(), sentStub())

	reject := []struct{ field, value string }{
		{"subject", "a\rb"}, {"subject", "a\nb"}, {"subject", "a\x00b"},
		{"subject", "a\tb"}, {"subject", "a\x7fb"}, {"subject", "a\u0085b"},
		{"text", "a\rb"}, {"text", "a\tb"}, {"text", "a\x0bb"},
		{"idempotency_key", "a\nb"},
	}
	for _, c := range reject {
		m := map[string]string{"recipient": "self-gmail", "subject": "s", "text": "t", "idempotency_key": "k"}
		m[c.field] = c.value
		body, _ := json.Marshal(m)
		if w := post(t, h, string(body)); w.Code != http.StatusBadRequest {
			t.Errorf("%s=%q: status = %d, want 400", c.field, c.value, w.Code)
		}
	}

	// LF in the body text is allowed.
	if w := post(t, h, validBody("lf-ok")); w.Code != http.StatusOK {
		t.Fatalf("LF body rejected: %d %s", w.Code, w.Body.String())
	}
}

func TestDuplicateKeyReplaysVerbatim(t *testing.T) {
	stub := sentStub()
	h, _, _ := newTestServer(t, testConfig(), stub)

	first := post(t, h, validBody("dup"))
	second := post(t, h, validBody("dup"))
	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("codes = %d, %d", first.Code, second.Code)
	}
	if !bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) {
		t.Fatalf("replay differs:\n%s\n%s", first.Body.String(), second.Body.String())
	}
	if stub.count() != 1 {
		t.Fatalf("send calls = %d, want 1", stub.count())
	}
}

func TestReplayForFailedAndAmbiguous(t *testing.T) {
	for _, outcome := range []smtpclient.Outcome{smtpclient.OutcomeFailed, smtpclient.OutcomeAmbiguous} {
		t.Run(string(outcome), func(t *testing.T) {
			stub := &stubSender{result: smtpclient.Result{Outcome: outcome, Code: "x"}}
			h, _, _ := newTestServer(t, testConfig(), stub)

			first := post(t, h, validBody("k"))
			second := post(t, h, validBody("k"))
			if !bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) {
				t.Fatalf("replay differs")
			}
			if stub.count() != 1 {
				t.Fatalf("send calls = %d, want 1 (no auto-retry)", stub.count())
			}
			var resp map[string]any
			json.Unmarshal(first.Body.Bytes(), &resp)
			if resp["status"] != string(outcome) {
				t.Fatalf("status = %v, want %s", resp["status"], outcome)
			}
		})
	}
}

func TestKeyReuseWithDifferentContentRejected(t *testing.T) {
	stub := sentStub()
	h, _, _ := newTestServer(t, testConfig(), stub)

	post(t, h, validBody("k"))
	w := post(t, h, `{"recipient":"self-gmail","subject":"Different","text":"other\n","idempotency_key":"k"}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "key_reuse") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if stub.count() != 1 {
		t.Fatalf("send calls = %d, want 1", stub.count())
	}
}

func TestInvalidRequestDoesNotBurnKey(t *testing.T) {
	h, _, _ := newTestServer(t, testConfig(), sentStub())

	bad := `{"recipient":"self-gmail","subject":"bad\r\nBcc: x","text":"t","idempotency_key":"reuse-me"}`
	if w := post(t, h, bad); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid request status = %d", w.Code)
	}
	if w := post(t, h, validBody("reuse-me")); w.Code != http.StatusOK {
		t.Fatalf("corrected request with same key rejected: %d %s", w.Code, w.Body.String())
	}
}

func TestConcurrentDuplicateGetsInFlightConflict(t *testing.T) {
	stub := sentStub()
	stub.block = make(chan struct{})
	stub.entered = make(chan struct{}, 1)
	h, _, _ := newTestServer(t, testConfig(), stub)

	done := make(chan *httptest.ResponseRecorder)
	go func() { done <- post(t, h, validBody("k")) }()
	<-stub.entered // first request is now mid-SMTP

	w := post(t, h, validBody("k"))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "in_flight") {
		t.Fatalf("duplicate while sending: status=%d body=%s", w.Code, w.Body.String())
	}

	close(stub.block)
	first := <-done
	if first.Code != http.StatusOK {
		t.Fatalf("original request failed: %d", first.Code)
	}
	if stub.count() != 1 {
		t.Fatalf("send calls = %d, want 1", stub.count())
	}
}

func TestRateLimitBoundaryAndRejectionsNotCounted(t *testing.T) {
	cfg := testConfig()
	cfg.Limits.PerHour = 2
	stub := sentStub()
	h, _, _ := newTestServer(t, cfg, stub)

	// Rejected requests must not consume budget.
	for i := 0; i < 5; i++ {
		post(t, h, `{"recipient":"nope","subject":"s","text":"t","idempotency_key":"x"}`)
	}

	if w := post(t, h, validBody("a")); w.Code != http.StatusOK {
		t.Fatalf("first send: %d", w.Code)
	}
	if w := post(t, h, validBody("b")); w.Code != http.StatusOK {
		t.Fatalf("second send: %d", w.Code)
	}
	w := post(t, h, validBody("c"))
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "per_hour") {
		t.Fatalf("third send: status=%d body=%s", w.Code, w.Body.String())
	}
	if stub.count() != 2 {
		t.Fatalf("send calls = %d, want 2", stub.count())
	}

	// Replays of terminal rows still work while the budget is exhausted.
	if w := post(t, h, validBody("a")); w.Code != http.StatusOK {
		t.Fatalf("replay while limited: %d", w.Code)
	}
}

func TestPerRecipientLimit(t *testing.T) {
	stub := sentStub()
	h, _, _ := newTestServer(t, testConfig(), stub)

	workBody := func(key string) string {
		return fmt.Sprintf(`{"recipient":"work","subject":"s","text":"t","idempotency_key":%q}`, key)
	}
	if w := post(t, h, workBody("w1")); w.Code != http.StatusOK {
		t.Fatalf("first: %d", w.Code)
	}
	w := post(t, h, workBody("w2"))
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "recipient per_hour") {
		t.Fatalf("second: status=%d body=%s", w.Code, w.Body.String())
	}
	// The global budget is untouched for other aliases.
	if w := post(t, h, validBody("g1")); w.Code != http.StatusOK {
		t.Fatalf("other alias blocked: %d", w.Code)
	}
}

func TestFailClosedWhenStoreUnavailable(t *testing.T) {
	stub := sentStub()
	h, st, _ := newTestServer(t, testConfig(), stub)
	st.Close()

	w := post(t, h, validBody("k"))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("send with dead store: status = %d, want 503", w.Code)
	}
	if stub.count() != 0 {
		t.Fatal("SMTP attempt happened with store unavailable")
	}

	req := httptest.NewRequest("GET", "/v1/health", nil)
	hw := httptest.NewRecorder()
	h.ServeHTTP(hw, req)
	if hw.Code != http.StatusServiceUnavailable {
		t.Fatalf("health with dead store: %d", hw.Code)
	}
}

func TestHealthOK(t *testing.T) {
	h, _, _ := newTestServer(t, testConfig(), sentStub())
	req := httptest.NewRequest("GET", "/v1/health", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("health: %d", w.Code)
	}
	out := w.Body.String()
	if strings.Contains(out, "@") || strings.Contains(out, "smtp") {
		t.Fatalf("health leaks details: %s", out)
	}
}

func TestAuditRecordsForAllTerminalStates(t *testing.T) {
	outcomes := map[string]smtpclient.Result{
		"sent":      {Outcome: smtpclient.OutcomeSent, Code: "250"},
		"failed":    {Outcome: smtpclient.OutcomeFailed, Code: "550"},
		"ambiguous": {Outcome: smtpclient.OutcomeAmbiguous, Code: "timeout"},
	}
	for state, result := range outcomes {
		t.Run(state, func(t *testing.T) {
			stub := &stubSender{result: result}
			h, st, _ := newTestServer(t, testConfig(), stub)

			post(t, h, validBody("k"))
			row, err := st.Get(t.Context(), "k")
			if err != nil {
				t.Fatalf("no audit row: %v", err)
			}
			if row.State != state || row.RequestID == "" || row.Alias != "self-gmail" ||
				row.SubjectHash == "" || row.BodyHash == "" || row.ResultCode != result.Code {
				t.Fatalf("audit row = %+v", row)
			}
			if state == "sent" && row.MessageID == "" {
				t.Fatal("sent row missing message id")
			}
			if row.CreatedAt.IsZero() || row.UpdatedAt.IsZero() {
				t.Fatal("audit row missing timestamps")
			}
		})
	}
}

func TestNoPlaintextOrAddressesInDatabase(t *testing.T) {
	stub := sentStub()
	h, st, dbPath := newTestServer(t, testConfig(), stub)

	subject := "unique-subject-marker-zq81x"
	text := "unique-body-marker-vv93k about something private"
	body := fmt.Sprintf(`{"recipient":"self-gmail","subject":%q,"text":%q,"idempotency_key":"k"}`, subject, text)
	if w := post(t, h, body); w.Code != http.StatusOK {
		t.Fatalf("send: %d", w.Code)
	}
	st.Close()

	raw, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{subject, "unique-body-marker", "dest@example.test", "sender@example.test"} {
		if bytes.Contains(raw, []byte(needle)) {
			t.Fatalf("database contains %q", needle)
		}
	}
}
