#!/usr/bin/env bash
# Smoke test: calls every endpoint once with the API key and checks the HTTP status code. It stops at the first
# failure and prints which call failed, what was expected and what came back.
#
#   scripts/smoke_test.sh                 # against the staging stack (http://localhost:8080, the demo key)
#   BASE_URL=http://localhost:4000 API_KEY=... scripts/smoke_test.sh
#
# It needs the demo seed data (make staging/seed, or docker compose --profile demo up) and it WRITES: it creates an
# order, forms and accepts a batch for rider_010 (who has room for 4 orders, so it can run a few times in a row).
# Reseed to start again from a clean state.
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
API_KEY="${API_KEY:-zemi-demo-key-not-for-production-0001}"
API="$BASE_URL/api/v2"
RIDER="rider_010"          # Aisha Hassan: seeded with no orders and room for 4, in Kilimani
VENDOR="ven_004"           # Yaya Fashion House, Kilimani
LAT="-1.293"
LNG="36.786"

NL=$'\n'
BODY=""     # the last response body, kept in memory (a temp file path would not survive MSYS path handling under make)
checks=0

# fail MESSAGE: print the problem and the last response body, then stop.
fail() {
  echo "SMOKE FAILED: $1" >&2
  if [ -n "$BODY" ]; then echo "  response body: ${BODY:0:600}" >&2; fi
  exit 1
}

# call NAME EXPECTED_STATUS METHOD URL BODY [extra curl args...]: one request, one status check. The response body is
# left in $BODY for the next step to read. Set NO_KEY=1 in front of the call to send no X-API-Key.
call() {
  local name="$1" expected="$2" method="$3" url="$4" data="$5"
  shift 5
  local args=(--silent --show-error --max-time 20 --write-out "${NL}%{http_code}" -X "$method")
  if [ "${NO_KEY:-}" != "1" ]; then args+=(-H "X-API-Key: $API_KEY"); fi
  if [ -n "$data" ]; then args+=(-H 'Content-Type: application/json' --data "$data"); fi
  local out status
  BODY=""
  out="$(curl "${args[@]}" "$@" "$url")" || fail "$name: could not reach $url (is the stack up?)"
  status="${out##*"$NL"}"
  BODY="${out%"$NL"*}"
  checks=$((checks + 1))
  if [ "$status" != "$expected" ]; then
    fail "$name: $method $url expected HTTP $expected, got $status"
  fi
  printf '  ok  %-34s %s %s\n' "$name" "$status" "${url#"$BASE_URL"}"
}

# field NAME: the first value of a string field in the last response (the API answers compact JSON).
field() { printf '%s' "$BODY" | grep -o "\"$1\":\"[^\"]*\"" | head -1 | cut -d'"' -f4; }

echo "Smoke test against $BASE_URL"

# Public routes: no key needed.
NO_KEY=1 call "healthz"                    200 GET "$API/healthz" ""
NO_KEY=1 call "no key is refused"          401 GET "$API/riders/$RIDER" ""

# The demo data must be there, and the rider must not be full from earlier runs.
call "get rider"                           200 GET "$API/riders/$RIDER" ""
grep -q '"current_orders_count":[0-3],' <<<"$BODY" \
  || fail "$RIDER is already full from earlier runs: reseed (make staging/seed) and run again"

# Rider locations in the seed go stale: report a fresh one first.
call "patch location"                      200 PATCH "$API/riders/$RIDER/location" \
  "{\"latitude\":$LAT,\"longitude\":$LNG,\"bearing_degrees\":225,\"speed_kmh\":15}"
call "patch availability"                  200 PATCH "$API/riders/$RIDER/availability" '{"is_available":true}'
call "put preferences"                     200 PUT "$API/riders/$RIDER/preferences" \
  '{"min_order_value_kes":0,"preferred_categories":[],"preferred_areas":[]}'

# Orders. The idempotency key makes the retry a replay, not a second order.
order='{"vendor_id":"'"$VENDOR"'","category":"clothing","delivery_location":{"lat":-1.3070,"lng":36.8130},"value_kes":3200,"delivery_fee_kes":200}'
key="smoke-$(date +%s)-$$"
call "create order"                        201 POST "$API/orders" "$order" -H "Idempotency-Key: $key"
order_id="$(field id)"
[ -n "$order_id" ] || fail "create order: no order id in the response"
call "create order (replay)"               201 POST "$API/orders" "$order" -H "Idempotency-Key: $key"
call "get order"                           200 GET "$API/orders/$order_id" ""
call "create order (invalid body)"         422 POST "$API/orders" '{"vendor_id":"'"$VENDOR"'"}'

# Matching.
call "match orders (cone)"                 200 POST "$API/riders/match-orders" \
  "{\"rider_id\":\"$RIDER\",\"current_location\":{\"latitude\":$LAT,\"longitude\":$LNG},\"bearing_degrees\":225,\"speed_kmh\":15,\"current_orders_count\":0}"
call "match riders"                        200 POST "$API/orders/match-riders" "{\"order_id\":\"$order_id\"}"

# Batching: one-order batches, so the batch fits the rider's capacity however many times this has run.
call "create batches"                      200 POST "$API/orders/batch" \
  "{\"rider_id\":\"$RIDER\",\"rider_location\":{\"latitude\":$LAT,\"longitude\":$LNG},\"max_orders\":1,\"radius_km\":2}"
batch_id="$(field batch_id)"
[ -n "$batch_id" ] || fail "create batches: no batch was offered (reseed, or wait for open batches to expire)"
call "list batches"                        200 GET "$API/batches?lat=$LAT&lng=$LNG&radius_km=3" ""
call "accept batch"                        200 POST "$API/batches/$batch_id/accept" "{\"rider_id\":\"$RIDER\"}"
call "accept batch again (taken)"          409 POST "$API/batches/$batch_id/accept" '{"rider_id":"rider_002"}'

# Operations and documentation.
call "metrics"                             200 GET "$BASE_URL/debug/vars" ""
NO_KEY=1 call "swagger ui"                 200 GET "$BASE_URL/docs" ""
NO_KEY=1 call "openapi spec"               200 GET "$BASE_URL/openapi.yaml" ""

echo "Smoke test passed: $checks checks."
