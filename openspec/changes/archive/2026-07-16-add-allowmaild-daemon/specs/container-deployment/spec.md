# container-deployment

## ADDED Requirements

### Requirement: The daemon is reachable only through the shared-volume socket
The deployment SHALL run `allowmaild` in its own container with the Unix socket on a shared volume mounted into the caller's container, and SHALL publish no container ports.

#### Scenario: No published ports
- **WHEN** the compose deployment is running
- **THEN** the allowmaild container exposes no host or network ports and the socket is reachable from the caller container via the shared volume

### Requirement: Config and credentials are scoped to the daemon container
The allowlist configuration SHALL be mounted read-only into the allowmaild container only, and SMTP credentials SHALL be provided only to the allowmaild container (root-owned file mount or engine secret). The caller's container SHALL have no mount or environment path to either.

#### Scenario: Caller container cannot read secrets
- **WHEN** commands run inside the caller (OpenClaw) container
- **THEN** neither the allowlist configuration nor SMTP credentials are readable or writable through any mount or environment variable

### Requirement: The daemon runs unprivileged with fail-closed startup
The allowmaild container SHALL run its process as a non-root user with the root filesystem read-only where practical, writing only to its state volume, and SHALL exit at startup when config, credentials, or the state directory are unavailable.

#### Scenario: Missing secret prevents startup
- **WHEN** the credentials mount is absent
- **THEN** the container exits with an error and the socket is never created

#### Scenario: Unprivileged runtime
- **WHEN** the container is running
- **THEN** the daemon process UID is non-root and writes occur only under the state volume and socket volume
