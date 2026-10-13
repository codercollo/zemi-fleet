DROP TABLE IF EXISTS batch_offers;
DROP TABLE IF EXISTS batch_stops;
DROP TABLE IF EXISTS orders;
DROP TABLE IF EXISTS batches;
DROP TABLE IF EXISTS rider_preferred_areas;
DROP TABLE IF EXISTS rider_preferred_categories;
DROP TABLE IF EXISTS rider_preferences;
DROP TABLE IF EXISTS riders;
DROP TABLE IF EXISTS vendors;
DROP TABLE IF EXISTS areas;
DROP TABLE IF EXISTS categories;

DROP FUNCTION IF EXISTS set_updated_at();

DROP TYPE IF EXISTS stop_type;
DROP TYPE IF EXISTS batch_status;
DROP TYPE IF EXISTS order_status;
