# approval-gating

## ADDED Requirements

### Requirement: Recipients can be flagged as requiring per-message approval
The configuration SHALL accept an optional boolean `require_approval` on each recipient alias, defaulting to `false` when absent. A config that does not use the flag SHALL behave identically to one written before the flag existed.

#### Scenario: Flag absent means no approval needed
- **WHEN** an alias is configured without `require_approval` and a valid send request names it
- **THEN** the request is processed exactly as before this capability existed

#### Scenario: Flag parses and loads
- **WHEN** an alias is configured with `require_approval: true`
- **THEN** the daemon starts normally and treats that alias as approval-required

### Requirement: Sends to flagged recipients must assert approval
`POST /v1/send` SHALL accept an optional boolean `approved` field. When the resolved recipient has `require_approval: true` and the request does not carry `approved: true`, the daemon SHALL reject the request with HTTP 403 and error code `approval_required` without attempting delivery.

#### Scenario: Unasserted send to flagged recipient is rejected
- **WHEN** a send request names a flagged alias and omits `approved` or sets it to `false`
- **THEN** the response is HTTP 403 with error code `approval_required` and no SMTP delivery is attempted

#### Scenario: Asserted send to flagged recipient proceeds
- **WHEN** a send request names a flagged alias with `approved: true`
- **THEN** the request proceeds through the normal reserve-and-deliver flow

#### Scenario: Approved flag on unflagged recipient is harmless
- **WHEN** a send request names an alias with `require_approval: false` and sets `approved: true`
- **THEN** the request is processed normally

### Requirement: Approval rejection consumes no rate-limit budget or idempotency key
The approval check SHALL run before the request is reserved in the store. A request rejected with `approval_required` SHALL NOT count against any rate limit and SHALL NOT bind its idempotency key.

#### Scenario: Rejected request leaves no reservation
- **WHEN** a send to a flagged recipient is rejected with `approval_required`
- **THEN** a subsequent request reusing the same idempotency key with `approved: true` is treated as a fresh request, not a replay or key reuse

### Requirement: Approval assertion is excluded from idempotency content matching
Key-reuse detection SHALL compare only the alias, subject hash, and body hash. The `approved` field SHALL NOT participate in content matching, and a replayed terminal request SHALL return its stored result regardless of how it terminated.

#### Scenario: Replay with identical content succeeds despite approved flag
- **WHEN** a request to a flagged recipient completed with `approved: true`, and the identical request (same key, alias, subject, body, `approved: true`) is submitted again
- **THEN** the stored terminal response is replayed without a new delivery attempt

#### Scenario: Replay without approval assertion is rejected before the store is consulted
- **WHEN** a request to a flagged recipient completed, and the same key is resubmitted without `approved: true`
- **THEN** the response is HTTP 403 `approval_required` and the store is not consulted
