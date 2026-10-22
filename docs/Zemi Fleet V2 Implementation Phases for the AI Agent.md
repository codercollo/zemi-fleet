# Zemi Fleet V2: Implementation Phases for the AI Agent

A phased build plan for taking Zemi Fleet V2 to production readiness, written from the README, DOCUMENTATION.md, TESTING.md and openapi.yaml. Each phase is one pull request. Hand the agent one phase at a time.

## Overview

| Phase | Goal | Migration | Flag (default) |
| --- | --- | --- | --- |
| 0 | Baseline: load seed, load tests, fault injection | no | none |
| 1 | Service-area (Kenya) coordinate validation | no | `GEO_BOUNDS_ENABLED` (off, then on) |
| 2 | Spatial work and paging moved into SQL | maybe | `SPATIAL_QUERY_MODE` (legacy) |
| 3 | Background batch expiry | no | `BATCH_EXPIRY_INTERVAL` (0 = off) |
| 4 | Order and batch lifecycle: stop completion, cancellation, failed attempts | yes | none (new endpoints) |
| 5 | Offline rider detection and reassignment | yes | `RIDER_OFFLINE_AFTER` (0 = off) |
| 6 | Priority ordering and adaptive radius | yes | `BATCH_ORDERING`, `BATCH_RADIUS_MODE` (fifo, fixed) |
| 7 | Dynamic rider capacity | yes | `CAPACITY_MODE` (count) |
| 8 | Routing provider, better sequencing | no | `ROUTING_PROVIDER` (haversine) |
| 9 | Per-rider tokens and scopes | no | `AUTH_MODE` (apikey) |
| 10 | Outbox events and partner webhooks | yes | `WEBHOOKS_ENABLED` (off) |
| 11 | Prometheus metrics, alerts, shared rate limiter | no | `LIMITER_STORE` (memory) |

The order matters: phases 0 to 3 are safe groundwork, 4 and 5 add the state the rest of the plan needs, 6 to 8 change the algorithms, 9 to 11 harden and operate the system.

## Rules the agent follows in every phase

