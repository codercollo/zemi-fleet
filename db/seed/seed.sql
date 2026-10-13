-- Zemi Fleet V2 seed data (Nairobi). Re-runnable: truncates first, then reloads in one transaction.
-- Run: make dev/db/seed
--
-- Schema 000001 still has a users table and riders.user_id is NOT NULL, so each rider has a user row
-- with a placeholder hash that matches no password. Nobody can log in as a seeded user.
--
-- Rider cases covered: idle, partly loaded, AT CAPACITY (rider_003), stationary with unreliable bearing
-- (rider_004), off shift (rider_005), lower max_capacity (rider_006), brand new with no location (rider_008).
BEGIN;

TRUNCATE batch_offers, batch_stops, orders, batches, rider_preferred_areas, rider_preferred_categories,
         rider_preferences, riders, sessions, users, vendors, areas, categories CASCADE;

INSERT INTO categories (code, label) VALUES
  ('electronics', 'Electronics'),
  ('fragile', 'Fragile'),
  ('food', 'Food'),
  ('documents', 'Documents'),
  ('clothing', 'Clothing'),
  ('groceries', 'Groceries');

INSERT INTO areas (id, name, center_lat, center_lng, radius_m) VALUES
  ('area_001', 'Kilimani', -1.2905, 36.787, 2500),
  ('area_002', 'Westlands', -1.2676, 36.8108, 3000),
  ('area_003', 'CBD', -1.2864, 36.8172, 2000),
  ('area_004', 'Kileleshwa', -1.275, 36.783, 2200),
  ('area_005', 'Upper Hill', -1.297, 36.819, 2000),
  ('area_006', 'Lavington', -1.278, 36.769, 3000),
  ('area_007', 'Parklands', -1.263, 36.817, 2500),
  ('area_008', 'South B', -1.31, 36.833, 2500);

INSERT INTO vendors (id, name, phone, lat, lng) VALUES
  ('ven_001', 'Sarit Centre Gadgets', '+254711000001', -1.2635, 36.803),
  ('ven_002', 'Westlands Bites', '+254711000002', -1.266, 36.8045),
  ('ven_003', 'Kenyatta Avenue Documents Hub', '+254711000003', -1.284, 36.823),
  ('ven_004', 'Yaya Fashion House', '+254711000004', -1.2917, 36.7878),
  ('ven_005', 'Kilimani Fresh Market', '+254711000005', -1.2975, 36.786),
  ('ven_006', 'South B Pharmacy and Stores', '+254711000006', -1.3095, 36.8335);

INSERT INTO users (id, full_name, email, phone, hashed_password, role) VALUES
  ('usr_001', 'Brian Otieno', 'brian.otieno@seed.zemi.test', '+254722000001', '$2a$10$seedplaceholderhashthatmatchesnopassword', 'rider'),
  ('usr_002', 'Mercy Wanjiru', 'mercy.wanjiru@seed.zemi.test', '+254722000002', '$2a$10$seedplaceholderhashthatmatchesnopassword', 'rider'),
  ('usr_003', 'Kevin Mutua', 'kevin.mutua@seed.zemi.test', '+254722000003', '$2a$10$seedplaceholderhashthatmatchesnopassword', 'rider'),
  ('usr_004', 'Faith Achieng', 'faith.achieng@seed.zemi.test', '+254722000004', '$2a$10$seedplaceholderhashthatmatchesnopassword', 'rider'),
  ('usr_005', 'Samuel Kiprop', 'samuel.kiprop@seed.zemi.test', '+254722000005', '$2a$10$seedplaceholderhashthatmatchesnopassword', 'rider'),
  ('usr_006', 'Grace Njeri', 'grace.njeri@seed.zemi.test', '+254722000006', '$2a$10$seedplaceholderhashthatmatchesnopassword', 'rider'),
  ('usr_007', 'Peter Ochieng', 'peter.ochieng@seed.zemi.test', '+254722000007', '$2a$10$seedplaceholderhashthatmatchesnopassword', 'rider'),
  ('usr_008', 'Lucy Wambui', 'lucy.wambui@seed.zemi.test', '+254722000008', '$2a$10$seedplaceholderhashthatmatchesnopassword', 'rider'),
  ('usr_009', 'David Kamau', 'david.kamau@seed.zemi.test', '+254722000009', '$2a$10$seedplaceholderhashthatmatchesnopassword', 'rider'),
  ('usr_010', 'Aisha Hassan', 'aisha.hassan@seed.zemi.test', '+254722000010', '$2a$10$seedplaceholderhashthatmatchesnopassword', 'rider');

