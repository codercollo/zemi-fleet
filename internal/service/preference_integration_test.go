//go:build integration

package service

import (
	"context"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codercollo/zemi-fleet/internal/clock"
	"github.com/codercollo/zemi-fleet/internal/config"
	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/repository"
)

// Rider preference matching against the real database: the candidate and preference queries, the
// areas join, and the filters end to end. It uses throwaway riders (and filters the result down to them),
// so the seeded riders only add noise at the bottom of a large result cap.
func TestIntegration_MatchRidersFiltersAndRanksFromTheDatabase(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	var areas int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM areas WHERE id = 'area_001'`).Scan(&areas))
	if areas == 0 {
		t.Skip("seeded areas not found (run make dev/db/seed)")
	}

	now := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	clk := clock.NewTest(now)
	svc := NewPreferenceService(repository.NewStore(pool, 3*time.Second), clk, config.MatchConfig{
		MaxDistanceKM: 10, LocationStaleAfter: 30 * time.Minute,
		WeightCategory: 0.25, WeightArea: 0.20, WeightProximity: 0.35, WeightAcceptance: 0.20, MaxResults: 100,
	}, 50)

	token := fmt.Sprintf("%08x", rand.Uint32())
	var riderIDs []string
	add := func(name string, available bool, current int, minCents int64, lat, lng *float64, seen *time.Time, offers, accepted int, cats, areaIDs []string) string {
		id := fmt.Sprintf("rider_it_%s_%s", token, name)
		riderIDs = append(riderIDs, id)
		_, err := pool.Exec(ctx, `INSERT INTO riders (id, full_name, phone, is_available, current_orders_count, last_lat, last_lng,
				location_updated_at, offers_count, accepted_count) VALUES ($1, 'Match Test', $2, $3, $4, $5, $6, $7, $8, $9)`,
			id, fmt.Sprintf("+254%09d", rand.IntN(1_000_000_000)), available, current, lat, lng, seen, offers, accepted)
		require.NoError(t, err, name)
		_, err = pool.Exec(ctx, `INSERT INTO rider_preferences (rider_id, min_order_value_cents) VALUES ($1, $2)`, id, minCents)
		require.NoError(t, err)
		for _, c := range cats {
			_, err = pool.Exec(ctx, `INSERT INTO rider_preferred_categories (rider_id, category_code) VALUES ($1, $2)`, id, c)
			require.NoError(t, err)
		}
		for _, a := range areaIDs {
			_, err = pool.Exec(ctx, `INSERT INTO rider_preferred_areas (rider_id, area_id) VALUES ($1, $2)`, id, a)
			require.NoError(t, err)
		}
		return id
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM riders WHERE id = ANY($1)`, riderIDs) })

	f := func(v float64) *float64 { return &v }
	fresh, stale := now.Add(-2*time.Minute), now.Add(-2*time.Hour)
	// The order: electronics worth KES 5,000, picked up at Yaya Centre in Kilimani.
	nearLat, nearLng := yaya.Lat+0.0045, yaya.Lng // about 500 m

	best := add("best", true, 1, 0, f(nearLat), f(nearLng), &fresh, 10, 8, []string{"electronics"}, []string{"area_001"})
	plain := add("plain", true, 0, 0, nil, nil, nil, 0, 0, nil, nil)
	staleFar := add("stale_far", true, 0, 0, f(yaya.Lat+0.45), f(yaya.Lng), &stale, 10, 5, nil, nil) // 50 km away but a 2 h old ping
	exactMin := add("exact_min", true, 0, 500000, nil, nil, nil, 10, 5, nil, nil)
	foodOnly := add("food_only", true, 0, 0, nil, nil, nil, 10, 5, []string{"food"}, nil)
	wrongArea := add("wrong_area", true, 0, 0, nil, nil, nil, 10, 5, nil, []string{"area_003"}) // CBD
	pricey := add("pricey", true, 0, 500100, nil, nil, nil, 10, 5, nil, nil)                    // wants 1 KES more than the order is worth
	full := add("full", true, 5, 0, nil, nil, nil, 10, 5, nil, nil)
	off := add("off", false, 0, 0, nil, nil, nil, 10, 5, nil, nil)
	tooFar := add("too_far", true, 0, 0, f(yaya.Lat+0.12), f(yaya.Lng), &fresh, 10, 5, nil, nil) // fresh ping, 13 km away

	got, err := svc.MatchRiders(ctx, MatchRidersInput{Details: &OrderDetails{
		Category: "electronics", Pickup: yaya, Delivery: hurlingham, ValueCents: 500000,
	}})
	require.NoError(t, err)

	var ours []string
	byID := map[string]float64{}
	for _, m := range got {
		for _, id := range riderIDs {
			if m.RiderID == id {
				ours = append(ours, id)
				byID[id] = m.Score
			}
		}
	}

	for _, excluded := range []string{foodOnly, wrongArea, pricey, full, off, tooFar} {
		assert.NotContains(t, ours, excluded, "%s should have been filtered out", excluded)
	}
	assert.Contains(t, ours, plain, "no preferences: matches")
	assert.Contains(t, ours, exactMin, "a value exactly equal to the minimum is included")
	assert.Contains(t, ours, staleFar, "a stale location is unknown, not a reason to exclude")
	require.Contains(t, ours, best)
	assert.Equal(t, best, ours[0], "explicit category and area, close, good acceptance: ranks first")

	var bestMatch *struct{ prefs []string }
	for _, m := range got {
		if m.RiderID == best {
			bestMatch = &struct{ prefs []string }{m.MatchingPreferences}
			require.NotNil(t, m.DistanceM)
			assert.InDelta(t, 500, *m.DistanceM, 5)
			assert.Equal(t, 1, m.CurrentOrders)
			assert.InDelta(t, 0.8, m.AcceptanceRate, 1e-9)
		}
		if m.RiderID == staleFar {
			assert.Nil(t, m.DistanceM)
		}
	}
	require.NotNil(t, bestMatch)
	assert.Equal(t, []string{"electronics", "Kilimani"}, bestMatch.prefs, "category code, then area name")
	// 0.25*1 + 0.20*1 + 0.35*(1-0.5/10) + 0.20*0.8 = 0.9425
	assert.Equal(t, 0.94, byID[best])
	assert.Equal(t, 0.23, byID[plain], "0.25*0.5 + 0.20*0.5 + 0 + 0 = 0.225")

	// Move the clock past the freshness window: best's location becomes unknown but it still matches.
	clk.Advance(time.Hour)
	later, err := svc.MatchRiders(ctx, MatchRidersInput{Details: &OrderDetails{Category: "electronics", Pickup: yaya, Delivery: hurlingham, ValueCents: 500000}})
	require.NoError(t, err)
	var bestLater, tooFarLater bool
	for _, m := range later {
		if m.RiderID == best {
			bestLater = true
			assert.Nil(t, m.DistanceM)
		}
		if m.RiderID == tooFar {
			tooFarLater = true // its ping is now stale too, so it is no longer filtered by distance
		}
	}
	assert.True(t, bestLater)
	assert.True(t, tooFarLater, "once its ping is stale, the far rider is unknown rather than excluded")

	// A stored order is matched by ID too, and an unknown ID is not found.
	_, err = svc.MatchRiders(ctx, MatchRidersInput{OrderID: "ord_does_not_exist"})
	assert.ErrorIs(t, err, domain.ErrNotFound)
}
