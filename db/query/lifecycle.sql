-- Queries used by the Phase 4 lifecycle transactions. Callers lock rider rows
-- before batch rows, matching the accept transaction's deadlock-safe order.

-- name: GetOrderRider :one
SELECT rider_id, batch_id FROM orders WHERE id = $1;

-- name: GetOrderForUpdate :one
SELECT * FROM orders WHERE id = $1 FOR UPDATE;

-- name: GetBatchForUpdate :one
SELECT * FROM batches WHERE id = $1 FOR UPDATE;

-- name: GetBatchStop :one
SELECT * FROM batch_stops WHERE batch_id = $1 AND seq = $2;

-- name: MarkOrderPickedUp :one
UPDATE orders SET status = 'picked_up', version = version + 1
WHERE id = $1 AND status = 'assigned'
RETURNING *;

-- name: MarkOrderDelivered :one
UPDATE orders SET status = 'delivered', delivered_at = $2, version = version + 1
WHERE id = $1 AND status = 'picked_up'
RETURNING *;

-- name: MarkOrderReturned :one
UPDATE orders SET status = 'returned', version = version + 1
WHERE id = $1 AND status = 'returning'
RETURNING *;

-- name: RecordDeliveryAttempt :one
UPDATE orders
SET delivery_attempts = delivery_attempts + 1,
    status = CASE WHEN delivery_attempts + 1 = 3 THEN 'returning' ELSE status END,
    version = version + 1
WHERE id = $1 AND status = 'picked_up' AND delivery_attempts < 3
RETURNING *;

-- name: CancelOrder :one
UPDATE orders
SET status = 'cancelled', cancelled_at = $2, cancel_reason = $3,
    rider_compensation_cents = $4, version = version + 1
WHERE id = $1 AND status IN ('assigned', 'picked_up')
RETURNING *;

-- name: CompleteBatchStop :execrows
UPDATE batch_stops SET completed_at = $3
WHERE batch_id = $1 AND seq = $2 AND completed_at IS NULL;

-- name: CompleteOrderStops :exec
UPDATE batch_stops SET completed_at = $2
WHERE batch_id = $1 AND order_id = $3 AND completed_at IS NULL;

-- name: MarkBatchInProgress :execrows
UPDATE batches SET status = 'in_progress', version = version + 1
WHERE id = $1 AND status = 'accepted';

-- name: CompleteBatchIfDone :one
UPDATE batches
SET status = 'completed', completed_at = $2, version = version + 1
WHERE id = $1 AND status IN ('accepted', 'in_progress')
  AND NOT EXISTS (SELECT 1 FROM batch_stops WHERE batch_id = $1 AND completed_at IS NULL)
RETURNING *;

-- name: ReleaseRiderCapacity :exec
UPDATE riders
SET current_orders_count = GREATEST(current_orders_count - 1, 0), version = version + 1
WHERE id = $1;
