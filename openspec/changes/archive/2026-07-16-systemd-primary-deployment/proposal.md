# Proposal: systemd-primary-deployment

## Why

The `container-deployment` spec hardcodes containers as *the* deployment shape, but the actual isolation requirements (socket-only reachability, secrets scoped to the daemon, unprivileged fail-closed runtime) are deployment-agnostic. A hardened systemd service now exists (`deploy/systemd/`), is the documented primary deployment in DEPLOY.md, and delivers the same guarantees with simpler operations on the target host — the specs must stop contradicting the shipped docs.

## What Changes

- New capability `deployment-isolation` carrying the deployment-agnostic requirements previously expressed in container terms, with systemd as the primary deployment and containers as the supported alternative.
- Requirements are restated in terms of *boundaries* (who can reach the socket, who can read config/credentials/state, what identity the daemon runs as) with scenarios covering both deployments:
  - systemd: dedicated `allowmail` user, config `root:allowmail 0640`, secret via `LoadCredential`, socket gated by the `allowmail-sock` group on the `0750` socket directory, full unit sandboxing.
  - containers: shared socket volume, read-only config mount and engine secret in the daemon container only, no published ports, non-root user, read-only rootfs.
- **BREAKING (spec-level only)**: capability `container-deployment` is removed; its requirements fold into `deployment-isolation`. No runtime behavior changes — the daemon itself is untouched.

## Capabilities

### New Capabilities

- `deployment-isolation`: deployment-agnostic isolation requirements — socket-only reachability, config/credential/state scoping to the daemon identity, unprivileged fail-closed runtime — with systemd as primary and compose as alternative deployment.

### Modified Capabilities

- `container-deployment`: removed entirely; all requirements are superseded by `deployment-isolation`.

## Impact

- Specs: `openspec/specs/container-deployment/` removed on sync; `openspec/specs/deployment-isolation/` created.
- No code changes; `cmd/`, `internal/`, `Containerfile`, `compose.yaml`, and `deploy/systemd/` already implement both deployments.
- Docs already aligned (DEPLOY.md, deploy/systemd/README.md, compose.yaml header, config.example.yaml) — this change brings the specs up to match.
