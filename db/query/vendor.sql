-- name: GetVendor :one
SELECT * FROM vendors WHERE id = $1;
