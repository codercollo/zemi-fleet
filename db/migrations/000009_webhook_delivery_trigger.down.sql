DROP TRIGGER IF EXISTS outbox_event_webhook_delivery ON outbox_events;
DROP FUNCTION IF EXISTS enqueue_webhook_deliveries();
