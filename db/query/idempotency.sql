-- Queries behind the Idempotency-Key header. Timestamps come from the injected clock.

-- name: DeleteExpiredIdempotencyKeys :exec
-- Keys older than IDEMPOTENCY_TTL are ignored; they are deleted whenever a new key is written.
DELETE FROM idempotency_keys WHERE created_at <= sqlc.arg(cutoff);

-- name: ClaimIdempotencyKey :execrows
-- Atomic: of any number of concurrent requests with one key, exactly one inserts the row.
INSERT INTO idempotency_keys (key, endpoint, request_hash, created_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (key, endpoint) DO NOTHING;

-- name: GetIdempotencyKey :one
SELECT * FROM idempotency_keys WHERE key = $1 AND endpoint = $2;

-- name: TakeOverIdempotencyKey :execrows
-- A claim whose request never completed (a crash) can be taken over once it is older than the processing timeout.
UPDATE idempotency_keys
SET request_hash = sqlc.arg(request_hash), created_at = sqlc.arg(now)
WHERE key = sqlc.arg(key) AND endpoint = sqlc.arg(endpoint)
  AND response_status IS NULL AND created_at <= sqlc.arg(stale_before);

-- name: CompleteIdempotencyKey :exec
UPDATE idempotency_keys
SET response_status = sqlc.arg(response_status), response_body = sqlc.arg(response_body),
    response_headers = sqlc.arg(response_headers)
WHERE key = sqlc.arg(key) AND endpoint = sqlc.arg(endpoint) AND response_status IS NULL;

-- name: ReleaseIdempotencyKey :exec
-- A request that did not succeed leaves no trace, so the client can retry the same key.
DELETE FROM idempotency_keys WHERE key = $1 AND endpoint = $2 AND response_status IS NULL;
