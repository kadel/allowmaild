## Context

allowmaild exposes a Unix-socket HTTP API (`GET /v1/health`, `POST /v1/send`) where each request names a recipient alias resolved against a root-owned YAML config. The main goal of this change is connecting it to an OpenClaw agent: a plugin in this repo exposing a `send_email` tool. One plugin requirement is per-message human confirmation for selected recipients; today every configured alias is equally pre-approved (adding the alias to the config has been the sole approval step), so the daemon needs enabling work — a per-recipient flag, a discovery endpoint, and a tripwire — for the plugin to implement that requirement.

OpenClaw provides a framework-level approval mechanism: a plugin's `before_tool_call` hook runs after the model selects a tool and before execution, and may return a `requireApproval` object. The framework renders the prompt (locally or routed to a channel), and deny, timeout, or malformed decisions fail closed. `allow-always` persistence is explicitly the plugin's responsibility — the framework does not persist it.

## Goals / Non-Goals

**Goals:**
- An in-repo OpenClaw plugin exposing `send_email`, with good tool ergonomics (recipient discovery, daemon outcomes and error codes surfaced to the model).
- The approval requirement implemented end-to-end: flagged recipients gated through the framework approval prompt, deny/timeout blocking the call before it reaches the daemon.
- Daemon enabling work: per-recipient opt-in approval flag (zero behavior change for configs that do not use it), a discovery endpoint that never exposes addresses, and a tripwire so an unasserted send to a flagged recipient fails loudly instead of silently delivering.

**Non-Goals:**
- Hard enforcement against a caller with direct socket access: a process that can reach the socket can set `approved: true` itself. Defending against that requires a second, differently-permissioned approval channel (two-phase pending state) and is explicitly deferred. The deployment mitigation is keeping the agent's shell out of the `allowmail-sock` group / sandboxing the exec tool.
- Approval UI of any kind in this repo — the OpenClaw framework owns the prompt.
- Config hot-reload; the existing restart-to-apply rule is unchanged.

## Decisions

### Plugin

**1. The plugin uses `definePluginEntry`, not `defineToolPlugin`.**
`defineToolPlugin` builds tool-only plugins with no hooks; this plugin needs both `api.registerTool` (send_email) and `api.on("before_tool_call", ...)`, so it uses the general entry point. Manifest lists the tool under `contracts.tools`.

**2. The hook queries `/v1/recipients` per call; `allowedDecisions` is `["allow-once", "deny"]`.**
A per-call GET on a local Unix socket is cheap and never stale (config changes require a daemon restart anyway), so no cache. `allow-always` is deliberately not offered: persistent trust lives only in the daemon config (`require_approval: false`), which the docs endorse ("restrict … when persistent trust is unsafe") — the plugin implements no persistence. The prompt title/description name the recipient alias and subject (truncated to the 512-char description limit), never the body. If the recipients query itself fails, the hook fails closed and blocks the call.

**3. `execute` posts to the socket with a generated idempotency key and sets `approved: true` only for flagged recipients whose prompt resolved allow-once.**
The plugin generates a random idempotency key per tool call (agent-level retries are new requests; the daemon's rate limits bound the damage). The tool result surfaces `request_id`, `status`, and `detail` from the daemon response so the model can report the outcome; daemon error codes (`approval_required`, `rate_limited`, `unknown_recipient` with its `valid_recipients` list) pass through as tool errors.

**4. Node toolchain via the nix flake.**
`openclaw-plugin/` is a TypeScript ESM subproject (own `package.json`, `openclaw.plugin.json` manifest). The flake dev shell grows Node.js so the Go daemon and the plugin build from one environment; the Go build is untouched.

### Daemon enabling work

**5. Advisory + tripwire, not two-phase enforcement.**
The `before_tool_call` gate is the user-facing control; the daemon additionally rejects flagged sends lacking `approved: true` with HTTP 403, code `approval_required`. Alternatives: pure advisory (daemon delivers regardless — a plugin bug silently bypasses the prompt) and daemon-enforced two-phase (pending state + separate approval socket — real enforcement, but doubles the daemon's state machine for a single-user deployment whose remaining hole is fixable at the deployment layer). The tripwire costs a few lines, makes the contract explicit in the API, and does not preclude upgrading to two-phase later: the config flag and discovery endpoint are identical in both models.

**6. `require_approval` defaults to `false`.**
Existing aliases and non-plugin callers behave exactly as today, preserving "adding the alias is the approval." Approval is opt-in per recipient.

**7. The approval check runs before `store.Reserve`, unconditionally.**
A rejected request consumes no rate-limit budget and no idempotency key. Consequence: a replay of an already-terminal request to a flagged recipient must also carry `approved: true` to reach the replay path — without it, the request gets 403 before the store is consulted. This is deliberate: it keeps the check a pure precondition (fail closed, no store dependency), and a well-behaved caller replays the identical request body anyway. The alternative — checking after Reserve so replays skip the gate — would let the approval outcome depend on store state.

**8. `approved` is not part of idempotency content matching.**
Key-reuse detection continues to compare alias, subject hash, and body hash only. The flag gates new delivery attempts; it is not message content.

**9. `GET /v1/recipients` returns aliases and flags only, sorted.**
Response: `{"recipients": [{"alias": "...", "requires_approval": bool}, ...]}` sorted by alias. Addresses are never returned, consistent with the existing redaction rules (`AliasNames` already never returns addresses). The endpoint doubles as recipient discovery for building the tool schema, replacing guess-and-fail via `valid_recipients` errors.

**10. Audit rows record the approval assertion.**
The SQLite requests table gains an `approved` integer column (0/1, default 0), set from the request field on accepted sends. Schema creation is idempotent today; adding the column uses `ALTER TABLE` guarded by a column-existence check so existing state directories migrate on startup.

## Risks / Trade-offs

- [Agent with direct socket access can assert `approved: true`] → Accepted by design (see Non-Goals); mitigate in deployment by restricting the socket group and sandboxing the agent's exec tool. Upgrade path to two-phase enforcement stays open.
- [Plugin and daemon flag semantics drift (e.g. plugin caches, config restarts)] → No plugin-side cache; per-call discovery query; hook fails closed when the query fails.
- [Schema migration on existing state dirs] → Guarded `ALTER TABLE` add-column; column has a default so old rows read as unasserted.
- [Replay without `approved: true` returns 403 instead of the stored terminal state] → Documented behavior (Decision 7); the plugin always sends the flag after an approval, so its replays are identical requests.
- [OpenClaw API surface shifts (hook/approval contract)] → Plugin pins compatible OpenClaw versions in `package.json` `openclaw` metadata; contract is validated by `plugin:validate` at build time.

## Migration Plan

1. Daemon changes ship first; deploying them with an unchanged config is a no-op (flag defaults false, new endpoint is read-only, `approved` field optional).
2. Startup migrates the SQLite schema (add `approved` column) automatically; rollback to a prior binary is safe because old code ignores the extra column.
3. Flag recipients in config and restart the daemon; the plugin picks the change up on its next per-call query.

## Open Questions

- Exact OpenClaw version floor for the `requireApproval` hook contract — pin when the plugin scaffold is created against a concrete SDK release.
