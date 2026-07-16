# send-api

## ADDED Requirements

### Requirement: Unix socket is the only listener
The daemon SHALL serve HTTP exclusively on a configured Unix domain socket and SHALL NOT open any TCP or UDP listener.

#### Scenario: No network listener exists
- **WHEN** the daemon is running
- **THEN** the configured Unix socket accepts HTTP requests and no TCP/UDP port is bound by the process

### Requirement: Send endpoint accepts only the fixed request schema
`POST /v1/send` SHALL accept a JSON object with exactly the fields `recipient` (alias string), `subject` (string), `text` (string), and `idempotency_key` (string). Requests containing any other property (e.g. `to`, `from`, `cc`, `bcc`, `reply_to`, `headers`, `html`, `attachments`) SHALL be rejected with a validation error before any state is written.

#### Scenario: Unknown property rejected
- **WHEN** a request body includes an unrecognized property such as `"cc": "x@example.com"`
- **THEN** the daemon rejects the request with a 4xx validation error and no request row or audit send is created

#### Scenario: Valid request accepted
- **WHEN** a request contains exactly the four schema fields with valid values
- **THEN** the daemon processes it and responds with `request_id`, `status`, and `recipient` (alias)

### Requirement: Field validation rejects malformed input
The daemon SHALL reject requests where `recipient`, `subject`, `text`, or `idempotency_key` is missing or empty; where `subject` or `text` exceeds the configured byte limits; where `subject` contains any control character; or where `text` contains control characters other than line feed. Rejections SHALL occur before idempotency-key reservation.

#### Scenario: Oversized body rejected
- **WHEN** `text` exceeds `max_body_bytes`
- **THEN** the request is rejected with a 4xx error naming the violated limit and the idempotency key remains unused

#### Scenario: Control characters in subject rejected
- **WHEN** `subject` contains a CR, LF, or other control character
- **THEN** the request is rejected with a 4xx validation error

#### Scenario: Missing field rejected
- **WHEN** any of the four required fields is absent or empty
- **THEN** the request is rejected with a 4xx validation error

### Requirement: Unknown recipient aliases are rejected with valid aliases named
The daemon SHALL reject a `recipient` value that does not exactly match a configured alias, and the error SHALL list the configured alias names (never the underlying addresses).

#### Scenario: Unknown alias
- **WHEN** `recipient` is `"self-Gmail"` and only `"self-gmail"` is configured
- **THEN** the request is rejected and the error message names the valid aliases without exposing email addresses

### Requirement: Health endpoint reports readiness without secrets
`GET /v1/health` SHALL report whether the daemon is ready to accept send requests (config loaded, database reachable) and SHALL NOT include credentials, recipient addresses, or SMTP details.

#### Scenario: Healthy daemon
- **WHEN** the daemon is running with valid config and reachable database
- **THEN** `GET /v1/health` returns a success status containing no secret or address data

### Requirement: Errors are sanitized
All error responses SHALL be structured and redacted: they SHALL NOT contain SMTP credentials, SMTP conversation transcripts, resolved email addresses, or internal file paths.

#### Scenario: SMTP failure is redacted
- **WHEN** SMTP delivery fails with a server error
- **THEN** the API response contains a sanitized status and request ID but no SMTP transcript or credential material
