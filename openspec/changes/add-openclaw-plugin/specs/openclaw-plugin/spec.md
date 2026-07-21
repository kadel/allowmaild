# openclaw-plugin

## ADDED Requirements

### Requirement: Plugin exposes a send_email tool backed by the daemon socket
The repo SHALL contain an OpenClaw plugin (TypeScript subproject at `openclaw-plugin/`) registering a `send_email` tool with parameters `recipient`, `subject`, and `text`. The tool's execute handler SHALL POST to the daemon's Unix socket `/v1/send` with a per-call generated idempotency key and SHALL surface the daemon's `request_id`, `status`, and `detail` in the tool result.

#### Scenario: Successful send surfaces the daemon outcome
- **WHEN** the agent calls `send_email` for an unflagged recipient and the daemon reports `sent`
- **THEN** the tool result includes the request ID and `sent` status from the daemon response

#### Scenario: Daemon errors pass through sanitized
- **WHEN** the daemon rejects the request (for example `unknown_recipient` or `rate_limited`)
- **THEN** the tool reports the daemon's error code and message, including `valid_recipients` for unknown aliases

### Requirement: Flagged recipients are gated through the framework approval prompt
The plugin SHALL register a `before_tool_call` hook that, for `send_email` calls, queries `GET /v1/recipients` and, when the target recipient has `requires_approval: true`, returns a `requireApproval` object with `allowedDecisions` restricted to `["allow-once", "deny"]`. The prompt SHALL name the recipient alias and subject and SHALL NOT include the message body.

#### Scenario: Approval prompt shown for flagged recipient
- **WHEN** the agent calls `send_email` for a recipient whose `requires_approval` is `true`
- **THEN** the hook returns `requireApproval` and the framework prompts the user before the tool executes

#### Scenario: Unflagged recipient proceeds without prompt
- **WHEN** the agent calls `send_email` for a recipient whose `requires_approval` is `false`
- **THEN** the hook returns no approval requirement and the tool executes directly

#### Scenario: Persistent trust is not offered in the prompt
- **WHEN** the approval prompt is presented
- **THEN** the available decisions are exactly `allow-once` and `deny`; changing a recipient's standing trust requires editing the daemon config

### Requirement: The approved assertion is set only after an approval resolves
The tool SHALL send `approved: true` to the daemon only for recipients that require approval and whose framework prompt resolved `allow-once`. Denied, timed-out, or otherwise unresolved prompts SHALL block the call before any request reaches the daemon.

#### Scenario: Approved call carries the assertion
- **WHEN** the user resolves the prompt with `allow-once`
- **THEN** the resulting `/v1/send` request carries `approved: true`

#### Scenario: Denial never reaches the daemon
- **WHEN** the user denies the prompt or it times out
- **THEN** no `/v1/send` request is made and the tool call reports it was blocked

### Requirement: The plugin fails closed when discovery is unavailable
When the `GET /v1/recipients` query fails during the `before_tool_call` hook, the plugin SHALL block the `send_email` call rather than proceeding without approval information.

#### Scenario: Daemon unreachable at hook time
- **WHEN** the recipients query fails (daemon down, socket missing)
- **THEN** the `send_email` call is blocked and the failure is reported to the agent
