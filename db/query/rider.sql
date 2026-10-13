-- name: GetRider :one
SELECT * FROM riders WHERE id = $1;

-- name: GetRiderForUpdate :one
-- Row lock: serialises concurrent preference replacements for one rider.
SELECT * FROM riders WHERE id = $1 FOR UPDATE;
