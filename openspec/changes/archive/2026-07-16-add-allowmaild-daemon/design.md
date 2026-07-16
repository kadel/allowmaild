# Design: add-allowmaild-daemon

## Context

OpenClaw (agentic assistant) must be able to send email to a handful of Tomas-approved addresses without ever holding SMTP credentials or the ability to choose an arbitrary recipient. The model and all tool arguments are untrusted; prompt injection is the assumed failure mode. IDEA.md (repo root) records the full exploration; key decisions were made 2026-07-16: no per-message approval, containers-only deployment, explicit idempotency state machine.

The enforcement point is `allowmaild`, a new Go daemon. The OpenClaw plugin (follow-up change) is a convenience layer only — every guarantee in this design must hold when the plugin is bypassed and the socket is called directly with `curl`.

## Goals / Non-Goals

**Goals:**

- Emails can only reach addresses mapped from configured aliases; nothing the caller sends can select a different destination.
- SMTP credentials and the allowlist are unreachable from the calling (OpenClaw) container.
- At-most-once delivery per idempotency key, with ambiguous outcomes reported honestly.
- Bounded blast radius via rate limits that fail closed.
- Auditability without storing message bodies.

**Non-Goals:**

- Per-message human approval (the allowlist is the approval; a daemon-side pending/approve flow can be added later — the state machine leaves room, but nothing is built now).
- CC/BCC, multiple recipients, HTML, attachments, caller-controlled headers, wildcard recipients.
- The OpenClaw TypeScript plugin (separate change).
- Kubernetes, public listeners, multi-tenant use.

## Decisions

### D1: Dedicated Go daemon, not Postfix or a SaaS sandbox

Postfix can enforce recipient policy but drags in a full mail-server configuration surface. Mailgun-style authorized recipients impose provider constraints. A single-purpose daemon keeps the entire policy surface small enough to audit line-by-line. (Alternatives considered in IDEA.md → References.)

### D2: Unix socket only; aliases, not addresses

The daemon listens exclusively on a Unix socket shared via container volume — no TCP listener exists in the code at all (not merely firewalled off). The API accepts a recipient *alias* (`self-gmail`); the daemon resolves it internally with exact, case-sensitive matching. No wildcards, plus-address stripping, substring or domain matching. This removes address parsing from the attack surface entirely.

### D3: No per-message approval — the allowlist is the approval

Any process in the OpenClaw container can reach the socket, so a plugin-side approval prompt is bypassable and therefore not a security control. Rather than pretend otherwise, v1's model is: adding an alias to the protected config *is* the human approval; rate limits and audit bound the residual risk (worst case: a few emails/hour to Tomas's own addresses, logged). Alternative (daemon-side pending queue + admin-only approve command) was designed and deliberately deferred; the request state machine (D5) is shaped so it can be added additively.

### D4: Validation and rate-checks before key reservation

Invalid or rate-limited requests are rejected without persisting anything, so they cannot burn idempotency keys. The SQLite insert of the request row is the atomic commit point; only requests that pass all checks reach it.

### D5: Idempotency state machine with an honest `ambiguous` state

```
[sending] ──► [sent]       250 after DATA (+ provider message ID)
    │
    ├─────► [failed]      definitive failure before DATA — provably not delivered
    └─────► [ambiguous]   timeout / connection lost after DATA — may have delivered
```

- Exactly one SMTP attempt per key; bounded total deadline; no internal retry.
- Duplicate key in a terminal state → replay stored response verbatim.
- Duplicate key with different subject/body hashes → rejected as key reuse (Stripe-style).
- Duplicate key still `sending` → in-flight conflict.
- Startup sweeps rows stuck in `sending` to `ambiguous` (crash recovery).
- `failed` is safe to retry with a new key; `ambiguous` is terminal and must never be auto-retried — the caller is told "delivery uncertain" and a human decides.

This is the only way to satisfy "at most one accepted email" without silently losing mail: the timeout-after-DATA case genuinely cannot be resolved by the client, so it gets its own state instead of being misreported as success or failure.

### D6: SQLite via `modernc.org/sqlite` (pure Go)

One database holds request/idempotency rows and doubles as the audit log and rate-limit source of truth (limits are computed by counting reservation rows in the window — durable across restarts, no separate counter state to drift). Pure-Go driver keeps cross-compilation and scratch-image container builds trivial. Alternative `mattn/go-sqlite3` (cgo) rejected for build friction; alternative "audit to flat file" rejected because idempotency needs transactional storage anyway.

### D7: MIME construction with stdlib, no message-building library

The message is plain text with a fixed header set (From, To, Subject, Date, Message-ID, MIME-Version, Content-Type). Subject uses `mime.QEncoding` for non-ASCII (RFC 2047); body is UTF-8 `text/plain`. Subject/body are rejected if they contain CR/LF or other control characters (body may contain LF newlines only). Building this by hand from validated inputs is ~50 auditable lines; a full message library (e.g. `go-mail`) adds surface without adding safety here. SMTP client: stdlib `net/smtp` with explicit TLS config (implicit TLS :465 or STARTTLS :587 per config), `tls.Config` with verification on, and connection/IO deadlines. If stdlib ergonomics prove too limiting during implementation, `wneessen/go-mail`'s client (not its builder) is the fallback.

### D8: Fail-closed everywhere

Missing/unparseable config, unreadable credentials, unavailable database, or an undeliverable state directory → the daemon refuses to start or the request is rejected. There is no degraded send path.

### D9: Containers-only deployment

`allowmaild` runs as its own container next to OpenClaw's (Docker/Podman compose; no k8s):

- socket on a shared named volume mounted into both containers; **no published ports**;
- allowlist config mounted read-only into the daemon container only;
- SMTP credentials via root-owned file mount or engine secret, daemon container only;
- one writable volume for the SQLite state directory;
- non-root container user; read-only root filesystem where practical.

The container boundary replaces the OS-user boundary from the original bare-host sketch; systemd hardening notes survive in IDEA.md as a bare-host footnote only.

## Risks / Trade-offs

- [Plugin approval prompt is not a security control] → Accepted deliberately (D3); mitigation is minimal allowlist + rate limits + audit. Documented so nobody later assumes otherwise.
- [`ambiguous` outcomes require a human to check the inbox] → Rare in practice (timeout after DATA); receipt text tells the caller exactly what happened; rows are visible in the audit log.
- [Socket file permissions across container UID/GID mapping] → Compose pins matching UIDs/GIDs for the shared volume; integration test verifies the socket is usable from a caller container and nothing else is exposed.
- [Rate-limit windows computed from DB rows could be slow at scale] → Volume is ≤ dozens of rows/day; a covering index on (timestamp) is ample.
- [Frozen `net/smtp` lacks some extensions] → Only AUTH PLAIN/LOGIN + TLS are needed; fallback client named in D7 if this proves wrong.
- [Config changes require restart] → Feature, not bug: no live-reload races, and edits stay a deliberate human act.

## Migration Plan

Greenfield — nothing to migrate. Rollout order: build → unit/adversarial tests against fake SMTP → container compose on the target host → real send to `self-gmail` only → add further aliases one at a time (IDEA.md Phase 5). Rollback = stop the container; OpenClaw returns to draft-only behavior.

## Open Questions

- Exact SMTP provider endpoint/port/auth for the sender address (config values, not design blockers).
- Final socket path convention inside the shared volume (working default: `/run/allowmail/allowmail.sock`).
- Retention default confirmed at 90 days, or shorter?
