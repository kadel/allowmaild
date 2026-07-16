# recipient-allowlist

## Purpose

The recipient restriction enforced outside the calling agent: config schema, exact alias-to-address resolution with no pattern matching, fixed sender identity, and fail-closed config handling. Adding an alias to the protected config is the human approval step.

## Requirements

### Requirement: Aliases resolve by exact match only
The daemon SHALL resolve recipient aliases to email addresses solely via exact, case-sensitive lookup in the loaded configuration. Wildcards, address patterns, plus-address stripping, substring matching, and domain expansion SHALL NOT be supported.

#### Scenario: Case variation does not match
- **WHEN** the config defines alias `self-gmail` and a request specifies `SELF-GMAIL`
- **THEN** resolution fails and the request is rejected

#### Scenario: Only the mapped address is used
- **WHEN** a request specifies alias `self-gmail` mapped to `you@example.com`
- **THEN** the SMTP envelope recipient and the To header contain exactly `you@example.com` and no other address

### Requirement: Exactly one recipient per request
The daemon SHALL deliver each message to exactly one alias. Input that attempts to smuggle additional recipients (separators, encoded addresses, or list values in the `recipient` field) SHALL fail alias resolution.

#### Scenario: Recipient smuggling attempt
- **WHEN** `recipient` is `"self-gmail,attacker@example.com"` or a JSON array
- **THEN** the request is rejected because the value does not exactly match a configured alias

### Requirement: Sender identity is fixed by configuration
The From address and display name SHALL come exclusively from the daemon configuration. No request field SHALL influence the From, Reply-To, or envelope sender.

#### Scenario: Caller cannot select sender
- **WHEN** any valid send request is processed
- **THEN** the envelope sender and From header equal the configured sender address regardless of request content

### Requirement: Configuration is validated at startup and fails closed
The daemon SHALL parse and validate the allowlist configuration at startup, refusing to start when the file is missing, unparseable, defines no sender, or defines an alias with an invalid address. Configuration changes SHALL take effect only by restarting the daemon.

#### Scenario: Invalid config prevents startup
- **WHEN** the configuration file is malformed or missing required fields
- **THEN** the daemon exits with an error and never opens the socket

#### Scenario: Config edits require restart
- **WHEN** the configuration file changes while the daemon is running
- **THEN** the running daemon continues using the previously loaded configuration until restarted
