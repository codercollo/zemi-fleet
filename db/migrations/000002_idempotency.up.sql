-- Idempotency keys for POST /orders and POST /orders/batch.
--
-- A client that retries a request with the same Idempotency-Key gets the stored response instead of a second
-- order or a second batch. A row is CLAIMED first (response_status NULL = being processed), so two concurrent
-- requests with the same key cannot both run, then COMPLETED with the response that is replayed.
CREATE TABLE idempotency_keys (
    key              TEXT        NOT NULL CHECK (length(key) BETWEEN 1 AND 64),
    endpoint         TEXT        NOT NULL,                       -- the route, e.g. /api/v2/orders: a key is scoped to one endpoint
    request_hash     TEXT        NOT NULL,                       -- sha256 of the (whitespace-compacted) request body
    response_status  INTEGER     CHECK (response_status BETWEEN 100 AND 599),
    response_body    TEXT,
    response_headers JSONB       NOT NULL DEFAULT '{}'::jsonb,   -- headers worth replaying, e.g. Location
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (key, endpoint),
    -- a row is either still being processed (no response yet) or complete (status and body together)
    CONSTRAINT chk_idempotency_response CHECK ((response_status IS NULL) = (response_body IS NULL))
);

-- Cleanup on write deletes by age.
CREATE INDEX idx_idempotency_keys_created_at ON idempotency_keys (created_at);
