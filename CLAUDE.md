# Dont push code to github

# Dont initialize gihub or git add .

# Git commands are done specifically me the coder.

# CLAUDE.md — Zemi Fleet V2

Context for Claude (and any other contributor) working in this repo. Read this first, then `docs/claude/build-phases.md`.

## What this is

A Go REST API for **Zemi Africa's** rider app (Zemi Fleet). It's built as a 5-day volunteer backend assignment.
**Deadline: Fri 16 Oct 2026, 22:00 EAT.** The deliverables are the GitHub repo, `README.md`, `docs/DOCUMENTATION.md`, a Postman collection, migrations and `.env.example`.

Three features, all under `/api/v2`:

| Endpoint                    | Feature                                                                                                                                                                                                                                                                       |
| --------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `POST /orders/batch`        | **Batching.** Group up to 5 pending orders whose pickups are within 2 km of each other and whose trips head in similar directions, oldest first (FIFO). Return each batch with a suggested pickup/delivery sequence, total distance, earnings and `route_optimization_score`. |
| `POST /riders/match-orders` | **Proximity (60° cone).** Find pending pickups ≤ 1 km from a moving rider and within ±30° of their bearing. Sort by distance, then by order age. Return `can_accept_more_orders` and `max_additional_orders` (5 − current orders).                                            |
| `POST /orders/match-riders` | **Preference matching.** Rank riders for a new order by category, area, minimum order value, distance and acceptance rate, best match first.                                                                                                                                  |

Supporting endpoints: `GET /healthz`, `POST /orders`, `GET /orders/:id`, `GET /riders/:id`, `PUT /riders/:id/preferences`, `PATCH /riders/:id/location`, `GET /batches?lat=&lng=`, `POST /batches/:id/accept`.

### Clarifications from Zemi (email from Raphael, 12 Oct). These override the brief.

1. **Batches are open offers.** Any eligible rider near the vendor can see a batch. The **first rider to accept gets it exclusively**; anyone after that gets `409`. The goal is to minimise travel to the vendor and overall delivery time.
2. **The 2 km rule clusters pickup points.** Deliveries that are close together or on the same route are a bonus, not a requirement.
3. **Areas** (Kilimani, Westlands, …) are a centre point plus a coverage radius of a few km. The service covers all of Nairobi and its surroundings.
4. **Earnings:** the rider gets **90% of the delivery fee** and the platform keeps 10%. This is configurable via `RIDER_PAYOUT_BPS=9000` and snapshotted on each order.

### How it's graded

Code quality 30%, algorithm logic 30% (correctness and edge cases: no orders, rider at capacity), API design 20%, docs 15%, bonus 5% (tests, Docker, Swagger, rate limiting, logging). **No hardcoded values.**

## Stack

