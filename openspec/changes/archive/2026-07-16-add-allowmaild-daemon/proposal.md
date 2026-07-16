# Proposal: add-allowmaild-daemon

## Why

OpenClaw can currently only draft email; giving it direct SMTP or shell access to send would let a misbehaving or prompt-injected agent email anyone. `allowmaild` provides a narrowly scoped send path whose recipient restriction is enforced outside the model: a small Go daemon that owns the SMTP credentials and can only deliver to recipients Tomas has explicitly configured.

## What Changes

- New Go daemon `allowmaild` serving HTTP over a Unix socket only (no network listener).
- `POST /v1/send` accepting exactly `{recipient, subject, text, idempotency_key}` where `recipient` is a configured alias, never an address; `GET /v1/health` for readiness.
- Recipient allowlist and sender identity defined in a protected YAML config the calling agent cannot read or write; the daemon constructs all MIME headers itself with a fixed From address.
- Strict validation: unknown aliases, oversized or control-character-containing fields, unknown JSON properties, and header-injection attempts are rejected; non-ASCII subjects are RFC 2047 encoded.
- Idempotency state machine (`sending → sent | failed | ambiguous`) backed by SQLite: duplicate keys replay stored results, ambiguous outcomes (timeout after DATA) are terminal and never auto-retried, crash recovery sweeps in-flight rows to `ambiguous`.
- Global and per-recipient rate limits that fail closed.
- SQLite audit log (request ID, alias, status, subject/body hashes — never full bodies) with retention.
- SMTP delivery over TLS with certificate verification and bounded timeouts.
- Container deployment (Docker/Podman, no Kubernetes): allowmaild sidecar, socket on a shared volume, config and credentials mounted only into the daemon's container, no published ports.

Out of scope for this change: the OpenClaw TypeScript plugin (follow-up change), per-message human approval (the allowlist is the approval), CC/BCC/multiple recipients, HTML, attachments, caller-controlled headers.

## Capabilities

### New Capabilities

- `send-api`: Unix-socket HTTP surface — `POST /v1/send` request/response contract, strict input validation, `GET /v1/health`, sanitized errors.
- `recipient-allowlist`: config schema, alias-to-address resolution with no wildcards or pattern matching, fixed sender identity, config protection expectations.
- `smtp-delivery`: internal MIME construction, header-injection prevention, RFC 2047 subject encoding, TLS with verification, bounded timeouts, redacted SMTP errors.
- `delivery-idempotency`: request state machine, duplicate-key replay, key-reuse rejection, ambiguous-outcome handling, crash recovery.
- `rate-limiting`: global and per-recipient hourly/daily limits, fail-closed behavior.
- `audit-log`: SQLite audit records, privacy constraints (no bodies, hashes only), retention.
- `container-deployment`: isolation requirements — socket-only exposure, secret and config mounts scoped to the daemon container, non-root process.

### Modified Capabilities

_None — this is the first change in a new project._

## Impact

- New Go module (daemon, pure-Go SQLite driver `modernc.org/sqlite`), new Containerfile and compose definition.
- New protected config file and SMTP credential secret, owned outside the OpenClaw container.
- No existing code affected; OpenClaw integration remains draft-only until the follow-up plugin change lands.
