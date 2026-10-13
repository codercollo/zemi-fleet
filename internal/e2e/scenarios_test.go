//go:build e2e

package e2e

import (
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codercollo/zemi-fleet/internal/testdb"
)

// Scenario 1: one order's whole life across every feature, from creation to a rider owning it.
func TestE2E_OrderLifecycle(t *testing.T) {
	e := newEnv(t)
	// A prefers electronics in Kilimani and is nearby with a good record; B has no preferences and is farther; C only takes food.
	a := testdb.AddRider(t, e.pool, testdb.RiderFixture{ID: "rider_a", Lat: f64(nearYayaLat), Lng: f64(nearYayaL), SeenAt: e.seenNow(),
		Offers: 10, Accepted: 9, Categories: []string{"electronics"}, AreaIDs: []string{"area_001"}})
	b := testdb.AddRider(t, e.pool, testdb.RiderFixture{ID: "rider_b", Lat: f64(-1.2780), Lng: f64(36.8000), SeenAt: e.seenNow(), Offers: 10, Accepted: 5})
	c := testdb.AddRider(t, e.pool, testdb.RiderFixture{ID: "rider_c", Lat: f64(nearYayaLat), Lng: f64(nearYayaL), SeenAt: e.seenNow(), Categories: []string{"food"}})

	// POST /orders x3 at one vendor, all heading the same way.
	orderIDs := e.orders("ven_004", 3)
	require.Len(t, orderIDs, 3)

	// match-riders ranks the fixture rider first, and the food-only rider is not offered an electronics order.
	ranked := e.post("/orders/match-riders", map[string]any{"order_id": orderIDs[0]})
	require.Equal(t, http.StatusOK, ranked.Status, "%v", ranked.Body)
	riders := strs(ranked.list("matching_riders"), "rider_id")
	require.NotEmpty(t, riders)
	assert.Equal(t, a, riders[0], "the rider who prefers electronics in Kilimani and is close ranks first")
	assert.Contains(t, riders, b)
	assert.NotContains(t, riders, c, "preferences are hard filters")
	assert.Equal(t, []any{"electronics", "Kilimani"}, ranked.list("matching_riders")[0]["matching_preferences"])

	// match-orders for that rider, facing the vendor, shows all 3.
	seen := e.matchOrders(a, nearYayaLat, nearYayaL, 38, 20, 0)
	require.Equal(t, http.StatusOK, seen.Status, "%v", seen.Body)
	assert.ElementsMatch(t, orderIDs, strs(seen.list("matching_orders"), "order_id"))
	assert.Equal(t, true, seen.Body["direction_filter_applied"])
	assert.Equal(t, true, seen.Body["can_accept_more_orders"])

	// POST /orders/batch puts them in one batch.
	batches := e.requestBatches(a, nearYayaLat, nearYayaL, 5)
	require.Equal(t, http.StatusOK, batches.Status, "%v", batches.Body)
	require.Len(t, batches.list("batches"), 1)
	batch := batches.list("batches")[0]
	assert.ElementsMatch(t, orderIDs, batchOrderIDs(batch))
	batchID := batch["batch_id"].(string)

	// match-orders now returns [] for them: they are no longer pending.
	gone := e.matchOrders(a, nearYayaLat, nearYayaL, 38, 20, 0)
	assert.Empty(t, gone.list("matching_orders"))

	// GET /batches lists the batch.
	listed := e.get("/batches?lat=-1.2917&lng=36.7878")
	require.Equal(t, http.StatusOK, listed.Status)
	assert.Equal(t, []string{batchID}, strs(listed.list("batches"), "batch_id"))
	assert.EqualValues(t, 1, listed.object("metadata")["total_records"])

	// Rider A accepts: 200. Rider B, a moment later: 409.
	won := e.accept(batchID, a)
	require.Equal(t, http.StatusOK, won.Status, "%v", won.Body)
	assert.Equal(t, "accepted", won.object("batch")["status"])
	assert.Equal(t, a, won.object("batch")["rider_id"])
	lost := e.accept(batchID, b)
	assert.Equal(t, http.StatusConflict, lost.Status)

	// GET /riders/A shows +3 orders, and the batch is gone from GET /batches.
	rider := e.get("/riders/" + a)
	assert.EqualValues(t, 3, rider.object("rider")["current_orders_count"])
	assert.EqualValues(t, 2, rider.object("rider")["max_additional_orders"])
	after := e.get("/batches?lat=-1.2917&lng=36.7878")
	assert.Empty(t, after.list("batches"))
	assert.EqualValues(t, 0, e.get("/riders/" + b).object("rider")["current_orders_count"], "the loser gained nothing")
}

