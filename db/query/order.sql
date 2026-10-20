-- created_at comes from the injected clock (UTC), not now(): the column DEFAULT is only a fallback.
-- name: CreateOrder :one
INSERT INTO orders (
    id, vendor_id, category_code, pickup_lat, pickup_lng, delivery_lat, delivery_lng,
    trip_distance_m, trip_bearing_deg, value_cents, delivery_fee_cents, rider_payout_bps, urgency, created_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14
)
RETURNING *;

-- name: GetOrder :one
SELECT * FROM orders WHERE id = $1;

-- name: ListPendingOrdersInBox :many
-- Cone-matching candidates: pending pickups inside a lat/lng bounding box. The literal
-- status = 'pending' lets Postgres use the partial index idx_orders_pending_pickup, and it
-- keeps orders that are already batched or assigned out. Exact distance and angle are
-- applied in Go (internal/matching).
SELECT id, pickup_lat, pickup_lng, created_at, rider_earnings_cents
FROM orders
WHERE status = 'pending'
  AND pickup_lat BETWEEN sqlc.arg(min_lat)::float8 AND sqlc.arg(max_lat)::float8
  AND pickup_lng BETWEEN sqlc.arg(min_lng)::float8 AND sqlc.arg(max_lng)::float8
ORDER BY created_at, id;
