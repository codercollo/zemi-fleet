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
    offline_flagged_at = NULL,
    version = version + 1
WHERE id = sqlc.arg(id) AND version = sqlc.arg(version)
RETURNING *;

-- name: UpdateRiderLocationTelemetry :one
-- Telemetry is last-write-wins: it clears an offline flag and never conflicts
-- with a stale document version or increments the rider version.
UPDATE riders
SET last_lat = sqlc.arg(last_lat),
    last_lng = sqlc.arg(last_lng),
    last_bearing_deg = sqlc.arg(last_bearing_deg),
    last_speed_kmh = sqlc.arg(last_speed_kmh),
    location_updated_at = sqlc.arg(location_updated_at),
    offline_flagged_at = NULL
WHERE id = sqlc.arg(id)
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
    location_updated_at = sqlc.arg(location_updated_at),
    offline_flagged_at = NULL
WHERE id = sqlc.arg(id);

-- name: ListStaleRiders :many
SELECT * FROM riders
WHERE current_orders_count > 0
  AND offline_flagged_at IS NULL
  AND (location_updated_at IS NULL OR location_updated_at < $1)
ORDER BY id
FOR UPDATE SKIP LOCKED;

-- name: MarkRiderOffline :one
UPDATE riders SET offline_flagged_at = $2, version = version + 1
WHERE id = $1 AND offline_flagged_at IS NULL
RETURNING *;

-- name: ListAssignedOrdersForRider :many
SELECT id, batch_id, delivery_fee_cents, rider_earnings_cents
FROM orders WHERE rider_id = $1 AND status = 'assigned' ORDER BY id;

-- name: ReleaseAssignedOrder :one
UPDATE orders
SET status = 'pending', batch_id = NULL, rider_id = NULL, batch_seq = NULL, version = version + 1
WHERE id = $1 AND status = 'assigned'
RETURNING *;

-- name: DecrementRiderOrders :exec
UPDATE riders
SET current_orders_count = GREATEST(current_orders_count - $2::smallint, 0), version = version + 1
WHERE id = $1;

-- name: CancelBatchIfNoActiveOrders :exec
UPDATE batches AS b
SET status = 'cancelled', version = version + 1
WHERE b.id = $1 AND b.status IN ('accepted', 'in_progress')
  AND NOT EXISTS (SELECT 1 FROM orders AS o WHERE o.batch_id = $1 AND o.status IN ('assigned', 'picked_up', 'returning'));

-- name: RemoveReleasedOrderFromBatch :exec
-- A batch may still contain picked-up orders. Detach a released assigned order
-- from that batch so it can be offered again without stale stops or aggregates.
DELETE FROM batch_stops
WHERE batch_stops.batch_id = $1 AND batch_stops.order_id = $2
  AND EXISTS (SELECT 1 FROM orders WHERE orders.batch_id = $1 AND orders.status IN ('assigned', 'picked_up', 'returning'));

-- name: DecrementBatchOrderTotals :exec
UPDATE batches AS b
SET orders_count = b.orders_count - 1,
    total_delivery_fee_cents = b.total_delivery_fee_cents - $2,
    estimated_earnings_cents = b.estimated_earnings_cents - $3,
    version = b.version + 1
WHERE b.id = $1
  AND b.status IN ('accepted', 'in_progress')
  AND b.orders_count > 1
  AND EXISTS (SELECT 1 FROM orders WHERE orders.batch_id = $1 AND orders.status IN ('assigned', 'picked_up', 'returning'));
