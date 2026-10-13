//go:build e2e

package e2e

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codercollo/zemi-fleet/internal/testdb"
)

// orderBody is a valid POST /orders body from the Yaya vendor.
func orderBody(valueKES float64) map[string]any {
	return map[string]any{
		"vendor_id": "ven_004", "category": "electronics",
		"delivery_location": map[string]float64{"lat": -1.27, "lng": 36.81},
		"value_kes":         valueKES, "delivery_fee_kes": 250,
	}
}

func (e *env) postKeyed(path, key string, body any) response {
	e.t.Helper()
	return e.call(http.MethodPost, path, body, "Idempotency-Key", key)
}

func (e *env) orderCount() int {
	return scalar(e.t, e.pool, `SELECT count(*) FROM orders`)
}

// Scenario 6: a retried POST /orders creates exactly one order, however the retry arrives.
func TestE2E_IdempotentRetries(t *testing.T) {
	e := newEnv(t)

	t.Run("a retry replays the response and creates no second order", func(t *testing.T) {
		first := e.postKeyed("/orders", "key-retry-1", orderBody(5000))
		require.Equal(t, http.StatusCreated, first.Status, "%v", first.Body)
		assert.Empty(t, first.Header.Get("Idempotent-Replay"), "the first request is computed, not replayed")
		orderID := first.object("order")["id"].(string)

		retry := e.postKeyed("/orders", "key-retry-1", orderBody(5000))

		assert.Equal(t, http.StatusCreated, retry.Status)
		assert.Equal(t, "true", retry.Header.Get("Idempotent-Replay"))
		assert.Equal(t, first.Body, retry.Body, "the very same response")
		assert.Equal(t, "/api/v2/orders/"+orderID, retry.Header.Get("Location"), "the Location header is replayed too")
		assert.Equal(t, 1, e.orderCount(), "exactly one order exists")
	})

	t.Run("the same key with a different body is refused", func(t *testing.T) {
		before := e.orderCount()

		res := e.postKeyed("/orders", "key-retry-1", orderBody(9999))

		assert.Equal(t, http.StatusUnprocessableEntity, res.Status)
		assert.Contains(t, res.Body["error"], "idempotency_key")
		assert.Equal(t, before, e.orderCount(), "nothing was created")
	})

	t.Run("a different key is a different request", func(t *testing.T) {
		before := e.orderCount()

		res := e.postKeyed("/orders", "key-retry-2", orderBody(5000))

		assert.Equal(t, http.StatusCreated, res.Status)
		assert.Empty(t, res.Header.Get("Idempotent-Replay"))
		assert.Equal(t, before+1, e.orderCount())
	})

	t.Run("a failed request releases its key, so the corrected retry runs", func(t *testing.T) {
		bad := orderBody(5000)
		bad["vendor_id"] = "ven_999"
		before := e.orderCount()

		failed := e.postKeyed("/orders", "key-fix", bad)
		fixed := e.postKeyed("/orders", "key-fix", orderBody(5000))

		assert.Equal(t, http.StatusUnprocessableEntity, failed.Status)
		assert.Equal(t, http.StatusCreated, fixed.Status, "%v", fixed.Body)
		assert.Equal(t, before+1, e.orderCount())
	})

	t.Run("ten concurrent identical requests create one order", func(t *testing.T) {
		before := e.orderCount()
		const n = 10
		results := make([]response, n)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				results[i] = e.postKeyed("/orders", "key-race", orderBody(1234))
			}()
		}
		close(start)
		wg.Wait()

		ids := map[string]bool{}
		created := 0
		for _, r := range results {
			switch r.Status {
			case http.StatusCreated:
				ids[r.object("order")["id"].(string)] = true
				if r.Header.Get("Idempotent-Replay") == "" {
					created++
				}
			case http.StatusConflict: // the first request was still running: told to retry
			default:
				t.Errorf("unexpected status %d: %v", r.Status, r.Body)
			}
		}
		assert.Equal(t, 1, created, "exactly one request did the work")
		assert.Len(t, ids, 1, "every success carries the same order")
		assert.Equal(t, before+1, e.orderCount())
	})

	t.Run("a key older than IDEMPOTENCY_TTL is ignored and cleaned up", func(t *testing.T) {
		before := e.orderCount()
		keys := scalar(t, e.pool, `SELECT count(*) FROM idempotency_keys WHERE key = 'key-retry-1'`)
		require.Equal(t, 1, keys)

		e.clk.Advance(e.cfg.IdempotencyTTL + time.Minute)
		res := e.postKeyed("/orders", "key-retry-1", orderBody(5000))

		assert.Equal(t, http.StatusCreated, res.Status, "the key has expired, so this is a new request")
		assert.Empty(t, res.Header.Get("Idempotent-Replay"))
		assert.Equal(t, before+1, e.orderCount())
		assert.Equal(t, 1, scalar(t, e.pool, `SELECT count(*) FROM idempotency_keys`), "writing a key deleted all the expired ones")
	})

	t.Run("a malformed key is a 400", func(t *testing.T) {
		before := e.orderCount()
		for _, key := range []string{"has space", strings.Repeat("k", 65), "café"} {
			res := e.postKeyed("/orders", key, orderBody(5000))
			assert.Equal(t, http.StatusBadRequest, res.Status, "key %q", key)
		}
		longest := strings.Repeat("k", 64)
		assert.Equal(t, http.StatusCreated, e.postKeyed("/orders", longest, orderBody(5000)).Status, "64 characters is the limit and is allowed")
		before++
		assert.Equal(t, before, e.orderCount())
	})
}

// Batches are idempotent too: retrying POST /orders/batch returns the same batches and forms no second set.
func TestE2E_IdempotentBatchRequest(t *testing.T) {
	e := newEnv(t)
	rider := testdb.AddRider(t, e.pool, testdb.RiderFixture{ID: "rider_a", Lat: f64(nearYayaLat), Lng: f64(nearYayaL), SeenAt: e.seenNow()})
	e.orders("ven_004", 3)
	body := map[string]any{"rider_id": rider, "rider_location": loc(nearYayaLat, nearYayaL), "max_orders": 5, "radius_km": 2}

	first := e.postKeyed("/orders/batch", "batch-key", body)
	require.Equal(t, http.StatusOK, first.Status, "%v", first.Body)
	require.Len(t, first.list("batches"), 1)

	retry := e.postKeyed("/orders/batch", "batch-key", body)

	assert.Equal(t, http.StatusOK, retry.Status)
	assert.Equal(t, "true", retry.Header.Get("Idempotent-Replay"))
	assert.Equal(t, first.Body, retry.Body)
	assert.Equal(t, 1, scalar(t, e.pool, `SELECT count(*) FROM batches`), "no second batch")
	assert.Equal(t, 1, scalar(t, e.pool, `SELECT offers_count FROM riders WHERE id = $1`, rider), "no second offer counted")

	// The same key on the other endpoint is independent.
	other := e.postKeyed("/orders", "batch-key", orderBody(5000))
	assert.Equal(t, http.StatusCreated, other.Status)
}
