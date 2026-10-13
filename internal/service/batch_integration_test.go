//go:build integration

package service

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codercollo/zemi-fleet/internal/clock"
	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/repository"
)

// A spot far from every seeded pickup (Nairobi), so these tests only ever see the orders they create.
var cluster = domain.Location{Lat: -1.5000, Lng: 36.5000}

type batchEnv struct {
	pool     *pgxpool.Pool
	clk      *clock.Test
	svc      *BatchService
	orderIDs []string // oldest first
	riderIDs []string
}

// newBatchEnv inserts n pending orders in one cluster, all heading north, created one minute apart.
func newBatchEnv(t *testing.T, n int) *batchEnv {
	t.Helper()
	pool := integrationPool(t)
	ctx := context.Background()

	var vendors int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM vendors WHERE id = 'ven_001'`).Scan(&vendors))
	require.Equal(t, 1, vendors, "reference data should provide vendor ven_001")

	clk := clock.NewTest(time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC))
	env := &batchEnv{pool: pool, clk: clk, svc: NewBatchService(repository.NewStore(pool, 3*time.Second), clk, batchCfg)}

	token := fmt.Sprintf("%08x", rand.Uint32())
	const metresPerDegLat = 111_195.0
	for i := range n {
		id := fmt.Sprintf("ord_it_%s_%02d", token, i)
		pickupLng := cluster.Lng + float64(i)*100/(metresPerDegLat*0.9997) // 100 m apart, eastwards
		_, err := pool.Exec(ctx, `INSERT INTO orders (id, vendor_id, category_code, pickup_lat, pickup_lng, delivery_lat, delivery_lng,
				trip_distance_m, trip_bearing_deg, value_cents, delivery_fee_cents, rider_payout_bps, created_at)
			VALUES ($1, 'ven_001', 'food', $2, $3, $4, $3, 4000, 0, 100000, 20000, 9000, $5)`,
			id, cluster.Lat, pickupLng, cluster.Lat+4000/metresPerDegLat, clk.Now().Add(time.Duration(i-n)*time.Minute))
		require.NoError(t, err)
		env.orderIDs = append(env.orderIDs, id)
	}

	t.Cleanup(func() {
		bg := context.Background()
		// Collect batch ids first: deleting a batch that still has 'batched' orders would violate the orders CHECK.
		rows, err := pool.Query(bg, `SELECT DISTINCT batch_id FROM batch_stops WHERE order_id = ANY($1)`, env.orderIDs)
		var batchIDs []string
		if err == nil {
			for rows.Next() {
				var id string
				if rows.Scan(&id) == nil {
					batchIDs = append(batchIDs, id)
				}
			}
			rows.Close()
		}
		_, _ = pool.Exec(bg, `DELETE FROM orders WHERE id = ANY($1)`, env.orderIDs)
		_, _ = pool.Exec(bg, `DELETE FROM batches WHERE id = ANY($1)`, batchIDs)
		_, _ = pool.Exec(bg, `DELETE FROM riders WHERE id = ANY($1)`, env.riderIDs)
	})
	return env
}

func (e *batchEnv) newRider(t *testing.T) string {
	t.Helper()
	id := fmt.Sprintf("rider_it_%08x", rand.Uint32())
	_, err := e.pool.Exec(context.Background(),
		`INSERT INTO riders (id, full_name, phone, is_available) VALUES ($1, 'Batch Test Rider', $2, TRUE)`,
		id, fmt.Sprintf("+254%09d", rand.IntN(1_000_000_000)))
	require.NoError(t, err)
	e.riderIDs = append(e.riderIDs, id)
	return id
}

func (e *batchEnv) request(riderID string) CreateBatchesInput {
	return CreateBatchesInput{RiderID: riderID, RiderLocation: cluster, MaxOrders: 5, RadiusKM: 2}
}

// batchSizes returns batch_id -> number of our orders in it.
func (e *batchEnv) batchOf(t *testing.T) map[string]string {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT id, COALESCE(batch_id, '') FROM orders WHERE id = ANY($1)`, e.orderIDs)
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, batch string
		require.NoError(t, rows.Scan(&id, &batch))
		out[id] = batch
	}
	return out
}

