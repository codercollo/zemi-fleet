CREATE OR REPLACE FUNCTION enqueue_webhook_deliveries() RETURNS trigger AS $$
BEGIN
    INSERT INTO webhook_deliveries (id, event_id, subscription_id, next_attempt_at)
    SELECT NEW.id || ':' || s.id, NEW.id, s.id, GREATEST(NEW.created_at, now()) + interval '1 second'
    FROM webhook_subscriptions s WHERE s.enabled
    ON CONFLICT (event_id, subscription_id) DO NOTHING;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER outbox_event_webhook_delivery
AFTER INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION enqueue_webhook_deliveries();
