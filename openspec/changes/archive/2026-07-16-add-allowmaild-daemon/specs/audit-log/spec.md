# audit-log

## ADDED Requirements

### Requirement: Every attempt produces an audit record
For every accepted request the daemon SHALL persist: request ID, idempotency key, timestamp, recipient alias, terminal state, sanitized SMTP result code, provider message ID when available, and subject/body hashes.

#### Scenario: Sent message is auditable
- **WHEN** a message reaches the `sent` state
- **THEN** its audit record contains the request ID, alias, timestamp, state, and hashes

#### Scenario: Failed and ambiguous attempts are auditable
- **WHEN** a request ends `failed` or `ambiguous`
- **THEN** an audit record exists with that terminal state and a sanitized result code

### Requirement: Audit records contain no message content or secrets
Audit records SHALL NOT contain subject or body plaintext, resolved recipient addresses beyond what configuration already holds, SMTP credentials, or SMTP conversation transcripts. Subject and body are represented by hashes only.

#### Scenario: No body text in storage
- **WHEN** any message has been processed
- **THEN** searching the database for the message body or credential strings finds nothing

### Requirement: Records are retained for a bounded period
The daemon SHALL delete audit records older than the configured retention period (default 90 days).

#### Scenario: Expired records purged
- **WHEN** records older than the retention period exist and the retention task runs
- **THEN** those records are removed while newer records remain
