//go:build integration

package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codercollo/zemi-fleet/internal/config"
	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/metrics"
	"github.com/codercollo/zemi-fleet/internal/repository"
)

// formBatches has a rider ask for batches over the 7-order cluster and returns the 5-order and 2-order batch.
func formBatches(t *testing.T, env *batchEnv) (five, two domain.Batch) {
	t.Helper()
	former := env.newRider(t)
	offers, err := env.svc.CreateBatches(context.Background(), env.request(former))
	require.NoError(t, err)
	require.Len(t, offers, 2)
	for _, o := range offers {
		switch len(o.Batch.Orders) {
		case 5:
			five = o.Batch
		case 2:
			two = o.Batch
		}
	}
	require.NotEmpty(t, five.ID)
	require.NotEmpty(t, two.ID)
	return five, two
}

type acceptResult struct {
	rider string
	batch domain.Batch
	err   error
}

// acceptConcurrently releases all accepts at once. calls[i] is (batchID, riderID).
func acceptConcurrently(env *batchEnv, calls [][2]string) []acceptResult {
	results := make([]acceptResult, len(calls))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, c := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			b, err := env.svc.Accept(context.Background(), c[0], c[1])
			results[i] = acceptResult{rider: c[1], batch: b, err: err}
		}()
	}
	close(start)
	wg.Wait()
	return results
}

func TestIntegration_TwentyRidersAcceptTheSameBatch_OneWinner(t *testing.T) {
	env := newBatchEnv(t, 7)
	five, _ := formBatches(t, env)
	var riders []string
	var calls [][2]string
	for range 20 {
		r := env.newRider(t)
		riders = append(riders, r)
		calls = append(calls, [2]string{five.ID, r})
	}

	results := acceptConcurrently(env, calls)

	var winners []string
	taken := 0
	for _, r := range results {
		switch {
		case r.err == nil:
			winners = append(winners, r.rider)
			assert.Equal(t, domain.BatchAccepted, r.batch.Status)
			require.NotNil(t, r.batch.RiderID)
			assert.Equal(t, r.rider, *r.batch.RiderID)
		case errors.Is(r.err, domain.ErrBatchTaken):
			taken++
		default:
			t.Errorf("rider %s: unexpected error %v", r.rider, r.err)
		}
	}
	require.Len(t, winners, 1, "exactly one 200")
	assert.Equal(t, 19, taken, "nineteen 409s")
	winner := winners[0]

	// The batch and its orders belong to the winner...
	assert.Equal(t, 1, env.scalar(t, `SELECT count(*) FROM batches WHERE id = $1 AND status = 'accepted' AND rider_id = $2 AND accepted_at IS NOT NULL AND version = 2`, five.ID, winner))
	assert.Equal(t, 5, env.scalar(t, `SELECT count(*) FROM orders WHERE batch_id = $1 AND status = 'assigned' AND rider_id = $2`, five.ID, winner))
	// ...and only the winner's rider row changed.
	for _, r := range riders {
		if r == winner {
			assert.Equal(t, 5, env.scalar(t, `SELECT current_orders_count FROM riders WHERE id = $1`, r))
			assert.Equal(t, 1, env.scalar(t, `SELECT accepted_count FROM riders WHERE id = $1`, r))
			assert.Equal(t, 1, env.scalar(t, `SELECT offers_count FROM riders WHERE id = $1`, r), "never offered, so the accept created the offer")
			assert.Equal(t, 2, env.scalar(t, `SELECT version FROM riders WHERE id = $1`, r), "state changed, version bumped")
			continue
		}
		assert.Equal(t, 0, env.scalar(t, `SELECT current_orders_count FROM riders WHERE id = $1`, r), r)
		assert.Equal(t, 0, env.scalar(t, `SELECT accepted_count FROM riders WHERE id = $1`, r), r)
		assert.Equal(t, 0, env.scalar(t, `SELECT offers_count FROM riders WHERE id = $1`, r), "losers leave no trace: %s", r)
		assert.Equal(t, 1, env.scalar(t, `SELECT version FROM riders WHERE id = $1`, r), r)
		assert.Equal(t, 0, env.scalar(t, `SELECT count(*) FROM batch_offers WHERE rider_id = $1`, r), r)
	}
	assert.Equal(t, 1, env.scalar(t, `SELECT count(*) FROM batch_offers WHERE batch_id = $1 AND accepted AND responded_at IS NOT NULL`, five.ID))
}

