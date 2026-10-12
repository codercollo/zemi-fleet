# Zemi Fleet V2

REST API for order batching, proximity (60° cone) matching and rider preference matching.

## Stack
- Go 1.23, Gin
- PostgreSQL 16, pgx/v5, sqlc, golang-migrate, Viper
- testify, go.uber.org/mock, faker

## Local setup
1. `cp .env.example app.env` and update local values.
2. Install Go 1.23+, Docker Compose, `migrate` and `sqlc`.
3. `make up`
4. Check `http://localhost:8080/api/v2/healthz`

## Authentication
Protected routes require the `X-API-Key` header. Configure `API_KEYS` in `app.env`.

## Documentation
See [docs/DOCUMENTATION.md](docs/DOCUMENTATION.md).
