//go:build e2e

// Package e2e drives the whole API over HTTP against a real PostgreSQL test database.
//
// The wiring is the production wiring (config.Load -> pgx pool -> Store -> services -> handlers, via
// app.Build, served by httptest.Server); only the clock is replaced by a clock.Test so scenarios can move
// time. Scenarios arrange state through the API (or the testdb fixture loader for reference data and riders),
// never by writing orders or batches into the database, and after every scenario assertInvariants checks the
// database with SQL. Scenarios run one after another: they share one database, so none may use t.Parallel.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/codercollo/zemi-fleet/internal/app"
	"github.com/codercollo/zemi-fleet/internal/clock"
	"github.com/codercollo/zemi-fleet/internal/config"
	"github.com/codercollo/zemi-fleet/internal/repository"
	"github.com/codercollo/zemi-fleet/internal/testdb"
)

// startOfDay is the instant every scenario begins at.
var startOfDay = time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)

// testAPIKey is the key every scenario sends (API_KEYS=e2e=<key>); long enough for any APP_ENV.
const testAPIKey = "e2e-test-api-key-0123456789abcdef0123"

// Seeded reference locations (db/testdata/reference.sql).
var (
	yayaLat, yayaLng       = -1.2917, 36.7878 // ven_004 Yaya Fashion House, Kilimani
	nearYayaLat, nearYayaL = -1.2940, 36.7860 // about 300 m south-west of Yaya
)

type env struct {
	t      *testing.T
	cfg    config.Config
	pool   *pgxpool.Pool
	clk    *clock.Test
	server *httptest.Server

	mu       sync.Mutex
	captured []string // every response body, for the timestamp invariant
}

// newEnv builds the real application on the test database and registers assertInvariants to run when the
// scenario ends.
// settings are extra environment variables as name, value pairs (the scenarios that exercise the rate limiter use them).
func newEnv(t *testing.T, settings ...string) *env {
	t.Helper()
	gin.SetMode(gin.TestMode)

	// Real configuration loading: environment over defaults, with DATABASE_URL pointed at the TEST database
	// (make exports the dev DATABASE_URL, which we override here so the app can never see it).
	t.Setenv("DATABASE_URL", testdb.URL(t))
	// Scenarios fire many requests from one address: the limiter is off unless a scenario turns it on.
	t.Setenv("LIMITER_ENABLED", "false")
	t.Setenv("API_KEYS", "e2e="+testAPIKey)
	for i := 0; i+1 < len(settings); i += 2 {
		t.Setenv(settings[i], settings[i+1])
	}
	cfg, err := config.Load("")
	require.NoError(t, err)

	pool, err := repository.OpenPool(context.Background(), cfg.DB)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	testdb.Reset(t, pool)
	testdb.LoadReference(t, pool)

	e := &env{t: t, cfg: cfg, pool: pool, clk: clock.NewTest(startOfDay)}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	e.server = httptest.NewServer(app.Build(cfg, logger, pool, e.clk, "e2e"))
	t.Cleanup(e.server.Close)

	t.Cleanup(func() {
		if !t.Failed() { // do not bury the real failure under invariant noise
			assertInvariants(t, e.pool, e.capturedBodies())
		}
	})
	return e
}

// response is a decoded JSON reply.
type response struct {
	Status int
	Body   map[string]any
	Header http.Header
}

// call performs a request and records the body. body may be nil, a string (sent as is) or any JSON value.
func (e *env) call(method, path string, body any, headers ...string) response {
	e.t.Helper()
	var reader io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		reader = bytes.NewBufferString(b)
	default:
		js, err := json.Marshal(b)
		require.NoError(e.t, err)
		reader = bytes.NewBuffer(js)
	}
	req, err := http.NewRequest(method, e.server.URL+"/api/v2"+path, reader)
	require.NoError(e.t, err)
	req.Header.Set("X-API-Key", testAPIKey)
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i+1] == "" { // an empty value removes the header, so a scenario can send no key at all
			req.Header.Del(headers[i])
			continue
		}
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	require.NoError(e.t, err)

	e.mu.Lock()
	e.captured = append(e.captured, string(raw))
	e.mu.Unlock()

	var parsed map[string]any
	require.NoError(e.t, json.Unmarshal(raw, &parsed), "not JSON: %s", raw)
	return response{Status: res.StatusCode, Body: parsed, Header: res.Header}
}

