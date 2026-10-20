DROP INDEX IF EXISTS idx_orders_rider_active_load;
ALTER TABLE orders DROP COLUMN IF EXISTS volume_cm3, DROP COLUMN IF EXISTS weight_g;
