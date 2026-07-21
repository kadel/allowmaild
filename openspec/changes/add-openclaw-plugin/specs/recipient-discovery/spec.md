# recipient-discovery

## ADDED Requirements

### Requirement: Recipients endpoint enumerates aliases and approval flags
The daemon SHALL serve `GET /v1/recipients` returning a JSON object of the form `{"recipients": [{"alias": <string>, "requires_approval": <bool>}, ...]}` listing every configured alias, sorted by alias name.

#### Scenario: All configured aliases are listed
- **WHEN** the config defines aliases `dad` (require_approval true) and `self-gmail` (flag absent)
- **THEN** `GET /v1/recipients` returns both entries sorted by alias, with `requires_approval` `true` and `false` respectively

#### Scenario: Empty recipient set
- **WHEN** the config defines no recipients
- **THEN** the endpoint returns `{"recipients": []}`

### Requirement: Discovery responses never contain addresses or secrets
The recipients endpoint SHALL return only alias names and approval flags. It SHALL NOT return email addresses, sender identity, limits, or any other configuration value.

#### Scenario: No address in response body
- **WHEN** `GET /v1/recipients` is served for any configuration
- **THEN** the response body contains no email address and no configuration value beyond aliases and `requires_approval` flags

### Requirement: Discovery reflects the loaded configuration only
The endpoint SHALL report the configuration loaded at startup. Changes to the config file SHALL NOT be visible until the daemon restarts, matching the existing restart-to-apply rule.

#### Scenario: Config edit invisible until restart
- **WHEN** the config file changes while the daemon is running
- **THEN** `GET /v1/recipients` continues to reflect the previously loaded configuration until restart
