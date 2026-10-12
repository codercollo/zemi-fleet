-- =============================================================================
-- Zemi Fleet V2 - initial schema
-- PostgreSQL 16

CREATE EXTENSION IF NOT EXISTS citext;

-- -----------------------------------------------------------------------------
-- Enums
-- -----------------------------------------------------------------------------
CREATE TYPE user_role    AS ENUM ('admin', 'dispatcher', 'rider');
CREATE TYPE order_status AS ENUM ('pending', 'batched', 'assigned', 'picked_up', 'delivered', 'cancelled');
CREATE TYPE batch_status AS ENUM ('open', 'accepted', 'in_progress', 'completed', 'cancelled', 'expired');
CREATE TYPE stop_type    AS ENUM ('pickup', 'delivery');

-- -----------------------------------------------------------------------------
-- Shared trigger: keep updated_at honest
-- -----------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- -----------------------------------------------------------------------------
-- Identity & auth (Simple Bank style: users + refresh-token sessions)
-- -----------------------------------------------------------------------------
CREATE TABLE users (
    id               TEXT PRIMARY KEY,
    full_name        TEXT        NOT NULL CHECK (length(full_name) BETWEEN 2 AND 120),
    email            CITEXT      NOT NULL UNIQUE,
    phone            TEXT        NOT NULL UNIQUE CHECK (phone ~ '^\+254[0-9]{9}$'),
    hashed_password  TEXT        NOT NULL,                    -- bcrypt
    role             user_role   NOT NULL,
    is_active        BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    version          INTEGER     NOT NULL DEFAULT 1
);
CREATE TRIGGER trg_users_updated_at BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE sessions (
    id             UUID PRIMARY KEY,                          -- refresh token id
    user_id        TEXT        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    refresh_token  TEXT        NOT NULL,
    user_agent     TEXT        NOT NULL DEFAULT '',
    client_ip      INET,
    is_blocked     BOOLEAN     NOT NULL DEFAULT FALSE,
    expires_at     TIMESTAMPTZ NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_sessions_user_id ON sessions (user_id);

-- -----------------------------------------------------------------------------
-- Reference data
-- -----------------------------------------------------------------------------
CREATE TABLE categories (
    code        TEXT PRIMARY KEY CHECK (code ~ '^[a-z][a-z_]{1,39}$'),   -- 'electronics', 'fragile'
    label       TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Areas are modelled as a centre + coverage radius (per Zemi's clarification).
-- A point is "in" an area when haversine(point, centre) <= radius_m.
-- Areas may overlap (e.g. Kilimani and Lavington) - that is intentional.
CREATE TABLE areas (
    id          TEXT PRIMARY KEY,
    name        TEXT             NOT NULL UNIQUE,                     -- 'Kilimani'
    center_lat  DOUBLE PRECISION NOT NULL CHECK (center_lat BETWEEN -90  AND 90),
    center_lng  DOUBLE PRECISION NOT NULL CHECK (center_lng BETWEEN -180 AND 180),
    radius_m    INTEGER          NOT NULL CHECK (radius_m BETWEEN 500 AND 20000),
    created_at  TIMESTAMPTZ      NOT NULL DEFAULT now()
);

-- -----------------------------------------------------------------------------
-- Vendors (pickup origin). Batches are surfaced to riders near the vendor.
-- -----------------------------------------------------------------------------
CREATE TABLE vendors (
    id          TEXT PRIMARY KEY,
    name        TEXT             NOT NULL,
    phone       TEXT             NOT NULL CHECK (phone ~ '^\+254[0-9]{9}$'),
    lat         DOUBLE PRECISION NOT NULL CHECK (lat BETWEEN -90  AND 90),
    lng         DOUBLE PRECISION NOT NULL CHECK (lng BETWEEN -180 AND 180),
    created_at  TIMESTAMPTZ      NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ      NOT NULL DEFAULT now()
);
CREATE TRIGGER trg_vendors_updated_at BEFORE UPDATE ON vendors
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- -----------------------------------------------------------------------------
-- Riders
-- -----------------------------------------------------------------------------
CREATE TABLE riders (
    id                    TEXT PRIMARY KEY,
    user_id               TEXT             NOT NULL UNIQUE REFERENCES users (id) ON DELETE RESTRICT,
    is_available          BOOLEAN          NOT NULL DEFAULT FALSE,     -- on shift
    max_capacity          SMALLINT         NOT NULL DEFAULT 5  CHECK (max_capacity BETWEEN 1 AND 5),
    current_orders_count  SMALLINT         NOT NULL DEFAULT 0,
    -- last known telemetry (updated by match-orders calls / location pings)
    last_lat              DOUBLE PRECISION CHECK (last_lat BETWEEN -90  AND 90),
    last_lng              DOUBLE PRECISION CHECK (last_lng BETWEEN -180 AND 180),
    last_bearing_deg      REAL             CHECK (last_bearing_deg >= 0 AND last_bearing_deg < 360),
    last_speed_kmh        REAL             CHECK (last_speed_kmh >= 0 AND last_speed_kmh <= 200),
    location_updated_at   TIMESTAMPTZ,
    -- acceptance rate = accepted / offered, derived so it can never drift
    offers_count          INTEGER          NOT NULL DEFAULT 0 CHECK (offers_count >= 0),
    accepted_count        INTEGER          NOT NULL DEFAULT 0 CHECK (accepted_count >= 0),
    acceptance_rate       NUMERIC(4,3)     GENERATED ALWAYS AS (
                              CASE WHEN offers_count = 0 THEN 0
                                   ELSE round(accepted_count::numeric / offers_count, 3) END
                          ) STORED,
    created_at            TIMESTAMPTZ      NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ      NOT NULL DEFAULT now(),
    version               INTEGER          NOT NULL DEFAULT 1,

    CONSTRAINT chk_riders_capacity   CHECK (current_orders_count BETWEEN 0 AND max_capacity),
    CONSTRAINT chk_riders_accepts    CHECK (accepted_count <= offers_count),
    CONSTRAINT chk_riders_location   CHECK ((last_lat IS NULL) = (last_lng IS NULL))
);
CREATE TRIGGER trg_riders_updated_at BEFORE UPDATE ON riders
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Candidate lookup for match-riders: available riders with spare capacity.
CREATE INDEX idx_riders_available_location
    ON riders (last_lat, last_lng)
    WHERE is_available AND current_orders_count < max_capacity;

-- -----------------------------------------------------------------------------
-- Rider preferences (1:1 settings + M:N categories/areas)
-- No rows in the M:N tables = "no preference" = matches everything.
-- -----------------------------------------------------------------------------
CREATE TABLE rider_preferences (
    rider_id               TEXT        PRIMARY KEY REFERENCES riders (id) ON DELETE CASCADE,
    min_order_value_cents  BIGINT      NOT NULL DEFAULT 0 CHECK (min_order_value_cents >= 0),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TRIGGER trg_rider_preferences_updated_at BEFORE UPDATE ON rider_preferences
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE rider_preferred_categories (
    rider_id       TEXT NOT NULL REFERENCES riders (id)       ON DELETE CASCADE,
    category_code  TEXT NOT NULL REFERENCES categories (code) ON DELETE CASCADE,
    PRIMARY KEY (rider_id, category_code)
);
CREATE INDEX idx_rpc_category ON rider_preferred_categories (category_code);

CREATE TABLE rider_preferred_areas (
    rider_id  TEXT NOT NULL REFERENCES riders (id) ON DELETE CASCADE,
    area_id   TEXT NOT NULL REFERENCES areas (id)  ON DELETE CASCADE,
    PRIMARY KEY (rider_id, area_id)
);
CREATE INDEX idx_rpa_area ON rider_preferred_areas (area_id);

-- -----------------------------------------------------------------------------
-- Batches
-- Lifecycle: open -> accepted -> in_progress -> completed
--            open -> expired | cancelled
-- Exclusive acceptance (no double-assignment) is enforced in one transaction:
--   1. SELECT ... FROM riders WHERE id = $rider FOR UPDATE      (lock rider)
--   2. UPDATE batches SET status='accepted', rider_id=$rider, version=version+1
--        WHERE id=$batch AND status='open' AND version=$v      (0 rows => 409)
--   3. UPDATE orders SET status='assigned', rider_id=$rider WHERE batch_id=$batch
--   4. UPDATE riders SET current_orders_count = current_orders_count + $n
--        (CHECK constraint rejects anything over capacity => 422)
-- -----------------------------------------------------------------------------
CREATE TABLE batches (
    id                        TEXT PRIMARY KEY,
    status                    batch_status     NOT NULL DEFAULT 'open',
    rider_id                  TEXT             REFERENCES riders (id) ON DELETE RESTRICT,
    -- centroid of the pickup cluster; used to show batches to nearby riders
    pickup_centroid_lat       DOUBLE PRECISION NOT NULL CHECK (pickup_centroid_lat BETWEEN -90  AND 90),
    pickup_centroid_lng       DOUBLE PRECISION NOT NULL CHECK (pickup_centroid_lng BETWEEN -180 AND 180),
    pickup_spread_m           INTEGER          NOT NULL CHECK (pickup_spread_m >= 0),  -- max pairwise pickup distance (<= 2000 by rule)
    orders_count              SMALLINT         NOT NULL CHECK (orders_count BETWEEN 1 AND 5),
    total_distance_m          INTEGER          NOT NULL CHECK (total_distance_m >= 0),
    estimated_duration_s      INTEGER          NOT NULL CHECK (estimated_duration_s >= 0),
    total_delivery_fee_cents  BIGINT           NOT NULL CHECK (total_delivery_fee_cents >= 0),
    estimated_earnings_cents  BIGINT           NOT NULL CHECK (estimated_earnings_cents >= 0),
    route_optimization_score  NUMERIC(4,3)     NOT NULL CHECK (route_optimization_score BETWEEN 0 AND 1),
    expires_at                TIMESTAMPTZ      NOT NULL,
    accepted_at               TIMESTAMPTZ,
    completed_at              TIMESTAMPTZ,
    created_at                TIMESTAMPTZ      NOT NULL DEFAULT now(),
    updated_at                TIMESTAMPTZ      NOT NULL DEFAULT now(),
    version                   INTEGER          NOT NULL DEFAULT 1,

    CONSTRAINT chk_batches_earnings  CHECK (estimated_earnings_cents <= total_delivery_fee_cents),
    -- a rider is attached iff the batch has been accepted (or later)
    CONSTRAINT chk_batches_rider     CHECK (
        (status IN ('open', 'expired') AND rider_id IS NULL AND accepted_at IS NULL)
     OR (status IN ('accepted', 'in_progress', 'completed') AND rider_id IS NOT NULL AND accepted_at IS NOT NULL)
     OR (status = 'cancelled')
    )
);
CREATE TRIGGER trg_batches_updated_at BEFORE UPDATE ON batches
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- "Show me open batches near me" - bounding-box prefilter, haversine in Go.
CREATE INDEX idx_batches_open_centroid
    ON batches (pickup_centroid_lat, pickup_centroid_lng)
    WHERE status = 'open';
CREATE INDEX idx_batches_rider_active
    ON batches (rider_id)
    WHERE status IN ('accepted', 'in_progress');

-- -----------------------------------------------------------------------------
-- Orders
-- -----------------------------------------------------------------------------
CREATE TABLE orders (
    id                    TEXT PRIMARY KEY,
    vendor_id             TEXT             NOT NULL REFERENCES vendors (id)      ON DELETE RESTRICT,
    category_code         TEXT             NOT NULL REFERENCES categories (code) ON DELETE RESTRICT,
    status                order_status     NOT NULL DEFAULT 'pending',
    -- pickup is snapshotted from the vendor at creation (vendor may move later)
    pickup_lat            DOUBLE PRECISION NOT NULL CHECK (pickup_lat   BETWEEN -90  AND 90),
    pickup_lng            DOUBLE PRECISION NOT NULL CHECK (pickup_lng   BETWEEN -180 AND 180),
    delivery_lat          DOUBLE PRECISION NOT NULL CHECK (delivery_lat BETWEEN -90  AND 90),
    delivery_lng          DOUBLE PRECISION NOT NULL CHECK (delivery_lng BETWEEN -180 AND 180),
    -- precomputed once on insert; used by batching for direction similarity
    trip_distance_m       INTEGER          NOT NULL CHECK (trip_distance_m >= 0),
    trip_bearing_deg      REAL             NOT NULL CHECK (trip_bearing_deg >= 0 AND trip_bearing_deg < 360),
    -- money (KES cents)
    value_cents           BIGINT           NOT NULL CHECK (value_cents > 0),       -- goods value (preference filter)
    delivery_fee_cents    BIGINT           NOT NULL CHECK (delivery_fee_cents >= 0),
    rider_payout_bps      INTEGER          NOT NULL CHECK (rider_payout_bps BETWEEN 0 AND 10000), -- 9000 = 90%
    rider_earnings_cents  BIGINT           GENERATED ALWAYS AS (delivery_fee_cents * rider_payout_bps / 10000) STORED,
    platform_fee_cents    BIGINT           GENERATED ALWAYS AS (delivery_fee_cents - delivery_fee_cents * rider_payout_bps / 10000) STORED,
    -- assignment
    batch_id              TEXT             REFERENCES batches (id) ON DELETE SET NULL,
    rider_id              TEXT             REFERENCES riders (id)  ON DELETE RESTRICT,
    batch_seq             SMALLINT         CHECK (batch_seq BETWEEN 1 AND 5),      -- FIFO rank inside its batch
    created_at            TIMESTAMPTZ      NOT NULL DEFAULT now(),                 -- FIFO key
    updated_at            TIMESTAMPTZ      NOT NULL DEFAULT now(),
    delivered_at          TIMESTAMPTZ,
    version               INTEGER          NOT NULL DEFAULT 1,

    CONSTRAINT chk_orders_assignment CHECK (
        (status = 'pending'   AND batch_id IS NULL     AND rider_id IS NULL)
     OR (status = 'batched'   AND batch_id IS NOT NULL AND rider_id IS NULL)
     OR (status IN ('assigned', 'picked_up', 'delivered') AND rider_id IS NOT NULL)
     OR (status = 'cancelled')
    )
);
CREATE TRIGGER trg_orders_updated_at BEFORE UPDATE ON orders
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Batching + cone matching both scan PENDING orders by pickup location, oldest first.
CREATE INDEX idx_orders_pending_pickup
    ON orders (pickup_lat, pickup_lng, created_at)
    WHERE status = 'pending';
CREATE INDEX idx_orders_pending_created
    ON orders (created_at)
    WHERE status = 'pending';
CREATE INDEX idx_orders_batch_id  ON orders (batch_id) WHERE batch_id IS NOT NULL;
CREATE INDEX idx_orders_rider_id  ON orders (rider_id) WHERE rider_id IS NOT NULL;
CREATE INDEX idx_orders_vendor_id ON orders (vendor_id);

-- -----------------------------------------------------------------------------
-- Batch stops: the suggested pickup/delivery sequence for a batch.
-- Invariant (enforced in Go, verified in tests): an order's pickup seq < its delivery seq.
-- -----------------------------------------------------------------------------
CREATE TABLE batch_stops (
    batch_id            TEXT             NOT NULL REFERENCES batches (id) ON DELETE CASCADE,
    seq                 SMALLINT         NOT NULL CHECK (seq BETWEEN 1 AND 10),   -- 5 orders x 2 stops
    order_id            TEXT             NOT NULL REFERENCES orders (id)  ON DELETE CASCADE,
    type                stop_type        NOT NULL,
    lat                 DOUBLE PRECISION NOT NULL CHECK (lat BETWEEN -90  AND 90),
    lng                 DOUBLE PRECISION NOT NULL CHECK (lng BETWEEN -180 AND 180),
    leg_distance_m      INTEGER          NOT NULL CHECK (leg_distance_m >= 0),     -- from previous stop
    eta_offset_s        INTEGER          NOT NULL CHECK (eta_offset_s >= 0),       -- from batch start
    completed_at        TIMESTAMPTZ,
    PRIMARY KEY (batch_id, seq),
    CONSTRAINT uq_batch_stops_order_type UNIQUE (batch_id, order_id, type)
);
CREATE INDEX idx_batch_stops_order_id ON batch_stops (order_id);

-- -----------------------------------------------------------------------------
-- Batch offers: which riders were shown which batch (drives acceptance_rate,
-- and gives an audit trail for "why did rider X not get batch Y").
-- -----------------------------------------------------------------------------
CREATE TABLE batch_offers (
    batch_id     TEXT        NOT NULL REFERENCES batches (id) ON DELETE CASCADE,
    rider_id     TEXT        NOT NULL REFERENCES riders (id)  ON DELETE CASCADE,
    distance_m   INTEGER     NOT NULL CHECK (distance_m >= 0),   -- rider -> pickup centroid at offer time
    offered_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    responded_at TIMESTAMPTZ,
    accepted     BOOLEAN,                                        -- NULL = no response yet
    PRIMARY KEY (batch_id, rider_id)
);
CREATE INDEX idx_batch_offers_rider ON batch_offers (rider_id, offered_at DESC);