// One rider tries to take two batches (5 + 2 orders) at the same time with room for only 5: the rider
// row lock serialises them, so exactly one succeeds and the count never passes max_capacity.
func TestIntegration_OneRiderAcceptingTwoBatchesRespectsCapacity(t *testing.T) {
	env := newBatchEnv(t, 7)
	five, two := formBatches(t, env)
	rider := env.newRider(t)

	results := acceptConcurrently(env, [][2]string{{five.ID, rider}, {two.ID, rider}})

	ok, atCapacity := 0, 0
	for _, r := range results {
		switch {
		case r.err == nil:
			ok++
		case errors.Is(r.err, domain.ErrRiderAtCapacity):
			atCapacity++
		default:
			t.Errorf("unexpected error %v", r.err)
		}
	}
	assert.Equal(t, 1, ok, "exactly one 200")
	assert.Equal(t, 1, atCapacity, "exactly one 422")
	count := env.scalar(t, `SELECT current_orders_count FROM riders WHERE id = $1`, rider)
	assert.Contains(t, []int{5, 2}, count)
	assert.LessOrEqual(t, count, 5, "never above max_capacity")
	assert.Equal(t, count, env.scalar(t, `SELECT count(*) FROM orders WHERE rider_id = $1 AND status = 'assigned'`, rider), "the count matches the orders actually assigned")
	assert.Equal(t, 1, env.scalar(t, `SELECT count(*) FROM batches WHERE rider_id = $1`, rider))
}

func TestIntegration_RiderWithSpareRoomTakesBothSmallerBatches(t *testing.T) {
	env := newBatchEnv(t, 7)
	_, two := formBatches(t, env)
	rider := env.newRider(t)

	// 2 orders fit; a second batch of 2 would too, but there is only one: accept it, then retry (idempotent).
	first, err := env.svc.Accept(context.Background(), two.ID, rider)
	require.NoError(t, err)
	again, err := env.svc.Accept(context.Background(), two.ID, rider)
	require.NoError(t, err, "a retry by the holder is a success")

	assert.Equal(t, first.ID, again.ID)
	assert.Equal(t, 2, env.scalar(t, `SELECT current_orders_count FROM riders WHERE id = $1`, rider), "the retry did not count the orders twice")
	assert.Equal(t, 1, env.scalar(t, `SELECT accepted_count FROM riders WHERE id = $1`, rider))
}

