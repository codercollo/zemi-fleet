//go:build e2e && chaos

package e2e

import (
	"bytes"
	"context"
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

func chaosProxy(t *testing.T) string {
	t.Helper()
	base := os.Getenv("TOXIPROXY_API_URL")
	if base == "" {
		t.Skip("TOXIPROXY_API_URL is not set; chaos infrastructure was not requested")
	}
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Fatal("TOXIPROXY_API_URL is set but TEST_DATABASE_URL is missing")
	}
	response, err := http.Get(base + "/version")
	if err != nil {
		t.Fatalf("Toxiproxy is unreachable at %s: %v", base, err)
	}
	response.Body.Close()
	if response.StatusCode >= 300 {
		t.Fatalf("Toxiproxy returned %s", response.Status)
	}
	return base
}

func addToxic(t *testing.T, base, kind string, attrs map[string]any) {
	t.Helper()
	name := os.Getenv("TOXIPROXY_PROXY_NAME")
	if name == "" {
		name = "postgres"
	}
	body, err := json.Marshal(map[string]any{"name": "chaos-" + kind, "type": kind, "stream": "downstream", "attributes": attrs})
	require.NoError(t, err)
	request, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/proxies/%s/toxics", base, name), bytes.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	response.Body.Close()
	require.Less(t, response.StatusCode, 300, response.Status)
	t.Cleanup(func() { removeToxic(t, base, kind) })
}

func removeToxic(t *testing.T, base, kind string) {
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

func chaosOffer(t *testing.T, e *env, rider string) string {
	t.Helper()
	testdb.AddRider(t, e.pool, testdb.RiderFixture{ID: rider, Lat: f64(nearYayaLat), Lng: f64(nearYayaL), SeenAt: e.seenNow()})
	e.newOrder("ven_004", "electronics", -1.27, 36.81)
	formed := e.requestBatches(rider, nearYayaLat, nearYayaL, 1)
	require.Equal(t, http.StatusOK, formed.Status, "%v", formed.Body)
	require.NotEmpty(t, formed.list("batches"))
	return formed.list("batches")[0]["batch_id"].(string)
}

func TestE2E_ChaosQueryTimeout(t *testing.T) {
	proxy := chaosProxy(t)
	t.Setenv("DB_QUERY_TIMEOUT", "100ms")
	e := newEnv(t)
	batchID := chaosOffer(t, e, "rider_901")
	addToxic(t, proxy, "latency", map[string]any{"latency": 500, "jitter": 0})
	failed := e.accept(batchID, "rider_901")
	removeToxic(t, proxy, "latency")
	assert.Equal(t, http.StatusInternalServerError, failed.Status)
	assert.Equal(t, "the server encountered a problem and could not process your request", failed.Body["error"])
	assert.Equal(t, 1, scalar(t, e.pool, `SELECT count(*) FROM batches WHERE id = $1 AND status = 'open'`, batchID))
}

func TestE2E_ChaosConnectionReset(t *testing.T) {
	proxy := chaosProxy(t)
	e := newEnv(t)
	batchID := chaosOffer(t, e, "rider_902")
	addToxic(t, proxy, "reset_peer", map[string]any{"timeout": 1})
	failed := e.accept(batchID, "rider_902")
	removeToxic(t, proxy, "reset_peer")
	assert.Equal(t, http.StatusInternalServerError, failed.Status)
	assert.Equal(t, "the server encountered a problem and could not process your request", failed.Body["error"])
	assert.Equal(t, 1, scalar(t, e.pool, `SELECT count(*) FROM batches WHERE id = $1 AND status IN ('open', 'accepted')`, batchID))
}

func TestE2E_ChaosOutboxWriteConnectionReset(t *testing.T) {
	proxy := chaosProxy(t)
	e := newEnv(t)
	batchID := chaosOffer(t, e, "rider_904")
	accepted := e.accept(batchID, "rider_904")
	require.Equal(t, http.StatusOK, accepted.Status, "%v", accepted.Body)

	var orderID string
	require.NoError(t, e.pool.QueryRow(context.Background(), `SELECT order_id FROM batch_stops WHERE batch_id = $1 AND type = 'pickup' LIMIT 1`, batchID).Scan(&orderID))
	before := scalar(t, e.pool, `SELECT count(*) FROM outbox_events`)
	addToxic(t, proxy, "reset_peer", map[string]any{"timeout": 1})
	failed := e.post("/orders/"+orderID+"/cancel", map[string]string{"reason_code": "customer_cancelled"})
	removeToxic(t, proxy, "reset_peer")

	if failed.Status == http.StatusInternalServerError {
		assert.Equal(t, 1, scalar(t, e.pool, `SELECT count(*) FROM orders WHERE id = $1 AND status = 'assigned'`, orderID))
		assert.Equal(t, before, scalar(t, e.pool, `SELECT count(*) FROM outbox_events`))
		return
	}
	// A reset may arrive after PostgreSQL commits. In that case the lifecycle and its outbox write
	// must both be visible; a partial commit is the failure this scenario protects against.
	require.Equal(t, http.StatusOK, failed.Status, "%v", failed.Body)
	assert.Equal(t, 1, scalar(t, e.pool, `SELECT count(*) FROM orders WHERE id = $1 AND status = 'cancelled'`, orderID))
	assert.Equal(t, before+1, scalar(t, e.pool, `SELECT count(*) FROM outbox_events`))
}

func TestE2E_ChaosPoolContention(t *testing.T) {
	proxy := chaosProxy(t)
	t.Setenv("DB_MAX_CONNS", "1")
	t.Setenv("DB_MIN_CONNS", "1")
	t.Setenv("DB_QUERY_TIMEOUT", "100ms")
	e := newEnv(t)
	batchID := chaosOffer(t, e, "rider_903")
	addToxic(t, proxy, "latency", map[string]any{"latency": 500, "jitter": 0})
	statuses := make([]int, 2)
	var wg sync.WaitGroup
	for i := range statuses {
		wg.Add(1)
		go func(i int) { defer wg.Done(); statuses[i] = e.accept(batchID, "rider_903").Status }(i)
	}
	wg.Wait()
	removeToxic(t, proxy, "latency")
	assert.Equal(t, []int{http.StatusInternalServerError, http.StatusInternalServerError}, statuses)
	assert.Equal(t, 1, scalar(t, e.pool, `SELECT count(*) FROM batches WHERE id = $1 AND status = 'open'`, batchID))
}
