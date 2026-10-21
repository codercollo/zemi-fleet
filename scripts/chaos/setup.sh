#!/usr/bin/env sh
set -eu

proxy_api=${TOXIPROXY_API_URL:-http://127.0.0.1:8474}
proxy_name=${TOXIPROXY_PROXY_NAME:-postgres}
proxy_port=${TOXIPROXY_POSTGRES_PORT:-15432}

curl -fsS "$proxy_api/version" >/dev/null
curl -fsS -X DELETE "$proxy_api/proxies/$proxy_name" >/dev/null 2>&1 || true
curl -fsS -X POST "$proxy_api/proxies" \
  -H 'Content-Type: application/json' \
  -d "{\"name\":\"$proxy_name\",\"listen\":\"0.0.0.0:$proxy_port\",\"upstream\":\"postgres:5432\",\"enabled\":true}" >/dev/null

echo "Toxiproxy proxy $proxy_name is listening on $proxy_port"
echo "Set TEST_DATABASE_URL to a DSN using localhost:$proxy_port before running:"
echo "  go test -tags='integration,e2e,chaos' -run Chaos ./internal/e2e/..."
