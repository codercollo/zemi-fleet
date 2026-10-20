ALTER TABLE orders DROP CONSTRAINT chk_orders_compensation;
ALTER TABLE orders DROP CONSTRAINT chk_orders_cancellation;
ALTER TABLE orders DROP CONSTRAINT chk_orders_assignment;
ALTER TABLE orders ADD CONSTRAINT chk_orders_assignment CHECK (
    (status = 'pending'   AND batch_id IS NULL     AND rider_id IS NULL)
 OR (status = 'batched'   AND batch_id IS NOT NULL AND rider_id IS NULL)
 OR (status IN ('assigned', 'picked_up', 'delivered') AND rider_id IS NOT NULL)
 OR (status = 'cancelled')
);
ALTER TABLE orders DROP COLUMN rider_compensation_cents, DROP COLUMN cancel_reason, DROP COLUMN cancelled_at, DROP COLUMN delivery_attempts;
