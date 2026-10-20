ALTER TABLE orders
    ADD COLUMN weight_g BIGINT NOT NULL DEFAULT 0 CHECK (weight_g >= 0),
    ADD COLUMN volume_cm3 BIGINT NOT NULL DEFAULT 0 CHECK (volume_cm3 >= 0);

CREATE INDEX idx_orders_rider_active_load
    ON orders (rider_id)
    WHERE rider_id IS NOT NULL AND status IN ('assigned', 'picked_up');
