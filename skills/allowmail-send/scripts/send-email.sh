#!/usr/bin/env bash
# Send a plain-text email via the local allowmaild daemon.
#
# Usage: send-email.sh <alias> <subject> [body-file]
#   Body is read from body-file, or from stdin when omitted (or "-").
#   ALLOWMAIL_SOCKET overrides the socket path (default /run/allowmail/allowmail.sock).
#
# Dependencies: curl, and either jq or python3.
#
# Exit codes:
#   0  sent      (accepted by the SMTP server)
#   2  failed    (delivery failed before acceptance; safe to retry — rerun, a fresh key is generated)
#   3  ambiguous (delivery uncertain; do NOT retry automatically)
#   4  daemon rejected the request (HTTP 4xx/5xx; error JSON printed on stdout)
#   1  usage or local error (bad args, missing socket, curl/tool failure)

set -u

SOCKET="${ALLOWMAIL_SOCKET:-/run/allowmail/allowmail.sock}"

die() { echo "send-email.sh: $*" >&2; exit 1; }

[ $# -ge 2 ] && [ $# -le 3 ] || die "usage: send-email.sh <alias> <subject> [body-file] (body from stdin if omitted)"

ALIAS="$1"
SUBJECT="$2"
BODY_SRC="${3:--}"

if [ "$BODY_SRC" = "-" ]; then
  BODY="$(cat)" || die "failed to read body from stdin"
else
  [ -r "$BODY_SRC" ] || die "body file not readable: $BODY_SRC"
  BODY="$(cat "$BODY_SRC")" || die "failed to read body file: $BODY_SRC"
fi

command -v curl >/dev/null 2>&1 || die "curl is required"
[ -S "$SOCKET" ] || die "socket not found: $SOCKET (is allowmaild running? set ALLOWMAIL_SOCKET to override)"

# ≤200 bytes, no control characters, per the daemon's idempotency_key rules.
KEY="agent-$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"

if command -v jq >/dev/null 2>&1; then
  JSON_TOOL=jq
elif command -v python3 >/dev/null 2>&1; then
  JSON_TOOL=python3
else
  die "need jq or python3 to build and parse JSON"
fi

if [ "$JSON_TOOL" = jq ]; then
  PAYLOAD=$(jq -n --arg r "$ALIAS" --arg s "$SUBJECT" --arg t "$BODY" --arg k "$KEY" \
    '{recipient:$r, subject:$s, text:$t, idempotency_key:$k}') || die "failed to build request JSON"
else
  PAYLOAD=$(AM_R="$ALIAS" AM_S="$SUBJECT" AM_T="$BODY" AM_K="$KEY" python3 -c '
import json, os
print(json.dumps({"recipient": os.environ["AM_R"], "subject": os.environ["AM_S"],
                  "text": os.environ["AM_T"], "idempotency_key": os.environ["AM_K"]}))
') || die "failed to build request JSON"
fi

RESP=$(curl -sS --unix-socket "$SOCKET" -X POST http://d/v1/send \
  -H 'Content-Type: application/json' --data-binary "$PAYLOAD" \
  -w $'\n%{http_code}') || die "request to daemon failed (socket: $SOCKET)"

HTTP_CODE="${RESP##*$'\n'}"
JSON="${RESP%$'\n'*}"

echo "$JSON"

if [ "$HTTP_CODE" != "200" ]; then
  echo "send-email.sh: daemon rejected the request (HTTP $HTTP_CODE); see error JSON above" >&2
  exit 4
fi

if [ "$JSON_TOOL" = jq ]; then
  STATUS=$(printf '%s' "$JSON" | jq -r '.status // empty')
else
  STATUS=$(printf '%s' "$JSON" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("status",""))')
fi

case "$STATUS" in
  sent)
    exit 0 ;;
  failed)
    echo "send-email.sh: delivery failed; safe to retry with a new idempotency_key (rerun this script)" >&2
    exit 2 ;;
  ambiguous)
    echo "send-email.sh: delivery uncertain — the message may or may not have been delivered; do NOT retry automatically" >&2
    exit 3 ;;
  *)
    echo "send-email.sh: unexpected response status: ${STATUS:-<none>}" >&2
    exit 1 ;;
esac
