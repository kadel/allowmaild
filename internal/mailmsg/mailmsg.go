// Package mailmsg constructs the complete MIME message.
//
// The header set is fixed: From, To, Subject, Date, Message-ID, MIME-Version,
// Content-Type, Content-Transfer-Encoding. No caller input is ever
// interpreted as a header. Subject and body must already be validated
// (no CR/LF in subject, LF-only line breaks in body); Subject is additionally
// RFC 2047 encoded when non-ASCII, so CR/LF injection is impossible by
// construction.
package mailmsg

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"mime"
	"net/mail"
	"strings"
	"time"
)

// Input carries validated, trusted-by-now values for one message.
type Input struct {
	FromAddress string // configured sender address
	FromName    string // configured sender display name (may be empty)
	ToAddress   string // resolved from the alias, never caller input
	Subject     string
	Body        string
	Date        time.Time
}

// Build returns the full message (CRLF header lines, blank line, LF body —
// the SMTP dot-writer converts body line endings to CRLF on the wire) and
// the generated Message-ID header value.
func Build(in Input) ([]byte, string) {
	msgID := newMessageID(in.FromAddress)

	from := mail.Address{Name: in.FromName, Address: in.FromAddress}

	var b bytes.Buffer
	writeHeader := func(name, value string) {
		fmt.Fprintf(&b, "%s: %s\r\n", name, value)
	}
	writeHeader("From", from.String())
	writeHeader("To", "<"+in.ToAddress+">")
	writeHeader("Subject", mime.QEncoding.Encode("utf-8", in.Subject))
	writeHeader("Date", in.Date.Format(time.RFC1123Z))
	writeHeader("Message-ID", msgID)
	writeHeader("MIME-Version", "1.0")
	writeHeader("Content-Type", "text/plain; charset=utf-8")
	writeHeader("Content-Transfer-Encoding", "8bit")
	b.WriteString("\r\n")
	b.WriteString(in.Body)
	return b.Bytes(), msgID
}

func newMessageID(fromAddress string) string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	domain := "allowmaild.invalid"
	if _, d, ok := strings.Cut(fromAddress, "@"); ok && d != "" {
		domain = d
	}
	return "<" + hex.EncodeToString(buf[:]) + "@" + domain + ">"
}
