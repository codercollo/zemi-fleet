-- Queries for batch creation (POST /orders/batch). All timestamps come from the injected clock.

-- name: ExpireStaleBatches :many
-- Open batches past their TTL are no longer on offer.
-- The locking CTE lets several application instances sweep safely: each instance
-- claims a disjoint set of expired batches before changing their state.
WITH expired AS (
    SELECT b.id
    FROM batches b
    WHERE b.status = 'open' AND b.expires_at <= sqlc.arg(now)
    ORDER BY b.expires_at, b.id
    FOR UPDATE SKIP LOCKED
)
UPDATE batches b
SET status = 'expired'
FROM expired
WHERE b.id = expired.id
RETURNING b.id;

-- name: ReleaseOrdersFromBatches :exec
-- Orders of expired batches go back to the pending pool.
UPDATE orders
SET status = 'pending', batch_id = NULL, batch_seq = NULL
WHERE batch_id = ANY(sqlc.arg(batch_ids)::text[]) AND status = 'batched';

-- name: ListPendingOrdersForBatching :many
-- Pending pickups inside a bounding box, oldest first, row-locked. SKIP LOCKED means a concurrent
-- batch request that already holds an order simply does not see it, so no order is batched twice.
SELECT o.id, o.pickup_lat, o.pickup_lng, o.delivery_lat, o.delivery_lng, o.created_at,
       o.delivery_fee_cents, o.rider_earnings_cents, o.value_cents, v.rating, o.urgency
FROM orders o
JOIN vendors v ON v.id = o.vendor_id
WHERE o.status = 'pending'
  AND o.pickup_lat BETWEEN sqlc.arg(min_lat)::float8 AND sqlc.arg(max_lat)::float8
  AND o.pickup_lng BETWEEN sqlc.arg(min_lng)::float8 AND sqlc.arg(max_lng)::float8
ORDER BY o.created_at, o.id
FOR UPDATE OF o SKIP LOCKED;

-- name: CreateBatch :one
INSERT INTO batches (
    id, pickup_centroid_lat, pickup_centroid_lng, pickup_spread_m, orders_count, total_distance_m,
    estimated_duration_s, total_delivery_fee_cents, estimated_earnings_cents, route_optimization_score,
    expires_at, created_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
)
RETURNING *;

-- name: CreateBatchStop :exec
INSERT INTO batch_stops (batch_id, seq, order_id, type, lat, lng, leg_distance_m, eta_offset_s)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: AssignOrderToBatch :execrows
-- Only a still-pending order can be batched; zero rows means it changed under us.
UPDATE orders
SET status = 'batched', batch_id = sqlc.arg(batch_id), batch_seq = sqlc.arg(batch_seq)
WHERE id = sqlc.arg(id) AND status = 'pending';

-- name: ListOpenBatchesNearPoint :many
-- Open, unexpired batches whose first pickup (stop 1) is inside a bounding box and that fit max_orders.
SELECT b.id, s.lat AS first_lat, s.lng AS first_lng
FROM batches b
JOIN batch_stops s ON s.batch_id = b.id AND s.seq = 1
WHERE b.status = 'open'
  AND b.expires_at > sqlc.arg(now)
  AND b.orders_count <= sqlc.arg(max_orders)::int
  AND s.lat BETWEEN sqlc.arg(min_lat)::float8 AND sqlc.arg(max_lat)::float8
  AND s.lng BETWEEN sqlc.arg(min_lng)::float8 AND sqlc.arg(max_lng)::float8
ORDER BY b.created_at, b.id;

-- name: GetBatchesByIDs :many
SELECT * FROM batches WHERE id = ANY(sqlc.arg(ids)::text[]);

-- name: ListBatchStops :many
SELECT * FROM batch_stops WHERE batch_id = ANY(sqlc.arg(batch_ids)::text[]) ORDER BY batch_id, seq;

-- name: ListBatchOrders :many
SELECT id, batch_id, batch_seq, pickup_lat, pickup_lng, delivery_lat, delivery_lng,
       trip_distance_m, created_at, rider_earnings_cents
FROM orders
WHERE batch_id = ANY(sqlc.arg(batch_ids)::text[])
ORDER BY batch_id, batch_seq;

