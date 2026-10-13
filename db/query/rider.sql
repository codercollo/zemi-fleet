-- name: GetRider :one
SELECT * FROM riders WHERE id = $1;

-- name: GetRiderForUpdate :one
-- Row lock: serialises concurrent preference replacements for one rider.
SELECT * FROM riders WHERE id = $1 FOR UPDATE;

-- name: UpdateRiderLocation :one
-- Optimistic lock (Let's Go Further ch. 8.2): no row comes back when the version moved on.
UPDATE riders
SET last_lat = sqlc.arg(last_lat),
    last_lng = sqlc.arg(last_lng),
    last_bearing_deg = sqlc.arg(last_bearing_deg),
    last_speed_kmh = sqlc.arg(last_speed_kmh),
    location_updated_at = sqlc.arg(location_updated_at),
    version = version + 1
WHERE id = sqlc.arg(id) AND version = sqlc.arg(version)
RETURNING *;

-- name: UpdateRiderAvailability :one
UPDATE riders
SET is_available = sqlc.arg(is_available),
    version = version + 1
WHERE id = sqlc.arg(id) AND version = sqlc.arg(version)
RETURNING *;

-- name: BumpRiderVersion :one
-- Used after replacing preferences, which live in other tables, so clients see the rider change.
UPDATE riders
SET version = version + 1
WHERE id = sqlc.arg(id) AND version = sqlc.arg(version)
RETURNING *;

-- name: UpdateRiderTelemetry :exec
-- Location ping taken from a match-orders call. Deliberately no version check and no version
-- bump: telemetry is last-write-wins and must never fail with an edit conflict, nor make a
-- rider's own versioned PATCHes conflict just because the app polled for orders.
UPDATE riders
SET last_lat = sqlc.arg(last_lat),
    last_lng = sqlc.arg(last_lng),
    last_bearing_deg = sqlc.arg(last_bearing_deg),
    last_speed_kmh = sqlc.arg(last_speed_kmh),
    location_updated_at = sqlc.arg(location_updated_at)
WHERE id = sqlc.arg(id);