INSERT INTO riders (id, user_id, is_available, max_capacity, current_orders_count, last_lat, last_lng,
    last_bearing_deg, last_speed_kmh, location_updated_at, offers_count, accepted_count) VALUES
  ('rider_001', 'usr_001', TRUE, 5, 0, -1.266, 36.806, 95, 22, now() - interval '2 minutes', 40, 31),
  ('rider_002', 'usr_002', TRUE, 5, 2, -1.289, 36.79, 180, 18, now() - interval '3 minutes', 25, 22),
  ('rider_003', 'usr_003', TRUE, 5, 5, -1.285, 36.82, 270, 15, now() - interval '4 minutes', 18, 9),
  ('rider_004', 'usr_004', TRUE, 5, 1, -1.308, 36.832, 0, 0, now() - interval '5 minutes', 12, 11),
  ('rider_005', 'usr_005', FALSE, 5, 0, -1.276, 36.784, NULL, NULL, now() - interval '6 minutes', 30, 12),
  ('rider_006', 'usr_006', TRUE, 3, 1, -1.27, 36.777, 45, 25, now() - interval '7 minutes', 50, 44),
  ('rider_007', 'usr_007', TRUE, 5, 3, -1.296, 36.818, 135, 20, now() - interval '8 minutes', 16, 5),
  ('rider_008', 'usr_008', TRUE, 5, 0, NULL, NULL, NULL, NULL, NULL, 0, 0),
  ('rider_009', 'usr_009', TRUE, 5, 4, -1.262, 36.815, 315, 28, now() - interval '10 minutes', 60, 48),
  ('rider_010', 'usr_010', TRUE, 4, 0, -1.293, 36.786, 225, 12, now() - interval '11 minutes', 35, 30);

-- Preferences: riders 4, 8 and 10 have no preference row; riders 7 and 9 have a row but no category/area lists
-- (no rows in the M:N tables = no preference = matches everything).
INSERT INTO rider_preferences (rider_id, min_order_value_cents) VALUES
  ('rider_001', 0),
  ('rider_002', 50000),
  ('rider_003', 0),
  ('rider_005', 100000),
  ('rider_006', 20000),
  ('rider_007', 0),
  ('rider_009', 0);

INSERT INTO rider_preferred_categories (rider_id, category_code) VALUES
  ('rider_001', 'electronics'),
  ('rider_001', 'documents'),
  ('rider_002', 'clothing'),
  ('rider_002', 'fragile'),
  ('rider_002', 'electronics'),
  ('rider_003', 'food'),
  ('rider_003', 'groceries'),
  ('rider_005', 'electronics'),
  ('rider_006', 'food'),
  ('rider_006', 'documents');

INSERT INTO rider_preferred_areas (rider_id, area_id) VALUES
  ('rider_001', 'area_002'),
  ('rider_001', 'area_007'),
  ('rider_002', 'area_001'),
  ('rider_002', 'area_004'),
  ('rider_002', 'area_006'),
  ('rider_003', 'area_003'),
  ('rider_003', 'area_005'),
  ('rider_006', 'area_006'),
  ('rider_006', 'area_004'),
  ('rider_007', 'area_005'),
  ('rider_007', 'area_008');

