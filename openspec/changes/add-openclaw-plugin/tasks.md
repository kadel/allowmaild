## 1. Daemon enabling work: config flag

- [x] 1.1 Add `RequireApproval bool` (`require_approval`) to `Recipient` in `internal/config/config.go`; no new validation (defaults false)
- [x] 1.2 Add config tests: flag absent defaults false, flag true parses, unknown fields still rejected
- [x] 1.3 Document the flag in `config.example.yaml`

## 2. Daemon enabling work: approval tripwire on /v1/send

- [x] 2.1 Add optional `approved` boolean to `sendRequest` in `internal/server/server.go`
- [x] 2.2 After alias lookup and before `store.Reserve`, reject flagged recipients lacking `approved: true` with HTTP 403, code `approval_required`
- [x] 2.3 Server tests: unasserted send to flagged alias → 403, no reservation (same key reusable after); asserted send proceeds; `approved: true` on unflagged alias harmless; replay with `approved: true` returns stored result; replay without it → 403

## 3. Daemon enabling work: audit approval assertion

- [x] 3.1 Add `approved` column (INTEGER 0/1, default 0) to the requests table in `internal/store/store.go` with a guarded `ALTER TABLE` migration for existing state dirs
- [x] 3.2 Thread the assertion through `ReserveParams` and persist it; expose it on `Row`
- [x] 3.3 Store tests: assertion persisted, old databases migrate on open, old rows read as unasserted

## 4. Daemon enabling work: GET /v1/recipients

- [x] 4.1 Implement the handler returning `{"recipients": [{"alias", "requires_approval"}]}` sorted by alias, aliases and flags only
- [x] 4.2 Register the route in `Handler()`
- [x] 4.3 Server tests: all aliases listed with correct flags, empty config → empty list, response contains no addresses

## 5. OpenClaw plugin (main deliverable)

- [x] 5.1 Scaffold `openclaw-plugin/`: `package.json` (ESM, `openclaw` metadata with pinned compatible version), `tsconfig.json`, `openclaw.plugin.json` manifest listing `send_email` under `contracts.tools`
- [x] 5.2 Implement a small socket client module: GET `/v1/recipients` and POST `/v1/send` over the configured Unix socket path (config schema with socket path, default `/run/allowmail/allowmail.sock`)
- [x] 5.3 Implement the `send_email` tool via `definePluginEntry` + `api.registerTool`: TypeBox parameters (`recipient`, `subject`, `text`), per-call random idempotency key, result surfacing `request_id`/`status`/`detail`, daemon error codes passed through (including `valid_recipients`)
- [x] 5.4 Implement the `before_tool_call` hook: on `send_email` calls, query `/v1/recipients`; flagged recipient → return `requireApproval` (title/description with alias and truncated subject, no body; `allowedDecisions: ["allow-once", "deny"]`); discovery failure → block the call
- [x] 5.5 Wire approval outcome to the send: set `approved: true` only for flagged recipients after `allow-once`; denied/timeout never reaches the daemon
- [x] 5.6 Plugin tests (mock socket server): prompt shown only for flagged recipients, assertion set only after approval, fail-closed on discovery error, error passthrough
- [x] 5.7 Run `plugin:build` and `plugin:validate`; commit the generated manifest (note: `openclaw plugins build/validate` only cover `defineToolPlugin` entries; this `definePluginEntry` plugin authors the manifest by hand and `plugin:validate` runs `scripts/validate.mjs`, which loads the built entry and checks it against the manifest)

## 6. Tooling and docs

- [x] 6.1 Add Node.js to the nix flake dev shell for the plugin subproject
- [x] 6.2 Update `README.md`: plugin overview and setup, `require_approval` flag, `/v1/recipients` endpoint, `approved` field and `approval_required` error, and the trust-boundary note (tripwire is not enforcement against direct socket access; keep the agent's shell out of the socket group)
- [x] 6.3 Run `go test ./...` and plugin tests; verify a config without the new flag behaves unchanged end-to-end
