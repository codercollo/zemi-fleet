ALTER TABLE orders DROP CONSTRAINT IF EXISTS chk_orders_assignment;
DROP INDEX IF EXISTS idx_orders_pending_pickup;
DROP INDEX IF EXISTS idx_orders_pending_created;
ALTER TABLE orders ALTER COLUMN status DROP DEFAULT;

ALTER TYPE order_status RENAME TO order_status_old;
CREATE TYPE order_status AS ENUM ('pending', 'batched', 'assigned', 'picked_up', 'delivered', 'cancelled');

ALTER TABLE orders ALTER COLUMN status TYPE order_status USING status::text::order_status;
DROP TYPE order_status_old;

ALTER TABLE orders ALTER COLUMN status SET DEFAULT 'pending'::order_status;

ALTER TABLE orders ADD CONSTRAINT chk_orders_assignment CHECK (
    (status = 'pending'   AND batch_id IS NULL     AND rider_id IS NULL)
 OR (status = 'batched'   AND batch_id IS NOT NULL AND rider_id IS NULL)
 OR (status IN ('assigned', 'picked_up', 'delivered') AND rider_id IS NOT NULL)
 OR (status = 'cancelled')
);

CREATE INDEX idx_orders_pending_pickup
    ON orders (pickup_lat, pickup_lng, created_at)
    WHERE status = 'pending';

CREATE INDEX idx_orders_pending_created
    ON orders (created_at)
    WHERE status = 'pending';
