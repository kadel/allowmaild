# smtp-delivery

## Purpose

Message construction and transport: internal MIME building with a fixed header set, header-injection prevention, RFC 2047 subject encoding, verified TLS, bounded timeouts, and exactly one delivery attempt per accepted request.

## Requirements

### Requirement: The daemon constructs the complete MIME message internally
The daemon SHALL build the entire message itself with a fixed header set (From, To, Subject, Date, Message-ID, MIME-Version, Content-Type). No caller input SHALL be interpreted as a header, and header values built from caller input (Subject) SHALL be constructed so that CR/LF injection is impossible.

#### Scenario: Header injection attempt neutralized
- **WHEN** a request's subject or text attempts to introduce header lines (validation rejects CR/LF, and construction encodes any residual specials)
- **THEN** the transmitted message contains only the fixed header set with the injected content absent

#### Scenario: Body is plain text only
- **WHEN** any message is constructed
- **THEN** its Content-Type is `text/plain; charset=utf-8` with no multipart structure or attachments

### Requirement: Non-ASCII subjects are RFC 2047 encoded
Subjects containing non-ASCII characters SHALL be encoded as RFC 2047 encoded words so that UTF-8 subjects (e.g. Czech diacritics) transmit correctly.

#### Scenario: UTF-8 subject
- **WHEN** the subject is `"Připomínka: doména"`
- **THEN** the Subject header is a valid RFC 2047 encoded word that decodes to the original text

### Requirement: SMTP uses verified TLS with bounded timeouts
The daemon SHALL deliver via the configured SMTP endpoint using TLS (implicit TLS or STARTTLS per configuration) with certificate verification enabled, and SHALL apply bounded connection, read, and write deadlines plus an overall per-attempt deadline.

#### Scenario: Certificate verification failure
- **WHEN** the SMTP server presents an untrusted certificate
- **THEN** delivery is aborted before authentication and the request is recorded as failed

#### Scenario: Hung server does not hang the daemon
- **WHEN** the SMTP server stops responding mid-conversation
- **THEN** the attempt terminates at the configured deadline and the request reaches a terminal state

### Requirement: One SMTP attempt per accepted request
The daemon SHALL make exactly one SMTP delivery attempt per accepted request and SHALL NOT retry internally.

#### Scenario: Transient failure is not retried internally
- **WHEN** the single SMTP attempt fails
- **THEN** the daemon records the terminal state and returns; no second connection is made for that request
