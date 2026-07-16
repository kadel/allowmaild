// Package app assembles the daemon: config, store, SMTP client, and the
// Unix-socket HTTP server. The daemon never opens a TCP or UDP listener.
package app

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kadel/allowmaild/internal/config"
	"github.com/kadel/allowmaild/internal/server"
	"github.com/kadel/allowmaild/internal/smtpclient"
	"github.com/kadel/allowmaild/internal/store"
)

type Option func(*options)

type options struct {
	tlsConfig *tls.Config
	overall   time.Duration
	perIO     time.Duration
}

// WithSMTPTLSConfig injects a TLS client config (tests use it to trust a
// fake server's certificate). Verification stays enabled.
func WithSMTPTLSConfig(c *tls.Config) Option {
	return func(o *options) { o.tlsConfig = c }
}

// WithSMTPTimeouts overrides the configured attempt timeouts (tests only).
func WithSMTPTimeouts(overall, perIO time.Duration) Option {
	return func(o *options) {
		o.overall = overall
		o.perIO = perIO
	}
}

type App struct {
	cfg      *config.Config
	log      *slog.Logger
	store    *store.Store
	http     *http.Server
	listener net.Listener
	cancel   context.CancelFunc
	done     chan struct{}
}

// New builds the daemon and fails closed on any unavailable dependency:
// state directory, database, or (already at config load) credentials.
func New(cfg *config.Config, smtpPassword string, log *slog.Logger, opts ...Option) (*App, error) {
	o := options{
		overall: time.Duration(cfg.SMTP.TimeoutSeconds) * time.Second,
		perIO:   time.Duration(cfg.SMTP.IOTimeoutSeconds) * time.Second,
	}
	for _, opt := range opts {
		opt(&o)
	}

	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return nil, errors.New("state_dir is not writable")
	}
	st, err := store.Open(filepath.Join(cfg.StateDir, "allowmaild.db"))
	if err != nil {
		return nil, errors.New("request store could not be opened")
	}

	ctx, cancel := context.WithCancel(context.Background())

	swept, err := st.SweepSending(ctx, time.Now())
	if err != nil {
		cancel()
		st.Close()
		return nil, errors.New("startup sweep failed")
	}
	if swept > 0 {
		log.Warn("swept in-flight rows to ambiguous", "count", swept)
	}

	smtpCfg := smtpclient.Config{
		Host:     cfg.SMTP.Host,
		Port:     cfg.SMTP.Port,
		TLSMode:  smtpclient.TLSMode(cfg.SMTP.TLSMode),
		Auth:     smtpclient.AuthMethod(cfg.SMTP.Auth),
		Username: cfg.SMTP.Username,
		Password: smtpPassword,
		HeloName: heloName(cfg.Sender.Address),
		Overall:  o.overall,
		PerIO:    o.perIO,
		TLSConfig: o.tlsConfig,
	}
	send := func(from, to string, msg []byte) smtpclient.Result {
		return smtpclient.Send(smtpCfg, from, to, msg)
	}

	srv := server.New(cfg, st, send, log)
	a := &App{
		cfg:    cfg,
		log:    log,
		store:  st,
		cancel: cancel,
		done:   make(chan struct{}),
		http: &http.Server{
			Handler:           srv.Handler(),
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      5 * time.Minute, // a send request spans the SMTP attempt
			IdleTimeout:       2 * time.Minute,
		},
	}
	go a.purgeLoop(ctx)
	return a, nil
}

// Start creates the Unix socket and begins serving. It returns once the
// listener is ready; Serve errors are reported via the returned channel.
func (a *App) Start() (<-chan error, error) {
	if err := removeStaleSocket(a.cfg.SocketPath); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", a.cfg.SocketPath)
	if err != nil {
		return nil, errors.New("cannot listen on socket_path")
	}
	if err := os.Chmod(a.cfg.SocketPath, a.cfg.SocketFileMode()); err != nil {
		ln.Close()
		return nil, errors.New("cannot set socket mode")
	}
	a.listener = ln

	errCh := make(chan error, 1)
	go func() {
		defer close(a.done)
		if err := a.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()
	a.log.Info("listening", "endpoint", "unix socket")
	return errCh, nil
}

// Close shuts the server down gracefully and removes the socket.
func (a *App) Close() error {
	a.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := a.http.Shutdown(ctx)
	<-a.done
	os.Remove(a.cfg.SocketPath)
	if cerr := a.store.Close(); err == nil {
		err = cerr
	}
	return err
}

func (a *App) purgeLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		cutoff := time.Now().Add(-a.cfg.Retention())
		if n, err := a.store.Purge(ctx, cutoff); err != nil {
			if ctx.Err() == nil {
				a.log.Error("retention purge failed")
			}
		} else if n > 0 {
			a.log.Info("purged expired records", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// removeStaleSocket removes a leftover socket file. Anything else at the
// path is an error: the daemon must not delete unknown files.
func removeStaleSocket(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("cannot stat socket_path")
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return errors.New("socket_path exists and is not a socket")
	}
	if err := os.Remove(path); err != nil {
		return errors.New("cannot remove stale socket")
	}
	return nil
}

func heloName(senderAddress string) string {
	if _, domain, ok := strings.Cut(senderAddress, "@"); ok && domain != "" {
		return domain
	}
	return "allowmaild.invalid"
}
