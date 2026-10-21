#!/usr/bin/env sh
set -eu
api=${TOXIPROXY_API_URL:-http://127.0.0.1:8474}
name=${TOXIPROXY_PROXY_NAME:-postgres}
curl -fsS -X DELETE "$api/proxies/$name" >/dev/null 2>&1 || true