func (e *env) capturedBodies() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.captured...)
}

func (e *env) post(path string, body any) response {
	e.t.Helper()
	return e.call(http.MethodPost, path, body)
}
func (e *env) get(path string) response { e.t.Helper(); return e.call(http.MethodGet, path, nil) }
func (e *env) patch(path string, body any) response {
	e.t.Helper()
	return e.call(http.MethodPatch, path, body)
}

// --- reading responses ---

// list returns Body[key] as a slice of objects.
func (r response) list(key string) []map[string]any {
	raw, ok := r.Body[key].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, v := range raw {
		out = append(out, v.(map[string]any))
	}
	return out
}

func (r response) object(key string) map[string]any {
	m, _ := r.Body[key].(map[string]any)
	return m
}

// strings returns field of every object in items.
func strs(items []map[string]any, field string) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it[field].(string))
	}
	return out
}

// batchOrderIDs returns the order IDs of one batch object from POST /orders/batch or GET /batches.
func batchOrderIDs(batch map[string]any) []string {
	var ids []string
	for _, o := range batch["orders"].([]any) {
		ids = append(ids, o.(map[string]any)["order_id"].(string))
	}
	return ids
}

// --- arranging state through the API ---

// loc is a latitude/longitude pair for request bodies.
func loc(lat, lng float64) map[string]float64 {
	return map[string]float64{"latitude": lat, "longitude": lng}
}

// newOrder creates an order from a vendor heading to (dLat, dLng) and returns its ID. It then moves the clock on
// a minute so orders have a clear FIFO order.
func (e *env) newOrder(vendorID, category string, dLat, dLng float64) string {
	e.t.Helper()
	res := e.post("/orders", map[string]any{
		"vendor_id": vendorID, "category": category,
		"delivery_location": map[string]float64{"lat": dLat, "lng": dLng},
		"value_kes":         5000, "delivery_fee_kes": 250,
	})
	require.Equal(e.t, http.StatusCreated, res.Status, "%v", res.Body)
	e.clk.Advance(time.Minute)
	return res.object("order")["id"].(string)
}

// northeast deliveries from the Kilimani vendors: all head about 45 degrees, so they batch together.
var northeast = [][2]float64{{-1.2700, 36.8100}, {-1.2650, 36.8150}, {-1.2600, 36.8200}, {-1.2550, 36.8250}, {-1.2500, 36.8300}, {-1.2450, 36.8350}, {-1.2400, 36.8400}}

// orders creates n electronics orders at the vendor, all heading north-east.
func (e *env) orders(vendorID string, n int) []string {
	e.t.Helper()
	var ids []string
	for i := range n {
		ids = append(ids, e.newOrder(vendorID, "electronics", northeast[i%len(northeast)][0], northeast[i%len(northeast)][1]))
	}
	return ids
}

// requestBatches is POST /orders/batch for a rider at (lat, lng).
func (e *env) requestBatches(riderID string, lat, lng float64, maxOrders int) response {
	e.t.Helper()
	return e.post("/orders/batch", map[string]any{
		"rider_id": riderID, "rider_location": loc(lat, lng), "max_orders": maxOrders, "radius_km": 2,
	})
}

// matchOrders is POST /riders/match-orders.
func (e *env) matchOrders(riderID string, lat, lng float64, bearing, speed float64, count int) response {
	e.t.Helper()
	return e.post("/riders/match-orders", map[string]any{
		"rider_id": riderID, "current_location": loc(lat, lng),
		"bearing_degrees": bearing, "speed_kmh": speed, "current_orders_count": count,
	})
}

func (e *env) accept(batchID, riderID string) response {
	e.t.Helper()
	return e.post("/batches/"+batchID+"/accept", map[string]string{"rider_id": riderID})
}

// seenNow is a pointer to the current clock time, for rider fixtures with a fresh location.
func (e *env) seenNow() *time.Time { t := e.clk.Now(); return &t }

func f64(v float64) *float64 { return &v }
