# container-deployment

## REMOVED Requirements

### Requirement: The daemon is reachable only through the shared-volume socket
**Reason**: Container-specific phrasing of a deployment-agnostic boundary; superseded by `deployment-isolation` "The daemon is reachable only through its Unix socket by designated callers", which covers both the systemd (primary) and container (alternative) deployments.
**Migration**: See `deployment-isolation`; the compose deployment continues to satisfy it unchanged.

### Requirement: Config and credentials are scoped to the daemon container
**Reason**: Superseded by `deployment-isolation` "Config, credentials, and state are readable by the daemon identity only", which expresses the same scoping for both deployments.
**Migration**: See `deployment-isolation`; container mounts/secrets are unchanged, systemd uses `root:allowmail 0640` config and `LoadCredential`.

### Requirement: The daemon runs unprivileged with fail-closed startup
**Reason**: Superseded verbatim-in-spirit by `deployment-isolation` "The daemon runs unprivileged and fails closed at startup".
**Migration**: See `deployment-isolation`; no behavioral change in either deployment.
