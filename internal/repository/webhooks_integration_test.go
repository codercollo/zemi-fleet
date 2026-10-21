//go:build integration

package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/codercollo/zemi-fleet/internal/testdb"
)

// The real ClaimDue query is the scale-out guarantee: two workers race on one
// row, but SKIP LOCKED plus the atomic UPDATE lets only one receive it.
func TestIntegration_TwoWebhookWorkersClaimOnce(t *testing.T) {
	pool := testdb.Open(t)
	ctx := context.Background()
	store := NewStore(pool, 5*time.Second)
	eventID, subscriptionID, deliveryID := "evt_claim_once", "sub_claim_once", "del_claim_once"
	_, err := pool.Exec(ctx, `INSERT INTO webhook_subscriptions (id, client_name, url, secret) VALUES ($1, $2, 'https://example.com/hook', 'secret') ON CONFLICT (id) DO NOTHING`, subscriptionID, "claim-once")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO outbox_events (id, event_type, payload, created_at) VALUES ($1, 'batch.accepted', '{}'::jsonb, now()) ON CONFLICT (id) DO NOTHING`, eventID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO webhook_deliveries (id, event_id, subscription_id, next_attempt_at) VALUES ($1, $2, $3, '2026-10-20 12:00:00+00') ON CONFLICT (id) DO NOTHING`, deliveryID, eventID, subscriptionID)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM webhook_subscriptions WHERE id = $1`, subscriptionID) })

	var wg sync.WaitGroup
	results := make(chan struct {
		claimed bool
		err     error
	}, 2)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
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
