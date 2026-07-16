# Deploying allowmaild as a systemd service

allowmaild runs as a hardened systemd service. The isolation goal: the caller
can reach the Unix socket and nothing else — not the config, not the SMTP
credential, not the state database. The boundaries are expressed with users
and file modes:

| What | Who can touch it |
|---|---|
| `/etc/allowmaild/config.yaml` | root writes; only the `allowmail` user reads |
| `/etc/allowmaild/smtp_password` | root only; handed to the service via systemd `LoadCredential` |
| `/run/allowmail/allowmail.sock` | members of `allowmail-sock` (the directory is the gate) |
| `/var/lib/allowmaild` (SQLite state) | `allowmail` user only |

## 1. Users and groups

```sh
useradd --system --user-group --no-create-home --shell /usr/sbin/nologin allowmail
groupadd --system allowmail-sock

# Every account that may send mail joins the socket group — nothing else:
usermod -aG allowmail-sock openclaw
```

`allowmail` (the daemon's supplementary group) gates the config;
`allowmail-sock` (the daemon's primary group) gates the socket. Do **not**
add anyone else to the `allowmail` group; membership there means read access
to the config (and therefore the recipient allowlist).

## 2. Config and secret

Copy `config.example.yaml` to `config.yaml` and set your sender address,
recipient aliases, and SMTP provider values. The systemd-specific values are:

```yaml
smtp:
  password_file: /run/credentials/allowmaild.service/smtp_password
socket_path: /run/allowmail/allowmail.sock
socket_mode: "0666"   # the 0750 directory is the access gate, not the socket
state_dir: /var/lib/allowmaild
```

```sh
install -d -m 0755 /etc/allowmaild

# Config: root-owned, group-readable by the daemon's own group only.
install -o root -g allowmail -m 0640 config.yaml /etc/allowmaild/config.yaml

# SMTP password: root-only. systemd reads it and exposes it exclusively to
# the service at /run/credentials/allowmaild.service/smtp_password.
install -o root -g root -m 0600 smtp_password /etc/allowmaild/smtp_password
```

## 3. Binary and unit

```sh
# from a checkout: nix build .#allowmaild   (or: CGO_ENABLED=0 go build ./cmd/allowmaild)
install -o root -g root -m 0755 result/bin/allowmaild /usr/local/bin/allowmaild
install -o root -g root -m 0644 allowmaild.service /etc/systemd/system/allowmaild.service
systemctl daemon-reload
systemctl enable --now allowmaild
```

## 4. Verify the boundary

```sh
# As a member of allowmail-sock (e.g. openclaw): socket works, config doesn't.
sudo -u openclaw curl -s --unix-socket /run/allowmail/allowmail.sock http://d/v1/health
sudo -u openclaw cat /etc/allowmaild/config.yaml               # Permission denied
sudo -u openclaw cat /etc/allowmaild/smtp_password             # Permission denied
sudo -u openclaw ls /var/lib/allowmaild                        # Permission denied

# As any user NOT in allowmail-sock: even the socket is unreachable.
sudo -u nobody curl -s --unix-socket /run/allowmail/allowmail.sock http://d/v1/health
```

## 5. First real send

As a member of `allowmail-sock`, with a fresh idempotency key:

```sh
curl -s --unix-socket /run/allowmail/allowmail.sock \
  -X POST http://d/v1/send -H 'Content-Type: application/json' \
  -d '{"recipient":"self-gmail","subject":"allowmaild first send","text":"Hello from allowmaild.","idempotency_key":"first-send-1"}'
```

Verify, in order:

1. Response is `"status":"sent"` with a `request_id` and `message_id`.
2. The message arrives in the recipient's inbox (check spam the first time —
   a brand-new automated sender often lands there once; mark it "Not spam");
   `From:` is exactly your configured sender, `To:` only the allowlisted
   address.
3. **Duplicate retry**: re-run the exact same curl. The response must be
   byte-identical (same `request_id`) and no second email arrives.
4. **Audit record**:
   ```sh
   sudo -u allowmail sqlite3 /var/lib/allowmaild/allowmaild.db \
     "SELECT key, alias, state, result_code, message_id, datetime(created_at,'unixepoch') FROM requests;"
   ```
   Expect the alias, state `sent`, code `250`, hashes only — no subject/body
   text anywhere in the DB.
5. **Logs are redacted**: `journalctl -u allowmaild` shows request IDs,
   aliases, states — no addresses, subjects, credentials, or paths.

## 6. Operations

- **Add a recipient** = edit `/etc/allowmaild/config.yaml` +
  `systemctl restart allowmaild`. The restart is the human approval step.
- **Rollback** = `systemctl disable --now allowmaild` — the socket disappears
  and the caller falls back to whatever it does without the daemon. State
  persists in `/var/lib/allowmaild` for the next start.
- A `failed` response is safe to retry with a **new** idempotency key; an
  `ambiguous` response means check the inbox yourself before doing anything.

## Notes

- Config changes require `systemctl restart allowmaild` — deliberate, per the
  design (restart is the human approval step for allowlist edits).
- `systemd-analyze security allowmaild` scores **1.5 "OK"** (measured on
  systemd 257); the unit drops all capabilities and restricts syscalls to
  `@system-service`.
- Logs go to the journal: `journalctl -u allowmaild`. They contain request
  IDs, aliases, and states — never addresses, subjects, or credentials.
