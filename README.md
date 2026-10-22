# Zemi Fleet V2

[![test](https://github.com/codercollo/zemi-fleet/actions/workflows/test.yml/badge.svg)](https://github.com/codercollo/zemi-fleet/actions/workflows/test.yml)

A Go REST API for Zemi Africa's rider app: it batches pending orders, finds pickups ahead of a moving rider, and ranks riders for an order.

## What it does

- **Order batching:** `POST /api/v2/orders/batch` groups up to 5 pending orders whose pickups are within 2 km of each other and heading the same way (oldest first), with a suggested stop sequence, distance, earnings and `route_optimization_score`. The first rider to accept a batch gets it (`POST /batches/:id/accept`, `409` for everyone after).
- **Proximity matching (60° cone):** `POST /api/v2/riders/match-orders` returns pending pickups within 1 km and ±30° of a moving rider's bearing, nearest first, with `can_accept_more_orders` and `max_additional_orders`.
- **Preference matching:** `POST /api/v2/orders/match-riders` ranks riders for an order by category, area, minimum order value, distance and acceptance rate.

## Quick start (Docker)

Needs only Docker. This builds the API, starts PostgreSQL, runs the migrations and loads the Nairobi demo data:

```bash
docker compose --profile demo up --build      # API on http://localhost:8080/api/v2
```

In a second terminal (the demo key is published here, so it is refused in production):

```bash
KEY=zemi-demo-key-not-for-production-0001
curl -s localhost:8080/api/v2/healthz
curl -s -H "X-API-Key: $KEY" -X POST localhost:8080/api/v2/orders/batch \
  -d '{"rider_id":"rider_001","rider_location":{"latitude":-1.2650,"longitude":36.8038},"max_orders":5,"radius_km":2}'
```

Swagger UI: <http://localhost:8080/docs>. Seeded riders' locations go stale after 30 minutes, so ping a location first when ranking riders (distances are `null` otherwise):

```bash
curl -s -H "X-API-Key: $KEY" -X PATCH localhost:8080/api/v2/riders/rider_002/location -d '{"latitude":-1.2890,"longitude":36.7900}'
```

`docker compose down -v` stops everything and deletes the database.

## Local development

Prerequisites: Go 1.26+, PostgreSQL, `make` and the [`migrate`](https://github.com/golang-migrate/migrate) CLI. As a PostgreSQL superuser:

```sql
CREATE ROLE zemi LOGIN PASSWORD 'zemi_dev_password';
CREATE DATABASE zemi_fleet OWNER zemi;
```

```bash
cp .env.example app.env            # then check DATABASE_URL and API_KEYS
make dev/db/migrations/up
make dev/db/seed
make dev/run                       # http://localhost:4000/api/v2/healthz
```

## Testing

- `make dev/audit`: formatting, vet, staticcheck and unit tests (what CI runs).
- `make dev/test/all`: unit, integration and e2e tests; needs a second database, `CREATE DATABASE zemi_fleet_test OWNER zemi;`.
- `make staging/postman`: runs the Postman collection with newman against the Docker stack (seed it first).

## API

All routes are under `/api/v2` and need an `X-API-Key` header, except `GET /healthz` and the docs pages (`/docs`, `/openapi.yaml`).

| Method | Path | Purpose |
|---|---|---|
| GET | `/healthz` | Status and database check |
| POST | `/orders` | Create a pending order |
| GET | `/orders/:id` | One order |
| POST | `/orders/batch` | Form and offer batches near a rider |
| POST | `/orders/match-riders` | Rank riders for an order |
| GET | `/riders/:id` | Rider with preferences |
| PUT | `/riders/:id/preferences` | Replace categories, areas, minimum order value |
| PATCH | `/riders/:id/location` | Report position, bearing, speed |
| PATCH | `/riders/:id/availability` | Go on or off shift |
| POST | `/riders/match-orders` | Pickups inside the 60° cone ahead of a rider |
| GET | `/batches?lat=&lng=` | Open batches near a point |
| POST | `/batches/:id/accept` | Accept a batch exclusively |

## Documentation

- [docs/DOCUMENTATION.md](docs/DOCUMENTATION.md): architecture, API reference, algorithms, setup and configuration, schema, security, assumptions, improvements
- [docs/TESTING.md](docs/TESTING.md): what each test layer proves and the known gaps
- [api/openapi.yaml](api/openapi.yaml): the OpenAPI 3.0.3 contract (served at `/docs`)
- [postman/](postman): collection with saved examples, and the demo environment