1. **Read before writing.** Read the sections of DOCUMENTATION.md, the tests and the SQL the phase touches. Follow the existing patterns (Let's Go Further handlers, Simple Bank repository, sqlc, gomock).
2. **Defaults reproduce today's behaviour.** Every new behaviour sits behind an environment variable read in `internal/config`, validated at startup (the server refuses to start on a bad value), and listed in `.env.example`, `docker-compose.yml` and the Configuration reference. With every flag at its default, the existing unit, integration and e2e tests must pass without edits.
3. **Pure packages stay pure.** `geo`, `batching`, `matching` and any new algorithm package import no config, clock, database or HTTP. Parameters are arguments.
4. **Time.** Only `internal/clock` reads the system clock (`TestNoTimeNowOutsideClockPackage` enforces it). Tests use `clock.Test` and `Advance`, never `time.Sleep`. All timestamps are UTC with a Z suffix.
5. **Money.** int64 cents, never floats. Generated columns keep rider earnings plus platform fee equal to the delivery fee. Any new money flow adds a CHECK constraint and an invariant.
6. **Database.** One migration pair (up and down) per schema change; CI runs up, down, up. Put queries in `db/query`, then run `make dev/sqlc` and `make dev/mock`; never hand-edit generated code. Constraints stay the last line of defence. Integration and e2e tests run with `-p 1` and e2e never calls `t.Parallel`.
7. **Contract.** Every API change updates `api/openapi.yaml` in the same PR. The route parity test, the example validation test and `TestE2E_ResponsesMatchTheOpenAPIContract` must pass; response schemas keep `additionalProperties: false`, so new fields must be documented. Never rename an existing field.
8. **Invariants.** Every new state, counter or money flow adds a check to `assertInvariants` in `internal/e2e`, and each new e2e scenario ends by calling it.
9. **Logging and errors.** Fixed log messages with data in attributes (CWE-117); phone numbers go through the masking helpers; keys, tokens and secrets are never logged; a 500 returns only the fixed generic message.
10. **Docs in the same PR.** Update DOCUMENTATION.md (the relevant section, Configuration reference, Database Schema, Assumptions), move delivered items out of Improvements, and update the Known gaps in TESTING.md.
11. **Finish line.** `make dev/audit` and `make dev/test/all` are green, `-race` passes in CI, and the PR description lists the flags added, the migration numbers, the tests added and any load numbers the phase requires.
12. **Stop and ask** if a change conflicts with Zemi's clarifications: batches are open offers, the 2 km rule is a pairwise pickup radius, direction similarity is a tolerance, and the rider earns 90% of the delivery fee as snapshotted on the order.

## Phase 0: Baseline, load tests and fault injection

**Goal:** measure before optimising. No production code changes.

**Tasks**

- Write a generator (`scripts/gen_load_seed.*`) that produces `db/seed/load.sql`: 500+ riders, 10,000 and 50,000 pending orders, a dense CBD cluster, suburban clusters and sparse outskirts, all inside Nairobi. Keep it separate from `seed.sql`.
- Add `make dev/test/load` (k6 or vegeta) against the staging stack with `LIMITER_ENABLED=false`. Scenarios: `POST /orders/batch`, `POST /riders/match-orders`, `POST /orders/match-riders`, `GET /batches`, `POST /batches/{id}/accept`. Record p50, p95, p99 and error rate.
- Capture `EXPLAIN (ANALYZE, BUFFERS)` for each main query.
- Add a Toxiproxy service (compose profile `chaos`) between the API and PostgreSQL, and chaos tests behind a `chaos` build tag: connection dropped mid-accept, latency above `DB_QUERY_TIMEOUT`, pool exhaustion.
- Add a non-blocking nightly CI job that publishes the numbers.

**Done when:** `docs/PERFORMANCE.md` holds the baseline, `EXPLAIN` plans and proposed latency targets (suggest p95 under 200 ms at 10k pending orders); chaos tests show a generic 500, no half-written state and a passing `assertInvariants`.

## Phase 1: Service-area validation

**Goal:** reject garbage coordinates. Today only range is checked (latitude -90 to 90) and the docs list no Nairobi or Kenya check as an assumption.

**Tasks**

- Config: `GEO_BOUNDS_ENABLED` (default false), `GEO_MIN_LAT=-4.7`, `GEO_MAX_LAT=4.7`, `GEO_MIN_LNG=33.9`, `GEO_MAX_LNG=41.9`; startup validation that min is below max.
- Add a bounds helper to `internal/validator` and apply it to: `POST /orders` delivery location, `PATCH /riders/{id}/location`, `POST /riders/match-orders` location, `POST /orders/batch` rider location, `GET /batches` lat and lng, and `order_details` pickup and delivery. Failure is `422` with a field message such as `must be within the service area`.
- Do not rebuild what exists: ID validation, order existence (`404`), the 1 MiB body cap and idempotency are already covered. Only add tests where a gap is found.
- Audit the seed, `reference.sql` and every test fixture for out-of-bounds points. When clean, flip the default to true for staging and production.

**Tests:** table-driven bounds cases (exactly on each edge, just outside by 1e-6, swapped latitude and longitude, zero point); handler `422` per endpoint; flag off behaves exactly as before; an e2e check that a rejected request changes no rows (model it on `TestE2E_UnauthenticatedRequestsChangeNothing`).

**Done when:** the OpenAPI 422 examples are updated and all existing tests pass with the flag both off and on.

## Phase 2: Spatial queries and paging in SQL

**Goal:** stop loading candidate sets into memory. Context: pending orders already use a bounding box plus `idx_orders_pending_pickup`. The real memory risks are `match-riders` (loads every available rider) and `GET /batches` (loads all candidates, then pages in Go).

**Tasks**

- Re-run the Phase 0 `EXPLAIN` and load test at 50k orders to confirm where the time goes.
- Try plain PostgreSQL first: a bounding box prefilter on riders (extend the partial index on available riders), a centroid index for open batches, and `ORDER BY`, `LIMIT/OFFSET` and the total count pushed into SQL for `GET /batches`.
- Adopt PostGIS (geography columns generated from lat and lng, GiST indexes, `ST_DWithin`, Docker image `postgis/postgis:16`) only if the Phase 0 targets are still missed. Keep the lat and lng columns either way.
- The database narrows the candidates; the pure `geo` and `matching` code still applies the exact inclusive rules (radius, 30 degree edge, 1 micrometre tolerance), so semantics do not change.
- Add `SPATIAL_QUERY_MODE=legacy|sql` (default `legacy`) so the old path stays as a rollback until the next release.

**Tests**

- **Differential test:** on seeded `math/rand` datasets (dense, sparse, points exactly on the radius and cone edge), run the legacy and SQL paths for batching, cone matching, rider matching and batch listing, and assert identical output.
- Integration: `FOR UPDATE SKIP LOCKED` and the conditional accept `UPDATE` still behave under the new queries.
- An `EXPLAIN` test asserting an index scan, not a sequential scan, on the load seed.

**Done when:** before and after numbers are in `docs/PERFORMANCE.md`. If targets are met, record the decision not to add Redis; if not, write a short decision record. Do not add Redis in this phase.

## Phase 3: Background batch expiry

**Goal:** orders return to `pending` the moment an offer lapses, not when the next request happens to repair state (lazy expiry today).

**Tasks**

- A ticker started through `Server.background` (so panics are recovered) calls the existing expire transaction. Config: `BATCH_EXPIRY_INTERVAL` (default `0` = off).
- Safe across instances: select expired batches with `FOR UPDATE SKIP LOCKED`.
- Metric `batches_expired`, incremented only after commit. It stops on shutdown and the shutdown waits for it.

**Tests:** `clock.Test` and `Advance`, no sleeps; shutdown test (task stops, shutdown waits); integration test where two concurrent sweeps release each order exactly once; extend the e2e expiry scenario so orders are released with no request.

**Done when:** invariants 1 and 4 hold after sweeps and the old lazy path still works with the flag off.

## Phase 4: Order and batch lifecycle

**Goal:** finish the loop the schema already anticipates (`in_progress`, `completed`, `picked_up`, `delivered`, `cancelled`, `batch_stops.completed_at`). Failed delivery attempts and cancellation are the main real-world cases.

**Tasks**

- Write the state machine first as a pure package (for example `internal/lifecycle`): `Transition(state, event)` with events pickup confirmed, delivered, attempt failed, cancel, return started, returned. Table-test it before any SQL.
- Migrations: add the enum values `returning` and `returned` in their own migration (a new enum value cannot be used in the transaction that adds it). In a second migration add `delivery_attempts` (0 to 3 CHECK), `cancelled_at`, `cancel_reason` (a code, no free text), `rider_compensation_cents`; update `chk_orders_assignment`.
- Endpoints, all documented in OpenAPI: `POST /batches/{id}/stops/{seq}/complete`, `POST /orders/{id}/attempts` (reason code; the third failure moves the order to `returning`), `POST /orders/{id}/cancel`. Support `Idempotency-Key`, validate IDs with the existing patterns, and use conditional `UPDATE ... WHERE status = ...` so a race has one winner (lock order: rider first, then batch).
- In the same transaction: delivered, cancelled and returned orders decrement `current_orders_count` (never below 0); the batch becomes `completed` when its last stop completes.
- Compensation: `CANCEL_COMPENSATION_BPS` (default 0) pays a rider part of their earnings when an order is cancelled after pickup. A CHECK ensures compensation plus platform fee never exceeds the delivery fee.
- Define an `EventSink` interface with a no-op implementation and call it from each transition; Phase 10 supplies the real one.

**Tests:** every legal and illegal transition; integration races (cancel against complete, two completes of one stop); e2e scenarios: full delivery frees capacity, three failed attempts then return, mid-route cancellation. New invariants: attempts at most 3; cancelled and returned orders hold no capacity; `current_orders_count` equals the rider's `assigned` plus `picked_up` orders; compensation money check. Extend invariant 2 to cover `picked_up` orders and `in_progress` batches.

## Phase 5: Offline rider detection

**Goal:** a rider whose phone dies does not hold orders forever. Uses Phases 3 and 4.

**Tasks**

- Config: `RIDER_OFFLINE_AFTER` (default `0` = off; proposed 5m) and `RIDER_OFFLINE_SWEEP_INTERVAL`.
- A background sweep finds riders holding active orders whose `location_updated_at` is older than the threshold (a null timestamp counts as stale). New column `offline_flagged_at`.
- **Assigned but not picked up:** release the orders to `pending` through the Phase 4 transition and decrement the count, so they become matchable again.
- **Picked up:** do not reassign (the parcel is with the rider). Flag the rider and emit an event for follow-up.
- The rider's next location ping clears the flag. The location route must still never return `409` for telemetry.
- Call the `EventSink` with `rider.offline` and `order.released`.

**Tests:** `clock.Advance` just under, at and over the threshold; null location; picked-up orders untouched; ping clears the flag; two sweeps do not double-release. New invariant: no `assigned` order belongs to a flagged rider.

## Phase 6: Priority ordering and adaptive radius

**Goal:** high-value or long-waiting orders are not stuck behind others, and dense areas are not over-batched.

**Tasks**

- Priority (pure, in `batching`): a score from order age, value, vendor rating and optional urgency. Weights `BATCH_WEIGHT_AGE`, `_VALUE`, `_RATING`, `_URGENCY` must sum to 1.0 (startup refusal, like the match weights). Migration for `vendors.rating` and optional `orders.urgency`; any new request field on `POST /orders` is optional with a default.
- `BATCH_ORDERING=fifo|priority` (default `fifo`). Priority mode must still pick the seed deterministically and keep FIFO as the final tie-break. The age term must grow without bound so no order starves (add a maximum-wait property test).
- Adaptive radius: `geo.Density` classifies the area around the seed; thresholds and radii are config (`BATCH_DENSITY_*`). `BATCH_RADIUS_MODE=fixed|adaptive` (default `fixed`). Adaptive may only narrow the requested `radius_km`. Widening beyond `BATCH_PICKUP_RADIUS_KM` (2 km) contradicts the brief, so stop and ask Zemi before doing it.

**Tests:** property tests for every mode (each order in at most one batch, every pair of pickups within the radius, size never above capacity); a **golden test** that with defaults the output on the Nairobi seed is byte-identical to today; weights sum to 1.0; determinism; starvation bound.

## Phase 7: Dynamic rider capacity

**Goal:** capacity reflects load and rider condition, with `max_capacity` (1 to 5) as the hard ceiling.

**Tasks**

- A pure `capacity` package: effective capacity from load (optional `weight_g` and `volume_cm3` on orders), hour of day in `APP_TIMEZONE`, the rider's recent completion rate and distance covered today (from completed batches, using the business-day boundary). The result is always between 0 and `max_capacity`.
- `CAPACITY_MODE=count|dynamic` (default `count`).
- Use one function in all four places so they agree: batch size, `max_additional_orders` in `match-orders`, the `match-riders` filter and the accept check. The database CHECK on `max_capacity` stays.

**Tests:** table and property tests (never above `max_capacity`, never negative); parameterise the e2e scenario 'capacity across features' for dynamic mode, asserting all four features agree at each load level.

## Phase 8: Routing provider and sequencing

**Goal:** road-aware distances and ETAs without ever depending on a third party being up.

**Tasks**

- Interface `routing.Router` (`Matrix`, `Route`) with a haversine implementation (today's behaviour) and an HTTP implementation for a self-hosted OSRM (the option the docs propose). Provider adapters for Mapbox or Google can follow behind the same interface.
- Wrapper: per-call timeout (`ROUTING_TIMEOUT`), retries only for 5xx and timeouts (at most 2, backoff), a circuit breaker driven by the injected clock, an in-process cache keyed by rounded coordinates, and a fallback to haversine whenever anything fails. Never use Redis here.
- Keep haversine as the cheap prefilter; call the router only for survivors.
- Better sequencing: cheapest insertion plus 2-opt under the pickup-before-delivery constraint, and `BATCH_MIN_SCORE` (a batch below it is split). Both behind `ROUTING_PROVIDER=haversine|osrm` (default `haversine`) and a sequencer flag.

**Tests:** fake `Router` for success, timeout, 5xx (retried), 4xx (not retried), breaker open, half-open and closed on the test clock, and fallback output equal to legacy; no real network in CI. Sequencer property test: every pickup precedes its delivery and the route is never longer than nearest-neighbour. An optional nightly contract test against a real OSRM container behind a `routing` build tag.

## Phase 9: Per-rider tokens and scopes

**Goal:** close the documented known limitation that any key holder can act as any rider (CWE-639, OWASP A01).

**Tasks**

- `POST /auth/token` issues a short-lived PASETO whose subject is a `rider_id`, with scopes such as `batches:accept`, `riders:write`, `orders:create` and a dispatcher scope.
- Handlers take the rider from the token; a body or path rider that differs is `403`.
- `AUTH_MODE=apikey|token|both` (default `apikey`); a signing key config value, length-validated, never logged.
- **Open question for Zemi:** the docs say riders are identified by name and phone only. Put token issuance behind an `IdentityVerifier` interface with a development stub, and ask Zemi how a rider proves identity (for example an SMS one-time code). Do not invent the verifier.

**Tests:** a table asserting every route enforces its scope; tampered, expired (test clock) and wrong-scope tokens; cross-rider attempts; extend the e2e 'unauthenticated requests change nothing' test; a log capture proving no token or signing key appears.

## Phase 10: Outbox events and partner webhooks

**Goal:** partners (for example delivery confirmation consumers) are told reliably, and retries are safe.

**Tasks**

- Tables: `outbox_events`, `webhook_subscriptions` (per API client name, https URL, secret) and `webhook_deliveries` (status pending, delivered or failed; attempts; next attempt time; last status).
- Implement `EventSink` so the outbox row is written in the same transaction as the state change. Events: `batch.accepted`, `order.delivered`, `order.cancelled`, `order.returned`, `rider.offline`.
- A background dispatcher (`SKIP LOCKED`, safe with several instances) signs each payload with HMAC, sends the event ID in an idempotency header, and retries at 1 min, 5 min, 30 min, 2 h and 24 h before marking `failed`.
- SSRF guard: https only, resolve and block private, loopback and link-local addresses.
- Endpoints: list failed deliveries and `POST` a manual retry, documented in OpenAPI.
- `WEBHOOKS_ENABLED` default off. Document the event contracts in `docs/EVENTS.md`. Mobile-app retry, M-Pesa and escrow retries belong to other services: write the contracts only.
- Add a general job queue only if Phase 0 and 2 numbers show a need; if so use a PostgreSQL-backed queue, not a new broker.

**Tests:** a fake partner server that fails N times then succeeds, with backoff checked on the test clock; duplicates carry the same event ID; two workers never send the same delivery twice; fault injection killing the database mid-commit leaves an outbox row if and only if the state change committed. New invariant: every pending delivery has a future attempt time, and every exhausted delivery is `failed`.

## Phase 11: Observability and scale-out hardening

**Goal:** see problems before riders do, and keep limits correct with several instances.

**Tasks**

- Prometheus `/metrics` behind the same chain and `METRICS_ENABLED` rules (API key, rate limited). Keep `/debug/vars`. Add request-duration histograms labelled by **route template only** (never raw paths or IDs), algorithm duration, pool stats, webhook backlog and failures, sweeper lag, breaker state. Counters still move only after the work succeeds.
- Optional OpenTelemetry traces carrying `X-Request-ID`.
- `LIMITER_STORE=memory|redis` (default `memory`): a Redis token bucket behind an interface, added only when more than one API instance runs. Document the fail-open or fail-closed choice.
- Alert rules and a dashboard under `deploy/monitoring/`: 5xx ratio, p99 latency, `canceled_acquire_count` growth, breaker open, failed webhooks above zero, expiry or offline sweeper stalled.

**Tests:** follow `metrics_test.go` (a counter does not move on failure); an e2e scrape of `/metrics`; a label-cardinality test; limiter interface tests with an in-memory fake and a Redis integration test behind a build tag.

## Release gates

- **Every PR:** `make dev/audit` and the new tests; existing tests unmodified unless behaviour intentionally changes.
- **Merge to main:** `make dev/test/all`, `-race -count=3`, migration up/down/up.
- **Nightly:** the load test (fail on more than 20% p95 regression against the baseline) and the chaos suite.
- **Release:** staging smoke and Postman runs; enable one flag at a time, canary a subset of riders or zones, then roll out; every migration has a tested down path and every flag a documented rollback.
