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
5. `make dev/run`
6. Check `http://localhost:4000/api/v2/healthz`

Run `make help` for all targets (`dev/*` for local work, `staging/*` for Docker).

## Build phases
The work is tracked phase by phase in [docs/BUILD_PHASES.md](docs/BUILD_PHASES.md).
