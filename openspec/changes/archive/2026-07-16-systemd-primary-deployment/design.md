# Design: systemd-primary-deployment

## Context

The archived `add-allowmaild-daemon` change (D9) specified containers as the only deployment; systemd hardening survived only as an IDEA.md footnote. Since then a hardened systemd unit was built (`deploy/systemd/allowmaild.service` + README) and the docs (DEPLOY.md, compose.yaml header, config.example.yaml) were rewritten systemd-first — leaving `openspec/specs/container-deployment/spec.md` contradicting the shipped documentation. This change realigns the specs. The daemon code is untouched; both deployments already work.

## Goals / Non-Goals

**Goals:**

- One deployment-agnostic capability (`deployment-isolation`) expressing the boundaries: socket-only reachability for designated callers, config/credential/state scoped to the daemon identity, unprivileged fail-closed runtime.
- systemd recorded as the primary deployment, containers as the supported alternative — matching DEPLOY.md.
- Scenario-level coverage of both deployments so neither regresses silently.

**Non-Goals:**

- Any change to daemon code, API, or config schema.
- Removing the container deployment (it stays supported and tested).
- Kubernetes or any third deployment shape.

## Decisions

### D1: Generalize the capability instead of adding a sibling `systemd-deployment` spec

Two parallel deployment specs would duplicate the same three boundary requirements and drift apart. One `deployment-isolation` capability states each boundary once, with per-deployment scenarios. Alternative (keep `container-deployment`, add `systemd-deployment`) rejected as duplication.

### D2: Two-group model is the normative systemd access mechanism

`allowmail` (daemon's group, sole reader of config) and `allowmail-sock` (socket clients) are separate, so socket access never implies config access. The socket file is `0666` but its directory is `0750 allowmail:allowmail-sock` — the directory is the gate, avoiding any need for the daemon to chgrp its socket. Alternative (single group + 0660 socket) rejected: clients in that group could read a group-readable config.

### D3: Credentials via systemd `LoadCredential`

The secret source stays root-only (`0600`); systemd exposes it to the service alone at the deterministic path `/run/credentials/allowmaild.service/smtp_password`, which the static config can reference. Alternative (secret file owned by the service user) rejected: a compromised daemon identity could read it from any process, and it widens who can be tricked into exposing it.

### D4: Spec-level removal of `container-deployment`, not deprecation

Keeping a deprecated spec around invites future edits landing in the wrong file. The REMOVED delta records reason and migration; the archive preserves history.

## Risks / Trade-offs

- [Container scenarios become "alternative" and may rot] → They remain requirement scenarios in `deployment-isolation`, and the compose deployment keeps its verification steps in DEPLOY.md.
- [systemd unit hardening claims drift from the actual unit file] → `systemd-analyze security allowmaild` check is part of the verification tasks; the unit lives in-repo.
- [Breaking capability rename confuses future changes referencing `container-deployment`] → REMOVED delta carries explicit migration pointers; only one archived change references the old name.

## Migration Plan

Docs are already migrated. Remaining: verify the systemd example against the scenarios on a Linux host (macOS cannot run systemd units), sync delta specs (creates `deployment-isolation`, deletes `container-deployment` main spec), archive. Rollback = revert the spec sync; the container path never stops working.

## Open Questions

- None blocking. The target-host verification of the systemd unit doubles as the still-pending real-host deployment from the previous change.