-- name: CreateBatchOffer :execrows
-- One offer per (batch, rider): asking again does not create a second row.
INSERT INTO batch_offers (batch_id, rider_id, distance_m, offered_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (batch_id, rider_id) DO NOTHING;

-- name: AddRiderOffers :exec
-- offers_count feeds acceptance_rate. No version bump: like telemetry, it must not cause edit conflicts.
UPDATE riders SET offers_count = offers_count + sqlc.arg(offers)::int WHERE id = sqlc.arg(id);

-- name: GetBatch :one
SELECT * FROM batches WHERE id = $1;

-- name: ListOpenBatchSummaries :many
-- GET /batches candidates: open, unexpired batches whose first pickup is inside a bounding box,
-- with the creation time of their oldest order (for sorting). Read-only: nothing is expired or offered here.
SELECT b.id, s.lat AS first_lat, s.lng AS first_lng, MIN(o.created_at)::timestamptz AS oldest_order_at
FROM batches b
JOIN batch_stops s ON s.batch_id = b.id AND s.seq = 1
JOIN orders o ON o.batch_id = b.id
WHERE b.status = 'open'
  AND b.expires_at > sqlc.arg(now)
  AND s.lat BETWEEN sqlc.arg(min_lat)::float8 AND sqlc.arg(max_lat)::float8
  AND s.lng BETWEEN sqlc.arg(min_lng)::float8 AND sqlc.arg(max_lng)::float8
GROUP BY b.id, s.lat, s.lng;

-- name: CountOpenBatchSummariesSQL :one
-- SQL-mode candidate count: bounding box plus exact great-circle radius.
WITH candidates AS (
    SELECT b.id,
           6371000.0 * 2.0 * asin(sqrt(
               power(sin(radians(s.lat - sqlc.arg(point_lat)::float8) / 2.0), 2) +
               cos(radians(sqlc.arg(point_lat)::float8)) * cos(radians(s.lat)) *
               power(sin(radians(s.lng - sqlc.arg(point_lng)::float8) / 2.0), 2)
           )) AS distance_m
    FROM batches b
    JOIN batch_stops s ON s.batch_id = b.id AND s.seq = 1
    JOIN orders o ON o.batch_id = b.id
    WHERE b.status = 'open'
      AND b.expires_at > sqlc.arg(now)
      AND s.lat BETWEEN sqlc.arg(min_lat)::float8 AND sqlc.arg(max_lat)::float8
      AND s.lng BETWEEN sqlc.arg(min_lng)::float8 AND sqlc.arg(max_lng)::float8
    GROUP BY b.id, s.lat, s.lng
)
SELECT count(*)::bigint
FROM candidates
WHERE distance_m <= sqlc.arg(radius_m)::float8;

-- name: ListOpenBatchSummariesSQL :many
-- SQL-mode exact radius filter and stable ordering. Go still maps the selected rows and preserves the response DTO.
WITH candidates AS (
    SELECT b.id, s.lat AS first_lat, s.lng AS first_lng,
           MIN(o.created_at)::timestamptz AS oldest_order_at,
           6371000.0 * 2.0 * asin(sqrt(
               power(sin(radians(s.lat - sqlc.arg(point_lat)::float8) / 2.0), 2) +
               cos(radians(sqlc.arg(point_lat)::float8)) * cos(radians(s.lat)) *
               power(sin(radians(s.lng - sqlc.arg(point_lng)::float8) / 2.0), 2)
           )) AS distance_m
    FROM batches b
    JOIN batch_stops s ON s.batch_id = b.id AND s.seq = 1
    JOIN orders o ON o.batch_id = b.id
    WHERE b.status = 'open'
      AND b.expires_at > sqlc.arg(now)
      AND s.lat BETWEEN sqlc.arg(min_lat)::float8 AND sqlc.arg(max_lat)::float8
      AND s.lng BETWEEN sqlc.arg(min_lng)::float8 AND sqlc.arg(max_lng)::float8
    GROUP BY b.id, s.lat, s.lng
)
SELECT id, first_lat, first_lng, oldest_order_at, distance_m
FROM candidates
WHERE distance_m <= sqlc.arg(radius_m)::float8
ORDER BY distance_m, oldest_order_at, id
LIMIT sqlc.arg(page_limit)::int OFFSET sqlc.arg(page_offset)::int;

-- name: AcceptBatch :one
-- THE exclusivity guarantee. Only a batch that is still open and unexpired can be claimed. Postgres
-- locks the row on UPDATE; a concurrent claim waits, then re-checks the WHERE against the committed
-- row, finds status = 'accepted' and updates nothing.
UPDATE batches
SET status = 'accepted', rider_id = sqlc.arg(rider_id), accepted_at = sqlc.arg(now), version = version + 1
WHERE id = sqlc.arg(id) AND status = 'open' AND expires_at > sqlc.arg(now)
RETURNING *;

-- name: AssignBatchOrdersToRider :execrows
UPDATE orders
SET status = 'assigned', rider_id = sqlc.arg(rider_id)
WHERE batch_id = sqlc.arg(batch_id) AND status = 'batched';

-- name: MarkBatchOfferAccepted :exec
UPDATE batch_offers
SET accepted = TRUE, responded_at = sqlc.arg(now)
WHERE batch_id = sqlc.arg(batch_id) AND rider_id = sqlc.arg(rider_id);

-- name: RecordRiderAccept :exec
-- The rider takes on n orders and one more accepted offer. The version bump makes a client holding a
-- stale X-Expected-Version conflict, because the rider's state really did change.
UPDATE riders
SET current_orders_count = current_orders_count + sqlc.arg(orders)::int,
    accepted_count = accepted_count + 1,
    offers_count = offers_count + sqlc.arg(new_offers)::int,
    version = version + 1
WHERE id = sqlc.arg(id);
