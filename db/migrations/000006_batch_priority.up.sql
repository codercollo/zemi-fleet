ALTER TABLE vendors
    ADD COLUMN rating NUMERIC(2,1) NOT NULL DEFAULT 0
        CHECK (rating BETWEEN 0 AND 5);

ALTER TABLE orders
    ADD COLUMN urgency SMALLINT NOT NULL DEFAULT 0
        CHECK (urgency BETWEEN 0 AND 100);

CREATE INDEX idx_orders_pending_priority
    ON orders (created_at, id)
    WHERE status = 'pending';
