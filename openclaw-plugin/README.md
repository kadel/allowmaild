# openclaw-plugin-allowmail

OpenClaw plugin for [allowmaild](../README.md): a `send_email(recipient,
subject, text)` tool that posts to the daemon's Unix socket, plus a
`before_tool_call` hook that gates flagged recipients behind the OpenClaw
approval prompt.

## Behavior

- The tool sends to a configured recipient *alias*, never a raw address, with
  a fresh idempotency key per call, and surfaces the daemon's `request_id`,
  `status`, and `detail`. Daemon error codes (`unknown_recipient` with its
  `valid_recipients` list, `rate_limited`, `approval_required`, …) pass
  through as tool errors.
- Before every `send_email` call the hook queries `GET /v1/recipients`. If the
  target alias has `requires_approval: true`, the framework prompts the user
  (`allow-once`/`deny` only; `allow-always` is deliberately not offered —
  persistent trust lives in the daemon config). The prompt names the alias and
  subject, never the body.
- Only an `allow-once` decision sets `approved: true` on the send. Denied or
  timed-out prompts never reach the daemon. If the recipients query fails, the
  call is blocked (fail closed).

## Config

```json
{
  "socketPath": "/run/allowmail/allowmail.sock"
}
```

## Develop

```sh
npm install
npm run plugin:build     # tsc
npm run plugin:validate  # build + check entry against openclaw.plugin.json
npm test                 # vitest against a mock daemon socket
```

The manifest (`openclaw.plugin.json`) is authored by hand because the plugin
uses `definePluginEntry` (tool + hook); `openclaw plugins build/validate` only
cover tool-only plugins. `scripts/validate.mjs` keeps the entry and manifest
in sync.
