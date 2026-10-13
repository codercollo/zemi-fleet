# Zemi Fleet V2 — Build Phases

The build follows the chapter order of *Let's Go Further* (Greenlight). Each phase:

- builds on the one before it and leaves the repo **green**: `make dev/audit` passes and the API runs
- is small enough to review and commit on its own
- ends with a **Prompt** block. Paste it into the chat to get the next phase's code.

Where the book's chapters don't fit the brief (emails, user activation), they're skipped or reduced, with the reason noted.
The Zemi algorithms aren't in the book. They're added as their own phases right after CRUD (chapters 7–8), which is when the data they need exists.

**Stack mapping from the book:** `httprouter` → Gin (whose router is a fork of httprouter), `database/sql` + hand-written models → pgx + sqlc + a Store (Simple Bank pattern),
`flag` config → Viper (`app.env` + environment variables), plain `testing` → testify + mockgen + faker.

**Layers (n-tier):** `internal/api` (HTTP) → `internal/service` (business rules) → `internal/repository` (sqlc Store).
The algorithm packages (`geo`, `batching`, `matching`) are pure Go and are called only from the service layer.

**Environments**

| Machine | How the API runs | Driven by |
|---|---|---|
| Development (your laptop) | `go run ./cmd/api` against your locally installed PostgreSQL; no Docker. The DB and role are created by hand, and migrations run with the `migrate` CLI (as in Greenlight) | `make dev/...` |
| Staging (Docker Desktop) | Linux binary built by `make`, packaged into the image, then run with Compose | `make staging/...` |
| Production | The **same binary/image** that passed CI | `.github/workflows/deploy.yml` |

---

## Status

| # | Phase | Book chapters | Status |
|---|---|---|---|
| 0 | Foundation: server, JSON in/out, errors, validator, config, DB pool | 2, 3, 4, 5 | ✅ done |
| 1 | Build tooling: Makefile (dev/staging), versioning, Docker, CI workflows | 20, 21 (moved early) | ✅ done |
| 2 | Migrations, seed data, sqlc setup | 6 | ⏳ next |
| 3 | Domain models, repository Store, first CRUD (orders, riders, preferences) | 7 | |
| 4 | Advanced CRUD: optimistic locking, query timeouts, partial updates | 8 | |
| 5 | Geo core: haversine, bearing, angle difference | — (algorithm) | |
| 6 | Proximity matching: 60° cone, `POST /riders/match-orders` | 9 (sorting) | |
| 7 | Batching: clustering, sequencing, scoring, batch endpoints, exclusive accept | 8, 9 | |
| 8 | Preference matching: `POST /orders/match-riders` | 9 | |
| 9 | Structured logging, panic recovery, rate limiting, graceful shutdown | 10, 11, 12 | |
| 10 | Authentication: API key (+ PASETO stretch) | 16, 17 (13–15 skipped) | |
| 11 | CORS and metrics | 18, 19 | |
| 12 | Deliverables: OpenAPI, Postman, DOCUMENTATION.md, README, final audit | 20, 21 | |

Target pace (deadline Fri 16 Oct, 22:00): Mon 0–2 · Tue 3–5 · Wed 6–7 · Thu 8–10 · Fri 11–12, submit by 18:00.

---

## Phase 0 — Foundation ✅
**Book:** 2 Getting Started · 3 Sending JSON Responses · 4 Parsing JSON Requests · 5 Database Setup

