# allowmaild HTTP API reference

Complete contract for the daemon's API. The daemon listens **only** on a Unix socket
(default `/run/allowmail/allowmail.sock`); there is no TCP listener. The host in the
URL (`http://d/`) is a dummy. Access control is filesystem group membership on the
socket directory.

Exactly two endpoints exist.

## GET /v1/health

| HTTP | Body | Meaning |
|---|---|---|
| 200 | `{"status":"ok"}` | Daemon up, state store reachable |
| 503 | `{"status":"unavailable"}` | State store unreachable; sends will be rejected |

## POST /v1/send

### Request

`Content-Type: application/json`. Exactly four fields, all required; **any unknown
field is rejected** (strict decoding), as is trailing data after the JSON object.

| Field | Type | Rules |
|---|---|---|
| `recipient` | string | A configured alias name (not an email address). Exact, case-sensitive match. Alias names match `^[a-z0-9][a-z0-9._-]{0,63}$`. |
| `subject` | string | ≤ `max_subject_bytes` (default 200, hard cap 256). Valid UTF-8. **No control characters** (no CR, LF, or tab). |
| `text` | string | ≤ `max_body_bytes` (default 10000). Valid UTF-8. **LF is the only allowed control character** (no CR, no tab). |
| `idempotency_key` | string | Non-empty, ≤ 200 bytes, valid UTF-8, no control characters. No prescribed format — random hex, UUID, or ULID all work. |

The whole request body is capped at `6*(max_body_bytes + max_subject_bytes) + 4096` bytes.

Validation and rate-limit checks run **before** the idempotency key is reserved, so a
rejected request never consumes the key or the rate budget.

### Success response (HTTP 200)

```json
{
  "request_id": "e3b0c44298fc1c149afbf4c8996fb924",
  "status": "sent",
  "recipient": "self-gmail",
  "message_id": "<...>",
  "result_code": "250",
  "detail": "message accepted by the SMTP server"
}
```

- `request_id` — 16-byte hex identifier for this request (safe to log).
- `status` — `sent` | `failed` | `ambiguous`. **HTTP 200 covers all three**; the
  delivery outcome is in `status`, not the HTTP code.
- `message_id` — present only when `status == "sent"`.
- `result_code` — `"250"` on sent; the SMTP reply code on failed/ambiguous; `"swept"`
  for a request that was in flight when the daemon crashed and was recovered as
  ambiguous on restart.
- `detail` — fixed human-readable string per status:
  - `sent` → "message accepted by the SMTP server"
  - `failed` → "delivery failed before the message was accepted; safe to retry with a new idempotency_key"
  - `ambiguous` → "delivery uncertain: the message may or may not have been delivered; do not retry automatically"

`failed` means the SMTP failure happened **before** the message data was transmitted —
no email went out, retrying with a new key is safe. `ambiguous` means the failure
happened **after** the data was sent (or the connection dropped) — the email may have
been delivered, so an automatic retry risks a duplicate.

### Error responses

Shape: `{"error":{"code":"...","message":"..."}}`. The `valid_recipients` array is
added only for `unknown_recipient`. Errors never contain addresses, SMTP transcripts,
credentials, or file paths.

| HTTP | `error.code` | When | Message (example) |
|---|---|---|---|
| 400 | `validation` | Any field/format rule above violated, unknown JSON field, trailing data, or oversized body | `"subject exceeds max_subject_bytes"`, `"text contains a control character other than newline"` |
| 400 | `unknown_recipient` | Alias not in config | `"unknown recipient alias; recipient must exactly match a configured alias"` + `"valid_recipients": ["self-gmail", ...]` |
| 429 | `rate_limited` | A send budget is exhausted | `"rate limit exceeded: <limit>"` where `<limit>` ∈ `per_hour`, `per_day`, `recipient per_hour`, `recipient per_day` |
| 409 | `key_reuse` | Key already used with a **different** subject or text | `"idempotency_key was already used with different content"` |
| 409 | `in_flight` | A request with this key is still processing | `"a request with this idempotency_key is still in progress"` |
| 503 | `unavailable` | State store unreachable; request rejected, nothing sent | `"request store unavailable; request rejected"` |

## Idempotency semantics

The daemon fingerprints each key against `sha256(subject)` and `sha256(text)`:

- **Same key, same subject+text, prior request in a terminal state** → the stored
  response is replayed verbatim (HTTP 200) with **no new SMTP attempt**. This is the
  safe way to recover from a lost response: repeat the identical request.
- **Same key, different subject or text** → 409 `key_reuse`; nothing is sent.
- **Same key while the original is still in flight** → 409 `in_flight`; wait briefly
  and repeat the identical request.
- A request rejected before sending (validation, unknown alias, rate limit, store
  down) does **not** reserve the key — it may be reused after fixing the request.
- Crash recovery: requests that were mid-send when the daemon stopped are marked
  `ambiguous` with `result_code: "swept"` on restart; a duplicate replays that
  ambiguous result.

## Rate limiting

- Global and optional per-recipient limits, hourly and daily, computed over sliding
  windows (last 1h / last 24h from now).
- On 429 there is **no `Retry-After` header** and no timing information — back off
  conservatively or report the limit and stop.
- Rejected requests (including 429s themselves) do not consume budget.
- If limits cannot be evaluated (store down), the daemon fails closed with 503.
