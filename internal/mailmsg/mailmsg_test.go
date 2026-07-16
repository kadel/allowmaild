package mailmsg

import (
	"bytes"
	"mime"
	"net/mail"
	"sort"
	"strings"
	"testing"
	"time"
)

var testDate = time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)

func build(t *testing.T, subject, body string) (*mail.Message, []byte, string) {
	t.Helper()
	raw, msgID := Build(Input{
		FromAddress: "sender@example.test",
		FromName:    "Sender Name",
		ToAddress:   "dest@example.test",
		Subject:     subject,
		Body:        body,
		Date:        testDate,
	})
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("message does not parse: %v", err)
	}
	return msg, raw, msgID
}

func TestFixedHeaderSet(t *testing.T) {
	msg, _, msgID := build(t, "Hello", "body\n")

	want := []string{
		"Content-Transfer-Encoding", "Content-Type", "Date", "From",
		"Message-Id", "Mime-Version", "Subject", "To",
	}
	var got []string
	for k := range msg.Header {
		got = append(got, k)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("header set = %v, want %v", got, want)
	}

	if v := msg.Header.Get("From"); v != `"Sender Name" <sender@example.test>` {
		t.Errorf("From = %q", v)
	}
	if v := msg.Header.Get("To"); v != "<dest@example.test>" {
		t.Errorf("To = %q", v)
	}
	if v := msg.Header.Get("Content-Type"); v != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q", v)
	}
	if v := msg.Header.Get("Message-Id"); v != msgID {
		t.Errorf("Message-Id = %q, want %q", v, msgID)
	}
	if !strings.HasSuffix(msgID, "@example.test>") || !strings.HasPrefix(msgID, "<") {
		t.Errorf("message id shape: %q", msgID)
	}
}

func TestASCIISubjectUnchanged(t *testing.T) {
	msg, _, _ := build(t, "A quick joke", "b\n")
	if v := msg.Header.Get("Subject"); v != "A quick joke" {
		t.Errorf("Subject = %q", v)
	}
}

func TestRFC2047SubjectRoundTrip(t *testing.T) {
	original := "Připomínka: doména žluťoučký"
	msg, _, _ := build(t, original, "b\n")

	encoded := msg.Header.Get("Subject")
	if !strings.Contains(encoded, "=?utf-8?") {
		t.Fatalf("subject not RFC 2047 encoded: %q", encoded)
	}
	dec := new(mime.WordDecoder)
	decoded, err := dec.DecodeHeader(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded != original {
		t.Fatalf("round trip = %q, want %q", decoded, original)
	}
}

func TestBodyPreservedVerbatim(t *testing.T) {
	body := "line one\nline two\n"
	msg, _, _ := build(t, "s", body)
	got := new(strings.Builder)
	if _, err := readAll(got, msg); err != nil {
		t.Fatalf("read body: %v", err)
	}
	if got.String() != body {
		t.Fatalf("body = %q, want %q", got.String(), body)
	}
}

func readAll(sb *strings.Builder, msg *mail.Message) (int64, error) {
	buf := make([]byte, 4096)
	var n int64
	for {
		m, err := msg.Body.Read(buf)
		sb.Write(buf[:m])
		n += int64(m)
		if err != nil {
			if err.Error() == "EOF" {
				return n, nil
			}
			return n, err
		}
	}
}

// Injection attempts reach Build only if validation failed; even then the
// encoder must keep caller content out of the header block.
func TestInjectionContentAbsentFromHeaders(t *testing.T) {
	// A subject that tries to smuggle a header via encoded-word specials.
	msg, raw, _ := build(t, "hi =?utf-8?q?Bcc=3A_x=40evil?= there", "b\n")
	headerBlock := string(raw[:bytes.Index(raw, []byte("\r\n\r\n"))])
	if strings.Contains(headerBlock, "Bcc: ") {
		t.Fatal("injected header appeared in header block")
	}
	if len(msg.Header["Bcc"]) != 0 {
		t.Fatal("parsed message has a Bcc header")
	}
}

func TestHeaderLinesAreCRLF(t *testing.T) {
	_, raw, _ := build(t, "s", "b\n")
	headerBlock := raw[:bytes.Index(raw, []byte("\r\n\r\n"))+2]
	if bytes.Contains(bytes.ReplaceAll(headerBlock, []byte("\r\n"), nil), []byte("\n")) {
		t.Fatal("header block contains a bare LF")
	}
}
