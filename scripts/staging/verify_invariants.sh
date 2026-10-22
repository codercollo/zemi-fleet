#!/usr/bin/env bash
set -euo pipefail

COMPOSE="${COMPOSE:-docker compose -p zemi-staging -f docker-compose.yml}"
$COMPOSE exec -T postgres psql -U "${DB_USER:-zemi}" -d "${DB_NAME:-zemi_fleet}" <<'SQL'
WITH checks(name, violations) AS (
  VALUES
    ('batched_open', (SELECT count(*) FROM orders o WHERE o.status='batched' AND ((SELECT count(*) FROM batch_stops s JOIN batches b ON b.id=s.batch_id WHERE s.order_id=o.id AND s.type='pickup' AND b.status='open')<>1 OR NOT EXISTS (SELECT 1 FROM batches b WHERE b.id=o.batch_id AND b.status='open')))),
    ('assigned_batch', (SELECT count(*) FROM orders o WHERE o.status='assigned' AND NOT EXISTS (SELECT 1 FROM batches b WHERE b.id=o.batch_id AND b.status IN ('accepted','in_progress','completed') AND b.rider_id=o.rider_id))),
    ('rider_counts', (SELECT count(*) FROM riders r WHERE r.current_orders_count <> (SELECT count(*) FROM orders o WHERE o.rider_id=r.id AND o.status IN ('assigned','picked_up','returning')) OR r.current_orders_count>r.max_capacity)),
    ('money', (SELECT count(*) FROM orders WHERE rider_earnings_cents+platform_fee_cents<>delivery_fee_cents OR rider_compensation_cents<0 OR rider_compensation_cents+platform_fee_cents>delivery_fee_cents)),
    ('offline_assigned', (SELECT count(*) FROM orders o JOIN riders r ON r.id=o.rider_id WHERE o.status='assigned' AND r.offline_flagged_at IS NOT NULL)),
    ('duplicate_batches', (SELECT count(*) FROM (SELECT s.order_id FROM batch_stops s JOIN batches b ON b.id=s.batch_id WHERE s.type='pickup' AND b.status NOT IN ('expired','cancelled') GROUP BY s.order_id HAVING count(*)>1) x)),
    ('pickup_order', (SELECT count(*) FROM batch_stops p JOIN batch_stops d ON d.batch_id=p.batch_id AND d.order_id=p.order_id AND d.type='delivery' WHERE p.type='pickup' AND p.seq>=d.seq)),
    ('stop_pairs', (SELECT count(*) FROM (SELECT batch_id,order_id FROM batch_stops GROUP BY batch_id,order_id HAVING count(*) FILTER (WHERE type='pickup')<>1 OR count(*) FILTER (WHERE type='delivery')<>1) x)),
    ('batch_totals', (SELECT count(*) FROM batches b WHERE b.orders_count<>(SELECT count(DISTINCT s.order_id) FROM batch_stops s WHERE s.batch_id=b.id) OR b.orders_count*2<>(SELECT count(*) FROM batch_stops s WHERE s.batch_id=b.id))),
    ('idempotency_claims', (SELECT count(*) FROM idempotency_keys WHERE response_status IS NULL)),
    ('batch_state', (SELECT count(*) FROM batches WHERE (status IN ('open','expired') AND (rider_id IS NOT NULL OR accepted_at IS NOT NULL)) OR (status='accepted' AND (rider_id IS NULL OR accepted_at IS NULL))))
)
SELECT name, violations FROM checks ORDER BY name;
SQL
