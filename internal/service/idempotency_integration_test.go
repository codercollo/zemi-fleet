//go:build integration

package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codercollo/zemi-fleet/internal/clock"
	"github.com/codercollo/zemi-fleet/internal/repository"
)

// The claim, takeover, completion and cleanup SQL against the real database.
func TestIntegration_IdempotencyKeyLifecycle(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	clk := clock.NewTest(time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC))
	svc := NewIdempotencyService(repository.NewStore(pool, 3*time.Second), clk, idemTTL, idemTimeout)
	const endpoint = "/api/v2/orders"

	decide := func(key, hash string) Decision {
		t.Helper()
		d, _, err := svc.Begin(ctx, key, endpoint, hash)
		require.NoError(t, err)
		return d
	}

	// The first request owns the key; a second while it runs is told to wait.
	assert.Equal(t, Proceed, decide("k1", "h1"))
	assert.Equal(t, InProgress, decide("k1", "h1"))
	assert.Equal(t, Mismatch, decide("k1", "different"))

	// A claim abandoned past the processing timeout is taken over, once.
	clk.Advance(idemTimeout + time.Second)
	assert.Equal(t, Proceed, decide("k1", "h1"), "stale claim taken over")
	assert.Equal(t, InProgress, decide("k1", "h1"), "and now it is fresh again")

	// Completing stores the response; later requests replay it.
	require.NoError(t, svc.Complete(ctx, "k1", endpoint, Stored{Status: 201, Body: []byte(`{"order":{"id":"ord_1"}}`), Headers: map[string]string{"Location": "/api/v2/orders/ord_1"}}))
	d, stored, err := svc.Begin(ctx, "k1", endpoint, "h1")
	require.NoError(t, err)
	assert.Equal(t, Replay, d)
	assert.Equal(t, 201, stored.Status)
	assert.Equal(t, `{"order":{"id":"ord_1"}}`, string(stored.Body))
	assert.Equal(t, "/api/v2/orders/ord_1", stored.Headers["Location"])
	assert.Equal(t, Mismatch, decide("k1", "different"))

	// Completing twice cannot overwrite the first response.
	require.NoError(t, svc.Complete(ctx, "k1", endpoint, Stored{Status: 200, Body: []byte(`{}`)}))
	_, again, err := svc.Begin(ctx, "k1", endpoint, "h1")
	require.NoError(t, err)
	assert.Equal(t, 201, again.Status)

	// Release only frees an unfinished claim, never a stored response.
	require.NoError(t, svc.Release(ctx, "k1", endpoint))
	assert.Equal(t, Replay, decide("k1", "h1"))
	assert.Equal(t, Proceed, decide("k2", "h2"))
	require.NoError(t, svc.Release(ctx, "k2", endpoint))
	assert.Equal(t, Proceed, decide("k2", "h2"), "a released claim can be claimed again")

	// The same key on another endpoint is independent.
	d, _, err = svc.Begin(ctx, "k1", "/api/v2/orders/batch", "h1")
	require.NoError(t, err)
	assert.Equal(t, Proceed, d)

	// Past the TTL a key is ignored, and writing a new one deletes every expired key.
	clk.Advance(idemTTL + time.Minute)
	assert.Equal(t, Proceed, decide("k1", "brand-new-body"), "expired: this is a new request")
	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM idempotency_keys`).Scan(&n))
	assert.Equal(t, 1, n, "the expired rows are gone, only the fresh claim remains")
}

// Of many simultaneous requests with one key, exactly one gets to run.
func TestIntegration_IdempotencyClaimIsExclusive(t *testing.T) {
	pool := integrationPool(t)
	svc := NewIdempotencyService(repository.NewStore(pool, 3*time.Second), clock.NewTest(time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)), idemTTL, idemTimeout)
	const n = 20
	decisions := make([]Decision, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			d, _, err := svc.Begin(context.Background(), "race", "/e", "h")
			assert.NoError(t, err)
			decisions[i] = d
		}()
	}
	close(start)
	wg.Wait()

	proceeded := 0
	for _, d := range decisions {
		switch d {
		case Proceed:
			proceeded++
		case InProgress:
		default:
			t.Errorf("unexpected decision %v", d)
		}
	}
	assert.Equal(t, 1, proceeded, "exactly one request owns the key")
}
