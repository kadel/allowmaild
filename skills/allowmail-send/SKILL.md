---
name: allowmail-send
description: Send a plain-text email to a pre-approved recipient alias via the local allowmaild daemon's Unix socket. Use when asked to send an email, notify someone by email, or deliver a message, report, or alert via email on a host running allowmaild. Recipients are alias names from the daemon config, never raw email addresses.
compatibility: Requires curl (plus jq or python3 for the bundled script) and a running allowmaild daemon (Unix socket, default /run/allowmail/allowmail.sock)
---

# Send email via allowmaild

allowmaild sends plain-text email to recipients pre-approved in its root-owned config. You name a recipient *alias*; the daemon resolves the address, builds the message, and delivers it with credentials only it can read. You cannot supply an email address, HTML, attachments, CC/BCC, or custom headers — the API rejects them by design, so don't attempt workarounds.

## How to send

Default: use the bundled script. It generates the idempotency key, builds the JSON safely, and maps the outcome to exit codes.

```sh
scripts/send-email.sh <alias> <subject> [body-file]    # body from stdin if no file
```

Example:

```sh
echo "Backup completed at $(date)." | scripts/send-email.sh self-gmail "Nightly backup OK"
```

Exit codes: `0` sent, `2` failed (safe to retry — just rerun the script; it uses a fresh key), `3` ambiguous (do **not** retry), `4` daemon rejected the request (inspect the JSON on stdout), `1` local/usage error. Set `ALLOWMAIL_SOCKET` to override the socket path.

If the script isn't available, call the API directly:

```sh
curl -sS --unix-socket /run/allowmail/allowmail.sock \
  -X POST http://d/v1/send -H 'Content-Type: application/json' \
  -d '{"recipient":"self-gmail","subject":"Hi","text":"Hello.","idempotency_key":"agent-4f3c2a1b9e8d7c6f"}'
```

Health check: `curl -sS --unix-socket /run/allowmail/allowmail.sock http://d/v1/health` → `{"status":"ok"}`.

## Interpreting the result

A 200 response does **not** mean the email was delivered — check the `status` field:

| `status` | Meaning | What to do |
|---|---|---|
| `sent` | Accepted by the SMTP server | Done; report the `message_id` if asked |
| `failed` | Delivery failed before the message was accepted | Safe to retry with a **new** `idempotency_key` (rerunning the script does this) |
| `ambiguous` | The email may or may not have been delivered | Do **not** retry automatically — a retry could send a duplicate. Report the uncertainty to the user |

## Error handling

Errors return `{"error":{"code":"...","message":"..."}}`:

- **400 `unknown_recipient`** — the alias isn't configured. The response includes `valid_recipients: [...]`; pick the right alias from that list, or ask the user which to use. Never invent aliases or addresses.
- **400 `validation`** — a field violated the limits (see Gotchas). Fix the request; the idempotency key was not consumed, so it may be reused.
- **429 `rate_limited`** — hourly or daily send budget exhausted. There is no `Retry-After` header; windows are sliding (1h/24h). Do not hammer — report the limit and stop, or wait substantially before one retry.
- **409 `key_reuse`** — the key was already used with a different subject/body. Use a new key.
- **409 `in_flight`** — a request with this key is still processing. Wait a few seconds, then repeat the *identical* request to get the stored result.
- **503 `unavailable`** — daemon's store is down; nothing was sent. Report it.

If you hit an error case not covered here, read [references/api.md](references/api.md) for the complete contract.

## Gotchas

- `recipient` is an **alias** (e.g. `self-gmail`), never an email address. Matched exactly and case-sensitively.
- The request JSON takes **exactly four fields**: `recipient`, `subject`, `text`, `idempotency_key` — all required. Any extra field (`to`, `html`, `cc`, `headers`, ...) → 400.
- `subject`: max 200 bytes (default config), no control characters at all — no newlines or tabs.
- `text`: max 10000 bytes (default config), plain text, LF (`\n`) is the only control character allowed — no CR, no tabs.
- `idempotency_key`: ≤200 bytes, UTF-8, no control characters. A rejected request (validation, rate limit, unknown alias) does **not** burn the key. Repeating a completed request with the same key and identical subject+text replays the stored result verbatim without sending again.
- The URL host (`http://d/`) is a dummy — the daemon only listens on the Unix socket. If the socket is missing, allowmaild isn't running (or you lack group permission on the socket directory).
