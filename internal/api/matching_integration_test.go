//go:build integration

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type matchResponse struct {
	MatchingOrders []struct {
		OrderID        string `json:"order_id"`
		PickupLocation struct {
			Lat float64 `json:"lat"`
		} `json:"pickup_location"`
		DistanceKM float64  `json:"distance_from_rider_km"`
		Angle      *float64 `json:"angle_from_rider_degrees"`
	} `json:"matching_orders"`
	CanAccept     bool `json:"can_accept_more_orders"`
	MaxAdditional int  `json:"max_additional_orders"`
	DirectionUsed bool `json:"direction_filter_applied"`
}

func postMatch(t *testing.T, h http.Handler, riderID string, bearing, speed string) matchResponse {
	t.Helper()
	body := fmt.Sprintf(`{"rider_id":%q,"current_location":{"latitude":-1.2650,"longitude":36.8038},"bearing_degrees":%s,"speed_kmh":%s,"current_orders_count":0}`,
		riderID, bearing, speed)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authed(httptest.NewRequest(http.MethodPost, "/api/v2/riders/match-orders", strings.NewReader(body))))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out matchResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

const (
	saritLat    = -1.2635 // vendor ven_001 pickups
	westlandsLt = -1.2660 // vendor ven_002 pickups
)

// Rider standing between the Sarit and Westlands Bites pickup clusters (about 160 m from each),
// against the seeded data: heading towards one cluster excludes the other, speed 0 shows both.
func TestIntegration_MatchOrdersAgainstSeededClusters(t *testing.T) {
	h, riderID, pool := integrationEnvWith(t, true) // the development seed, loaded into the TEST database
	var pending int
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM orders WHERE status = 'pending' AND vendor_id IN ('ven_001','ven_002')`).Scan(&pending))
	require.Positive(t, pending, "the seed should provide pending orders at the Sarit and Westlands Bites vendors")

	towardsWestlands := postMatch(t, h, riderID, "150", "25")
	towardsSarit := postMatch(t, h, riderID, "330", "25")
	stationary := postMatch(t, h, riderID, "150", "0")

	require.NotEmpty(t, towardsWestlands.MatchingOrders)
	assert.True(t, towardsWestlands.DirectionUsed)
	for _, o := range towardsWestlands.MatchingOrders {
		assert.InDelta(t, westlandsLt, o.PickupLocation.Lat, 1e-9, "only the cluster ahead (Westlands Bites)")
		require.NotNil(t, o.Angle)
		assert.LessOrEqual(t, *o.Angle, 30.0)
	}
	require.NotEmpty(t, towardsSarit.MatchingOrders)
	for _, o := range towardsSarit.MatchingOrders {
		assert.InDelta(t, saritLat, o.PickupLocation.Lat, 1e-9, "turned around: the other cluster")
		assert.LessOrEqual(t, *o.Angle, 30.0)
	}
	assert.False(t, stationary.DirectionUsed, "speed 0: heading ignored")
	assert.Len(t, stationary.MatchingOrders, len(towardsWestlands.MatchingOrders)+len(towardsSarit.MatchingOrders), "full circle sees both clusters")
	for _, o := range stationary.MatchingOrders {
		assert.Nil(t, o.Angle)
	}
	assert.True(t, stationary.CanAccept)
	assert.Equal(t, 5, stationary.MaxAdditional)
}

// Telemetry from match-orders never fails with 409 and never bumps the rider's version, even
// under concurrency, so it cannot make the rider's own X-Expected-Version writes conflict.
func TestIntegration_MatchOrdersTelemetryNeverConflicts(t *testing.T) {
	h, riderID, pool := integrationEnv(t)

	statuses := fireConcurrently(t, h, concurrentRequests, func(i int) *http.Request {
		body := fmt.Sprintf(`{"rider_id":%q,"current_location":{"latitude":-1.26%02d,"longitude":36.8038},"speed_kmh":10,"current_orders_count":0}`, riderID, i)
		return authed(httptest.NewRequest(http.MethodPost, "/api/v2/riders/match-orders", strings.NewReader(body)))
	})

	assert.Equal(t, concurrentRequests, count(statuses, http.StatusOK), "%v", statuses)
	assert.Equal(t, 1, riderVersion(t, pool, riderID), "version untouched")
	var stored bool
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT last_lat IS NOT NULL AND last_bearing_deg IS NULL AND location_updated_at IS NOT NULL FROM riders WHERE id = $1`, riderID).Scan(&stored))
	assert.True(t, stored, "position stored, omitted bearing is NULL, timestamp set")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newPatch("/api/v2/riders/"+riderID+"/availability", `{"is_available":false}`, "1"))
	assert.Equal(t, http.StatusOK, rec.Code, "the rider's versioned PATCH still succeeds after all those pings")
}

// The candidate query must be able to use the partial index on pending pickups.
func TestIntegration_CandidateQueryUsesPendingPickupIndex(t *testing.T) {
	_, _, pool := integrationEnvWith(t, true) // real rows, so the planner has something to choose between
	ctx := context.Background()
	_, err := pool.Exec(ctx, `ANALYZE orders`)
	require.NoError(t, err)
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `SET LOCAL enable_seqscan = off`) // tiny seed tables would otherwise be scanned sequentially
	require.NoError(t, err)

	rows, err := tx.Query(ctx, `EXPLAIN SELECT id, pickup_lat, pickup_lng, created_at, rider_earnings_cents
		FROM orders WHERE status = 'pending'
		AND pickup_lat BETWEEN -1.27 AND -1.26 AND pickup_lng BETWEEN 36.79 AND 36.81 ORDER BY created_at, id`)
	require.NoError(t, err)
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		plan.WriteString(line + "\n")
	}
	assert.Contains(t, plan.String(), "idx_orders_pending_pickup", plan.String())
}
