-- Queries for rider preference matching (POST /orders/match-riders).

-- name: ListMatchCandidateRiders :many
-- Riders who could take another order at all: on shift and with spare capacity. Everything else
-- (preferences, minimum value, distance) is applied by internal/matching, which has no SQL.
SELECT r.id, r.is_available, r.current_orders_count, r.max_capacity, r.last_lat, r.last_lng,
       r.location_updated_at, r.acceptance_rate, r.offers_count, r.accepted_count,
       COALESCE(p.min_order_value_cents, 0)::bigint AS min_order_value_cents
FROM riders r
LEFT JOIN rider_preferences p ON p.rider_id = r.id
WHERE r.is_available AND r.current_orders_count < r.max_capacity
ORDER BY r.id;

-- name: ListPreferredCategoriesForRiders :many
SELECT rider_id, category_code
FROM rider_preferred_categories
WHERE rider_id = ANY(sqlc.arg(rider_ids)::text[])
ORDER BY rider_id, category_code;

-- name: ListPreferredAreasForRiders :many
SELECT pa.rider_id, a.id AS area_id, a.name, a.center_lat, a.center_lng, a.radius_m
FROM rider_preferred_areas pa
JOIN areas a ON a.id = pa.area_id
WHERE pa.rider_id = ANY(sqlc.arg(rider_ids)::text[])
ORDER BY pa.rider_id, a.name;