// Scenario 2: capacity is enforced consistently by every feature once a rider is full.
func TestE2E_CapacityAcrossFeatures(t *testing.T) {
	e := newEnv(t)
	x := testdb.AddRider(t, e.pool, testdb.RiderFixture{ID: "rider_x", Lat: f64(nearYayaLat), Lng: f64(nearYayaL), SeenAt: e.seenNow()})
	y := testdb.AddRider(t, e.pool, testdb.RiderFixture{ID: "rider_y", Lat: f64(nearYayaLat), Lng: f64(nearYayaL), SeenAt: e.seenNow()})

	// Get X to 3/5 through the API.
	e.orders("ven_004", 3)
	first := e.requestBatches(x, nearYayaLat, nearYayaL, 5)
	require.Len(t, first.list("batches"), 1)
	require.Equal(t, http.StatusOK, e.accept(first.list("batches")[0]["batch_id"].(string), x).Status)
	require.EqualValues(t, 3, e.get("/riders/" + x).object("rider")["current_orders_count"])

	// With 2 slots left, batches are formed with at most 2 orders.
	e.orders("ven_004", 5)
	second := e.requestBatches(x, nearYayaLat, nearYayaL, 5)
	require.Equal(t, http.StatusOK, second.Status)
	var twoBatchID, otherBatchID string
	total := 0
	for _, b := range second.list("batches") {
		n := len(batchOrderIDs(b))
		total += n
		assert.LessOrEqual(t, n, 2, "effective size is min(max_orders, spare capacity)")
		switch {
		case n == 2 && twoBatchID == "":
			twoBatchID = b["batch_id"].(string)
		default:
			otherBatchID = b["batch_id"].(string)
		}
	}
	assert.Equal(t, 5, total, "all five new orders were batched")
	require.NotEmpty(t, twoBatchID)
	require.NotEmpty(t, otherBatchID)

	// Accepting a batch of 2 brings X to 5/5.
	require.Equal(t, http.StatusOK, e.accept(twoBatchID, x).Status)
	assert.EqualValues(t, 5, e.get("/riders/" + x).object("rider")["current_orders_count"])

	// Now every feature agrees X is full.
	m := e.matchOrders(x, nearYayaLat, nearYayaL, 38, 20, 5)
	assert.Equal(t, false, m.Body["can_accept_more_orders"])
	assert.EqualValues(t, 0, m.Body["max_additional_orders"])
	assert.Empty(t, m.list("matching_orders"))

	again := e.requestBatches(x, nearYayaLat, nearYayaL, 5)
	require.Equal(t, http.StatusOK, again.Status)
	assert.Empty(t, again.list("batches"))

	ranked := e.post("/orders/match-riders", map[string]any{"order_details": map[string]any{
		"category":          "electronics",
		"pickup_location":   map[string]float64{"lat": yayaLat, "lng": yayaLng},
		"delivery_location": map[string]float64{"lat": -1.2700, "lng": 36.8100},
		"value_kes":         5000,
	}})
	require.Equal(t, http.StatusOK, ranked.Status)
	ids := strs(ranked.list("matching_riders"), "rider_id")
	assert.NotContains(t, ids, x, "a full rider is not offered new orders")
	assert.Contains(t, ids, y)

	// Accepting any other batch is refused.
	refused := e.accept(otherBatchID, x)
	assert.Equal(t, http.StatusUnprocessableEntity, refused.Status, "%v", refused.Body)
	assert.EqualValues(t, 5, e.get("/riders/" + x).object("rider")["current_orders_count"])
	assert.Equal(t, http.StatusOK, e.accept(otherBatchID, y).Status, "another rider can still take it")
}

