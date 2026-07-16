package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kadel/allowmaild/internal/config"
	"github.com/kadel/allowmaild/internal/smtptest"
)

const testPassword = "integration-test-secret-pw"

type daemon struct {
	client *http.Client
	cfg    *config.Config
	app    *App
}

// startDaemon runs the full daemon on a real Unix socket against the given
// fake SMTP server. A nil trust config leaves the default certificate roots
// in place, so the fake server's self-signed certificate is not trusted.
func startDaemon(t *testing.T, fake *smtptest.Server, trust *tls.Config) *daemon {
	t.Helper()
	// Keep the socket path short: sun_path is limited to ~104 bytes.
	dir, err := os.MkdirTemp("", "amd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	cfg := &config.Config{
		Sender: config.Sender{Address: "sender@example.test", Name: "Sender Name"},
		Recipients: map[string]config.Recipient{
			"self-gmail": {Address: "dest@example.test"},
		},
		Limits: config.Limits{PerHour: 50, PerDay: 100, MaxSubjectBytes: 200, MaxBodyBytes: 10000},
		SMTP: config.SMTP{
			Host: fake.Host, Port: fake.Port, TLSMode: "implicit",
			Auth: "plain", Username: "sender@example.test", PasswordFile: "unused",
			TimeoutSeconds: 60, IOTimeoutSeconds: 20,
		},
		SocketPath:    filepath.Join(dir, "s.sock"),
		SocketMode:    "0660",
		StateDir:      filepath.Join(dir, "state"),
		RetentionDays: 90,
	}

	opts := []Option{WithSMTPTimeouts(5*time.Second, 700*time.Millisecond)}
	if trust != nil {
		opts = append(opts, WithSMTPTLSConfig(trust))
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := New(cfg, testPassword, log, opts...)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	if _, err := a.Start(); err != nil {
		t.Fatalf("app.Start: %v", err)
	}
	t.Cleanup(func() { a.Close() })

	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", cfg.SocketPath)
			},
		},
	}
	return &daemon{client: client, cfg: cfg, app: a}
}

