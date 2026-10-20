ALTER TABLE riders ADD COLUMN offline_flagged_at TIMESTAMPTZ;
CREATE INDEX idx_riders_offline_candidates
    ON riders (location_updated_at)
    WHERE current_orders_count > 0 AND offline_flagged_at IS NULL;
