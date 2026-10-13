//go:build integration

// Integration tests run against the real PostgreSQL in TEST_DATABASE_URL (never the dev database; each test
// resets it and loads its own fixtures):
//
//	make dev/test/integration
package api

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codercollo/zemi-fleet/internal/clock"
	"github.com/codercollo/zemi-fleet/internal/config"
	"github.com/codercollo/zemi-fleet/internal/repository"
	"github.com/codercollo/zemi-fleet/internal/service"
	"github.com/codercollo/zemi-fleet/internal/testdb"
	"github.com/codercollo/zemi-fleet/internal/util"
)

const concurrentRequests = 10

// integrationMatchCfg mirrors the default MATCH_* settings.
var integrationMatchCfg = config.MatchConfig{MaxDistanceKM: 10, LocationStaleAfter: 30 * time.Minute, WeightCategory: 0.25, WeightArea: 0.20, WeightProximity: 0.35, WeightAcceptance: 0.20, MaxResults: 10}

// integrationBatchCfg mirrors the default BATCH_* settings.
var integrationBatchCfg = config.BatchConfig{MaxOrders: 5, PickupRadiusKM: 2, DirectionToleranceDeg: 45, SearchRadiusKM: 5, VisibilityRadiusKM: 3, TTL: 10 * time.Minute, AvgSpeedKMH: 20, StopDwellMin: 2}

// integrationEnv connects to DATABASE_URL, creates a throwaway rider at version 1 and
// returns the real router plus that rider's ID. The rider is deleted afterwards, so the
// test never touches seeded data.
func integrationEnv(t *testing.T) (h http.Handler, riderID string, pool *pgxpool.Pool) {
	t.Helper()
	return integrationEnvWith(t, false)
}

// integrationEnvWith is integrationEnv; with withSeed it loads the full development seed (db/seed/seed.sql)
// into the test database instead of just the reference data.
func integrationEnvWith(t *testing.T, withSeed bool) (h http.Handler, riderID string, pool *pgxpool.Pool) {
	t.Helper()
	dsn := testdb.URL(t)

	ctx := context.Background()
	dbCfg := config.DBConfig{DSN: dsn, MaxConns: 25, MinConns: 0, MaxConnIdleTime: time.Minute, QueryTimeout: 3 * time.Second}
	pool, err := repository.OpenPool(ctx, dbCfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	testdb.Reset(t, pool)
	if withSeed {
		testdb.LoadSeed(t, pool)
	} else {
		testdb.LoadReference(t, pool)
	}

	clk := clock.Real{}
	riderID = util.NewRiderID(clk.Now())
	phone := fmt.Sprintf("+254%09d", rand.IntN(1_000_000_000))
	_, err = pool.Exec(ctx, `INSERT INTO riders (id, full_name, phone, is_available) VALUES ($1, 'Integration Rider', $2, TRUE)`, riderID, phone)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM riders WHERE id = $1`, riderID)
	})

	gin.SetMode(gin.TestMode)
	store := repository.NewStore(pool, dbCfg.QueryTimeout)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := testConfig()
	cfg.DB = dbCfg
	srv := NewServer(cfg, logger, pool, Services{
		Orders:      service.NewOrderService(store, clk, 9000, 50),
		Riders:      service.NewRiderService(store, clk),
		Matching:    service.NewMatchingService(store, clk, logger, config.ConeConfig{RadiusKM: 1, HalfAngleDeg: 30, MinSpeedKMH: 3, MaxResults: 20}),
		Batches:     service.NewBatchService(store, clk, integrationBatchCfg),
		Preferences: service.NewPreferenceService(store, clk, integrationMatchCfg, 50),
		Idempotency: service.NewIdempotencyService(store, clk, testIdempotencyTTL, testIdempotencyProcessing),
	}, "integration")
	return srv.Routes(), riderID, pool
}

// fireConcurrently releases all requests at once and returns their status codes.
func fireConcurrently(t *testing.T, h http.Handler, n int, build func(i int) *http.Request) []int {
	t.Helper()
	statuses := make([]int, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := build(i)
			rec := httptest.NewRecorder()
			<-start
			h.ServeHTTP(rec, req)
			statuses[i] = rec.Code
		}()
	}
	close(start)
	wg.Wait()
	return statuses
}

func count(statuses []int, code int) (n int) {
	for _, s := range statuses {
		if s == code {
			n++
		}
	}
	return n
}

func riderVersion(t *testing.T, pool *pgxpool.Pool, id string) int {
	t.Helper()
	var v int
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT version FROM riders WHERE id = $1`, id).Scan(&v))
	return v
}

