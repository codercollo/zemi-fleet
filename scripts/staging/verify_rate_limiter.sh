#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
API_KEY="${API_KEY:?set API_KEY}"
TARGET="${TARGET:-$BASE_URL/api/v2/riders/rider_001}"
BURST="${BURST:-100}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

export API_KEY TARGET
seq "$BURST" | xargs -P "$BURST" -I{} bash -c '
  if [ $(( $1 % 2 )) -eq 0 ]; then
    code=$(curl --silent --show-error --max-time 10 -o /dev/null -w "%{http_code}" -H "X-API-Key: $API_KEY" -H "X-Forwarded-For: 203.0.113.77" "$TARGET")
    echo "spoofed $code"
  else
    code=$(curl --silent --show-error --max-time 10 -o /dev/null -w "%{http_code}" -H "X-API-Key: $API_KEY" "$TARGET")
    echo "real $code"
  fi
' _ {} >"$tmp/burst"

echo "burst status counts:"
awk '{count[$1 FS $2]++} END {for (code in count) print code, count[code]}' "$tmp/burst" | sort
grep -q '^real 429$' "$tmp/burst" || { echo 'FAIL: no real-client 429 response'; exit 1; }
grep -q '^spoofed 429$' "$tmp/burst" || { echo 'FAIL: spoofed X-Forwarded-For changed the bucket'; exit 1; }

health="$(curl --silent --show-error --max-time 10 -o /dev/null -w '%{http_code}' "$BASE_URL/api/v2/healthz")"
echo "healthz=$health"
[ "$health" = 200 ] || { echo 'FAIL: healthz was rate limited'; exit 1; }

echo 'rate limiter verification passed'