func TestIntegration_AcceptErrors(t *testing.T) {
	env := newBatchEnv(t, 7)
	five, _ := formBatches(t, env)
	rider := env.newRider(t)
	ctx := context.Background()

	t.Run("unknown batch", func(t *testing.T) {
		_, err := env.svc.Accept(ctx, "batch_nope", rider)
		assert.ErrorIs(t, err, domain.ErrNotFound)
	})
	t.Run("unknown rider", func(t *testing.T) {
		_, err := env.svc.Accept(ctx, five.ID, "rider_nope")
		assert.ErrorIs(t, err, domain.ErrNotFound)
	})
	t.Run("off shift rider", func(t *testing.T) {
		off := env.newRider(t)
		_, err := env.pool.Exec(ctx, `UPDATE riders SET is_available = FALSE WHERE id = $1`, off)
		require.NoError(t, err)

		_, err = env.svc.Accept(ctx, five.ID, off)

		assert.ErrorIs(t, err, domain.ErrRiderUnavailable)
		assert.Equal(t, 0, env.scalar(t, `SELECT count(*) FROM batches WHERE id = $1 AND rider_id IS NOT NULL`, five.ID), "nothing was claimed")
	})
	t.Run("rider whose capacity is already used", func(t *testing.T) {
		busy := env.newRider(t)
		_, err := env.pool.Exec(ctx, `UPDATE riders SET current_orders_count = 1 WHERE id = $1`, busy)
		require.NoError(t, err)

		_, err = env.svc.Accept(ctx, five.ID, busy) // 1 + 5 > 5

		assert.ErrorIs(t, err, domain.ErrRiderAtCapacity)
		assert.Equal(t, 0, env.scalar(t, `SELECT count(*) FROM batches WHERE id = $1 AND rider_id IS NOT NULL`, five.ID))
	})
	t.Run("a different rider after it is taken", func(t *testing.T) {
		_, err := env.svc.Accept(ctx, five.ID, rider)
		require.NoError(t, err)
		other := env.newRider(t)

		_, err = env.svc.Accept(ctx, five.ID, other)

		assert.ErrorIs(t, err, domain.ErrBatchTaken)
		assert.Equal(t, 0, env.scalar(t, `SELECT current_orders_count FROM riders WHERE id = $1`, other))
	})
}

// Past expires_at a batch can no longer be claimed, even though nobody has expired it in the database yet.
func TestIntegration_CannotAcceptAfterExpiry(t *testing.T) {
	for _, tc := range []struct {
		name    string
		advance time.Duration
		wantErr bool
	}{
		{"one second before expiry still works", batchCfg.TTL - time.Second, false},
		{"exactly at expiry is too late (expires_at > now)", batchCfg.TTL, true},
		{"after expiry", batchCfg.TTL + time.Minute, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newBatchEnv(t, 7)
			five, _ := formBatches(t, env)
			rider := env.newRider(t)
			env.clk.Advance(tc.advance)

			_, err := env.svc.Accept(context.Background(), five.ID, rider)

			if tc.wantErr {
				assert.ErrorIs(t, err, domain.ErrBatchTaken)
				assert.Equal(t, 5, env.scalar(t, `SELECT count(*) FROM orders WHERE batch_id = $1 AND status = 'batched'`, five.ID), "its orders were not assigned")
				assert.Equal(t, 0, env.scalar(t, `SELECT current_orders_count FROM riders WHERE id = $1`, rider))
				return
			}
			require.NoError(t, err)
		})
	}
}

// Once accepted, a batch disappears from listings and from new batch requests, and its orders from match-orders.
func TestIntegration_AcceptedBatchIsGoneEverywhere(t *testing.T) {
	env := newBatchEnv(t, 7)
	ctx := context.Background()
	store := repository.NewStore(env.pool, 3*time.Second)
	matcher := NewMatchingService(store, env.clk, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		config.ConeConfig{RadiusKM: 1, HalfAngleDeg: 30, MinSpeedKMH: 3, MaxResults: 20})
	scout := env.newRider(t)

	// Before batching, match-orders offers the pending orders.
	before, err := matcher.MatchOrders(ctx, MatchOrdersInput{RiderID: scout, Position: cluster})
	require.NoError(t, err)
	require.Len(t, before.Matches, 7)

	five, two := formBatches(t, env)
	list := ListBatchesInput{Point: cluster, RadiusKM: 3, Page: 1, PageSize: 20}
	listed, err := env.svc.ListBatches(ctx, list)
	require.NoError(t, err)
	require.Equal(t, 2, listed.Total)

	winner := env.newRider(t)
	_, err = env.svc.Accept(ctx, five.ID, winner)
	require.NoError(t, err)

	// GET /batches
	after, err := env.svc.ListBatches(ctx, list)
	require.NoError(t, err)
	assert.Equal(t, 1, after.Total)
	assert.Equal(t, []string{two.ID}, ids(after.Offers))

	// POST /orders/batch for someone else
	other, err := env.svc.CreateBatches(ctx, env.request(env.newRider(t)))
	require.NoError(t, err)
	assert.Equal(t, []string{two.ID}, ids(other), "the accepted batch is not offered again, and nothing new forms")

	// match-orders
	matched, err := matcher.MatchOrders(ctx, MatchOrdersInput{RiderID: scout, Position: cluster})
	require.NoError(t, err)
	for _, m := range matched.Matches {
		for _, o := range five.Orders {
			assert.NotEqual(t, o.ID, m.OrderID, "an assigned order is not offered by match-orders")
		}
	}
	assert.Empty(t, matched.Matches, "the other batch's orders are batched, so they are gone from matching too")
}

