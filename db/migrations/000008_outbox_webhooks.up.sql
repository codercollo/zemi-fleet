CREATE TABLE outbox_events (
    id          TEXT PRIMARY KEY,
    event_type  TEXT NOT NULL CHECK (event_type IN ('batch.accepted', 'order.delivered', 'order.cancelled', 'order.returned', 'rider.offline')),
    payload     JSONB NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ
);
CREATE INDEX idx_outbox_events_pending ON outbox_events (created_at) WHERE published_at IS NULL;

CREATE TABLE webhook_subscriptions (
    id          TEXT PRIMARY KEY,
    client_name TEXT NOT NULL UNIQUE,
    url         TEXT NOT NULL,
    secret      TEXT NOT NULL,
    enabled     BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_webhook_url_https CHECK (url LIKE 'https://%')
);

CREATE TABLE webhook_deliveries (
    id              TEXT PRIMARY KEY,
    event_id        TEXT NOT NULL REFERENCES outbox_events(id) ON DELETE CASCADE,
    subscription_id TEXT NOT NULL REFERENCES webhook_subscriptions(id) ON DELETE CASCADE,
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'delivered', 'failed')),
    attempts        INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL,
    last_status     INTEGER,
    UNIQUE (event_id, subscription_id)
);
CREATE INDEX idx_webhook_deliveries_due ON webhook_deliveries (next_attempt_at) WHERE status = 'pending';