func (d *daemon) send(t *testing.T, body string) (int, map[string]any) {
	t.Helper()
	resp, err := d.client.Post("http://allowmaild/v1/send", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /v1/send: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var parsed map[string]any
	json.Unmarshal(raw, &parsed)
	return resp.StatusCode, parsed
}

func reqBody(key, subject, text string) string {
	b, _ := json.Marshal(map[string]string{
		"recipient": "self-gmail", "subject": subject, "text": text, "idempotency_key": key,
	})
	return string(b)
}

func TestHealthOverSocket(t *testing.T) {
	fake, pool := smtptest.Start(t, smtptest.Script{})
	d := startDaemon(t, fake, &tls.Config{RootCAs: pool})

	resp, err := d.client.Get("http://allowmaild/v1/health")
	if err != nil {
		t.Fatalf("GET /v1/health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health = %d", resp.StatusCode)
	}
}

// Task 5.2: only the alias-mapped address appears in envelope and headers,
// whatever the request contents look like.
func TestOnlyMappedAddressInEnvelopeAndHeaders(t *testing.T) {
	fake, pool := smtptest.Start(t, smtptest.Script{})
	d := startDaemon(t, fake, &tls.Config{RootCAs: pool})

	// A hostile-but-valid body: contains address-like and header-like lines.
	hostileText := "Please contact attacker@evil.test\nTo: attacker@evil.test\nBcc: attacker@evil.test\n"
	code, resp := d.send(t, reqBody("k1", "Fwd: attacker@evil.test <script>", hostileText))
	if code != http.StatusOK || resp["status"] != "sent" {
		t.Fatalf("send: code=%d resp=%v", code, resp)
	}

	// Smuggling shapes must be rejected outright.
	for _, body := range []string{
		`{"recipient":"self-gmail,attacker@evil.test","subject":"s","text":"t","idempotency_key":"k2"}`,
		`{"recipient":["self-gmail","attacker@evil.test"],"subject":"s","text":"t","idempotency_key":"k3"}`,
		`{"recipient":"self-gmail","subject":"s","text":"t","idempotency_key":"k4","cc":"attacker@evil.test"}`,
		`{"recipient":"attacker@evil.test","subject":"s","text":"t","idempotency_key":"k5"}`,
	} {
		if code, _ := d.send(t, body); code != http.StatusBadRequest {
			t.Errorf("smuggling shape not rejected (%d): %s", code, body)
		}
	}

	msgs := fake.Messages()
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	m := msgs[0]
	if m.From != "sender@example.test" {
		t.Errorf("envelope from = %q", m.From)
	}
	if len(m.To) != 1 || m.To[0] != "dest@example.test" {
		t.Fatalf("envelope to = %v, want exactly [dest@example.test]", m.To)
	}

	parsed, err := mail.ReadMessage(bytes.NewReader(m.Data))
	if err != nil {
		t.Fatalf("delivered message does not parse: %v", err)
	}
	if v := parsed.Header.Get("To"); v != "<dest@example.test>" {
		t.Errorf("To header = %q", v)
	}
	// The header set must be exactly the fixed one: nothing from the body
	// (To:/Bcc: lines) became a header, and no address-bearing header other
	// than From/To exists. The subject text itself lives only in Subject.
	allowed := map[string]bool{
		"From": true, "To": true, "Subject": true, "Date": true,
		"Message-Id": true, "Mime-Version": true, "Content-Type": true,
		"Content-Transfer-Encoding": true,
	}
	for name, values := range parsed.Header {
		if !allowed[name] {
			t.Errorf("unexpected header %s", name)
		}
		if name != "Subject" {
			for _, v := range values {
				if strings.Contains(v, "attacker@evil.test") {
					t.Errorf("attacker address leaked into %s header: %q", name, v)
				}
			}
		}
	}
}

// Task 5.3: a timeout after DATA yields ambiguous, replays on retry, and
// never causes a second SMTP attempt.
func TestTimeoutAfterDataIsAmbiguousAndNeverResent(t *testing.T) {
	fake, pool := smtptest.Start(t, smtptest.Script{HangAt: smtptest.PhaseFinal})
	d := startDaemon(t, fake, &tls.Config{RootCAs: pool})

	code, first := d.send(t, reqBody("k1", "Hello", "body\n"))
	if code != http.StatusOK || first["status"] != "ambiguous" {
		t.Fatalf("first: code=%d resp=%v", code, first)
	}
	if detail, _ := first["detail"].(string); !strings.Contains(detail, "do not retry") {
		t.Errorf("ambiguous detail does not warn the caller: %v", first)
	}

	connsAfterFirst := fake.Connections()
	code, second := d.send(t, reqBody("k1", "Hello", "body\n"))
	if code != http.StatusOK || second["status"] != "ambiguous" {
		t.Fatalf("replay: code=%d resp=%v", code, second)
	}
	if second["request_id"] != first["request_id"] {
		t.Errorf("replay produced a different request: %v vs %v", second, first)
	}
	if fake.Connections() != connsAfterFirst {
		t.Fatalf("retry opened a new SMTP connection: %d -> %d", connsAfterFirst, fake.Connections())
	}
	if len(fake.Messages()) != 0 {
		t.Fatalf("server recorded an accepted message despite hang")
	}
}

// Task 5.4: certificate-verification failure and pre-DATA failures yield
// failed with no delivery.
func TestCertVerificationFailureIsFailed(t *testing.T) {
	fake, _ := smtptest.Start(t, smtptest.Script{})
	// Do not trust the fake server's self-signed certificate.
	d := startDaemon(t, fake, nil)

	code, resp := d.send(t, reqBody("k1", "Hello", "body\n"))
	if code != http.StatusOK || resp["status"] != "failed" {
		t.Fatalf("code=%d resp=%v", code, resp)
	}
	if resp["result_code"] != "tls" {
		t.Errorf("result_code = %v, want tls", resp["result_code"])
	}
	if len(fake.Messages()) != 0 {
		t.Fatal("message delivered despite certificate failure")
	}
}

func TestPreDataFailuresAreFailed(t *testing.T) {
	cases := []struct {
		name   string
		script smtptest.Script
		code   string
	}{
		{"auth rejected", smtptest.Script{FailAt: smtptest.PhaseAuth, FailCode: 535}, "535"},
		{"recipient rejected", smtptest.Script{FailAt: smtptest.PhaseRcpt, FailCode: 550}, "550"},
		{"data rejected", smtptest.Script{FailAt: smtptest.PhaseData, FailCode: 554}, "554"},
		{"greeting hang", smtptest.Script{HangAt: smtptest.PhaseGreeting}, "timeout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake, pool := smtptest.Start(t, tc.script)
			d := startDaemon(t, fake, &tls.Config{RootCAs: pool})

			code, resp := d.send(t, reqBody("k1", "Hello", "body\n"))
			if code != http.StatusOK || resp["status"] != "failed" {
				t.Fatalf("code=%d resp=%v", code, resp)
			}
			if fmt.Sprint(resp["result_code"]) != tc.code {
				t.Errorf("result_code = %v, want %s", resp["result_code"], tc.code)
			}
			if len(fake.Messages()) != 0 {
				t.Fatal("message delivered despite scripted failure")
			}
		})
	}
}

// Drop after DATA (connection closed with no reply) is also ambiguous.
func TestDropAfterDataIsAmbiguous(t *testing.T) {
	fake, pool := smtptest.Start(t, smtptest.Script{DropAfterData: true})
	d := startDaemon(t, fake, &tls.Config{RootCAs: pool})

	code, resp := d.send(t, reqBody("k1", "Hello", "body\n"))
	if code != http.StatusOK || resp["status"] != "ambiguous" {
		t.Fatalf("code=%d resp=%v", code, resp)
	}
}

// The SMTP password and message content must never appear in the state dir.
func TestNoSecretsInStateDirectory(t *testing.T) {
	fake, pool := smtptest.Start(t, smtptest.Script{})
	d := startDaemon(t, fake, &tls.Config{RootCAs: pool})

	body := "super-unique-body-marker-77described"
	if code, _ := d.send(t, reqBody("k1", "unique-subject-marker-88", body+"\n")); code != http.StatusOK {
		t.Fatal("send failed")
	}
	d.app.Close()

	err := filepath.WalkDir(d.cfg.StateDir, func(path string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, needle := range []string{testPassword, body, "unique-subject-marker-88"} {
			if bytes.Contains(raw, []byte(needle)) {
				t.Errorf("%s contains %q", path, needle)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
