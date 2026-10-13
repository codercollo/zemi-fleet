-- name: GetAreasByIDs :many
SELECT * FROM areas WHERE id = ANY(sqlc.arg(ids)::text[]) ORDER BY id;