-- Orders: all pending, created_at spread over the last ~2 hours (oldest first by id).
-- Pickups form 4 clusters: Westlands (ven_001/002), CBD (ven_003), Kilimani (ven_004/005), South B (ven_006).
-- Money is KES cents; payout is 90% (9000 bps), snapshotted per order.
INSERT INTO orders (id, vendor_id, category_code, status, pickup_lat, pickup_lng, delivery_lat, delivery_lng,
    trip_distance_m, trip_bearing_deg, value_cents, delivery_fee_cents, rider_payout_bps, created_at) VALUES
  ('ord_001', 'ven_005', 'groceries', 'pending', -1.2975, 36.786, -1.29, 36.792, 1068, 38.7, 139000, 13000, 9000, now() - interval '118 minutes'),  -- to Hurlingham
  ('ord_002', 'ven_001', 'electronics', 'pending', -1.2635, 36.803, -1.222, 36.896, 11322, 65.9, 2922200, 44000, 9000, now() - interval '115 minutes'),  -- to Kasarani
  ('ord_003', 'ven_002', 'food', 'pending', -1.266, 36.8045, -1.275, 36.846, 4721, 102.2, 179300, 24000, 9000, now() - interval '112 minutes'),  -- to Eastleigh
  ('ord_004', 'ven_004', 'clothing', 'pending', -1.2917, 36.7878, -1.307, 36.813, 3278, 121.3, 663200, 20000, 9000, now() - interval '109 minutes'),  -- to Madaraka
  ('ord_005', 'ven_004', 'fragile', 'pending', -1.2917, 36.7878, -1.324, 36.826, 5562, 130.2, 452800, 27000, 9000, now() - interval '106 minutes'),  -- to South C
  ('ord_006', 'ven_001', 'documents', 'pending', -1.2635, 36.803, -1.29, 36.792, 3190, 202.5, 74800, 20000, 9000, now() - interval '103 minutes'),  -- to Hurlingham
  ('ord_007', 'ven_003', 'documents', 'pending', -1.284, 36.823, -1.324, 36.826, 4460, 175.7, 76400, 23000, 9000, now() - interval '100 minutes'),  -- to South C
  ('ord_008', 'ven_006', 'groceries', 'pending', -1.3095, 36.8335, -1.233, 36.805, 9077, 339.6, 341400, 37000, 9000, now() - interval '97 minutes'),  -- to Gigiri
  ('ord_009', 'ven_001', 'fragile', 'pending', -1.2635, 36.803, -1.222, 36.896, 11322, 65.9, 368600, 44000, 9000, now() - interval '94 minutes'),  -- to Kasarani
  ('ord_010', 'ven_004', 'electronics', 'pending', -1.2917, 36.7878, -1.307, 36.813, 3278, 121.3, 2263000, 20000, 9000, now() - interval '91 minutes'),  -- to Madaraka
  ('ord_011', 'ven_004', 'electronics', 'pending', -1.2917, 36.7878, -1.324, 36.826, 5562, 130.2, 3399600, 27000, 9000, now() - interval '88 minutes'),  -- to South C
  ('ord_012', 'ven_003', 'documents', 'pending', -1.284, 36.823, -1.324, 36.826, 4460, 175.7, 62800, 23000, 9000, now() - interval '85 minutes'),  -- to South C
  ('ord_013', 'ven_004', 'fragile', 'pending', -1.2917, 36.7878, -1.307, 36.813, 3278, 121.3, 351300, 20000, 9000, now() - interval '82 minutes'),  -- to Madaraka
  ('ord_014', 'ven_005', 'food', 'pending', -1.2975, 36.786, -1.222, 36.896, 14833, 55.5, 174000, 54000, 9000, now() - interval '79 minutes'),  -- to Kasarani
  ('ord_015', 'ven_001', 'electronics', 'pending', -1.2635, 36.803, -1.29, 36.792, 3190, 202.5, 1116400, 20000, 9000, now() - interval '76 minutes'),  -- to Hurlingham
  ('ord_016', 'ven_002', 'food', 'pending', -1.266, 36.8045, -1.315, 36.8, 5471, 185.2, 71800, 26000, 9000, now() - interval '73 minutes'),  -- to Langata Rd
  ('ord_017', 'ven_006', 'documents', 'pending', -1.3095, 36.8335, -1.278, 36.769, 7980, 296.0, 34700, 34000, 9000, now() - interval '70 minutes'),  -- to Lavington
  ('ord_018', 'ven_004', 'clothing', 'pending', -1.2917, 36.7878, -1.324, 36.826, 5562, 130.2, 716600, 27000, 9000, now() - interval '67 minutes'),  -- to South C
  ('ord_019', 'ven_003', 'documents', 'pending', -1.284, 36.823, -1.324, 36.826, 4460, 175.7, 28800, 23000, 9000, now() - interval '64 minutes'),  -- to South C
  ('ord_020', 'ven_002', 'food', 'pending', -1.266, 36.8045, -1.275, 36.846, 4721, 102.2, 246300, 24000, 9000, now() - interval '61 minutes'),  -- to Eastleigh
  ('ord_021', 'ven_002', 'food', 'pending', -1.266, 36.8045, -1.315, 36.8, 5471, 185.2, 67600, 26000, 9000, now() - interval '59 minutes'),  -- to Langata Rd
  ('ord_022', 'ven_005', 'groceries', 'pending', -1.2975, 36.786, -1.29, 36.792, 1068, 38.7, 140300, 13000, 9000, now() - interval '56 minutes'),  -- to Hurlingham
  ('ord_023', 'ven_003', 'clothing', 'pending', -1.284, 36.823, -1.307, 36.813, 2789, 203.5, 547700, 18000, 9000, now() - interval '53 minutes'),  -- to Madaraka
  ('ord_024', 'ven_003', 'clothing', 'pending', -1.284, 36.823, -1.307, 36.813, 2789, 203.5, 555200, 18000, 9000, now() - interval '50 minutes'),  -- to Madaraka
  ('ord_025', 'ven_005', 'food', 'pending', -1.2975, 36.786, -1.222, 36.896, 14833, 55.5, 105200, 54000, 9000, now() - interval '47 minutes'),  -- to Kasarani
  ('ord_026', 'ven_006', 'groceries', 'pending', -1.3095, 36.8335, -1.233, 36.805, 9077, 339.6, 291600, 37000, 9000, now() - interval '44 minutes'),  -- to Gigiri
  ('ord_027', 'ven_001', 'fragile', 'pending', -1.2635, 36.803, -1.29, 36.792, 3190, 202.5, 497100, 20000, 9000, now() - interval '41 minutes'),  -- to Hurlingham
  ('ord_028', 'ven_003', 'documents', 'pending', -1.284, 36.823, -1.324, 36.826, 4460, 175.7, 44600, 23000, 9000, now() - interval '38 minutes'),  -- to South C
  ('ord_029', 'ven_001', 'documents', 'pending', -1.2635, 36.803, -1.222, 36.896, 11322, 65.9, 60400, 44000, 9000, now() - interval '35 minutes'),  -- to Kasarani
  ('ord_030', 'ven_003', 'documents', 'pending', -1.284, 36.823, -1.324, 36.826, 4460, 175.7, 26000, 23000, 9000, now() - interval '32 minutes'),  -- to South C
  ('ord_031', 'ven_003', 'clothing', 'pending', -1.284, 36.823, -1.307, 36.813, 2789, 203.5, 257200, 18000, 9000, now() - interval '29 minutes'),  -- to Madaraka
  ('ord_032', 'ven_005', 'groceries', 'pending', -1.2975, 36.786, -1.29, 36.792, 1068, 38.7, 471600, 13000, 9000, now() - interval '26 minutes'),  -- to Hurlingham
  ('ord_033', 'ven_003', 'clothing', 'pending', -1.284, 36.823, -1.307, 36.813, 2789, 203.5, 877300, 18000, 9000, now() - interval '23 minutes'),  -- to Madaraka
  ('ord_034', 'ven_002', 'food', 'pending', -1.266, 36.8045, -1.275, 36.846, 4721, 102.2, 103900, 24000, 9000, now() - interval '20 minutes'),  -- to Eastleigh
  ('ord_035', 'ven_001', 'electronics', 'pending', -1.2635, 36.803, -1.222, 36.896, 11322, 65.9, 1416800, 44000, 9000, now() - interval '17 minutes'),  -- to Kasarani
  ('ord_036', 'ven_002', 'food', 'pending', -1.266, 36.8045, -1.315, 36.8, 5471, 185.2, 163900, 26000, 9000, now() - interval '14 minutes'),  -- to Langata Rd
  ('ord_037', 'ven_006', 'groceries', 'pending', -1.3095, 36.8335, -1.233, 36.805, 9077, 339.6, 174500, 37000, 9000, now() - interval '11 minutes'),  -- to Gigiri
  ('ord_038', 'ven_003', 'clothing', 'pending', -1.284, 36.823, -1.307, 36.813, 2789, 203.5, 274300, 18000, 9000, now() - interval '8 minutes'),  -- to Madaraka
  ('ord_039', 'ven_006', 'documents', 'pending', -1.3095, 36.8335, -1.278, 36.769, 7980, 296.0, 49600, 34000, 9000, now() - interval '5 minutes'),  -- to Lavington
  ('ord_040', 'ven_002', 'food', 'pending', -1.266, 36.8045, -1.315, 36.8, 5471, 185.2, 134800, 26000, 9000, now() - interval '2 minutes');  -- to Langata Rd

COMMIT;
