## Why

allowmaild exists to give an agent a safe way to send email, but nothing connects it to an agent yet. The main goal of this change is an OpenClaw plugin, developed in this repo, that exposes a `send_email` tool backed by the daemon's Unix-socket API. One requirement for that plugin is per-message human confirmation for selected recipients: today every configured alias is equally pre-approved, so the plugin has no way to know which recipients should prompt the user, and the daemon would deliver even if the prompt were skipped. Supporting that requirement needs daemon-side enabling work.

## What Changes

**Primary deliverable — OpenClaw plugin** (`openclaw-plugin/` TypeScript subproject in this repo):
- A `send_email(recipient, subject, text)` tool that posts to the daemon socket and surfaces the daemon's outcome (`request_id`, `status`, `detail`, error codes including `valid_recipients`).
- A `before_tool_call` hook implementing the approval requirement: it queries the daemon for the recipient's approval flag and, when required, returns `requireApproval` with `allowedDecisions: ["allow-once", "deny"]` so the OpenClaw framework prompts the user. Deny or timeout blocks the call before it reaches the daemon. Persistent trust ("always allow") is deliberately not offered — it lives only in the daemon config.

**Enabling daemon work** (so the plugin can implement the approval requirement):
- Optional per-recipient `require_approval` boolean in the config schema (default `false`; existing configs behave exactly as today).
- `GET /v1/recipients`: returns configured aliases and their `requires_approval` flags — never addresses — giving the plugin recipient discovery for both the approval hook and tool ergonomics.
- Tripwire on `POST /v1/send`: an optional `approved` boolean; a send to a flagged recipient without `approved: true` is rejected with HTTP 403, code `approval_required`, before any store reservation (no rate-limit budget or idempotency key consumed). The plugin sets `approved: true` only after the framework prompt resolves. The `approved` field does not participate in idempotency content matching.
- Audit records capture the approval assertion.

Enforcement is advisory-plus-tripwire by design: the OpenClaw framework gate is the user-facing control, and the daemon-side rejection makes accidental bypass (plugin bug, naive caller) fail loudly and auditable. It is not a defense against a caller that can reach the socket directly and assert `approved: true`; keeping the agent's shell out of the socket group remains a deployment concern.

## Capabilities

### New Capabilities

- `openclaw-plugin`: the in-repo OpenClaw tool plugin — `send_email` tool contract, approval hook behavior, and its use of the discovery endpoint. The main deliverable.
- `recipient-discovery`: read-only API for enumerating configured recipient aliases and their approval requirements, with the same redaction rules as the rest of the API (aliases only, never addresses).
- `approval-gating`: the per-recipient `require_approval` config flag and the `/v1/send` tripwire (`approved` field, `approval_required` rejection, idempotency and audit interaction).

### Modified Capabilities

- `audit-log`: audit records additionally capture whether the request asserted approval.

## Impact

- `openclaw-plugin/`: new TypeScript subproject (package.json, manifest, tool + hook implementation, tests); Node toolchain added to the dev environment (nix flake).
- `internal/config`: new `RequireApproval` field on `Recipient`, no new validation failures (bool, defaults false).
- `internal/server`: new `GET /v1/recipients` handler; `approved` field on the send request schema; `approval_required` rejection path ahead of `store.Reserve`.
- `internal/store`: audit row gains an approval-asserted column (schema migration for the SQLite requests table).
- `config.example.yaml`, `README.md`: document the new flag and endpoint, plus the plugin.
- No breaking changes: existing configs, requests, and callers behave identically.