// Scenario 3: a batch nobody takes in time expires, its orders come back, and the old batch cannot be accepted.
func TestE2E_ExpiryAndRelease(t *testing.T) {
	e := newEnv(t)
	a := testdb.AddRider(t, e.pool, testdb.RiderFixture{ID: "rider_a", Lat: f64(nearYayaLat), Lng: f64(nearYayaL), SeenAt: e.seenNow()})
	orderIDs := e.orders("ven_004", 3)

	formed := e.requestBatches(a, nearYayaLat, nearYayaL, 5)
	require.Len(t, formed.list("batches"), 1)
	oldBatch := formed.list("batches")[0]["batch_id"].(string)
	assert.ElementsMatch(t, orderIDs, batchOrderIDs(formed.list("batches")[0]))
	assert.Empty(t, e.matchOrders(a, nearYayaLat, nearYayaL, 38, 20, 0).list("matching_orders"), "batched orders are not offered")

	// Time passes beyond BATCH_TTL; nobody accepted.
	e.clk.Advance(e.cfg.Batch.TTL + time.Minute)

	// match-orders sees the released orders again, before anyone has re-batched them.
	back := e.matchOrders(a, nearYayaLat, nearYayaL, 38, 20, 0)
	assert.ElementsMatch(t, orderIDs, strs(back.list("matching_orders"), "order_id"), "expired batches release their orders")

	// The expired batch cannot be accepted.
	assert.Equal(t, http.StatusConflict, e.accept(oldBatch, a).Status)

	// The next batch request re-batches the released orders into a new batch.
	again := e.requestBatches(a, nearYayaLat, nearYayaL, 5)
	require.Len(t, again.list("batches"), 1)
	newBatch := again.list("batches")[0]
	assert.NotEqual(t, oldBatch, newBatch["batch_id"])
	assert.ElementsMatch(t, orderIDs, batchOrderIDs(newBatch))

	assert.Equal(t, 1, scalar(t, e.pool, `SELECT count(*) FROM batches WHERE id = $1 AND status = 'expired'`, oldBatch))
	assert.Equal(t, http.StatusOK, e.accept(newBatch["batch_id"].(string), a).Status, "the new batch can be taken")
}

// Scenario 4: five riders hammer the API at once; no batch is ever won twice and nothing is double-booked.
func TestE2E_ConcurrentLoad(t *testing.T) {
	e := newEnv(t)
	riders := []string{"rider_1", "rider_2", "rider_3", "rider_4", "rider_5"}
	for _, id := range riders {
		testdb.AddRider(t, e.pool, testdb.RiderFixture{ID: id, Lat: f64(nearYayaLat), Lng: f64(nearYayaL), SeenAt: e.seenNow()})
	}
	e.orders("ven_004", 7)
	e.orders("ven_005", 5)

	// 1. All five ask for batches at the same moment.
	var wg sync.WaitGroup
	start := make(chan struct{})
	statuses := make([]int, len(riders))
	for i, id := range riders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			statuses[i] = e.requestBatches(id, nearYayaLat, nearYayaL, 5).Status
		}()
	}
	close(start)
	wg.Wait()
	for i, s := range statuses {
		assert.Equal(t, http.StatusOK, s, "rider %s", riders[i])
	}

	// 2. Whatever was formed is visible to everyone.
	visible := e.get("/batches?lat=-1.2917&lng=36.7878&page_size=50")
	require.Equal(t, http.StatusOK, visible.Status)
	batchIDs := strs(visible.list("batches"), "batch_id")
	require.NotEmpty(t, batchIDs)
	seenOrders := map[string]string{}
	for _, b := range visible.list("batches") {
		for _, oid := range batchOrderIDs(b) {
			prev, dup := seenOrders[oid]
			assert.False(t, dup, "order %s appears in batches %s and %s", oid, prev, b["batch_id"])
			seenOrders[oid] = b["batch_id"].(string)
		}
	}
	assert.Len(t, seenOrders, 12, "every order is in exactly one batch")

	// 3. Everyone races to accept every visible batch.
	type result struct {
		batch, rider string
		status       int
	}
	var mu sync.Mutex
	var results []result
	start = make(chan struct{})
	for _, id := range riders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for _, b := range batchIDs {
				res := e.accept(b, id)
				mu.Lock()
				results = append(results, result{b, id, res.Status})
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	winners := map[string][]string{}
	for _, r := range results {
		assert.Contains(t, []int{http.StatusOK, http.StatusConflict, http.StatusUnprocessableEntity}, r.status, "batch %s rider %s", r.batch, r.rider)
		if r.status == http.StatusOK {
			winners[r.batch] = append(winners[r.batch], r.rider)
		}
	}
	for batch, who := range winners {
		assert.Len(t, who, 1, "batch %s was accepted by %v", batch, who)
	}
	assert.NotEmpty(t, winners)
	assert.Len(t, results, len(riders)*len(batchIDs))

	// Each rider's count equals what they actually won, and every winning batch is theirs in the database.
	won := map[string]int{}
	for batch, who := range winners {
		won[who[0]] += len(batchOrderIDs(batchByID(visible, batch)))
	}
	for _, id := range riders {
		got := e.get("/riders/" + id).object("rider")["current_orders_count"]
		assert.EqualValues(t, won[id], got, "rider %s", id)
		assert.LessOrEqual(t, won[id], 5)
	}
	remaining := e.get("/batches?lat=-1.2917&lng=36.7878&page_size=50")
	for _, b := range strs(remaining.list("batches"), "batch_id") {
		assert.NotContains(t, slices.Collect(mapsKeys(winners)), b, "an accepted batch is no longer listed")
	}
}