func (e *batchEnv) scalar(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, e.pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

func ids(offers []BatchOffer) []string {
	out := make([]string, 0, len(offers))
	for _, o := range offers {
		out = append(out, o.Batch.ID)
	}
	return out
}

// Two riders ask at the same moment over the same cluster: SKIP LOCKED means no order is batched twice.
func TestIntegration_ConcurrentRidersNeverShareAnOrder(t *testing.T) {
	env := newBatchEnv(t, 7)
	a, b := env.newRider(t), env.newRider(t)
	ctx := context.Background()

	results := make([][]BatchOffer, 2)
	errs := make([]error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, rider := range []string{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i], errs[i] = env.svc.CreateBatches(ctx, env.request(rider))
		}()
	}
	close(start)
	wg.Wait()

	require.NoError(t, errs[0])
	require.NoError(t, errs[1])

	// Across both responses, no order appears in two different batches.
	seenIn := map[string]string{}
	for _, res := range results {
		for _, o := range res {
			for _, bo := range o.Batch.Orders {
				if prev, ok := seenIn[bo.ID]; ok {
					assert.Equal(t, prev, o.Batch.ID, "order %s offered in two batches", bo.ID)
				}
				seenIn[bo.ID] = o.Batch.ID
			}
		}
	}

	// In the database: every order is batched exactly once, with exactly one pickup stop.
	for orderID, batchID := range env.batchOf(t) {
		assert.NotEmpty(t, batchID, "order %s must be batched", orderID)
		assert.Equal(t, 1, env.scalar(t, `SELECT count(*) FROM batch_stops WHERE order_id = $1 AND type = 'pickup'`, orderID), orderID)
		assert.Equal(t, 1, env.scalar(t, `SELECT count(*) FROM batch_stops WHERE order_id = $1 AND type = 'delivery'`, orderID), orderID)
	}
	sizes := map[string]int{}
	for _, batchID := range env.batchOf(t) {
		sizes[batchID]++
	}
	var got []int
	for _, n := range sizes {
		got = append(got, n)
	}
	assert.ElementsMatch(t, []int{5, 2}, got, "7 orders in one cluster make 5 + 2")

	// The oldest five share a batch.
	batchOf := env.batchOf(t)
	first := batchOf[env.orderIDs[0]]
	for _, id := range env.orderIDs[:5] {
		assert.Equal(t, first, batchOf[id], "the 5 oldest orders are batched together")
	}
	assert.NotEqual(t, first, batchOf[env.orderIDs[5]])
}

// A later request sees the open batches that already exist, forms nothing new, and records one offer per batch.
func TestIntegration_SecondRiderSeesExistingOpenBatches(t *testing.T) {
	env := newBatchEnv(t, 7)
	a, b := env.newRider(t), env.newRider(t)
	ctx := context.Background()

	first, err := env.svc.CreateBatches(ctx, env.request(a))
	require.NoError(t, err)
	require.Len(t, first, 2)
	assert.Len(t, first[0].Batch.Orders, 5, "the 5 oldest first")
	assert.Equal(t, domain.BatchOpen, first[0].Batch.Status, "a batch stays open until a rider accepts it")
	assert.True(t, first[0].Batch.ExpiresAt.Equal(env.clk.Now().Add(batchCfg.TTL)))

	second, err := env.svc.CreateBatches(ctx, env.request(b))
	require.NoError(t, err)
	assert.ElementsMatch(t, ids(first), ids(second), "the second rider sees the same open batches")
	assert.Equal(t, 2, env.scalar(t, `SELECT count(DISTINCT batch_id) FROM batch_stops WHERE order_id = ANY($1)`, env.orderIDs), "nothing new was formed")

	// A smaller request only sees batches that fit it.
	small := env.request(b)
	small.MaxOrders = 2
	fits, err := env.svc.CreateBatches(ctx, small)
	require.NoError(t, err)
	require.Len(t, fits, 1)
	assert.Len(t, fits[0].Batch.Orders, 2, "only the batch of 2 fits a max_orders of 2")

	// Repeating a request does not duplicate offers or inflate offers_count.
	for range 2 {
		_, err = env.svc.CreateBatches(ctx, env.request(b))
		require.NoError(t, err)
	}
	assert.Equal(t, 2, env.scalar(t, `SELECT count(*) FROM batch_offers WHERE rider_id = $1`, b))
	assert.Equal(t, 2, env.scalar(t, `SELECT offers_count FROM riders WHERE id = $1`, b))
	assert.Equal(t, 2, env.scalar(t, `SELECT offers_count FROM riders WHERE id = $1`, a))
	assert.Equal(t, 1, env.scalar(t, `SELECT count(*) FROM batches WHERE id = $1 AND rider_id IS NULL AND status = 'open'`, first[0].Batch.ID),
		"offered batches are assigned to nobody")
}

