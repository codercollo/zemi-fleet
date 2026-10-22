DROP INDEX IF EXISTS idx_orders_pending_priority;
ALTER TABLE orders DROP COLUMN urgency;
ALTER TABLE vendors DROP COLUMN rating;