func batchByID(res response, id string) map[string]any {
	for _, b := range res.list("batches") {
		if b["batch_id"] == id {
			return b
		}
	}
	return nil
}

func mapsKeys[K comparable, V any](m map[K]V) func(yield func(K) bool) {
	return func(yield func(K) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// Scenario 5: a rider's location counts for matching only while it is fresh.
func TestE2E_LocationFreshness(t *testing.T) {
	e := newEnv(t)
	rider := testdb.AddRider(t, e.pool, testdb.RiderFixture{ID: "rider_f"}) // never reported a location
	order := e.orders("ven_004", 1)[0]
	stale := e.cfg.Match.LocationStaleAfter

	scoreAndDistance := func() (float64, any) {
		t.Helper()
		res := e.post("/orders/match-riders", map[string]any{"order_id": order})
		require.Equal(t, http.StatusOK, res.Status, "%v", res.Body)
		list := res.list("matching_riders")
		require.Len(t, list, 1)
		require.Equal(t, rider, list[0]["rider_id"])
		return list[0]["match_score"].(float64), list[0]["distance_to_pickup_km"]
	}

	// No location yet: unknown distance, no proximity.
	neutralScore, distance := scoreAndDistance()
	assert.Nil(t, distance)
	assert.InDelta(t, 0.325, neutralScore, 0.0051, "newcomer: 0.25*0.5 + 0.20*0.5 + 0 + 0.20*0.5")

	// A ping makes the distance known and adds proximity to the score.
	ping := e.patch("/riders/"+rider+"/location", map[string]any{"latitude": nearYayaLat, "longitude": nearYayaL})
	require.Equal(t, http.StatusOK, ping.Status, "%v", ping.Body)
	freshScore, distance := scoreAndDistance()
	require.NotNil(t, distance, "a fresh ping gives a distance")
	assert.InDelta(t, 0.35, distance, 0.1, "about 300 m from the pickup")
	assert.Greater(t, freshScore, neutralScore)
	assert.InDelta(t, neutralScore+0.35*(1-distance.(float64)/e.cfg.Match.MaxDistanceKM), freshScore, 0.0101)

	// Past RIDER_LOCATION_STALE_AFTER the location is unknown again: no distance, proximity back to 0.
	e.clk.Advance(stale + time.Minute)
	staleScore, distance := scoreAndDistance()
	assert.Nil(t, distance, "an old ping is unknown, not a distance")
	assert.Equal(t, neutralScore, staleScore)

	// Pinging again restores it.
	require.Equal(t, http.StatusOK, e.patch("/riders/"+rider+"/location", map[string]any{"latitude": nearYayaLat, "longitude": nearYayaL}).Status)
	againScore, distance := scoreAndDistance()
	require.NotNil(t, distance)
	assert.Equal(t, freshScore, againScore)
}
