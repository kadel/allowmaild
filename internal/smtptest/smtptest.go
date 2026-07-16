// Package smtptest provides an in-process fake SMTP server with scriptable
// behavior: accept, reject at any phase, hang, or drop the connection after
// DATA. Tests use it to prove outcome classification and envelope contents.
package smtptest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"
)

// Phase names a point in the SMTP conversation.
type Phase string

const (
	PhaseGreeting Phase = "greeting"
	PhaseEHLO     Phase = "ehlo"
	PhaseAuth     Phase = "auth"
	PhaseMail     Phase = "mail"
	PhaseRcpt     Phase = "rcpt"
	PhaseData     Phase = "data"  // reply to the DATA command (before 354)
	PhaseFinal    Phase = "final" // reply after the message content
)

// Script controls how the fake server behaves.
type Script struct {
	// FailAt makes the server reply with FailCode at the given phase.
	FailAt   Phase
	FailCode int
	// HangAt makes the server stop responding at the given phase.
	HangAt Phase
	// DropAfterData closes the connection after receiving the message
	// content, without any reply.
	DropAfterData bool
}

// Message is one fully received message.
type Message struct {
	From string
	To   []string
	Data []byte
}

type Server struct {
	Host string
	Port int

	ln     net.Listener
	script Script
	done   chan struct{}

	mu    sync.Mutex
	msgs  []Message
	conns int
}

// Start runs a fake SMTP server with implicit TLS on 127.0.0.1. It returns
// the server and a cert pool that trusts its self-signed certificate.
func Start(t *testing.T, script Script) (*Server, *x509.CertPool) {
	t.Helper()
	cert, pool := GenerateCert(t, "127.0.0.1")
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatalf("smtptest listen: %v", err)
	}
	s := &Server{
		ln:     ln,
		script: script,
		done:   make(chan struct{}),
	}
	addr := ln.Addr().(*net.TCPAddr)
	s.Host, s.Port = "127.0.0.1", addr.Port

	go s.acceptLoop()
	t.Cleanup(s.Close)
	return s, pool
}

func (s *Server) Close() {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	s.ln.Close()
}

// Messages returns the fully received messages.
func (s *Server) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.msgs...)
}

// Connections returns how many connections were accepted.
func (s *Server) Connections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns
}

func (s *Server) acceptLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns++
		s.mu.Unlock()
		go s.handle(conn)
	}
}

// handle runs one SMTP conversation. Returning closes the connection.
func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	text := textproto.NewConn(conn)

	// act performs the scripted behavior for a phase. It returns false when
	// the conversation must stop (hang or scripted failure was terminal
	// enough that the client will bail).
	hang := func() bool { <-s.done; return false }
	act := func(phase Phase, okReply string) bool {
		if s.script.HangAt == phase {
			return hang()
		}
		if s.script.FailAt == phase {
			text.PrintfLine("%d scripted failure", s.script.FailCode)
			return true // keep reading; client decides to quit
		}
		text.PrintfLine("%s", okReply)
		return true
	}

	if !act(PhaseGreeting, "220 smtptest ready") {
		return
	}

	var from string
	var to []string
	for {
		line, err := text.ReadLine()
		if err != nil {
			return
		}
		verb := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(verb, "EHLO"), strings.HasPrefix(verb, "HELO"):
			if s.script.HangAt == PhaseEHLO {
				hang()
				return
			}
			if s.script.FailAt == PhaseEHLO {
				text.PrintfLine("%d scripted failure", s.script.FailCode)
				continue
			}
			text.PrintfLine("250-smtptest")
			text.PrintfLine("250 AUTH PLAIN LOGIN")
		case strings.HasPrefix(verb, "AUTH"):
			if !act(PhaseAuth, "235 2.7.0 accepted") {
				return
			}
		case strings.HasPrefix(verb, "MAIL FROM:"):
			if !act(PhaseMail, "250 sender ok") {
				return
			}
			from = extractAddr(line)
		case strings.HasPrefix(verb, "RCPT TO:"):
			if !act(PhaseRcpt, "250 recipient ok") {
				return
			}
			if s.script.FailAt != PhaseRcpt {
				to = append(to, extractAddr(line))
			}
		case verb == "DATA":
			if s.script.HangAt == PhaseData {
				hang()
				return
			}
			if s.script.FailAt == PhaseData {
				text.PrintfLine("%d scripted failure", s.script.FailCode)
				continue
			}
			text.PrintfLine("354 go ahead")
			data, err := io.ReadAll(text.DotReader())
			if err != nil {
				return
			}
			if s.script.DropAfterData {
				return // close with no reply
			}
			if s.script.HangAt == PhaseFinal {
				hang()
				return
			}
			if s.script.FailAt == PhaseFinal {
				text.PrintfLine("%d scripted failure", s.script.FailCode)
				continue
			}
			s.mu.Lock()
			s.msgs = append(s.msgs, Message{From: from, To: to, Data: data})
			s.mu.Unlock()
			text.PrintfLine("250 2.0.0 OK queued")
		case verb == "QUIT":
			text.PrintfLine("221 bye")
			return
		case verb == "RSET":
			from, to = "", nil
			text.PrintfLine("250 ok")
		case verb == "NOOP":
			text.PrintfLine("250 ok")
		default:
			text.PrintfLine("502 command not implemented")
		}
	}
}

func extractAddr(line string) string {
	open := strings.Index(line, "<")
	closing := strings.Index(line, ">")
	if open < 0 || closing <= open {
		return ""
	}
	return line[open+1 : closing]
}

// GenerateCert returns a self-signed certificate for host and a pool that
// trusts it.
func GenerateCert(t *testing.T, host string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "smtptest"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}
