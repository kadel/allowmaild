# deployment-isolation

## ADDED Requirements

### Requirement: A hardened systemd service is the primary deployment
The project SHALL provide a systemd unit as the primary, documented deployment (dedicated system user, sandboxing directives, credentials via `LoadCredential`), and a container/compose definition as a supported alternative delivering the same isolation properties.

#### Scenario: systemd deployment is documented and complete
- **WHEN** an operator follows DEPLOY.md
- **THEN** the primary path installs the daemon as a systemd service from `deploy/systemd/`, and the container path remains available with equivalent guarantees

### Requirement: The daemon is reachable only through its Unix socket by designated callers
The deployment SHALL expose the daemon exclusively via its Unix socket, SHALL grant socket access only to explicitly designated caller identities, and SHALL expose no network listener or published port in any deployment.

#### Scenario: systemd — socket gated by the client group
- **WHEN** the systemd deployment is running
- **THEN** members of the `allowmail-sock` group can connect to the socket via the `0750` socket directory, while accounts outside the group cannot traverse to it, and no TCP/UDP port is bound

#### Scenario: containers — socket shared by volume only
- **WHEN** the compose deployment is running
- **THEN** the socket is reachable from the caller container via the shared volume, and the allowmaild container publishes no host or network ports

### Requirement: Config, credentials, and state are readable by the daemon identity only
The allowlist configuration SHALL be readable only by the daemon's identity (and writable only by the administrator), SMTP credentials SHALL be provided only to the daemon process, and the state directory SHALL be private to the daemon. Caller identities SHALL have no filesystem or environment path to any of them.

#### Scenario: systemd — callers cannot read config, secret, or state
- **WHEN** a member of the socket-access group attempts to read `/etc/allowmaild/config.yaml`, the credential source file, or the state directory
- **THEN** every attempt is denied; the config is `root:allowmail 0640`, the secret source is root-only and delivered via `LoadCredential`, and the state directory is mode `0700`

#### Scenario: containers — caller container has no path to secrets
- **WHEN** commands run inside the caller container
- **THEN** neither the allowlist configuration nor SMTP credentials are readable or writable through any mount or environment variable

### Requirement: The daemon runs unprivileged and fails closed at startup
The daemon SHALL run as a dedicated non-root identity with write access only to its state and socket locations, and SHALL exit at startup — never opening the socket — when config, credentials, or the state directory are unavailable.

#### Scenario: Missing secret prevents startup
- **WHEN** the credential is absent (missing `LoadCredential` source or missing container secret)
- **THEN** the daemon exits with an error and the socket is never created

#### Scenario: Unprivileged runtime
- **WHEN** the daemon is running under either deployment
- **THEN** its process runs as the dedicated non-root identity and writes occur only under the state and socket locations
