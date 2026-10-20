//go:build integration

package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codercollo/zemi-fleet/internal/config"
	"github.com/codercollo/zemi-fleet/internal/testdb"
)

func TestIntegration_ConcurrentExpirySweepsReleaseEachBatchOnce(t *testing.T) {
	pool, err := OpenPool(context.Background(), config.DBConfig{DSN: testdb.URL(t), MaxConns: 5, MaxConnIdleTime: time.Minute})
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	const first, second = "batch_expiry_concurrent_a", "batch_expiry_concurrent_b"
	_, err = pool.Exec(context.Background(), `DELETE FROM batches WHERE id IN ($1, $2)`, first, second)
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(), `
		INSERT INTO batches (id, pickup_centroid_lat, pickup_centroid_lng, pickup_spread_m, orders_count,
			total_distance_m, estimated_duration_s, total_delivery_fee_cents, estimated_earnings_cents,
			route_optimization_score, expires_at, created_at)
		VALUES ($1, -1.29, 36.78, 0, 1, 1000, 60, 100, 90, 1, now() - interval '1 minute', now()),
		       ($2, -1.29, 36.78, 0, 1, 1000, 60, 100, 90, 1, now() - interval '1 minute', now())`, first, second)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM batches WHERE id IN ($1, $2)`, first, second)
	})

	storeA, storeB := NewStore(pool, 5*time.Second), NewStore(pool, 5*time.Second)
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan int, 2)
	errs := make(chan error, 2)
	var now time.Time
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT now()`).Scan(&now))
	for _, store := range []*SQLStore{storeA, storeB} {
		wg.Add(1)
		go func(s *SQLStore) {
			defer wg.Done()
			<-start
			n, sweepErr := s.ExpireBatchesTx(context.Background(), now)
			errs <- sweepErr
			results <- n
		}(store)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	total := 0
	for n := range results {
		total += n
	}
	assert.Equal(t, 2, total, "SKIP LOCKED lets the concurrent workers expire each batch exactly once")
	var expired int64
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM batches WHERE id IN ($1, $2) AND status = 'expired'`, first, second).Scan(&expired))
	assert.Equal(t, int64(2), expired)
}