Go 1.25 (go.mod; pgx and gin require ≥1.25; the Dockerfile builder image matches) · **Gin** (its router is a fork of httprouter, so don't add httprouter) · PostgreSQL 16 · **pgx/v5** (pool) · **sqlc** · golang-migrate · **Viper** · log/slog · testify · go.uber.org/mock (mockgen) · go-faker/faker.
Module: `github.com/itscollinsmaina/zemi-fleet`.

## How the code is organised

Patterns come from _Let's Go Further_ (Greenlight) for the HTTP layer and build process, and from Tech School's _Simple Bank_ for the data layer.

```
cmd/api/main.go        config -> logger -> pgx pool -> api.NewServer -> http.Server. `-version` flag.
internal/config        Viper. Precedence: environment variables > app.env > defaults. Validated at startup.
internal/api           HTTP layer: Server struct (the book's `application`), routes.go, handlers,
                       middleware, writeJSON/envelope, readJSON, errors.go (all error responses)
internal/validator     the book's Validator (Check/AddError/PermittedValue/Unique/Between)
internal/service       business rules; calls the repository and the algorithm packages        (Phase 3+)
internal/repository    OpenPool; later the sqlc code in ./sqlc, a Store interface + ExecTx  (Phase 3+)
internal/domain        shared types + domain errors (ErrNotFound, ErrEditConflict, ...)    (Phase 3+)
internal/geo           pure: Haversine, Bearing, AngleDiff, BoundingBox                     (Phase 5)
internal/matching      pure: cone.go, preference.go                                         (Phase 6, 8)
internal/batching      pure: batcher.go, sequencer.go, scoring.go                           (Phase 7)
internal/mock          mockgen output for repository.Store                                  (Phase 3)
db/migrations          golang-migrate, 000001_init_schema (tested)
db/query, db/seed      sqlc input SQL, Nairobi seed data                                    (Phase 2-3)
```

### Rules for the layers

- The layers only call downward: `api` → `service` → `repository`. Handlers never run SQL; services never touch `gin.Context`.
- `geo`, `matching` and `batching` are **pure**: no HTTP, no DB, no config imports. Parameters go in as arguments. They must have table-driven tests.
- Each handler does: `readJSON` → validate with `validator` → call the service → `writeJSON(c, status, envelope{...}, nil)`. Every error goes through the helpers in `internal/api/errors.go`.
- Domain errors are mapped to HTTP status in one place: not found → 404, edit conflict or batch already taken → 409, validation failure or rider at capacity → 422.

## Conventions

- **Response shape:** an envelope with a named key, e.g. `{"batches": [...]}`, `{"matching_orders": [...]}`, `{"error": "..."}` or `{"error": {"field": "msg"}}`. Field names follow the brief exactly (snake_case: `estimated_earnings_kes`, `angle_from_rider_degrees`, …).
- **Money:** `BIGINT` KES cents in the DB and in Go (`int64`). Convert to KES only in the API DTOs. Never use floats for money.
- **Distances:** metres (int) in the DB, km in the API. **Bearings:** degrees in `[0, 360)`.
- **IDs:** app-generated, prefixed and sortable (`ord_…`, `rider_…`, `batch_…`, `ven_…`, `area_…`) to match the brief's `"rider_123"` style.
- **Concurrency:**
  - Mutable rows have a `version` column. Updates use `WHERE id=$1 AND version=$2`, and 0 rows affected means an edit conflict.
  - Batch acceptance runs in one transaction: lock the rider `FOR UPDATE`, then claim the batch `WHERE status='open' AND version=$v`.
  - Batch creation selects orders with `FOR UPDATE SKIP LOCKED`.
  - DB `CHECK` constraints are the last line of defence (capacity, state/assignment consistency).
- **Config:** every tunable is an environment variable read in `internal/config` (radius, cone angle, payout bps, average speed, batch TTL, rate limits, weights). Add new keys to `defaults`, `validate()`, `.env.example` and `docker-compose.yml`.
- **Errors:** wrap with `fmt.Errorf("context: %w", err)`, log once at the edge, and never leak internals in a 500.
- **Tests:** testify (`require` for preconditions, `assert` for checks), table-driven, `t.Run` subtests; gomock Store for handler/service tests; faker for data.
- **Comments:** explain _why_, and cite the book chapter when following a Greenlight pattern (e.g. `// Let's Go Further ch. 8.2`).

## Schema (db/migrations/000001_init_schema)

`categories`, `areas` (centre + radius_m), `vendors`, `riders` (capacity, last location/bearing/speed, offers/accepted counters → generated `acceptance_rate`), `rider_preferences` (min_order_value_cents) + `rider_preferred_categories` / `rider_preferred_areas` (no rows = no preference = matches all), `orders` (pickup/delivery coords, precomputed `trip_distance_m` / `trip_bearing_deg`, `value_cents`, `delivery_fee_cents`, `rider_payout_bps` → generated `rider_earnings_cents` / `platform_fee_cents`, status + batch/rider assignment), `batches` (open → accepted → in_progress → completed | expired | cancelled, pickup centroid, score, earnings, `expires_at`), `batch_stops` (sequence, pickup before delivery), `batch_offers` (audit + acceptance rate).
Enums: `order_status`, `batch_status`, `stop_type`. Auth tables (`users`, `sessions`) are **not** in 000001. They'd come in a later `000002_auth` only if the PASETO stretch goal is built.

## Commands (Makefile has only dev/_ and staging/_)

```bash
# dev = local PostgreSQL + `go run`, no Docker. DB and role are created by hand (README).
cp .env.example app.env && go mod tidy     # first time
make dev/db/migrations/up   # golang-migrate CLI against DATABASE_URL
make dev/run          # API natively on :4000
make dev/audit        # what CI runs: tidy -diff, verify, gofmt, vet, staticcheck@v0.8.2, tests
make dev/test | dev/test/race | dev/test/cover | dev/build
make dev/db/psql | dev/db/migrations/new name=x | dev/db/migrations/down | dev/db/migrations/version
make staging/up       # cross-compile bin/linux_amd64/api -> image (target prebuilt) -> compose on :8080 (override with STAGING_API_PORT / STAGING_DB_PORT, e.g. when 8080 or 5433 are taken)
make staging/smoke | staging/logs | staging/down
make help
```

- **Production** is never deployed from a laptop. `.github/workflows/deploy.yml` (on `main` and `v*` tags) re-runs `test.yml`, builds the binary with `make staging/binary`, pushes that exact binary's image to GHCR, attaches it to a Release on tags, and deploys over SSH only if `vars.PROD_HOST` is set.
- The **version** is stamped via `-ldflags "-X main.version=$(git describe --always --dirty --tags)"`. A plain `go run` reports `dev`.
- The Dockerfile's default target `runtime` compiles inside Docker, so a reviewer can run `docker compose up --build` without Go or make.

## Build process (follow it)

The work proceeds **one phase at a time** in `docs/claude/build-phases.md`, in Greenlight chapter order. Phase 0 (foundation, ch. 2–5) is done. Phase 1 (tooling, ch. 20–21) is done except one check: `make dev/db/migrations/up` has not yet been run against the user's local Postgres. **The next phase is Phase 2** (migrations, seed data, sqlc).
For each phase:

1. Build only that phase's scope, on top of what already exists. Don't rewrite earlier phases unless they're broken.
2. Keep the repo green: `make dev/audit` passes and the API starts.
3. Verify for real: run the tests, hit the endpoint with curl, run migrations up/down.
4. Update the phase's status in `docs/claude/build-phases.md`, and update `README.md` / `.env.example` if commands or config changed.
5. Finish by stating what changed, how it was verified, and the next phase's prompt.

## Gotchas

- **Development uses no Docker:** a locally installed PostgreSQL and the `migrate` CLI, as in Greenlight. Don't add Docker to `dev/*` targets. Docker is for staging only.
- The developer is on **Windows + Git Bash**. Use LF line endings (`.gitattributes`), real TABs in Makefile recipes, and keep `MSYS_NO_PATHCONV=1` for docker paths.
- `app.env` is read by both Viper and Make: plain `KEY=value` only, with no quotes, `$`, `#` or spaces in values. It's git-ignored; `.env.example` is the template and must never hold real secrets.
- In Gin, `c.JSON` is not used. Use `writeJSON` so responses are consistent. 404/405 are already JSON via `NoRoute`/`NoMethod`.
- Speed near 0 means the bearing is unreliable. Cone matching falls back to a full circle (document this as an assumption).
- Angle difference must handle wraparound: `d = mod(b − h + 540, 360) − 180`, and check `|d| ≤ 30`.
- `go.sum` is generated locally with `go mod tidy`. Don't hand-edit `go.mod` versions.

## Out of scope (unless asked)

GraphQL, user registration, emails, activation (Greenlight ch. 13–15), PostGIS, OSRM road routing, WebSocket push. List these as **Improvements** in `DOCUMENTATION.md` instead.