// After the TTL the batch expires on the next request, its orders go back to pending and get re-batched.
func TestIntegration_ExpiredBatchReleasesOrdersAndRebatches(t *testing.T) {
	for _, tc := range []struct {
		name    string
		advance time.Duration
	}{
		{"one minute past the TTL", batchCfg.TTL + time.Minute},
		{"exactly at the TTL (expires_at <= now)", batchCfg.TTL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newBatchEnv(t, 7)
			rider := env.newRider(t)
			ctx := context.Background()

			first, err := env.svc.CreateBatches(ctx, env.request(rider))
			require.NoError(t, err)
			require.Len(t, first, 2)
			oldIDs := ids(first)

			// Halfway through the TTL nothing changes.
			env.clk.Advance(batchCfg.TTL / 2)
			mid, err := env.svc.CreateBatches(ctx, env.request(rider))
			require.NoError(t, err)
			assert.ElementsMatch(t, oldIDs, ids(mid), "still open and unchanged")

			env.clk.Advance(tc.advance - batchCfg.TTL/2)
			again, err := env.svc.CreateBatches(ctx, env.request(rider))
			require.NoError(t, err)

			for _, id := range oldIDs {
				assert.Equal(t, 1, env.scalar(t, `SELECT count(*) FROM batches WHERE id = $1 AND status = 'expired'`, id), "%s expired", id)
			}
			require.Len(t, again, 2, "the released orders are re-batched")
			assert.Len(t, again[0].Batch.Orders, 5)
			for _, id := range ids(again) {
				assert.NotContains(t, oldIDs, id, "fresh batches, not the expired ones")
			}
			for orderID, batchID := range env.batchOf(t) {
				assert.Contains(t, ids(again), batchID, "order %s is in a new batch", orderID)
			}
			assert.Equal(t, 7, env.scalar(t, `SELECT count(*) FROM orders WHERE id = ANY($1) AND status = 'batched' AND batch_seq IS NOT NULL`, env.orderIDs))
			assert.True(t, again[0].Batch.ExpiresAt.Equal(env.clk.Now().Add(batchCfg.TTL)), "the new batches get a fresh TTL")
		})
	}
}

// Orders far from the rider are not batched for them, and batches far away are not shown.
func TestIntegration_OnlyNearbyOrdersAndBatches(t *testing.T) {
	env := newBatchEnv(t, 3)
	rider := env.newRider(t)
	far := env.request(rider)
	far.RiderLocation = domain.Location{Lat: cluster.Lat + 0.2, Lng: cluster.Lng} // ~22 km away, beyond the 5 km search radius

	got, err := env.svc.CreateBatches(context.Background(), far)

	require.NoError(t, err)
	assert.Empty(t, got)
	for _, batchID := range env.batchOf(t) {
		assert.Empty(t, batchID, "far orders stay pending")
	}

	// A rider near the cluster does get them.
	near, err := env.svc.CreateBatches(context.Background(), env.request(rider))
	require.NoError(t, err)
	require.Len(t, near, 1)
	assert.Len(t, near[0].Batch.Orders, 3)

	// ...and a far rider asking later does not see that batch.
	again, err := env.svc.CreateBatches(context.Background(), far)
	require.NoError(t, err)
	assert.Empty(t, again)
}
