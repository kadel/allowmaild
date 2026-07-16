# Tasks: add-allowmaild-daemon

## 1. Project scaffolding

- [x] 1.1 Initialize Go module, repo layout (`cmd/allowmaild`, `internal/...`), and `modernc.org/sqlite` dependency
- [x] 1.2 Define config schema (sender, recipients, limits, SMTP endpoint/auth mode, socket path, state dir, retention) with strict parsing and startup validation that fails closed
- [x] 1.3 Add SQLite store: schema/migrations for request rows (key, alias, hashes, state, timestamps, result code, message ID) with index for rate-limit window queries

## 2. Request pipeline

- [x] 2.1 Implement Unix-socket-only HTTP server with `GET /v1/health` (readiness, no secrets)
- [x] 2.2 Implement `POST /v1/send` decoding with unknown-property rejection and field validation (missing/empty, byte limits, control characters; LF-only allowance in body)
- [x] 2.3 Implement exact-match alias resolution with error listing valid aliases (never addresses)
- [x] 2.4 Implement rate-limit checks from persisted rows (global + per-recipient, hourly + daily, fail closed on DB errors)
- [x] 2.5 Implement atomic key reservation after validation, including duplicate handling: terminal replay, key-reuse rejection on hash mismatch, in-flight conflict

## 3. Message construction and delivery

- [x] 3.1 Implement internal MIME construction: fixed header set, RFC 2047 subject encoding, `text/plain; charset=utf-8` body, CR/LF-injection-proof by construction
- [x] 3.2 Implement SMTP client: implicit TLS / STARTTLS per config, certificate verification, AUTH, per-phase and overall deadlines, single attempt
- [x] 3.3 Map SMTP outcomes to terminal states (`sent` / `failed` before DATA / `ambiguous` after DATA) and persist result with sanitized code and provider message ID
- [x] 3.4 Implement startup sweep of `sending` rows to `ambiguous`
- [x] 3.5 Implement retention purge of records older than configured period (default 90 days)
- [x] 3.6 Ensure all API errors and logs are redacted (no credentials, transcripts, addresses, paths)

## 4. Unit and adversarial tests

- [x] 4.1 Validation tests: unknown/case-variant aliases, missing/empty/oversized fields, control characters, unknown JSON properties, recipient-smuggling values
- [x] 4.2 Idempotency tests: duplicate keys per terminal state, hash-mismatch key reuse, concurrent in-flight duplicates, invalid requests not burning keys, crash-sweep behavior
- [x] 4.3 Rate-limit tests: boundary counts, per-recipient vs global, rejected requests not counting, persistence across restart, DB-unavailable fail-closed
- [x] 4.4 MIME tests: header set fixed, RFC 2047 encoding round-trip, injection attempts absent from output
- [x] 4.5 Audit tests: record fields present for all terminal states; no body/subject plaintext or credential strings anywhere in the DB; retention purge

## 5. Integration tests against fake SMTP

- [x] 5.1 Stand up an in-process fake SMTP server with scriptable behavior (accept, reject at each phase, hang, drop after DATA)
- [x] 5.2 Prove only the alias-mapped address appears in envelope and headers for every request shape
- [x] 5.3 Prove timeout-after-DATA yields `ambiguous`, is replayed on retry, and never re-sends
- [x] 5.4 Prove certificate-verification failure and pre-DATA failures yield `failed` with no delivery

## 6. Container deployment

- [x] 6.1 Write Containerfile (static build, non-root user, read-only rootfs) and compose definition with shared socket volume, read-only config mount, credential secret, writable state volume, no published ports
- [x] 6.2 Add container healthcheck wired to `/v1/health` via the socket
- [x] 6.3 Verify from a caller container: socket works, config/credentials unreachable, no network path to the daemon
- [x] 6.4 Exercise operational paths: restart mid-flight (sweep runs), SMTP outage (fail closed), missing secret (no startup), rollback by stopping the container

## 7. First real send

- [x] 7.1 Configure real SMTP endpoint and `self-gmail` as the only alias; deploy on the target host
- [x] 7.2 Send and verify a real message end-to-end; review audit record, duplicate-retry behavior, and logs
