#!/usr/bin/env bash
set -euo pipefail

TOXIPROXY_API_URL="${TOXIPROXY_API_URL:-http://127.0.0.1:8474}"
TEST_DATABASE_URL="${TEST_DATABASE_URL:-postgres://zemi:zemi_staging_password@127.0.0.1:15432/zemi_fleet_test?sslmode=disable}"
BASE_URL="${BASE_URL:-http://localhost:8080}"
COMPOSE="${COMPOSE:-docker compose -p zemi-staging -f docker-compose.yml}"
curl --fail --silent "$TOXIPROXY_API_URL/version" >/dev/null
curl --fail --silent "$TOXIPROXY_API_URL/proxies/postgres" | grep -F '"upstream":"postgres:5432"'
TEST_DATABASE_URL="$TEST_DATABASE_URL" TOXIPROXY_API_URL="$TOXIPROXY_API_URL" \
  go test -tags='e2e,chaos' -timeout=90s -count=1 -p 1 -run Chaos ./internal/e2e/...

api_id_before="$($COMPOSE ps -q api)"
$COMPOSE stop postgres >/dev/null
sleep 2
health_during_stop="$(curl --silent --show-error --max-time 5 "$BASE_URL/api/v2/healthz" || true)"
echo "healthz during database stop: ${health_during_stop:-unavailable}"
$COMPOSE start postgres >/dev/null
recovered=0
for _ in $(seq 1 60); do
  if curl --silent --show-error --max-time 2 "$BASE_URL/api/v2/healthz" | grep -q '\"status\":\"available\"'; then
    recovered=1
    break
  fi
  sleep 1
done
[ "$recovered" = 1 ] || { echo 'FAIL: API did not recover after PostgreSQL restart'; exit 1; }
api_id_after="$($COMPOSE ps -q api)"
[ "$api_id_before" = "$api_id_after" ] || { echo 'FAIL: API container restarted during DB recovery'; exit 1; }
echo "database restart recovery passed without API restart: $api_id_after"
