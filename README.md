# Zemi Fleet V2

REST API for order batching, proximity (60° cone) matching and rider preference matching.

## Stack
- Go 1.25, Gin
- PostgreSQL, pgx/v5, sqlc, golang-migrate, Viper
- testify, go.uber.org/mock, faker

## Local setup (no Docker)
1. Create the database and role in your local PostgreSQL (`zemi_fleet`, owned by `zemi`).
2. `cp .env.example app.env` and set `DATABASE_URL`.
3. Install Go 1.25+, `make` and the `migrate` CLI.
4. `make dev/db/migrations/up`
5. `make dev/db/seed` (optional: Nairobi sample data; truncates first, safe to re-run)
6. `make dev/run`
7. Check `http://localhost:4000/api/v2/healthz`

`make dev/sqlc` regenerates the sqlc code and `make dev/mock` regenerates the gomock `Store` (run both after changing `db/query` or the `Store` interface). On staging, `make staging/seed` loads the same data into the Postgres container.

`make dev/test/integration` runs the tests that need the real database (build tag `integration`, uses `DATABASE_URL`).

Run `make help` for all targets (`dev/*` for local work, `staging/*` for Docker).

## API (so far)
All under `/api/v2`. Money is KES in JSON (cents in the database).

| Method | Path | |
|---|---|---|
| GET | `/healthz` | status + database check |
| POST | `/orders` | create a pending order, `201` + `Location` |
| GET | `/orders/:id` | one order |
| GET | `/riders/:id` | rider with preferences |
| PUT | `/riders/:id/preferences` | replace categories, areas and minimum order value; returns the rider |
| POST | `/riders/match-orders` | pending pickups within `CONE_RADIUS_KM` and inside the 60° cone ahead of a moving rider (full circle when slow or heading unknown); also records the rider's position |
| PATCH | `/riders/:id/location` | `latitude`, `longitude`, optional `bearing_degrees`, `speed_kmh`; returns the rider |
| PATCH | `/riders/:id/availability` | `is_available`; returns the rider |

**Time.** Everything is UTC: the database session, stored values and JSON (`"created_at": "2026-10-13T09:30:15Z"`). `APP_TIMEZONE` (default `Africa/Nairobi`) only names the business zone for presentation and day boundaries. The location PATCH stores an omitted `bearing_degrees` / `speed_kmh` as `null` (unknown) because they describe that one reading.

Errors: `400` malformed JSON or header, `404` unknown ID, `409` edit conflict, `422` validation (per-field messages).

**Optimistic locking.** Riders carry a `version`. Every write bumps it, and a write that loses a race gets `409`. Send `X-Expected-Version: <n>` on the PATCHes and the preferences PUT to be rejected with `409` when your copy is stale. Every statement is cut off after `DB_QUERY_TIMEOUT` (default 3s).

See [docs/DOCUMENTATION.md](docs/DOCUMENTATION.md) for the algorithms (proximity matching so far).

## Build phases
The work is tracked phase by phase in [docs/BUILD_PHASES.md](docs/BUILD_PHASES.md).
