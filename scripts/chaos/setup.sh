#!/usr/bin/env sh
set -eu
api=${TOXIPROXY_API_URL:-http://127.0.0.1:8474}
name=${TOXIPROXY_PROXY_NAME:-postgres}
port=${TOXIPROXY_POSTGRES_PORT:-15432}
curl -fsS "$api/version" >/dev/null
curl -fsS -X DELETE "$api/proxies/$name" >/dev/null 2>&1 || true
curl -fsS -X POST "$api/proxies" -H 'Content-Type: application/json' \
  -d "{\"name\":\"$name\",\"listen\":\"0.0.0.0:$port\",\"upstream\":\"postgres:5432\",\"enabled\":true}" >/dev/null
echo "Toxiproxy proxy $name is ready on port $port"
