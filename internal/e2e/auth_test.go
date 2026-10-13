//go:build e2e

package e2e

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codercollo/zemi-fleet/internal/testdb"
)

// Scenario 9: a request without a valid API key is rejected before it reaches any service, so the database is
// exactly as it was, whichever write route it aimed at.
func TestE2E_UnauthenticatedRequestsChangeNothing(t *testing.T) {
	e := newEnv(t)
	rider := testdb.AddRider(t, e.pool, testdb.RiderFixture{ID: "rider_auth", Lat: f64(nearYayaLat), Lng: f64(nearYayaL), SeenAt: e.seenNow()})
	e.orders("ven_004", 2) // some real state, created with the valid key

	// Everything a write could touch: rows, idempotency records, and the rider's version.
	snapshot := func() []int {
		return []int{
			scalar(t, e.pool, `SELECT count(*) FROM orders`),
			scalar(t, e.pool, `SELECT count(*) FROM batches`),
			scalar(t, e.pool, `SELECT count(*) FROM batch_offers`),
			scalar(t, e.pool, `SELECT count(*) FROM idempotency_keys`),
			scalar(t, e.pool, `SELECT sum(version) FROM riders`),
			scalar(t, e.pool, `SELECT count(*) FROM orders WHERE status <> 'pending'`),
		}
	}
	before := snapshot()

	attempts := []struct {
		name, method, path string
		body               any
		headers            []string
	}{
		{"create order", http.MethodPost, "/orders", orderBody(5000), []string{"X-API-Key", "", "Idempotency-Key", "no-key-1"}},
		{"create batches", http.MethodPost, "/orders/batch", map[string]any{
			"rider_id": rider, "rider_location": loc(nearYayaLat, nearYayaL), "max_orders": 5}, []string{"X-API-Key", ""}},
		{"accept batch", http.MethodPost, "/batches/batch_nope/accept", map[string]any{"rider_id": rider}, []string{"X-API-Key", ""}},
		{"update location", http.MethodPatch, "/riders/" + rider + "/location", map[string]any{"latitude": -1.28, "longitude": 36.8}, []string{"X-API-Key", ""}},
		{"update availability", http.MethodPatch, "/riders/" + rider + "/availability", map[string]any{"is_available": false}, []string{"X-API-Key", ""}},
		{"update preferences", http.MethodPut, "/riders/" + rider + "/preferences", map[string]any{"min_order_value_kes": 100}, []string{"X-API-Key", ""}},
		{"match orders", http.MethodPost, "/riders/match-orders", map[string]any{
			"rider_id": rider, "current_location": loc(nearYayaLat, nearYayaL), "bearing_degrees": 10, "speed_kmh": 20, "current_orders_count": 0}, []string{"X-API-Key", ""}},
		{"a wrong key", http.MethodPatch, "/riders/" + rider + "/availability", map[string]any{"is_available": false}, []string{"X-API-Key", "not-the-key-0123456789abcdef"}},
	}
	for _, tc := range attempts {
		t.Run(tc.name, func(t *testing.T) {
			res := e.call(tc.method, tc.path, tc.body, tc.headers...)

			assert.Equal(t, http.StatusUnauthorized, res.Status, "%v", res.Body)
			assert.Equal(t, `ApiKey realm="zemi-fleet"`, res.Header.Get("WWW-Authenticate"))
			assert.Equal(t, "invalid or missing API key", res.Body["error"])
		})
	}

	assert.Equal(t, before, snapshot(), "no rejected request may change the database")

	// The same write with the key does go through, so the rejections above were the key's doing, not the request's.
	ok := e.call(http.MethodPatch, "/riders/"+rider+"/availability", map[string]any{"is_available": false})
	require.Equal(t, http.StatusOK, ok.Status, "%v", ok.Body)

	// /healthz needs no key.
	health := e.call(http.MethodGet, "/healthz", nil, "X-API-Key", "")
	assert.Equal(t, http.StatusOK, health.Status)
}
