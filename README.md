# allowmaild

An email-sending daemon that can only deliver to an allowlisted set of
recipients. Built to give an autonomous agent (or any semi-trusted local
process) the ability to send mail without ever holding SMTP credentials or
choosing an address.

The caller talks to a Unix socket and names a recipient *alias*; the daemon
resolves the alias from its own config, builds the entire MIME message
itself, and delivers over SMTP with credentials only it can read.

```sh
curl -s --unix-socket /run/allowmail/allowmail.sock \
  -X POST http://d/v1/send -H 'Content-Type: application/json' \
  -d '{"recipient":"self-gmail","subject":"Hi","text":"Hello.","idempotency_key":"01J..."}'
```

## Properties

- **Allowlist only**: unknown aliases are rejected; the caller cannot supply
  `to`, `from`, `cc`, `bcc`, headers, HTML, or attachments. Adding a
  recipient means editing the root-owned config and restarting — that restart
  is the human approval step.
- **Socket-only surface**: no TCP listener exists in the code; access is
  gated by filesystem group membership on the socket directory.
- **Idempotent sends**: every request carries an idempotency key backed by a
  SQLite state machine, so retries can never produce duplicate emails.
- **Rate limits**: global and per-recipient hourly/daily caps.
- **Private audit log**: request IDs, aliases, states, and content hashes —
  never subjects, bodies, addresses, or credentials, in the DB or the logs.
- **Fail-closed startup**: missing config, credential, or state directory
  means the daemon exits before the socket ever exists.

## API

- `GET /v1/health` — store liveness.
- `GET /v1/recipients` — configured aliases and their `requires_approval`
  flags, sorted by alias: `{"recipients":[{"alias":"dad","requires_approval":true},...]}`.
  Never returns addresses or any other config value.
- `POST /v1/send` — deliver to an alias. Body:
  `recipient`, `subject`, `text`, `idempotency_key`, and optional `approved`.

### Per-recipient approval

A recipient can be flagged in the config:

```yaml
recipients:
  dad:
    address: dad@example.com
    require_approval: true
```

Sends to a flagged alias must assert `"approved": true` or they are rejected
with HTTP 403, code `approval_required`, before any rate-limit budget or
idempotency key is consumed. The `approved` field never participates in
idempotency content matching, and the audit log records whether each accepted
request asserted approval. Unflagged recipients are unaffected; configs
written before the flag existed behave identically.

**Trust boundary:** the `approved` field is a tripwire, not enforcement. It
makes an accidental bypass (a plugin bug, a naive caller) fail loudly and
auditably, but any process that can reach the socket can assert
`approved: true` itself. The human prompt lives in the OpenClaw plugin below;
keep the agent's shell out of the socket group so the plugin is the only path
to the daemon.

## OpenClaw plugin

[`openclaw-plugin/`](openclaw-plugin/) is a TypeScript OpenClaw plugin
exposing a `send_email(recipient, subject, text)` tool backed by the daemon
socket. Its `before_tool_call` hook queries `/v1/recipients` per call and, for
flagged recipients, has the OpenClaw framework prompt the user
(`allow-once`/`deny` only — persistent trust lives solely in the daemon
config). Only an `allow-once` decision sets `approved: true` on the send;
denials and timeouts never reach the daemon, and if recipient discovery fails
the call is blocked.

```sh
cd openclaw-plugin
npm install
npm run plugin:build && npm run plugin:validate
npm test
```

Install it into OpenClaw with `openclaw plugins install ./openclaw-plugin`,
then set the socket path in the plugin config if it differs from the default
`/run/allowmail/allowmail.sock`.

## Build

```sh
nix build .#allowmaild        # or: CGO_ENABLED=0 go build ./cmd/allowmaild
go test ./...
```

Prebuilt Linux binaries (amd64/arm64) are published on the
[releases page](https://github.com/kadel/allowmaild/releases): tagged
versions for `v*` tags, plus a rolling `latest` prerelease tracking `main`.

## Deploy

The daemon ships as a hardened systemd service (dedicated user, two-group
socket/config separation, `LoadCredential` for the SMTP password, full unit
sandboxing — `systemd-analyze security` scores 1.5 "OK"). Setup commands,
the permission model, and post-install verification live in
[`deploy/systemd/README.md`](deploy/systemd/README.md).

Configuration reference: [`config.example.yaml`](config.example.yaml).
Specs for every behavior live under [`openspec/specs/`](openspec/specs/).
