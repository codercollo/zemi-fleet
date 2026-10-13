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
4. `make dev/db/migrations/up` (re-run it after pulling: `000002_idempotency` adds a table)
5. `make dev/db/seed` (optional: Nairobi sample data; truncates first, safe to re-run)
6. `make dev/run`
7. Check `http://localhost:4000/api/v2/healthz`

`make dev/sqlc` regenerates the sqlc code and `make dev/mock` regenerates the gomock `Store` (run both after changing `db/query` or the `Store` interface). On staging, `make staging/seed` loads the same data into the Postgres container.

## Tests

```bash
make dev/test              # unit tests: pure packages, services and handlers with mocks (no database)
make dev/test/integration  # real PostgreSQL: the Store, SQL, transactions, time handling
make dev/test/e2e          # the whole API over HTTP on a real database, 5 scenarios + SQL invariants
make dev/test/all          # all three
make dev/audit             # formatting, vet, staticcheck (including the tagged tests), unit tests and govulncheck
```

Integration and e2e tests use **`TEST_DATABASE_URL`**, a separate database (`zemi_fleet_test`) that they truncate
freely, and refuse to run against any database whose name does not end in `_test`. Your development data is never
touched. Create it once with `CREATE DATABASE zemi_fleet_test OWNER zemi;` (the make targets migrate it for you).
See [docs/TESTING.md](docs/TESTING.md) for what each layer proves and the known gaps.

Run `make help` for all targets (`dev/*` for local work, `staging/*` for Docker).

## API (so far)
All under `/api/v2`. Money is KES in JSON (cents in the database).

| Method | Path | |
|---|---|---|
| GET | `/healthz` | status + database check |
| POST | `/orders/match-riders` | rank riders for an order (stored by `order_id`, or unsaved via `order_details`) by category, area, minimum order value, distance and acceptance rate; preferences are hard filters |
| POST | `/orders/batch` | form batches of up to 5 pending orders (pickups pairwise within `radius_km`, similar directions, oldest first) near a rider and return the open batches on offer, each with a suggested stop sequence |
| GET | `/batches` | open batches near `lat`,`lng` (`radius_km`, `page`, `page_size`), nearest first, with pagination `metadata`; read-only |
| POST | `/batches/:id/accept` | `{rider_id}`: the first rider to accept gets the batch exclusively (`409` after that, `422` if over capacity or off shift); a retry by the holder is `200` |
| POST | `/orders` | create a pending order, `201` + `Location` |
| GET | `/orders/:id` | one order |
| GET | `/riders/:id` | rider with preferences |
| PUT | `/riders/:id/preferences` | replace categories, areas and minimum order value; returns the rider |
| POST | `/riders/match-orders` | pending pickups within `CONE_RADIUS_KM` and inside the 60° cone ahead of a moving rider (full circle when slow or heading unknown); also records the rider's position |
| PATCH | `/riders/:id/location` | `latitude`, `longitude`, optional `bearing_degrees`, `speed_kmh`; returns the rider |
| PATCH | `/riders/:id/availability` | `is_available`; returns the rider |

**Retries.** `POST /orders` and `POST /orders/batch` accept an `Idempotency-Key` header (1 to 64 visible ASCII characters): a retry with the same key and body replays the first response (`Idempotent-Replay: true`) instead of creating a second order or batch; the same key with a different body is `422`. See "Idempotency" in [docs/DOCUMENTATION.md](docs/DOCUMENTATION.md).

**Limits.** Every ID must match its format (`rider_...`, `ord_...`, `batch_...`, `ven_...`, `area_...`), money is KES with at most 2 decimals and at most `ORDER_MAX_VALUE_KES` / `ORDER_MAX_DELIVERY_FEE_KES` (10,000,000), and the "Security" section of the documentation maps all of this to CWE/OWASP and lists the known limitation (a shared API key can act as any rider).

**Time.** Everything is UTC: the database session, stored values and JSON (`"created_at": "2026-10-13T09:30:15Z"`). `APP_TIMEZONE` (default `Africa/Nairobi`) only names the business zone for presentation and day boundaries. The location PATCH stores an omitted `bearing_degrees` / `speed_kmh` as `null` (unknown) because they describe that one reading.

Errors: `400` malformed JSON or header, `404` unknown ID, `409` edit conflict, `422` validation (per-field messages).

**Optimistic locking.** Riders carry a `version`. Every write bumps it, and a write that loses a race gets `409`. Send `X-Expected-Version: <n>` on the PATCHes and the preferences PUT to be rejected with `409` when your copy is stale. Every statement is cut off after `DB_QUERY_TIMEOUT` (default 3s).

See [docs/DOCUMENTATION.md](docs/DOCUMENTATION.md) for the algorithms (proximity matching, order batching, rider preference matching).

## Try it

After `make dev/db/seed` and `make dev/run`:

```bash
curl -s localhost:4000/api/v2/healthz
curl -s localhost:4000/api/v2/riders/rider_002          # a seeded rider with preferences
curl -s -X POST localhost:4000/api/v2/orders/batch -d '{"rider_id":"rider_001","rider_location":{"latitude":-1.2650,"longitude":36.8038},"max_orders":5,"radius_km":2}'
```

**Seeded rider locations go stale.** The seed stamps each rider's last location a few minutes before the moment you
loaded it, and `match-riders` only trusts a location for `RIDER_LOCATION_STALE_AFTER` (default 30 minutes). Later
than that, every rider's distance is `null` and their proximity score is 0, so rankings look flat. Send a fresh
location first, either with a location ping:

```bash
curl -s -X PATCH localhost:4000/api/v2/riders/rider_002/location -d '{"latitude":-1.2890,"longitude":36.7900}'
```

or by calling `match-orders` for that rider (it records the rider's position too):

```bash
curl -s -X POST localhost:4000/api/v2/riders/match-orders -d '{"rider_id":"rider_002","current_location":{"latitude":-1.2890,"longitude":36.7900},"current_orders_count":2}'
```

then rank riders for an order (an unsaved electronics order picked up at Yaya Centre, Kilimani):

```bash
curl -s -X POST localhost:4000/api/v2/orders/match-riders -d '{"order_id":"ord_new","order_details":{"category":"electronics","pickup_location":{"lat":-1.2917,"lng":36.7878},"delivery_location":{"lat":-1.2900,"lng":36.7920},"value_kes":5000}}'
```

`distance_to_pickup_km` is a number for riders with a fresh location and `null` for the rest. Re-run `make dev/db/seed`
at any time to reset the data (and the timestamps).

## Build phases
The work is tracked phase by phase in [docs/BUILD_PHASES.md](docs/BUILD_PHASES.md).