// GET /batches is read-only and paginated: sorted nearest first, pages are stable, expiry only filters.
func TestIntegration_ListBatchesPagingAndReadOnly(t *testing.T) {
	env := newBatchEnv(t, 7)
	ctx := context.Background()
	five, two := formBatches(t, env)
	rider := env.newRider(t)
	offersBefore := env.scalar(t, `SELECT count(*) FROM batch_offers WHERE batch_id = ANY($1)`, []string{five.ID, two.ID})

	page1, err := env.svc.ListBatches(ctx, ListBatchesInput{Point: cluster, RadiusKM: 3, Page: 1, PageSize: 1})
	require.NoError(t, err)
	page2, err := env.svc.ListBatches(ctx, ListBatchesInput{Point: cluster, RadiusKM: 3, Page: 2, PageSize: 1})
	require.NoError(t, err)
	page3, err := env.svc.ListBatches(ctx, ListBatchesInput{Point: cluster, RadiusKM: 3, Page: 3, PageSize: 1})
	require.NoError(t, err)

	assert.Equal(t, 2, page1.Total)
	require.Len(t, page1.Offers, 1)
	require.Len(t, page2.Offers, 1)
	assert.NotEqual(t, page1.Offers[0].Batch.ID, page2.Offers[0].Batch.ID)
	assert.LessOrEqual(t, page1.Offers[0].DistanceToFirstPickupM, page2.Offers[0].DistanceToFirstPickupM, "nearest first")
	assert.Empty(t, page3.Offers, "past the last page")
	assert.Equal(t, 2, page3.Total)
	assert.ElementsMatch(t, []string{five.ID, two.ID}, []string{page1.Offers[0].Batch.ID, page2.Offers[0].Batch.ID})

	// A small radius around a point away from the cluster finds nothing.
	away, err := env.svc.ListBatches(ctx, ListBatchesInput{Point: domain.Location{Lat: cluster.Lat + 0.1, Lng: cluster.Lng}, RadiusKM: 3, Page: 1, PageSize: 20})
	require.NoError(t, err)
	assert.Zero(t, away.Total)

	// Expiry only filters: after the TTL nothing is listed, yet nothing was changed in the database.
	env.clk.Advance(batchCfg.TTL + time.Minute)
	expired, err := env.svc.ListBatches(ctx, ListBatchesInput{Point: cluster, RadiusKM: 3, Page: 1, PageSize: 20})
	require.NoError(t, err)
	assert.Zero(t, expired.Total)
	assert.Equal(t, 2, env.scalar(t, `SELECT count(*) FROM batches WHERE id = ANY($1) AND status = 'open'`, []string{five.ID, two.ID}), "GET /batches expires nothing")
	assert.Equal(t, 7, env.scalar(t, `SELECT count(*) FROM orders WHERE id = ANY($1) AND status = 'batched'`, env.orderIDs), "and releases nothing")
	assert.Equal(t, offersBefore, env.scalar(t, `SELECT count(*) FROM batch_offers WHERE batch_id = ANY($1)`, []string{five.ID, two.ID}), "and records no offers")
	assert.Equal(t, 0, env.scalar(t, `SELECT offers_count FROM riders WHERE id = $1`, rider))
}

