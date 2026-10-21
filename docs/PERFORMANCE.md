# Phase 0 Performance Baseline

This document records the development-only load workflow for Zemi Fleet V2. It does not run Docker or staging commands.

## Generate data

Load the normal development seed first, then generate and apply a dataset:

```bash
make dev/db/seed
make dev/load/apply LOAD_RIDERS=500 LOAD_ORDERS=10000
```

For the larger baseline:

```bash
make dev/load/apply LOAD_RIDERS=1000 LOAD_ORDERS=50000
```

The generated file is `db/seed/load.sql` and is ignored by Git. It deletes only rows whose IDs begin with `load_`, then inserts deterministic riders and pending orders in four Nairobi clusters. Re-running the target is safe after the normal reference/seed data has been loaded.

## Run the HTTP workload

Start the development API with `make dev/run` in another terminal, then run:

```bash
make dev/test/load LOAD_DURATION=60s LOAD_CONCURRENCY=20
```

The client exercises `match-orders`, `match-riders`, and `orders/batch`. It reports total requests, successful responses, and errors. The API key defaults to the development placeholder from `.env.example`; override it with `LOAD_API_KEY` when `app.env` uses a different key.

## Capture query plans

Run these statements against the local development database after applying the load data. Save the output with the date and row counts when recording a baseline.

```sql
EXPLAIN (ANALYZE, BUFFERS)
SELECT id, pickup_lat, pickup_lng, created_at
FROM orders
WHERE status = 'pending'
  AND pickup_lat BETWEEN -1.31 AND -1.24
  AND pickup_lng BETWEEN 36.77 AND 36.86
ORDER BY created_at, id
LIMIT 1000;

EXPLAIN (ANALYZE, BUFFERS)
SELECT id, last_lat, last_lng, current_orders_count, max_capacity
FROM riders
WHERE is_available AND current_orders_count < max_capacity;

EXPLAIN (ANALYZE, BUFFERS)
SELECT id, pickup_centroid_lat, pickup_centroid_lng, expires_at
FROM batches
WHERE status = 'open'
  AND expires_at > now()
  AND pickup_centroid_lat BETWEEN -1.31 AND -1.24
  AND pickup_centroid_lng BETWEEN 36.77 AND 36.86;
```

Record p50, p95, p99, error rate, and the `EXPLAIN` plans here before changing spatial query behavior. Phase 2 uses this baseline to decide whether plain PostgreSQL indexes are sufficient or PostGIS is justified.

## Current status

The reproducible generator, local load client, development Make targets, opt-in Toxiproxy profile, and nightly CI baseline workflow are implemented. The nightly job publishes the raw load summary and all three query plans as a 30-day GitHub Actions artifact; it does not commit generated data or overwrite this document.

The first recorded numbers should be copied here from the nightly artifact, including the run date, row counts, p50, p95, p99, error rate, and the three `EXPLAIN (ANALYZE, BUFFERS)` outputs. The acceptance target remains p95 under 200 ms at 10,000 pending orders, with no increase in error rate. Until that first scheduled run is attached, the measurement status is **pending**, rather than a claimed baseline.

Fault-injection tests are run separately because they require the `chaos` Compose profile and a test database DSN routed through Toxiproxy:

```bash
make chaos/up
TEST_DATABASE_URL='postgres://zemi:...@localhost:15432/zemi_fleet_test?sslmode=disable' make chaos/test
make chaos/down
```

The chaos suite covers query timeout, connection reset during batch acceptance, and two requests contending for a one-connection pool. It expects generic 500 responses and checks that the batch remains either wholly open or wholly accepted; the e2e invariant suite checks the complete database state.
