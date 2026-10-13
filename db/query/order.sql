-- created_at comes from the injected clock (UTC), not now(): the column DEFAULT is only a fallback.
-- name: CreateOrder :one
INSERT INTO orders (
    id, vendor_id, category_code, pickup_lat, pickup_lng, delivery_lat, delivery_lng,
    trip_distance_m, trip_bearing_deg, value_cents, delivery_fee_cents, rider_payout_bps, created_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13
)
RETURNING *;

-- name: GetOrder :one
SELECT * FROM orders WHERE id = $1;
