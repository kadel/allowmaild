# audit-log

## MODIFIED Requirements

### Requirement: Every attempt produces an audit record
For every accepted request the daemon SHALL persist: request ID, idempotency key, timestamp, recipient alias, terminal state, sanitized SMTP result code, provider message ID when available, subject/body hashes, and whether the request asserted approval (`approved: true`).

#### Scenario: Sent message is auditable
- **WHEN** a message reaches the `sent` state
- **THEN** its audit record contains the request ID, alias, timestamp, state, and hashes

#### Scenario: Failed and ambiguous attempts are auditable
- **WHEN** a request ends `failed` or `ambiguous`
- **THEN** an audit record exists with that terminal state and a sanitized result code

#### Scenario: Approval assertion is recorded
- **WHEN** an accepted request carried `approved: true`
- **THEN** its audit record marks the request as approval-asserted

#### Scenario: Absent assertion is recorded as unasserted
- **WHEN** an accepted request did not carry `approved: true`
- **THEN** its audit record marks the request as not approval-asserted
