# Event contracts

Phase 10 events are written to the outbox before delivery. Webhook payloads are JSON and are signed with
`X-Zemi-Signature: sha256=<hex HMAC-SHA256(payload)>`. `X-Zemi-Event-ID` is stable across retries and lets
partners deduplicate deliveries.

Supported event types are `batch.accepted`, `order.delivered`, `order.cancelled`, `order.returned`, and
`rider.offline`. Payloads include the event-specific identifiers (`batch_id`, `order_id`, or `rider_id`) and
are deliberately additive: consumers must ignore fields they do not understand.

Delivery is disabled by default with `WEBHOOKS_ENABLED=false`. Failed deliveries use the schedule 1 minute,
5 minutes, 30 minutes, 2 hours, and 24 hours, then become `failed`. Only HTTPS endpoints are accepted, and
loopback, private, link-local, and unspecified addresses are rejected to prevent SSRF.
