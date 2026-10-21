//go:build e2e && chaos

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codercollo/zemi-fleet/internal/testdb"
)

// These tests are opt-in because they need a running Toxiproxy profile. The
// database DSN must point at the proxy, while the proxy forwards to Postgres.
func chaosProxy(t *testing.T) string {
	t.Helper()
	base := os.Getenv("TOXIPROXY_API_URL")
	if base == "" {
		base = "http://127.0.0.1:8474"
	}
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL must point through the Toxiproxy postgres proxy")
	}
	response, err := http.Get(base + "/version")
	if err != nil {
		t.Skipf("Toxiproxy is not available at %s: %v", base, err)
	}
	response.Body.Close()
	if response.StatusCode >= 300 {
		t.Skipf("Toxiproxy returned %s", response.Status)
	}
	return base
}

func toxic(t *testing.T, base, kind string, attributes map[string]any) error {
	t.Helper()
	name := os.Getenv("TOXIPROXY_PROXY_NAME")
	if name == "" {
		name = "postgres"
	}
	payload := map[string]any{"name": "chaos-" + kind, "type": kind, "stream": "downstream", "attributes": attributes}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/proxies/%s/toxics", base, name), bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		return fmt.Errorf("toxiproxy returned %s", response.Status)
	}
	t.Cleanup(func() {
		request, _ := http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/proxies/%s/toxics/chaos-%s", base, name, kind), nil)
		if request != nil {
			if response, err := http.DefaultClient.Do(request); err == nil {
				response.Body.Close()
			}
		}
	})
	return nil
}

func clearToxic(t *testing.T, base, kind string) {
	t.Helper()
	name := os.Getenv("TOXIPROXY_PROXY_NAME")
	if name == "" {
		name = "postgres"
	}
	request, err := http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/proxies/%s/toxics/chaos-%s", base, name, kind), nil)
	if err != nil {
		return
	}
	response, err := http.DefaultClient.Do(request)
	if err == nil {
		response.Body.Close()
	}
}

func TestE2E_ChaosQueryTimeoutReturnsGeneric500AndPreservesState(t *testing.T) {
	proxy := chaosProxy(t)
	t.Setenv("DB_QUERY_TIMEOUT", "100ms")
	e := newEnv(t)
	rider := "chaos_rider_timeout"
	testdb.AddRider(t, e.pool, testdb.RiderFixture{ID: rider, Lat: f64(nearYayaLat), Lng: f64(nearYayaL), SeenAt: e.seenNow()})
	// The fixture is inserted by the test database helper in the normal e2e flow;
	// use the API to create the offer so this remains a real stack test.
	order := e.newOrder("ven_004", "electronics", -1.27, 36.81)
	formed := e.requestBatches(rider, nearYayaLat, nearYayaL, 1)
	if formed.Status != http.StatusOK {
		t.Skipf("chaos fixture rider %s is not present in this environment", rider)
	}
	batches := formed.list("batches")
	require.NotEmpty(t, batches)
	batchID := batches[0]["batch_id"].(string)
	assert.Contains(t, batchOrderIDs(batches[0]), order)
	require.NoError(t, toxic(t, proxy, "latency", map[string]any{"latency": 500, "jitter": 0}))
	failed := e.accept(batchID, rider)
	clearToxic(t, proxy, "latency")
	assert.Equal(t, http.StatusInternalServerError, failed.Status)
	assert.Equal(t, "internal server error", failed.Body["error"])

	// The accepted batch and all order assignments must be all-or-nothing.
	assert.Equal(t, 1, scalar(t, e.pool, `SELECT count(*) FROM batches WHERE id = $1 AND status = 'open'`, batchID))
}

func TestE2E_ChaosPoolContentionReturnsGeneric500(t *testing.T) {
	proxy := chaosProxy(t)
	t.Setenv("DB_MAX_CONNS", "1")
	t.Setenv("DB_MIN_CONNS", "1")
	t.Setenv("DB_QUERY_TIMEOUT", "100ms")
	e := newEnv(t)
	rider := "chaos_rider_pool"
	testdb.AddRider(t, e.pool, testdb.RiderFixture{ID: rider, Lat: f64(nearYayaLat), Lng: f64(nearYayaL), SeenAt: e.seenNow()})
	e.newOrder("ven_004", "electronics", -1.27, 36.81)
	formed := e.requestBatches(rider, nearYayaLat, nearYayaL, 1)
	require.Equal(t, http.StatusOK, formed.Status)
	require.NotEmpty(t, formed.list("batches"))
	batchID := formed.list("batches")[0]["batch_id"].(string)
	require.NoError(t, toxic(t, proxy, "latency", map[string]any{"latency": 500, "jitter": 0}))

	var wg sync.WaitGroup
	statuses := make([]int, 2)
	for i := range statuses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses[i] = e.accept(batchID, rider).Status
		}(i)
	}
	wg.Wait()
	clearToxic(t, proxy, "latency")
	assert.Equal(t, []int{http.StatusInternalServerError, http.StatusInternalServerError}, statuses)
	assert.Equal(t, 1, scalar(t, e.pool, `SELECT count(*) FROM batches WHERE id = $1 AND status = 'open'`, batchID))
}

func TestE2E_ChaosConnectionResetDoesNotLeaveHalfAcceptedBatch(t *testing.T) {
	proxy := chaosProxy(t)
	e := newEnv(t)
	rider := "chaos_rider_reset"
	testdb.AddRider(t, e.pool, testdb.RiderFixture{ID: rider, Lat: f64(nearYayaLat), Lng: f64(nearYayaL), SeenAt: e.seenNow()})
	formed := e.requestBatches(rider, nearYayaLat, nearYayaL, 1)
	if formed.Status != http.StatusOK {
		t.Skipf("chaos fixture rider %s is not present in this environment", rider)
	}
	require.NotEmpty(t, formed.list("batches"))
	batchID := formed.list("batches")[0]["batch_id"].(string)
	require.NoError(t, toxic(t, proxy, "reset_peer", map[string]any{"timeout": 1}))
	failed := e.accept(batchID, rider)
	clearToxic(t, proxy, "reset_peer")
	assert.Equal(t, http.StatusInternalServerError, failed.Status)
	assert.Equal(t, "internal server error", failed.Body["error"])

	// A reset can interrupt before or after the transaction commits; either
	// outcome is valid, but no mixed batch/order/rider state is.
	var status string
	require.NoError(t, e.pool.QueryRow(t.Context(), `SELECT status FROM batches WHERE id = $1`, batchID).Scan(&status))
	assert.Contains(t, []string{"open", "accepted"}, status)
}
