# deployment-isolation

## Purpose

Deployment isolation requirements: the daemon is reachable only through its Unix socket by designated callers, config/credentials/state are scoped to the daemon identity, and the process runs unprivileged with fail-closed startup. The deployment is a hardened systemd service.

## Requirements

### Requirement: A hardened systemd service is the deployment
The project SHALL provide a systemd unit as the documented deployment (dedicated system user, sandboxing directives, credentials via `LoadCredential`).

#### Scenario: systemd deployment is documented and complete
- **WHEN** an operator follows deploy/systemd/README.md
- **THEN** the daemon is installed as a systemd service from `deploy/systemd/` with the documented user, group, config, and credential layout

### Requirement: The daemon is reachable only through its Unix socket by designated callers
The deployment SHALL expose the daemon exclusively via its Unix socket, SHALL grant socket access only to explicitly designated caller identities, and SHALL expose no network listener or published port.

#### Scenario: Socket gated by the client group
- **WHEN** the systemd deployment is running
- **THEN** members of the `allowmail-sock` group can connect to the socket via the `0750` socket directory, while accounts outside the group cannot traverse to it, and no TCP/UDP port is bound

### Requirement: Config, credentials, and state are readable by the daemon identity only
The allowlist configuration SHALL be readable only by the daemon's identity (and writable only by the administrator), SMTP credentials SHALL be provided only to the daemon process, and the state directory SHALL be private to the daemon. Caller identities SHALL have no filesystem or environment path to any of them.

#### Scenario: Callers cannot read config, secret, or state
- **WHEN** a member of the socket-access group attempts to read `/etc/allowmaild/config.yaml`, the credential source file, or the state directory
- **THEN** every attempt is denied; the config is `root:allowmail 0640`, the secret source is root-only and delivered via `LoadCredential`, and the state directory is mode `0700`

### Requirement: The daemon runs unprivileged and fails closed at startup
The daemon SHALL run as a dedicated non-root identity with write access only to its state and socket locations, and SHALL exit at startup — never opening the socket — when config, credentials, or the state directory are unavailable.

#### Scenario: Missing secret prevents startup
- **WHEN** the credential is absent (missing `LoadCredential` source)
- **THEN** the daemon exits with an error and the socket is never created

#### Scenario: Unprivileged runtime
- **WHEN** the daemon is running
- **THEN** its process runs as the dedicated non-root identity and writes occur only under the state and socket locations
