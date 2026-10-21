-- name: CreateOutboxEvent :exec
INSERT INTO outbox_events (id, event_type, payload, created_at)
VALUES ($1, $2, $3, $4);

-- name: ClaimDueWebhookDelivery :one
WITH candidate AS (
    SELECT d.id, d.attempts
    FROM webhook_deliveries d
    JOIN webhook_subscriptions s ON s.id = d.subscription_id
    WHERE d.status = 'pending' AND d.next_attempt_at <= $1 AND s.enabled
    ORDER BY d.next_attempt_at, d.id
    FOR UPDATE OF d SKIP LOCKED
    LIMIT 1
)
UPDATE webhook_deliveries d
SET attempts = d.attempts + 1
  , next_attempt_at = $1::timestamptz + interval '5 minutes'
FROM candidate c
JOIN webhook_deliveries claimed ON claimed.id = c.id
JOIN webhook_subscriptions s ON s.id = claimed.subscription_id
JOIN outbox_events e ON e.id = claimed.event_id
WHERE d.id = c.id AND d.attempts = c.attempts
RETURNING d.id, d.attempts, d.next_attempt_at, e.id AS event_id, e.event_type, e.payload, s.url, s.secret;

-- name: MarkWebhookDelivered :exec
UPDATE webhook_deliveries SET status = 'delivered', last_status = $2 WHERE id = $1;

-- name: MarkWebhookRetry :exec
UPDATE webhook_deliveries SET next_attempt_at = $2, attempts = $3, last_status = $4 WHERE id = $1;

-- name: MarkWebhookFailed :exec
UPDATE webhook_deliveries SET status = 'failed', last_status = $2 WHERE id = $1;

-- name: ListFailedWebhookDeliveries :many
SELECT d.id, d.event_id, e.event_type, d.subscription_id, s.client_name, d.attempts, d.last_status, d.next_attempt_at
FROM webhook_deliveries d
JOIN outbox_events e ON e.id = d.event_id
JOIN webhook_subscriptions s ON s.id = d.subscription_id
WHERE d.status = 'failed'
ORDER BY d.next_attempt_at, d.id
LIMIT $1 OFFSET $2;

-- name: RetryWebhookDelivery :execrows
UPDATE webhook_deliveries SET status = 'pending', next_attempt_at = $2, last_status = NULL
WHERE id = $1 AND status = 'failed';
