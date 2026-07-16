# rate-limiting

## ADDED Requirements

### Requirement: Global and per-recipient limits are enforced
The daemon SHALL enforce configured global hourly and daily limits, and per-recipient limits where configured, counting accepted reservations (rows inserted) within a sliding window. Requests over a limit SHALL be rejected before key reservation with an error naming the limit.

#### Scenario: Global hourly limit reached
- **WHEN** the configured `per_hour` count of accepted requests already exists within the past hour
- **THEN** the next request is rejected with a rate-limit error and no row is created

#### Scenario: Rejected requests do not count
- **WHEN** requests fail validation or rate-limiting
- **THEN** they do not consume rate-limit budget

### Requirement: Limits survive restarts
Rate-limit accounting SHALL be derived from persisted request rows so that restarting the daemon cannot reset the window.

#### Scenario: Restart does not reset the budget
- **WHEN** the daemon restarts immediately after the hourly limit was reached
- **THEN** a new request within the same window is still rejected

### Requirement: Rate limiting fails closed
When the daemon cannot evaluate limits (e.g. the database is unavailable), it SHALL reject the request rather than send unchecked.

#### Scenario: Database unavailable
- **WHEN** the request-store cannot be queried
- **THEN** the send request is rejected with a service-unavailable error and no SMTP attempt occurs
