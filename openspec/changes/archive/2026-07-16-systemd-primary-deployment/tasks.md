# Tasks: systemd-primary-deployment

## 1. Verify the systemd deployment against the new scenarios

- [x] 1.1 On a Linux host (or a systemd container), install per DEPLOY.md and confirm: socket reachable for an `allowmail-sock` member, denied for others; no TCP/UDP listener
- [x] 1.2 Confirm config (`root:allowmail 0640`), credential source (root-only, via LoadCredential), and state dir (`0700`) are all unreadable by a socket-group member
- [x] 1.3 Confirm fail-closed startup: remove the credential source, `systemctl start` fails, socket never created
- [x] 1.4 Run `systemd-analyze security allowmaild` and record the score in deploy/systemd/README.md if notable

## 2. Docs consistency

- [x] 2.1 Re-check DEPLOY.md, deploy/systemd/README.md, compose.yaml, and config.example.yaml for any remaining "containers are primary" phrasing or stale paths

## 3. Spec sync and archive

- [x] 3.1 Sync delta specs to main specs (creates `openspec/specs/deployment-isolation/`, removes `openspec/specs/container-deployment/`)
- [x] 3.2 Validate specs (`openspec validate --specs`) and archive the change
