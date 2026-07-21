# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

allowmaild is an email-sending daemon for semi-trusted local processes (e.g. an autonomous agent). Callers POST to a Unix socket naming a recipient *alias*; the daemon resolves the alias from its root-owned config, builds the entire MIME message itself, and delivers over SMTP with credentials only it can read. The caller can never supply an address, header, HTML, or attachment.

## Commands

Go is not assumed to be on PATH — use nix (never brew):

```sh
nix develop                          # dev shell with go, gopls, gotools
nix run nixpkgs#go -- test ./...     # run all tests without entering the shell
nix run nixpkgs#go -- test ./internal/server -run TestName   # single test
nix build .#allowmaild               # release build (also runs the test suite)
CGO_ENABLED=0 go build ./cmd/allowmaild   # plain build inside the dev shell
```

- The build is pure Go (`CGO_ENABLED=0`); SQLite is `modernc.org/sqlite`, not mattn/go-sqlite3. Don't introduce cgo dependencies.
- When `go.mod`/`go.sum` change, `vendorHash` in `flake.nix` must be updated or `nix build` fails (build once, copy the hash from the error).
- Version comes from the `VERSION` file, stamped via ldflags; CI (`.github/workflows/release.yml`) builds Linux amd64/arm64 binaries with `nix build` on pushes to main and `v*` tags.

## Architecture

~3k lines of Go under `cmd/allowmaild` + `internal/`. Assembly flow:

`main.go` → `config.Load` → `app.New` (opens store, wires SMTP client, builds HTTP server) → `app.Start` (creates the Unix socket). Two endpoints only: `GET /v1/health` and `POST /v1/send`.

- `internal/config` — strict YAML parsing (unknown fields rejected); every invalid value refuses startup. Also loads the SMTP password file.
- `internal/server` — HTTP handlers, request validation, rate-limit checks, and the send orchestration.
- `internal/store` — one SQLite table is simultaneously the idempotency state machine (`sending` → `sent`/`failed`/`ambiguous`), the audit log, and the rate-limit source of truth (limits are computed by counting rows in the window). Row insert is the atomic commit point; on startup, leftover `sending` rows are swept to `ambiguous`.
- `internal/mailmsg` — builds the full MIME message with a fixed header set; subject/body are pre-validated so header injection is impossible by construction.
- `internal/smtpclient` — one delivery attempt per call, built on net/textproto. Outcome classification (sent/failed/ambiguous) depends on whether the failure happened before or after DATA was transmitted.
- `internal/smtptest` — scriptable in-process fake SMTP server (fail at any phase, hang, drop after DATA) used by server/app tests.

## Invariants (fail closed, stay silent)

These properties are load-bearing; don't weaken them in a refactor:

- **No TCP/UDP listener exists anywhere in the code.** The Unix socket is the only surface; access control is filesystem group membership.
- **Logs, error responses, and the DB never contain** subjects, bodies, email addresses, SMTP transcripts, credentials, or file paths — only request IDs, aliases, states, content hashes, and sanitized codes.
- **Startup fails closed**: any missing config, credential, or state directory exits before the socket exists.
- Every send carries a caller idempotency key; retries must never produce duplicate emails.

## Specs and workflow

Behavioral specs live in `openspec/specs/` (send-api, delivery-idempotency, rate-limiting, recipient-allowlist, smtp-delivery, audit-log, deployment-isolation) and are the source of truth for behavior. Changes go through the OpenSpec workflow (`/opsx:propose`, `/opsx:apply`, `/opsx:archive`); completed changes are archived under `openspec/changes/archive/`.

Deployment (hardened systemd unit, user/group permission model, `LoadCredential`) is documented in `deploy/systemd/README.md`.

## Local files

`config.local.yaml`, `smtp_password`, and `state_dir/` are gitignored local working files — never commit them or their contents. `config.example.yaml` is the configuration reference.