// acceptance_rate = accepted / offered, from the offers a rider was shown and the batches they accepted.
func TestIntegration_AcceptanceRateReflectsOffersAndAccepts(t *testing.T) {
	env := newBatchEnv(t, 7)
	ctx := context.Background()
	five, two := formBatches(t, env)
	rate := func(rider string) float64 {
		var r float64
		require.NoError(t, env.pool.QueryRow(ctx, `SELECT acceptance_rate::float8 FROM riders WHERE id = $1`, rider).Scan(&r))
		return r
	}

	shown := env.newRider(t) // sees both batches (2 offers), accepts one
	_, err := env.svc.CreateBatches(ctx, env.request(shown))
	require.NoError(t, err)
	assert.Equal(t, 2, env.scalar(t, `SELECT offers_count FROM riders WHERE id = $1`, shown))
	assert.Equal(t, 0.0, rate(shown), "offered 2, accepted 0")

	_, err = env.svc.Accept(ctx, five.ID, shown)
	require.NoError(t, err)
	assert.Equal(t, 0.5, rate(shown), "offered 2, accepted 1")

	_, err = env.svc.Accept(ctx, five.ID, shown) // an idempotent retry changes nothing
	require.NoError(t, err)
	assert.Equal(t, 0.5, rate(shown))
	assert.Equal(t, 2, env.scalar(t, `SELECT offers_count FROM riders WHERE id = $1`, shown))

	unseen := env.newRider(t) // never shown anything, accepts directly: the accept records the offer
	_, err = env.svc.Accept(ctx, two.ID, unseen)
	require.NoError(t, err)
	assert.Equal(t, 1, env.scalar(t, `SELECT offers_count FROM riders WHERE id = $1`, unseen))
	assert.Equal(t, 1.0, rate(unseen), "offered 1 (by accepting), accepted 1")

	assert.Equal(t, 1, env.scalar(t, `SELECT count(*) FROM batch_offers WHERE rider_id = $1 AND batch_id = $2 AND accepted`, shown, five.ID))
	assert.Equal(t, 1, env.scalar(t, `SELECT count(*) FROM batch_offers WHERE rider_id = $1 AND batch_id = $2 AND accepted IS NULL`, shown, two.ID), "the other offer stays unanswered")
}

// The counters come from the repository's explicit results, so they are exact even though the clock is frozen: every
// batch below is created, and accepted, at one instant, which a timestamp comparison could not tell apart.
func TestIntegration_MetricsAreExactWithAFrozenClock(t *testing.T) {
	env := newBatchEnv(t, 7)
	counters := metrics.New()
	env.svc.WithMetrics(counters)
	read := func(name string) string { return counters.Vars()[name].String() }
	riderA, riderB := env.newRider(t), env.newRider(t)

	first, err := env.svc.CreateBatches(context.Background(), env.request(riderA))
	require.NoError(t, err)
	require.Len(t, first, 2)
	second, err := env.svc.CreateBatches(context.Background(), env.request(riderB)) // same instant: nothing new to form
	require.NoError(t, err)
	require.Len(t, second, 2, "rider B is offered the same two open batches")

	assert.Equal(t, "2", read("batches_formed"), "two formed once, not four")

	batchID := first[0].Batch.ID
	_, err = env.svc.Accept(context.Background(), batchID, riderA)
	require.NoError(t, err)
	_, err = env.svc.Accept(context.Background(), batchID, riderA) // the idempotent retry, same instant
	require.NoError(t, err)
	_, err = env.svc.Accept(context.Background(), batchID, riderB)
	require.ErrorIs(t, err, domain.ErrBatchTaken)

	assert.Equal(t, "1", read("batches_accepted"), "the retry is not a second win")
	assert.Equal(t, "1", read("batch_accept_conflicts"))
}
