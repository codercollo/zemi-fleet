# Testing

How the project is tested, what each kind of test is allowed to touch, and what is deliberately not covered.

## Systems under test

| System under test | Boundary | Real vs test double | Test type | Location | Build tag | Make target |
|---|---|---|---|---|---|---|
| **Pure packages**: `geo`, `batching`, `matching`, `clock`, `validator`, `config` | Function arguments in, values out. No DB, HTTP, config or clock reads (`config` reads env/files, `clock` is the one place that reads the system time) | Nothing is doubled; inputs are literals or seeded `math/rand`. `config` tests use temp files and `t.Setenv` | Unit: table-driven, plus property tests (`geo` bounding box, `batching` partition invariants) | `internal/geo`, `internal/batching`, `internal/matching`, `internal/clock`, `internal/validator`, `internal/config` | none | `make dev/test` |
| **Repository `Store`** (`internal/repository`: sqlc queries, `ExecTx`, the create/accept/expire/list batch transactions, DBTX wrappers for timeouts, UTC and `ErrNotFound`) | SQL over a pgx pool | **Real PostgreSQL** (`TEST_DATABASE_URL`, a `*_test` database). The timeout/UTC/not-found wrappers also have unit tests over a fake `DBTX` | Integration | `internal/repository/*_integration_test.go`, `internal/service/*integration_test.go`, `internal/api/*integration_test.go` | `integration` | `make dev/test/integration` |
| **Services** (`internal/service`: order, rider, matching, batch, preference) | The `repository.Store` interface | **gomock `Store`** plus `clock.Test`, so every call and every timestamp is controlled | Unit with mocks | `internal/service/*_test.go` | none | `make dev/test` |
| **HTTP handlers** (`internal/api`: routing, validation, JSON shape, status mapping) | `http.Handler` driven with `httptest` | Real router, real services, **gomock `Store`**, `clock.Test` (no DB) | Unit with mocks | `internal/api/*_test.go` | none | `make dev/test` |
| **Whole API, end to end** | HTTP, over a real TCP listener | **Real wiring** (`config.Load` -> pgx pool -> `Store` -> services -> handlers, built by `app.Build`, served by `httptest.Server`) + **real PostgreSQL test database** + `clock.Test`. Nothing else is doubled | End-to-end scenarios + SQL invariants | `internal/e2e` | `e2e` | `make dev/test/e2e` |

Run everything with `make dev/test/all` (unit, then integration, then e2e).

## The test database

Integration and e2e tests **never touch your development data**.

- They connect to `TEST_DATABASE_URL` only (`.env.example`, `app.env`), a separate database named `zemi_fleet_test`.
- `internal/testdb` refuses to run unless the database name ends in `_test`, both from the URL and from `SELECT current_database()`.
- `make dev/test/integration` and `make dev/test/e2e` first run `migrate ... up` against `TEST_DATABASE_URL` (never `DATABASE_URL`).
- Every test resets its own state: `testdb.Reset` truncates the data tables (all but `schema_migrations`), then `testdb.LoadReference` loads categories, areas and vendors (`db/testdata/reference.sql`) and `testdb.AddRider` loads riders. No test relies on run order or on what another test left behind. A few tests load the whole development seed (`testdb.LoadSeed`) into the test database because they check the seed itself.
- The packages share one database, so tagged runs use `-p 1`, and the e2e scenarios never call `t.Parallel`.

One-time setup, as a PostgreSQL superuser (the same way the dev database was created):

```sql
CREATE DATABASE zemi_fleet_test OWNER zemi;
```

## What each layer is responsible for proving