**What exists**
- `cmd/api/main.go`: config, then logger, then pgx pool, then `http.Server` with timeouts
- `internal/config`: Viper settings (environment variables override `app.env`, which overrides defaults), validated at startup
- `internal/api`: `Server` struct (the book's `application`), `routes.go`, `writeJSON` + `envelope`, `readJSON` (size limit, unknown fields, single value), and centralised error responses (JSON 404/405)
- `internal/validator`: the book's `Validator` (Check / AddError / PermittedValue / Unique / Between)
- `internal/repository/db.go`: `OpenPool` with pool settings + ping
- `GET /api/v2/healthz`: returns 503 when the DB is down

**Done when:** `make dev/test` passes and, with the local database created and migrated, `curl :4000/api/v2/healthz` shows `"database": "up"`.

---

## Phase 1 — Build tooling ✅
**Book:** 20 Building, Versioning and Quality Control · 21 Deployment (moved forward so every later phase is built, tested and shipped the same way)

**Files:** `Makefile`, `Dockerfile`, `.dockerignore`, `docker-compose.yml` (staging only), `.github/workflows/test.yml`, `.github/workflows/deploy.yml`, `cmd/api/main.go` (version + `-version` flag), `README.md`

**Key decisions**
- The Makefile has only `dev/*` and `staging/*` targets. The `dev/*` targets use your local PostgreSQL and the `migrate` CLI; Docker appears only in `staging/*`. Production is run by CI, never from a laptop.
- `staging/build` cross-compiles `bin/linux_amd64/api` with the git version stamped in, and the Docker image just copies that binary (Dockerfile target `prebuilt`). CI's deploy workflow does the same, so the binary that passed the tests is the binary that ships.
- The Dockerfile's default target still compiles inside Docker, so a reviewer without Go or make can run `docker compose up --build`.
- **Go 1.25:** `go.mod` says `go 1.25.0` because pgx/v5 and gin both require ≥ 1.25. The Dockerfile builder image (`golang:1.25-alpine`) and CI (`go-version-file: go.mod`) follow it.
- **staticcheck is pinned to `v0.8.2`** in the Makefile and `test.yml` (the newest release; it runs on Go 1.27 locally and 1.25 in CI).
- **Compose healthcheck:** Postgres uses `pg_isready -h 127.0.0.1` (TCP). During first-run init Postgres listens only on a unix socket, so a socket check reports healthy too early and `migrate` fails with "connection refused".
- If a port is taken on Docker Desktop, override it: `STAGING_API_PORT` (default 8080) and `STAGING_DB_PORT` (default 5433).

**Done when:** `make dev/audit` passes; `make staging/up && make staging/smoke` returns 200; `./bin/api -version` prints the git version.

---

## Phase 2 — Migrations, seed data, sqlc setup
**Book:** 6 SQL Migrations

**Scope**
- Keep `db/migrations/000001_init_schema` (already drafted and tested; `users`/`sessions` are already removed and would return as `000002_auth` only for the Phase 10 PASETO stretch).
- `db/seed/seed.sql`: Nairobi areas (Kilimani, Westlands, CBD, Kileleshwa, Upper Hill, Lavington, Parklands, South B), 6 vendors, 10 riders with preferences, ~40 pending orders spread over 2 hours of `created_at`, all with real coordinates.
- `sqlc.yaml` (pgx/v5, `emit_interface`, `emit_json_tags`, overrides for numeric/timestamptz).
- Makefile: `dev/db/seed` (`psql "$(DATABASE_URL)" -f db/seed/seed.sql`), `dev/sqlc`; `staging/seed`.
- The CI test workflow runs migrations up → down → up.

**Done when:** `make dev/db/migrations/up && make dev/db/seed` loads the data into your local database; `make dev/sqlc` generates code that compiles; CI migration step passes.

> **Prompt:** Build Phase 2 from docs/BUILD_PHASES.md (Let's Go Further ch. 6). Write the Nairobi seed data, add sqlc.yaml and the Makefile seed/sqlc targets, and add the migration up/down/up check to test.yml. Keep everything green and show me how to verify.

---

## Phase 3 — Domain + repository + first CRUD
**Book:** 7 CRUD Operations

**Scope**
- `internal/domain`: `Location`, `Order`, `Rider`, `Preferences`, `Batch`, `Stop`, and domain errors (`ErrNotFound`, `ErrConflict`, `ErrRiderAtCapacity`, `ErrBatchTaken`).
- `db/query/{order,rider,preference,area,category}.sql` and the sqlc-generated code.
- `internal/repository/store.go`: `Store` interface + `SQLStore` + `ExecTx` (Simple Bank).
- `internal/service/order_service.go`, `rider_service.go`: these precompute each order's trip distance and bearing and snapshot the rider payout percentage (90%, `RIDER_PAYOUT_BPS`).
- `internal/util/id.go`: prefixed sortable IDs (`ord_`, `rider_`, `batch_`).
- Endpoints: `POST /orders`, `GET /orders/:id`, `GET /riders/:id`, `PUT /riders/:id/preferences`.
- Tests: handler tests with a gomock Store and faker data; `make dev/mock`.

**Done when:** an order created over HTTP can be read back; validation errors return 422 with per-field messages; unknown IDs return 404.

> **Prompt:** Build Phase 3 from docs/BUILD_PHASES.md (Let's Go Further ch. 7) on top of Phase 2. Add the domain package, the sqlc queries, the Store with ExecTx, the order and rider services, the prefixed ID helper, and the four endpoints, with gomock + faker handler tests.

---

## Phase 4 — Advanced CRUD
**Book:** 8 Advanced CRUD Operations

**Scope**
- `PATCH /riders/:id/location` and `PATCH /riders/:id/availability` (partial updates with pointer fields, as in the book).
- Optimistic concurrency on `version` (`WHERE id = $1 AND version = $2`, where 0 rows means `ErrEditConflict` and a 409).
- A per-query `context.WithTimeout` (`DB_QUERY_TIMEOUT`, default 3 s).

**Done when:** two concurrent PATCHes on the same rider give one 200 and one 409.

> **Prompt:** Build Phase 4 from docs/BUILD_PHASES.md (Let's Go Further ch. 8): partial updates for rider location and availability, optimistic locking on version with 409 edit conflicts, and query timeouts from config. Include a concurrency test.

---

## Phase 5 — Geo core
**Scope**
- `internal/geo`: `Haversine`, `Bearing`, `AngleDiff` (handles wraparound, e.g. 350° vs 10°), `BoundingBox` (prefilter for the SQL queries).
- Table-driven tests with known Nairobi distances, wraparound, identical points and antipodes.

**Done when:** `go test ./internal/geo -cover` reports ≥ 95% coverage.

> **Prompt:** Build Phase 5 from docs/BUILD_PHASES.md: the pure geo package (haversine, bearing, angle difference, bounding box) with table-driven tests at ≥95% coverage.

---

## Phase 6 — Proximity matching (60° cone)
**Book:** 9 Filtering, Sorting (sorting by distance, then by order age)

**Scope**
- `internal/matching/cone.go`: candidates come from a bounding-box SQL query. Keep those ≤ `CONE_RADIUS_KM` away whose angle from the rider's bearing is ≤ `CONE_HALF_ANGLE_DEG`. Sort by distance, then by age.
- Capacity: `max_additional_orders = max_capacity − current_orders_count`. At capacity, return an empty list and `can_accept_more_orders: false`.
- Speed below `CONE_MIN_SPEED_KMH`: the bearing is unreliable, so fall back to a full circle (documented).
- `POST /api/v2/riders/match-orders`, using the brief's exact response shape. This also updates the rider's last known location.

**Done when:** tests cover inside/outside the cone, the 0°/360° wraparound, a rider at 5/5, speed 0, and no orders.

> **Prompt:** Build Phase 6 from docs/BUILD_PHASES.md: cone matching in internal/matching with tests, the service method, and POST /api/v2/riders/match-orders with the exact response shape from the brief.

---

## Phase 7 — Batching + exclusive accept
**Book:** 8 (concurrency) · 9 (filtering)

**Scope**
- `internal/batching`:
  - **batcher**: take the oldest pending order as the seed (FIFO), cluster pickups within 2 km of it, rank candidates by how close their trip bearing is to the seed's, and cap the batch at 5 orders.
  - **sequencer**: nearest-neighbour stop order where each pickup comes before its delivery.
  - **scoring**: `route_optimization_score` = sum of solo trip distances ÷ batched route distance, capped at 1; earnings = 90% of the fees.
- `tx_create_batches.go`: `FOR UPDATE SKIP LOCKED`, so concurrent batch requests never pick the same order.
- `tx_accept_batch.go`: lock the rider, claim the batch only while it's `open` and its `version` matches, assign its orders, and increment the rider's order count.
- Endpoints: `POST /orders/batch` (the brief), `GET /batches?lat=&lng=&radius_km=` (open batches near a position), `POST /batches/:id/accept` (409 if taken, 422 if over capacity).

**Done when:** a test where N goroutines accept the same batch produces exactly one winner, and the edge cases (0, 1 and 6 orders, opposite directions) pass.

> **Prompt:** Build Phase 7 from docs/BUILD_PHASES.md: the batching package (batcher, sequencer, scoring) with tests, the create-batches and accept-batch transactions, and the three batch endpoints. Include the concurrent-accept test.

---

## Phase 8 — Preference matching
**Scope**
- `internal/matching/preference.go`:
  - **Hard filters:** the rider is available, under capacity, and the order's value is ≥ the rider's minimum.
  - **Score:** weighted category, area, proximity and acceptance rate. The weights come from config.
- `matching_preferences` lists which of the rider's preferences matched.
- `POST /api/v2/orders/match-riders`, using the brief's exact shape, best match first.

**Done when:** tests cover a rider with no preferences (matches everything), an order below a rider's minimum (excluded), the ranking order, and the case where no riders match.

> **Prompt:** Build Phase 8 from docs/BUILD_PHASES.md: preference filtering and scoring with tests, and POST /api/v2/orders/match-riders.

---

## Phase 9 — Logging, rate limiting, graceful shutdown
**Book:** 10 Structured Logging and Error Handling · 11 Rate Limiting · 12 Graceful Shutdown

**Scope:** request ID + slog access-log middleware, `recoverPanic`, per-IP token bucket (`x/time/rate`, configurable, 429 response), SIGINT/SIGTERM shutdown with a 30-second drain (`SHUTDOWN_TIMEOUT`).

> **Prompt:** Build Phase 9 from docs/BUILD_PHASES.md (Let's Go Further ch. 10–12): logging middleware with request IDs, panic recovery, per-IP rate limiting with config, and graceful shutdown.

---

## Phase 10 — Authentication
**Book:** 16 Authentication · 17 Permission-based Authorization. Chapters 13–15 (registration, emails, activation) are skipped because the brief asks for "simple API key or JWT".

**Scope:** `requireAPIKey` middleware (keys from `API_KEYS`, compared in constant time, 401 with `WWW-Authenticate`) applied to every route except `/healthz`. Stretch: `internal/token` with PASETO, and `POST /auth/token`.

> **Prompt:** Build Phase 10 from docs/BUILD_PHASES.md (Let's Go Further ch. 16–17): API-key authentication middleware on all routes except healthz, with tests. Add PASETO only if I say so.

---

## Phase 11 — CORS + metrics
**Book:** 18 Cross Origin Requests · 19 Metrics

**Scope:** trusted-origins CORS middleware (`CORS_TRUSTED_ORIGINS`), `expvar` metrics at `/debug/vars` (request counts, status codes, pool stats). Metrics are protected or disabled in production.

> **Prompt:** Build Phase 11 from docs/BUILD_PHASES.md (Let's Go Further ch. 18–19): CORS middleware and expvar metrics.

---

## Phase 12 — Deliverables
**Book:** 20–21 (final pass)

**Scope:** `api/openapi.yaml` + Swagger UI at `/swagger`, a Postman collection (folders: Health, Orders, Order Batching, Batches, Proximity Matching, Rider Matching, Rider Preferences) with saved examples, `docs/DOCUMENTATION.md` (overview, architecture + ERD diagrams, API reference, algorithm logic, setup, schema, assumptions, **improvements**), a final README, and `scripts/smoke_test.sh` covering every endpoint. Test the setup from a fresh clone.

> **Prompt:** Build Phase 12 from docs/BUILD_PHASES.md: OpenAPI spec with Swagger UI, the Postman collection with examples, DOCUMENTATION.md with every section the brief asks for, the final README, and a full smoke test.
