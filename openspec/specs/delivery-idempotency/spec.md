# delivery-idempotency

## Purpose

The request state machine (`sending → sent | failed | ambiguous`) that guarantees at-most-once delivery per idempotency key, with duplicate-key replay, key-reuse rejection, honest ambiguous outcomes, and crash recovery.

## Requirements

### Requirement: Keys are reserved atomically after validation
The daemon SHALL reserve the idempotency key by inserting a request row (key, alias, subject hash, body hash, timestamp, state `sending`) in a single transaction, only after validation and rate-limit checks pass. Requests failing those checks SHALL NOT consume the key.

#### Scenario: Invalid request does not burn the key
- **WHEN** a request fails validation and is then resubmitted corrected with the same idempotency key
- **THEN** the corrected request is processed normally

### Requirement: Outcomes map to exactly one terminal state
Each accepted request SHALL end in exactly one of: `sent` (SMTP server accepted the message after DATA; provider message ID stored when available), `failed` (definitive failure before DATA — provably not delivered), or `ambiguous` (timeout or connection loss after DATA — delivery unknown).

#### Scenario: Acceptance after DATA
- **WHEN** the SMTP server returns a success reply to the message data
- **THEN** the request state becomes `sent`

#### Scenario: Failure before DATA
- **WHEN** connection, TLS, authentication, or recipient negotiation fails before message data is sent
- **THEN** the request state becomes `failed`

#### Scenario: Timeout after DATA
- **WHEN** the connection times out or drops after message data was transmitted but before a reply is read
- **THEN** the request state becomes `ambiguous` and the response tells the caller delivery is uncertain and must not be retried automatically

### Requirement: Duplicate keys replay the stored result
A request whose idempotency key matches a terminal-state row with identical subject and body hashes SHALL receive the stored response verbatim without any new SMTP attempt. A matching key with different hashes SHALL be rejected as key reuse. A matching key still in `sending` SHALL receive an in-flight conflict response.

#### Scenario: Retry after success
- **WHEN** a duplicate of a `sent` request arrives
- **THEN** the original success response is returned and no email is sent

#### Scenario: Key reuse with different content
- **WHEN** a request reuses an existing key but its subject or body hash differs
- **THEN** the request is rejected with a key-reuse error and nothing is sent

#### Scenario: Concurrent duplicate
- **WHEN** a duplicate arrives while the original is still `sending`
- **THEN** the daemon responds with an in-flight conflict and makes no additional SMTP attempt

### Requirement: Crash recovery sweeps in-flight rows to ambiguous
On startup the daemon SHALL transition every row still in `sending` to `ambiguous`, because a crash mid-attempt leaves delivery unknowable.

#### Scenario: Restart after crash mid-send
- **WHEN** the daemon restarts and finds a row in `sending`
- **THEN** that row becomes `ambiguous` and a duplicate request for its key replays the ambiguous result
