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

The reproducible generator, local load client, development Make targets, opt-in Toxiproxy profile, and nightly CI baseline workflow are implemented. The first numeric baseline was captured before Phase 11 using the run below; the nightly workflow repeats the same measurement and publishes the load and query-plan artifacts.

The chaos suite is not allowed to silently pass when infrastructure was requested. With `TOXIPROXY_API_URL` unset, the tagged tests are intentionally skipped for ordinary local runs. When it is set, an unreachable Toxiproxy endpoint is a test failure.

## Recorded 10k baseline — 2026-10-21

This run used the isolated Docker staging stack, 500 generated riders, 10,000 generated pending orders, `LIMITER_ENABLED=false`, a 60-second workload, and 20 concurrent workers. The load client exercised matching and batching, including batch acceptance. The result was:

| Requests | Successful | Errors | Error rate | p50 | p95 | p99 |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 13,977 | 4,744 | 9,233 | 66.06% | 40.995 ms | 278.533 ms | 429.107 ms |

The high error rate is retained as observed: the workload intentionally races batch creation/acceptance and reports all non-2xx responses as errors. The proposed Phase 0 target of p95 under 200 ms was not met (`278.533 ms`), so this is the comparison baseline for later spatial and observability changes.

The captured plans were:

```text
orders pending pickup window: Index Scan using idx_orders_pending_created
  actual time=0.102..0.898 ms, rows=1001, Buffers: shared hit=32
  Incremental Sort actual time=0.362..1.504 ms
  Execution Time: 1.825 ms

available riders: Seq Scan on riders
  actual time=0.021..0.246 ms, rows=508, Buffers: shared hit=11
  Execution Time: 0.300 ms

open batches centroid window: Index Scan using idx_batches_open_centroid
  actual time=0.044..0.045 ms, rows=0, Buffers: shared hit=2
  Execution Time: 0.117 ms
```

The rider candidate query is the notable plan: PostgreSQL chose a sequential scan for the 510-row rider table. The complete raw output is retained in the local run artifact and should be uploaded by the nightly workflow for future comparisons.
