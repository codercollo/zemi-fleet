//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codercollo/zemi-fleet/internal/testdb"
)

// raw performs a request against the server root (not /api/v2), with the API key unless headers remove it.
func (e *env) raw(method, path string, headers ...string) (*http.Response, []byte) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.server.URL+path, nil)
	require.NoError(e.t, err)
	req.Header.Set("X-API-Key", testAPIKey)
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i+1] == "" {
			req.Header.Del(headers[i])
			continue
		}
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	require.NoError(e.t, err)
	return res, body
}

// scrape reads GET /debug/vars.
func (e *env) scrape() map[string]any {
	e.t.Helper()
	res, body := e.raw(http.MethodGet, "/debug/vars")
	require.Equal(e.t, http.StatusOK, res.StatusCode, string(body))
	var out map[string]any
	require.NoError(e.t, json.Unmarshal(body, &out), string(body))
	return out
}

// Scenario 9: the domain counters move with real traffic, only for what succeeded, and the pool statistics are real.
func TestE2E_MetricsFollowTheWorkflow(t *testing.T) {
	e := newEnv(t)
	a := testdb.AddRider(t, e.pool, testdb.RiderFixture{ID: "rider_a", Lat: f64(nearYayaLat), Lng: f64(nearYayaL), SeenAt: e.seenNow(), Offers: 10, Accepted: 9})
	b := testdb.AddRider(t, e.pool, testdb.RiderFixture{ID: "rider_b", Lat: f64(nearYayaLat), Lng: f64(nearYayaL), SeenAt: e.seenNow()})

	before := e.scrape()
	assert.EqualValues(t, 0, before["orders_created"])

	orderIDs := e.orders("ven_004", 3)
	e.post("/orders/match-riders", map[string]any{"order_id": orderIDs[0]})
	e.matchOrders(a, nearYayaLat, nearYayaL, 38, 20, 0)
	batches := e.requestBatches(a, nearYayaLat, nearYayaL, 5)
	require.Len(t, batches.list("batches"), 1)
	batchID := batches.list("batches")[0]["batch_id"].(string)
	require.Equal(t, http.StatusOK, e.accept(batchID, a).Status)
	require.Equal(t, http.StatusConflict, e.accept(batchID, b).Status)
	e.get("/riders/rider_nobody") // a 404

	after := e.scrape()
	assert.EqualValues(t, 3, after["orders_created"])
	assert.EqualValues(t, 1, after["batches_formed"])
	assert.EqualValues(t, 1, after["batches_accepted"])
	assert.EqualValues(t, 1, after["batch_accept_conflicts"], "the loser is counted as a conflict, not as an accept")
	assert.EqualValues(t, 0, after["batch_capacity_rejections"])
	assert.EqualValues(t, 1, after["cone_matches_served"])
	assert.EqualValues(t, 1, after["rider_matches_served"])

	byStatus := after["total_responses_sent_by_status"].(map[string]any)
	assert.EqualValues(t, 1, byStatus["409"])
	assert.EqualValues(t, 1, byStatus["404"])
	assert.EqualValues(t, 3, byStatus["201"])

	db := after["database"].(map[string]any)
	assert.Greater(t, db["acquire_count"], 0.0, "the real pgx pool reports its statistics")
	assert.Contains(t, db, "canceled_acquire_count")
	assert.Equal(t, "e2e", after["version"])
}

// Scenario 10: a browser on a trusted origin: the preflight needs no key, the real call needs one.
func TestE2E_CORSPreflightWithoutKey(t *testing.T) {
	e := newEnv(t, "CORS_TRUSTED_ORIGINS", "http://localhost:3000")

	res, _ := e.raw(http.MethodOptions, "/api/v2/orders", "X-API-Key", "", "Origin", "http://localhost:3000", "Access-Control-Request-Method", "POST")
	assert.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, "http://localhost:3000", res.Header.Get("Access-Control-Allow-Origin"))

	res, _ = e.raw(http.MethodOptions, "/api/v2/orders", "X-API-Key", "", "Origin", "http://evil.example", "Access-Control-Request-Method", "POST")
	assert.Equal(t, http.StatusUnauthorized, res.StatusCode)
	assert.Empty(t, res.Header.Get("Access-Control-Allow-Origin"))

	res, _ = e.raw(http.MethodGet, "/api/v2/riders/rider_nobody", "Origin", "http://localhost:3000")
	assert.Equal(t, http.StatusNotFound, res.StatusCode)
	assert.Equal(t, "http://localhost:3000", res.Header.Get("Access-Control-Allow-Origin"))
	assert.NotEmpty(t, res.Header.Get("X-Request-ID"))
}

// Scenario 11: the metrics route needs the key and can be switched off.
func TestE2E_MetricsProtectionAndSwitch(t *testing.T) {
	e := newEnv(t)
	res, _ := e.raw(http.MethodGet, "/debug/vars", "X-API-Key", "")
	assert.Equal(t, http.StatusUnauthorized, res.StatusCode)

	off := newEnv(t, "METRICS_ENABLED", "false")
	res, _ = off.raw(http.MethodGet, "/debug/vars")
	assert.Equal(t, http.StatusNotFound, res.StatusCode)
}
