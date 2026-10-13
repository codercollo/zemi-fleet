-- Reference data for the integration and e2e tests: the same categories, areas and vendors as db/seed/seed.sql
-- (a copy: change both together). No riders and no orders: tests create those themselves through a fixture
-- helper or through the API. Loaded by internal/testdb after it truncates the data tables.

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

