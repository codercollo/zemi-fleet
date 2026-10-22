# Zemi Fleet V2

[![test](https://github.com/codercollo/zemi-fleet/actions/workflows/test.yml/badge.svg)](https://github.com/codercollo/zemi-fleet/actions/workflows/test.yml)

Zemi Fleet is a Go/PostgreSQL REST API for courier operations. It creates orders, forms efficient rider batches, matches work to riders, tracks delivery lifecycle, and sends reliable webhook notifications through a database-backed outbox.

## Features

- Order creation and lookup.
- Batching of up to five nearby orders with an ordered pickup/delivery route.
- Exclusive batch acceptance: the first rider wins; competing accepts receive `409`.
- Rider location, availability, preferences, capacity, and acceptance history.
- Proximity matching for pickups ahead of a rider.
- Rider ranking by category, area, order value, distance, and acceptance rate.
- Transactional pickup, delivery, return, cancellation, and retry workflows.
- Webhook events for accepted batches and order lifecycle changes. Deliveries are created by a PostgreSQL trigger, claimed with `SKIP LOCKED`, retried, and exposed for dispatcher recovery.
- API-key authentication, idempotency, rate limiting, request IDs, audit logs, health checks, and OpenAPI documentation.

## Observability

- `GET /metrics` exposes Prometheus metrics for HTTP latency, algorithms, database pool usage, webhook backlog/failures, sweeps, and routing breaker state.
- `GET /debug/vars` exposes business counters as JSON.
- `GET /api/v2/healthz` checks application and database health.
- Structured `slog` logs include request IDs, route templates, status, duration, and safe client attributes.
- OpenTelemetry tracing is not currently implemented.

Metrics are enabled by default in development and staging and can be controlled with `METRICS_ENABLED`. Metrics and protected API routes require an API key; `GET /api/v2/healthz` does not.

## Quick start with Docker

```bash
docker compose --profile demo up --build
```

The API is available at `http://localhost:8080/api/v2`, Swagger UI at `http://localhost:8080/docs`, and the demo API key is `zemi-demo-key-not-for-production-0001`.

```bash
curl http://localhost:8080/api/v2/healthz
curl -H 'X-API-Key: zemi-demo-key-not-for-production-0001' \
  -X POST http://localhost:8080/api/v2/orders/batch \
  -d '{"rider_id":"rider_001","rider_location":{"latitude":-1.265,"longitude":36.8038},"max_orders":5,"radius_km":2}'
```

Stop the stack and remove its database with `docker compose down -v`.

## Local development

Requires Go, PostgreSQL, `make`, and the `migrate` CLI.

```bash
cp .env.example app.env
# Create the configured PostgreSQL role/database, then:
make dev/db/migrations/up
make dev/db/seed
make dev/run
```

The local API listens on `http://localhost:4000` by default.

## API highlights

All application routes are under `/api/v2`.

| Method | Path | Purpose |
|---|---|---|
| GET | `/healthz` | Health and database check |
| POST | `/orders` | Create an order |
| GET | `/orders/:id` | Get an order |
| POST | `/orders/batch` | Form batches near a rider |
| POST | `/orders/match-riders` | Rank riders for an order |
| GET | `/riders/:id` | Get rider details |
| PUT | `/riders/:id/preferences` | Replace rider preferences |
| PATCH | `/riders/:id/location` | Report rider location |
| PATCH | `/riders/:id/availability` | Change rider availability |
| POST | `/riders/match-orders` | Find pickups near a rider |
| GET | `/batches` | List nearby open batches |
| POST | `/batches/:id/accept` | Accept a batch exclusively |
| GET | `/webhooks/failed` | List failed webhook deliveries |
| POST | `/webhooks/:id/retry` | Retry a failed webhook delivery |

See [api/openapi.yaml](api/openapi.yaml) for the complete contract.

## Testing

```bash
make dev/test       # unit tests
make dev/test/all   # unit, integration, and e2e tests
make dev/audit      # tidy, verification, formatting, vet, staticcheck, and tests
```

Integration and e2e tests require a separate database whose name ends in `_test`. CI runs them against fresh PostgreSQL 16 and verifies migrations with `up`, `down -all`, and `up`.

Further architecture and test details are in [docs/DOCUMENTATION.md](docs/DOCUMENTATION.md) and [docs/TESTING.md](docs/TESTING.md).
