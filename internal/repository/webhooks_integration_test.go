//go:build integration

package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/codercollo/zemi-fleet/internal/testdb"
)

// The real ClaimDue query is the scale-out guarantee: two workers race on one
// row, but SKIP LOCKED plus the atomic UPDATE lets only one receive it.
func TestIntegration_TwoWebhookWorkersClaimOnce(t *testing.T) {
	pool := testdb.Open(t)
	ctx := context.Background()
	store := NewStore(pool, 5*time.Second)
	eventID, subscriptionID := "evt_claim_once", "sub_claim_once"
	_, err := pool.Exec(ctx, `INSERT INTO webhook_subscriptions (id, client_name, url, secret) VALUES ($1, $2, 'https://example.com/hook', 'secret') ON CONFLICT (id) DO NOTHING`, subscriptionID, "claim-once")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO outbox_events (id, event_type, payload, created_at) VALUES ($1, 'batch.accepted', '{}'::jsonb, now()) ON CONFLICT (id) DO NOTHING`, eventID)
	require.NoError(t, err)
	var deliveryID string
	require.NoError(t, pool.QueryRow(ctx, `SELECT id FROM webhook_deliveries WHERE event_id = $1 AND subscription_id = $2`, eventID, subscriptionID).Scan(&deliveryID))
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	_, err = pool.Exec(ctx, `UPDATE webhook_deliveries SET next_attempt_at = $2 WHERE id = $1`, deliveryID, now)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM webhook_subscriptions WHERE id = $1`, subscriptionID)
		_, _ = pool.Exec(ctx, `DELETE FROM outbox_events WHERE id = $1`, eventID)
	})

	var wg sync.WaitGroup
	results := make(chan struct {
		claimed bool
		err     error
	}, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := store.ClaimDue(ctx, now)
			results <- struct {
				claimed bool
				err     error
			}{d != nil, err}
		}()
	}
	wg.Wait()
	close(results)
	claimed := 0
	for got := range results {
		require.NoError(t, got.err)
		if got.claimed {
			claimed++
		}
	}
	require.Equal(t, 1, claimed)
}

func TestIntegration_WebhookDeliveriesFollowBatchLifecycle(t *testing.T) {
	pool := testdb.Open(t)
	ctx := context.Background()
	store := NewStore(pool, 5*time.Second)
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	const (
		subscriptionID = "sub_batch_lifecycle"
		riderID        = "rider_batch_lifecycle"
		batchID        = "batch_webhook_lifecycle"
		orderID        = "order_webhook_lifecycle"
	)

	_, err := pool.Exec(ctx, `
		INSERT INTO webhook_subscriptions (id, client_name, url, secret)
		VALUES ($1, 'batch-lifecycle', 'https://example.com/hook', 'secret')`,
		subscriptionID)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
		INSERT INTO riders (id, full_name, phone, is_available, max_capacity)
		VALUES ($1, 'Lifecycle Rider', '+254700000098', true, 5)`,
		riderID)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
		INSERT INTO batches (id, pickup_centroid_lat, pickup_centroid_lng, pickup_spread_m, orders_count,
			total_distance_m, estimated_duration_s, total_delivery_fee_cents, estimated_earnings_cents,
			route_optimization_score, expires_at, created_at)
		VALUES ($1, -1.2635, 36.803, 0, 1, 1000, 600, 1000, 900, 0.900, $2, $3)`,
		batchID, now.Add(time.Hour), now)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
		INSERT INTO orders (id, vendor_id, category_code, status, pickup_lat, pickup_lng, delivery_lat, delivery_lng,
			trip_distance_m, trip_bearing_deg, value_cents, delivery_fee_cents, rider_payout_bps, batch_id, batch_seq,
			created_at)
		VALUES ($1, 'ven_001', 'electronics', 'batched', -1.2635, 36.803, -1.2700, 36.8100,
			1000, 45, 5000, 1000, 9000, $2, 1, $3)`,
		orderID, batchID, now)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
		INSERT INTO batch_stops (batch_id, seq, order_id, type, lat, lng, leg_distance_m, eta_offset_s)
		VALUES ($1, 1, $2, 'pickup', -1.2635, 36.803, 0, 0),
		       ($1, 2, $2, 'delivery', -1.2700, 36.8100, 1000, 600)`,
		batchID, orderID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM webhook_subscriptions WHERE id = $1`, subscriptionID)
		_, _ = pool.Exec(ctx, `DELETE FROM outbox_events WHERE payload->>'batch_id' = $1`, batchID)
		_, _ = pool.Exec(ctx, `DELETE FROM batches WHERE id = $1`, batchID)
		_, _ = pool.Exec(ctx, `DELETE FROM riders WHERE id = $1`, riderID)
	})

	_, held, err := store.AcceptBatchTx(ctx, AcceptBatchParams{BatchID: batchID, RiderID: riderID, Now: now, Timezone: time.UTC})
	require.NoError(t, err)
	require.False(t, held)
	require.Equal(t, int64(1), countWebhookDeliveries(t, pool, subscriptionID, "batch.accepted"))

	_, err = store.CompleteBatchStopTx(ctx, CompleteStopParams{BatchID: batchID, Seq: 1, Now: now.Add(10 * time.Minute)})
	require.NoError(t, err)
	result, err := store.CompleteBatchStopTx(ctx, CompleteStopParams{BatchID: batchID, Seq: 2, Now: now.Add(20 * time.Minute)})
	require.NoError(t, err)
	require.Equal(t, "delivered", string(result.Order.Status))
	require.Equal(t, int64(1), countWebhookDeliveries(t, pool, subscriptionID, "order.delivered"))
	require.Equal(t, int64(2), countWebhookDeliveriesForSubscription(t, pool, subscriptionID))
}

func countWebhookDeliveries(t *testing.T, pool *pgxpool.Pool, subscriptionID, eventType string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, pool.QueryRow(context.Background(), `
		SELECT count(*) FROM webhook_deliveries d
		JOIN outbox_events e ON e.id = d.event_id
		WHERE d.subscription_id = $1 AND e.event_type = $2`, subscriptionID, eventType).Scan(&n))
	return n
}

func countWebhookDeliveriesForSubscription(t *testing.T, pool *pgxpool.Pool, subscriptionID string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM webhook_deliveries WHERE subscription_id = $1`, subscriptionID).Scan(&n))
	return n
}

// Killing the transaction connection before commit must leave neither the
// state mutation nor its outbox event visible on a fresh connection.
func TestIntegration_TerminatedTransactionLeavesNoPartialOutbox(t *testing.T) {
	pool := testdb.Open(t)
	ctx := context.Background()
	riderID, eventID := "fault_rider_atomic", "fault_event_atomic"
	conn, err := pool.Acquire(ctx)
	require.NoError(t, err)
	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO riders (id, full_name, phone, is_available, max_capacity) VALUES ($1, 'Fault Rider', '+254700000099', true, 5)`, riderID)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO outbox_events (id, event_type, payload, created_at) VALUES ($1, 'rider.offline', '{}'::jsonb, now())`, eventID)
	require.NoError(t, err)
	pid := conn.Conn().PgConn().PID()
	_, err = pool.Exec(ctx, `SELECT pg_terminate_backend($1)`, pid)
	require.NoError(t, err)
	_ = tx.Commit(ctx) // expected to fail because the backend was terminated
	conn.Release()

	var riders, events int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM riders WHERE id = $1`, riderID).Scan(&riders))
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE id = $1`, eventID).Scan(&events))
	require.Zero(t, riders)
	require.Zero(t, events)
}
