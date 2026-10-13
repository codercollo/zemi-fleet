-- name: GetPreferences :one
SELECT * FROM rider_preferences WHERE rider_id = $1;

-- name: UpsertPreferences :one
INSERT INTO rider_preferences (rider_id, min_order_value_cents)
VALUES ($1, $2)
ON CONFLICT (rider_id) DO UPDATE SET min_order_value_cents = EXCLUDED.min_order_value_cents
RETURNING *;

-- name: ListPreferredCategories :many
SELECT category_code FROM rider_preferred_categories WHERE rider_id = $1 ORDER BY category_code;

-- name: ListPreferredAreas :many
SELECT area_id FROM rider_preferred_areas WHERE rider_id = $1 ORDER BY area_id;

-- name: DeletePreferredCategories :exec
DELETE FROM rider_preferred_categories WHERE rider_id = $1;

-- name: DeletePreferredAreas :exec
DELETE FROM rider_preferred_areas WHERE rider_id = $1;

-- name: InsertPreferredCategories :exec
INSERT INTO rider_preferred_categories (rider_id, category_code)
SELECT sqlc.arg(rider_id)::text, unnest(sqlc.arg(category_codes)::text[]);

-- name: InsertPreferredAreas :exec
INSERT INTO rider_preferred_areas (rider_id, area_id)
SELECT sqlc.arg(rider_id)::text, unnest(sqlc.arg(area_ids)::text[]);