- **Pure packages** prove the *rules*: the cone is inclusive at exactly 30 degrees, a pickup must be within the radius of *every* other pickup in a batch, the weights sum to 1.0, a bounding box contains every point within its radius. They are fast, deterministic and exhaustive about edge cases.
- **Repository (integration)** proves the *SQL and the database contract*: that `FOR UPDATE SKIP LOCKED` really prevents double batching, that the conditional `UPDATE ... WHERE status = 'open'` really lets one rider win, that the row lock serialises one rider's two accepts, that statements are cancelled at `DB_QUERY_TIMEOUT`, that every `time.Time` read back is UTC, that CHECK constraints hold. Mocks cannot prove any of this.
- **Services (mocked `Store`)** prove the *orchestration*: which store calls are made, in what order, with which arguments; that errors map to the right domain errors; that capacity and availability short-circuit before expensive work; that the injected clock is the only source of time.
- **Handlers (mocked `Store`)** prove the *HTTP contract*: status codes (200/201/400/404/409/422/500), field names and key order, validation messages, headers, that a 500 never leaks internals.
- **End to end** proves that *the features work together* on the real stack: an order created over HTTP is matched, batched, listed, accepted and counted consistently; capacity is enforced by every feature; expiry releases orders; concurrent riders never double-book. It is deliberately small. Each scenario drives the API over HTTP only and ends by running `assertInvariants`, which checks the database with SQL:
  1. every `batched` order belongs to exactly one open batch (and `orders.batch_id` points at it);
  2. every `assigned` order belongs to an accepted batch with the same rider;
  3. each rider's `current_orders_count` equals their assigned orders and is at most `max_capacity`;
  4. no order is in two non-expired batches;
  5. every batch's pickups come before their deliveries, each order has one pickup and one delivery stop, and `orders_count` matches the stops;
  6. money: rider earnings + platform fee = delivery fee on every order, and each batch's estimated earnings = the sum of its orders' rider earnings;
  7. open/expired batches have no rider, accepted ones have a rider and a time, and no idempotency key is left claimed but unfinished;
  8. every timestamp in every captured JSON response is RFC 3339 in UTC (ends in `Z`).

The seven tests (five scenarios plus two for retries): **order lifecycle** (create, match-riders, match-orders, batch, list, accept, lose the race), **capacity across features** (3/5 then 5/5: batches shrink, match-orders, batch, match-riders and accept all agree), **expiry and release** (time passes, orders come back, the old batch cannot be accepted), **concurrent load** (five riders batch and race to accept every visible batch), **location freshness** (a ping makes the distance known; a stale ping makes it null), **idempotent retries** (a retried `POST /orders` creates exactly one order, however it arrives: sequentially, ten at once, after a failure, after the TTL), and **idempotent batch request**.

## Timestamps and time in tests

Only `internal/clock` reads the system clock (`TestNoTimeNowOutsideClockPackage` enforces it). Service, handler and e2e tests inject a `clock.Test`, so "ten minutes later" is `clk.Advance(...)`, never a sleep.

## Static checks

`make dev/audit` runs `go mod tidy -diff`, `go mod verify`, gofmt, `go vet`, staticcheck, the tests, and `govulncheck` (pinned) for known vulnerabilities in dependencies and in the Go toolchain. It also vets and staticchecks the files behind the `integration` and `e2e` build tags, which a plain `go vet ./...` never looks at.

## Known gaps

- **Race detector locally.** `go test -race` needs cgo (a C compiler). The Windows development machine has none, so the race detector runs only in CI (`integration-e2e` job: `-race -tags=e2e -count=3`).
- **Concurrency tests are evidence, not proof.** They show the invariants held in many racing runs; the guarantee itself comes from the SQL (`WHERE status = 'open'`, `SKIP LOCKED`, the rider-then-batch lock order), argued in `docs/DOCUMENTATION.md`.
- **No fault injection.** Nothing kills the database mid-request, drops connections, or fills the pool; only the query timeout path is exercised (a real `pg_sleep`).
- **No load or performance tests.** Matching and batching load all eligible riders/orders in memory; their behaviour at tens of thousands of rows is untested.
- **No black-box contract tests yet.** The OpenAPI spec and Postman collection arrive in Phase 12 and are not yet checked against the handlers.
- **govulncheck depends on the installed Go.** A local toolchain older than the newest patch of its line fails the audit on standard-library findings; CI installs the latest patch (`check-latest`).
- **Not yet covered:** request logging, rate limiting and graceful shutdown (Phase 9), API-key authentication (Phase 10), CORS and metrics (Phase 11).
- **End to end does not cover every endpoint.** `PUT /riders/:id/preferences`, `PATCH /riders/:id/availability` and `GET /orders/:id` are covered by handler and integration tests, not by an e2e scenario.
- **The seeded `now()` timestamps** (`db/seed/seed.sql`) are relative to real time, so tests that load the seed and depend on location freshness must run within `RIDER_LOCATION_STALE_AFTER` of loading it. The scenarios that depend on freshness use the test clock and fixtures instead.
- **The staging Docker stack** (`make staging/up`) is checked only by its smoke target, not by this suite.
- **`db/testdata/reference.sql` is a copy** of the categories, areas and vendors in the seed; `TestIntegration_ReferenceDataMatchesSeed` fails if the two drift apart.
