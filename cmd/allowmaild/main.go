// Command allowmaild is a narrowly scoped email-sending daemon: it accepts
// send requests over a Unix socket, resolves recipient aliases against a
// protected allowlist, and delivers via a configured SMTP endpoint. It fails
// closed on any missing or invalid dependency.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kadel/allowmaild/internal/app"
	"github.com/kadel/allowmaild/internal/config"
)

// version is stamped at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	configPath := flag.String("config", "/etc/allowmaild/config.yaml", "path to the configuration file")
	healthcheck := flag.Bool("healthcheck", false, "probe /v1/health via the socket and exit")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("allowmaild " + version)
		return
	}

	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	log.Info("starting", "version", version)

	if *healthcheck {
		os.Exit(runHealthcheck(*configPath))
	}

	cfg, smtpPassword, err := config.Load(*configPath)
	if err != nil {
		log.Error("startup refused", "reason", err.Error())
		os.Exit(1)
	}

	a, err := app.New(cfg, smtpPassword, log)
	if err != nil {
		log.Error("startup refused", "reason", err.Error())
		os.Exit(1)
	}

	errCh, err := a.Start()
	if err != nil {
		log.Error("startup refused", "reason", err.Error())
		os.Exit(1)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-sig:
		log.Info("shutting down")
	case err, ok := <-errCh:
		if ok && err != nil {
			log.Error("server error", "reason", "listener failed")
		}
	}
	if err := a.Close(); err != nil {
		log.Error("shutdown error")
		os.Exit(1)
	}
}

// runHealthcheck connects to the daemon's socket and reports readiness via
// the exit code, for use by service monitoring.
func runHealthcheck(configPath string) int {
	cfg, _, err := config.Load(configPath)
	if err != nil {
		return 1
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", cfg.SocketPath)
			},
		},
	}
	resp, err := client.Get("http://allowmaild/v1/health")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