func newPatch(path, body, version string) *http.Request {
	req := authed(httptest.NewRequest(http.MethodPatch, path, strings.NewReader(body)))
	if version != "" {
		req.Header.Set("X-Expected-Version", version)
	}
	return req
}

// Ten clients all believe the rider is at version 1 and send a PATCH. The versioned
// UPDATE lets exactly one through; the other nine get 409.
func TestIntegration_ConcurrentPatchSameExpectedVersion(t *testing.T) {
	tests := []struct {
		name string
		req  func(riderID string, i int) *http.Request
	}{
		{"location", func(id string, i int) *http.Request {
			body := fmt.Sprintf(`{"latitude":-1.28,"longitude":36.8,"bearing_degrees":%d,"speed_kmh":20}`, i*30)
			return newPatch("/api/v2/riders/"+id+"/location", body, "1")
		}},
		{"availability", func(id string, i int) *http.Request {
			return newPatch("/api/v2/riders/"+id+"/availability", fmt.Sprintf(`{"is_available":%t}`, i%2 == 0), "1")
		}},
		{"preferences", func(id string, i int) *http.Request {
			req := authed(httptest.NewRequest(http.MethodPut, "/api/v2/riders/"+id+"/preferences",
				strings.NewReader(fmt.Sprintf(`{"min_order_value_kes":%d,"preferred_categories":["food"]}`, i))))
			req.Header.Set("X-Expected-Version", "1")
			return req
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, riderID, pool := integrationEnv(t)
			if tc.name == "preferences" {
				_, err := pool.Exec(context.Background(), `INSERT INTO categories (code, label) VALUES ('food', 'Food') ON CONFLICT DO NOTHING`)
				require.NoError(t, err)
			}

			statuses := fireConcurrently(t, h, concurrentRequests, func(i int) *http.Request { return tc.req(riderID, i) })

			assert.Equal(t, 1, count(statuses, http.StatusOK), "exactly one writer wins: %v", statuses)
			assert.Equal(t, concurrentRequests-1, count(statuses, http.StatusConflict), "everyone else gets 409: %v", statuses)
			assert.Equal(t, 2, riderVersion(t, pool, riderID), "one successful write, one version bump")
		})
	}
}

// Without the header the service still reads then updates conditionally (ch. 8.2), so a race
// can never lose an update: every 200 is exactly one version bump and the rest are 409s.
func TestIntegration_ConcurrentPatchWithoutHeader(t *testing.T) {
	h, riderID, pool := integrationEnv(t)

	statuses := fireConcurrently(t, h, concurrentRequests, func(i int) *http.Request {
		return newPatch("/api/v2/riders/"+riderID+"/location", `{"latitude":-1.28,"longitude":36.8}`, "")
	})

	ok, conflict := count(statuses, http.StatusOK), count(statuses, http.StatusConflict)
	assert.GreaterOrEqual(t, ok, 1, "someone must win: %v", statuses)
	assert.Equal(t, concurrentRequests, ok+conflict, "only 200 or 409 are acceptable: %v", statuses)
	assert.Equal(t, 1+ok, riderVersion(t, pool, riderID))
}

// A sequential client that sends the version it just received keeps succeeding.
func TestIntegration_SequentialPatchesAdvanceTheVersion(t *testing.T) {
	h, riderID, _ := integrationEnv(t)

	for want := 1; want <= 3; want++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, newPatch("/api/v2/riders/"+riderID+"/availability", `{"is_available":false}`, strconv.Itoa(want)))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), fmt.Sprintf(`"version":%d`, want+1))
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newPatch("/api/v2/riders/"+riderID+"/availability", `{"is_available":true}`, "2"))
	assert.Equal(t, http.StatusConflict, rec.Code, "version 2 is stale now")
}
