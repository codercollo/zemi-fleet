ALTER TABLE orders
    ADD COLUMN delivery_attempts SMALLINT NOT NULL DEFAULT 0 CHECK (delivery_attempts BETWEEN 0 AND 3),
    ADD COLUMN cancelled_at TIMESTAMPTZ,
    ADD COLUMN cancel_reason TEXT,
    ADD COLUMN rider_compensation_cents BIGINT NOT NULL DEFAULT 0 CHECK (rider_compensation_cents >= 0);

ALTER TABLE orders DROP CONSTRAINT chk_orders_assignment;
ALTER TABLE orders ADD CONSTRAINT chk_orders_assignment CHECK (
    (status = 'pending'   AND batch_id IS NULL     AND rider_id IS NULL)
 OR (status = 'batched'   AND batch_id IS NOT NULL AND rider_id IS NULL)
 OR (status IN ('assigned', 'picked_up', 'returning', 'delivered', 'returned') AND rider_id IS NOT NULL)
 OR (status = 'cancelled')
);

ALTER TABLE orders ADD CONSTRAINT chk_orders_cancellation CHECK (
    (status = 'cancelled' AND cancelled_at IS NOT NULL AND cancel_reason IS NOT NULL
        AND cancel_reason ~ '^[a-z][a-z0-9_]{1,39}$')
 OR (status <> 'cancelled' AND cancelled_at IS NULL AND cancel_reason IS NULL)
);

ALTER TABLE orders ADD CONSTRAINT chk_orders_compensation CHECK (
    rider_compensation_cents + platform_fee_cents <= delivery_fee_cents
);
