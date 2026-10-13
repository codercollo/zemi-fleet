-- name: GetCategory :one
SELECT * FROM categories WHERE code = $1;

-- name: GetCategoriesByCodes :many
SELECT * FROM categories WHERE code = ANY(sqlc.arg(codes)::text[]) ORDER BY code;
