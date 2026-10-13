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

Run `make help` for all targets (`dev/*` for local work, `staging/*` for Docker).

## API (so far)
All under `/api/v2`. Money is KES in JSON (cents in the database).

| Method | Path | |
|---|---|---|
| GET | `/healthz` | status + database check |
| POST | `/orders` | create a pending order, `201` + `Location` |
| GET | `/orders/:id` | one order |
| GET | `/riders/:id` | rider with preferences |
| PUT | `/riders/:id/preferences` | replace categories, areas and minimum order value |

Errors: `400` malformed JSON, `404` unknown ID, `422` validation (per-field messages).

## Build phases
The work is tracked phase by phase in [docs/BUILD_PHASES.md](docs/BUILD_PHASES.md).
