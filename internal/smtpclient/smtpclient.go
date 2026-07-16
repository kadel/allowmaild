// Package smtpclient makes exactly one SMTP delivery attempt per call.
//
// It is a minimal client built on net/textproto so that each protocol phase
// is explicit: outcome classification (sent / failed / ambiguous) depends on
// whether the failure happened before or after message data was transmitted.
// TLS certificate verification is always on; every read and write carries a
// deadline, and the whole attempt is bounded by an overall deadline.
package smtpclient

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"net"
	"net/textproto"
	"strconv"
	"time"
)

type TLSMode string

const (
	TLSImplicit TLSMode = "implicit"
	TLSStartTLS TLSMode = "starttls"
)

type AuthMethod string

const (
	AuthPlain AuthMethod = "plain"
	AuthLogin AuthMethod = "login"
)

type Config struct {
	Host     string
	Port     int
	TLSMode  TLSMode
	Auth     AuthMethod
	Username string
	Password string
	// HeloName is the name sent with EHLO.
	HeloName string
	// Overall bounds the whole attempt; PerIO bounds each read/write.
	Overall time.Duration
	PerIO   time.Duration
	// TLSConfig optionally overrides the TLS client config (tests inject
	// their own RootCAs). ServerName defaults to Host. Verification is
	// never disabled.
	TLSConfig *tls.Config
}

// Outcome is the terminal classification of one delivery attempt.
type Outcome string

const (
	// OutcomeSent: the server accepted the message after DATA.
	OutcomeSent Outcome = "sent"
	// OutcomeFailed: definitive failure — provably not delivered. Either a
	// failure before DATA, or an explicit rejection reply to the message
	// data.
	OutcomeFailed Outcome = "failed"
	// OutcomeAmbiguous: timeout or connection loss after message data
	// started transmitting — the server may or may not have accepted it.
	OutcomeAmbiguous Outcome = "ambiguous"
)

// Result reports the classified outcome and a sanitized result code: the
// numeric SMTP reply code when the server replied, or one of the symbolic
// codes "timeout", "tls", "conn". It never contains reply text, addresses,
// or credentials.
type Result struct {
	Outcome Outcome
	Code    string
}

// Send performs the single delivery attempt. It never retries.
func Send(cfg Config, from, to string, msg []byte) Result {
	deadline := time.Now().Add(cfg.Overall)

	raw, err := net.DialTimeout("tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)), cfg.PerIO)
	if err != nil {
		return classify(err, false)
	}
	defer raw.Close()
	conn := &deadlineConn{Conn: raw, perIO: cfg.PerIO, overall: deadline}

	tcfg := &tls.Config{ServerName: cfg.Host}
	if cfg.TLSConfig != nil {
		tcfg = cfg.TLSConfig.Clone()
		if tcfg.ServerName == "" {
			tcfg.ServerName = cfg.Host
		}
	}

	var text *textproto.Conn
	if cfg.TLSMode == TLSImplicit {
		tc := tls.Client(conn, tcfg)
		if err := tc.Handshake(); err != nil {
			return classify(err, false)
		}
		text = textproto.NewConn(tc)
	} else {
		text = textproto.NewConn(conn)
	}

	cmd := func(expect int, format string, args ...any) (int, error) {
		if format != "" {
			if err := text.PrintfLine(format, args...); err != nil {
				return 0, err
			}
		}
		code, _, err := text.ReadResponse(expect)
		return code, err
	}

	// Greeting.
	if _, err := cmd(220, ""); err != nil {
		return classify(err, false)
	}
	if _, err := cmd(250, "EHLO %s", cfg.HeloName); err != nil {
		return classify(err, false)
	}

	if cfg.TLSMode == TLSStartTLS {
		if _, err := cmd(220, "STARTTLS"); err != nil {
			return classify(err, false)
		}
		tc := tls.Client(conn, tcfg)
		if err := tc.Handshake(); err != nil {
			return classify(err, false)
		}
		text = textproto.NewConn(tc)
		if _, err := cmd(250, "EHLO %s", cfg.HeloName); err != nil {
			return classify(err, false)
		}
	}

	b64 := base64.StdEncoding.EncodeToString
	switch cfg.Auth {
	case AuthLogin:
		if _, err := cmd(334, "AUTH LOGIN"); err != nil {
			return classify(err, false)
		}
		if _, err := cmd(334, "%s", b64([]byte(cfg.Username))); err != nil {
			return classify(err, false)
		}
		if _, err := cmd(235, "%s", b64([]byte(cfg.Password))); err != nil {
			return classify(err, false)
		}
	default: // AuthPlain
		token := b64([]byte("\x00" + cfg.Username + "\x00" + cfg.Password))
		if _, err := cmd(235, "AUTH PLAIN %s", token); err != nil {
			return classify(err, false)
		}
	}

	if _, err := cmd(250, "MAIL FROM:<%s>", from); err != nil {
		return classify(err, false)
	}
	// 250 or 251 both accept the recipient.
	if _, err := cmd(25, "RCPT TO:<%s>", to); err != nil {
		return classify(err, false)
	}
	if _, err := cmd(354, "DATA"); err != nil {
		return classify(err, false)
	}

	// From here on the message data is in flight: an unexplained failure is
	// ambiguous, but an explicit rejection reply is still provably-failed.
	dw := text.DotWriter()
	if _, err := dw.Write(msg); err != nil {
		return classify(err, true)
	}
	if err := dw.Close(); err != nil {
		return classify(err, true)
	}
	if _, _, err := text.ReadResponse(250); err != nil {
		return classify(err, true)
	}

	// Delivery is committed; QUIT is a courtesy.
	_ = text.PrintfLine("QUIT")
	return Result{Outcome: OutcomeSent, Code: "250"}
}

func classify(err error, afterData bool) Result {
	var tpErr *textproto.Error
	if errors.As(err, &tpErr) {
		// The server replied definitively; the message was not accepted.
		return Result{Outcome: OutcomeFailed, Code: strconv.Itoa(tpErr.Code)}
	}

	code := "conn"
	var netErr net.Error
	var verifyErr *tls.CertificateVerificationError
	var unknownAuthErr x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var recordErr tls.RecordHeaderError
	switch {
	case errors.As(err, &verifyErr), errors.As(err, &unknownAuthErr),
		errors.As(err, &hostErr), errors.As(err, &recordErr):
		code = "tls"
	case errors.As(err, &netErr) && netErr.Timeout():
		code = "timeout"
	}

	if afterData && code != "tls" {
		return Result{Outcome: OutcomeAmbiguous, Code: code}
	}
	return Result{Outcome: OutcomeFailed, Code: code}
}

// deadlineConn applies a per-operation deadline to every read and write,
// capped by the overall attempt deadline.
type deadlineConn struct {
	net.Conn
	perIO   time.Duration
	overall time.Time
}

func (c *deadlineConn) deadline() time.Time {
	d := time.Now().Add(c.perIO)
	if d.After(c.overall) {
		return c.overall
	}
	return d
}

func (c *deadlineConn) Read(b []byte) (int, error) {
	if err := c.Conn.SetReadDeadline(c.deadline()); err != nil {
		return 0, err
	}
	return c.Conn.Read(b)
}

func (c *deadlineConn) Write(b []byte) (int, error) {
	if err := c.Conn.SetWriteDeadline(c.deadline()); err != nil {
		return 0, err
	}
	return c.Conn.Write(b)
}
