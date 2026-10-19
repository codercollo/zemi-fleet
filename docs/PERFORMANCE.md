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

The reproducible generator, local load client, and development Make targets are implemented. Toxiproxy fault-injection and the nightly CI publication remain separate Phase 0 work; no staging run is required for the development workflow.
