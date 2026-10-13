package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/codercollo/zemi-fleet/internal/clock"
	"github.com/codercollo/zemi-fleet/internal/config"
	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/metrics"
	"github.com/codercollo/zemi-fleet/internal/mock"
	"github.com/codercollo/zemi-fleet/internal/repository/sqlc"
	"github.com/codercollo/zemi-fleet/internal/service"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// metricsHandler builds the real router and services (all wired to one set of counters) over a gomock Store.
func newMetricsHandler(t *testing.T, mutate func(*config.Config)) (http.Handler, *mock.MockStore) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	store := mock.NewMockStore(gomock.NewController(t))
	cfg := testConfig()
	cfg.MetricsEnabled = true
	if mutate != nil {
		mutate(&cfg)
	}
	counters := metrics.New()
	clk := clock.NewTest(testNow)
	logger := discardLogger()
	srv := NewServer(cfg, logger, fakeDB{}, Services{
		Orders:      service.NewOrderService(store, clk, testPayoutBPS, testMinTripM).WithMetrics(counters),
		Riders:      service.NewRiderService(store, clk),
		Matching:    service.NewMatchingService(store, clk, logger, testCone).WithMetrics(counters),
		Batches:     service.NewBatchService(store, clk, testBatchCfg).WithMetrics(counters),
		Preferences: service.NewPreferenceService(store, clk, testMatchCfg, testMinTripM).WithMetrics(counters),
		Idempotency: service.NewIdempotencyService(store, clk, testIdempotencyTTL, testIdempotencyProcessing).WithMetrics(counters),
		Metrics:     counters,
		Clock:       clk,
	}, "test-version")
	return srv.Routes(), store
}

// vars fetches GET /debug/vars with a valid key.
func vars(t *testing.T, h http.Handler) map[string]any {
	t.Helper()
	rec := send(t, h, http.MethodGet, "/debug/vars", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	return out
}

// number reads a published integer.
func number(t *testing.T, v map[string]any, name string) float64 {
	t.Helper()
	n, ok := v[name].(float64)
	require.True(t, ok, "%s is %v", name, v[name])
	return n
}

func TestMetrics_PublishesEveryValue(t *testing.T) {
	h, _ := newMetricsHandler(t, nil)

	v := vars(t, h)

	for _, name := range []string{
		"version", "goroutines", "timestamp", "database", "memstats",
		"total_requests_received", "total_responses_sent", "total_processing_time_μs", "total_responses_sent_by_status",
		"orders_created", "batches_formed", "batches_accepted", "batch_accept_conflicts", "batch_capacity_rejections",
		"cone_matches_served", "rider_matches_served", "idempotent_replays",
	} {
		assert.Contains(t, v, name)
	}
	assert.Equal(t, "test-version", v["version"])
	assert.Equal(t, float64(testNow.Unix()), v["timestamp"], "the clock's time")
	assert.Greater(t, number(t, v, "goroutines"), 0.0)
	assert.IsType(t, map[string]any{}, v["database"], "empty here: the fake database is not a pool")
}

func TestMetrics_RequestCountersGoUp(t *testing.T) {
	h, _ := newMetricsHandler(t, nil)
	before := vars(t, h)

	send(t, h, http.MethodGet, "/api/v2/healthz", "")
	send(t, h, http.MethodGet, "/api/v2/healthz", "")
	after := vars(t, h)

	assert.Equal(t, number(t, before, "total_requests_received")+3, number(t, after, "total_requests_received"), "two healthz and the first scrape's successor")
	assert.Equal(t, number(t, before, "total_responses_sent")+3, number(t, after, "total_responses_sent"), "the scrape that reads it is not yet sent")
}

func TestMetrics_StatusMapCounts200_401_404(t *testing.T) {
	h, _ := newMetricsHandler(t, nil)

	send(t, h, http.MethodGet, "/api/v2/healthz", "") // 200
	send(t, h, http.MethodGet, "/api/v2/nowhere", "") // 404
	rec := httptest.NewRecorder()                     // 401: no key
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v2/batches", nil))
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	v := vars(t, h)

	byStatus, ok := v["total_responses_sent_by_status"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, 1.0, byStatus["200"], "healthz (the scrape itself is not counted until it is sent)")
	assert.Equal(t, 1.0, byStatus["401"])
	assert.Equal(t, 1.0, byStatus["404"])
}

func TestMetrics_CountsRateLimitedResponses(t *testing.T) {
	h, _ := newMetricsHandler(t, func(c *config.Config) {
		c.Limiter = config.LimiterConfig{Enabled: true, RPS: 0.001, Burst: 1}
	})

	send(t, h, http.MethodGet, "/api/v2/nowhere", "") // uses the single token (404)
	limited := send(t, h, http.MethodGet, "/api/v2/nowhere", "")
	require.Equal(t, http.StatusTooManyRequests, limited.Code)

	// The scrape is limited too: the metrics route has no exemption.
	scrape := send(t, h, http.MethodGet, "/debug/vars", "")
	assert.Equal(t, http.StatusTooManyRequests, scrape.Code)
}

func TestMetrics_DisabledIs404(t *testing.T) {
	h, _ := newMetricsHandler(t, func(c *config.Config) { c.MetricsEnabled = false })

	rec := send(t, h, http.MethodGet, "/debug/vars", "")

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.NotContains(t, rec.Body.String(), "total_requests_received")
}

func TestMetrics_RequiresAPIKey(t *testing.T) {
	h, _ := newMetricsHandler(t, nil)

	for name, key := range map[string]string{"no key": "", "wrong key": "not-the-right-key-at-all-0000"} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/debug/vars", nil)
			if key != "" {
				req.Header.Set(apiKeyHeader, key)
			}
			rec := httptest.NewRecorder()

			h.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusUnauthorized, rec.Code)
			assert.NotContains(t, rec.Body.String(), "memstats")
		})
	}
}

func TestMetrics_PanicIsCountedAsA500(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := testConfig()
	cfg.MetricsEnabled = true
	srv := NewServer(cfg, discardLogger(), fakeDB{}, Services{}, "test")
	h := srv.router(func(r *gin.Engine) {
		r.GET("/boom", func(*gin.Context) { panic("boom") })
	})

	rec := send(t, h, http.MethodGet, "/boom", "")

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	v := vars(t, h)
	assert.Equal(t, 1.0, v["total_responses_sent_by_status"].(map[string]any)["500"])
}

func TestMetrics_ProcessingTimeUsesTheClock(t *testing.T) {
	gin.SetMode(gin.TestMode)
	clk := clock.NewTest(testNow)
	cfg := testConfig()
	cfg.MetricsEnabled = true
	srv := NewServer(cfg, discardLogger(), fakeDB{}, Services{Clock: clk}, "test")
	h := srv.router(func(r *gin.Engine) {
		r.GET("/slow", func(c *gin.Context) { clk.Advance(1500 * time.Microsecond); c.Status(http.StatusNoContent) })
	})

	send(t, h, http.MethodGet, "/slow", "")

	assert.Equal(t, 1500.0, number(t, vars(t, h), "total_processing_time_μs"))
}

// Domain counters: they move on success and only on success.

func TestMetrics_OrdersCreatedOnlyOnSuccess(t *testing.T) {
	validBody := `{"vendor_id":"ven_001","category":"electronics","delivery_location":{"lat":-1.2905,"lng":36.7870},
		"value_kes":4500.50,"delivery_fee_kes":250}`

	t.Run("a stored order counts", func(t *testing.T) {
		h, store := newMetricsHandler(t, nil)
		store.EXPECT().GetVendor(gomock.Any(), "ven_001").Return(fakeVendor(), nil)
		store.EXPECT().GetCategory(gomock.Any(), "electronics").Return(sqlc.Category{Code: "electronics"}, nil)
		store.EXPECT().CreateOrder(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, p sqlc.CreateOrderParams) (sqlc.Order, error) { return orderRowFrom(p), nil })

		rec := send(t, h, http.MethodPost, "/api/v2/orders", validBody)

		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		assert.Equal(t, 1.0, number(t, vars(t, h), "orders_created"))
	})

	t.Run("a rejected or failed order does not", func(t *testing.T) {
		h, store := newMetricsHandler(t, nil)
		store.EXPECT().GetVendor(gomock.Any(), "ven_001").Return(fakeVendor(), nil).Times(2)
		store.EXPECT().GetCategory(gomock.Any(), "electronics").Return(sqlc.Category{Code: "electronics"}, nil).Times(2)
		store.EXPECT().CreateOrder(gomock.Any(), gomock.Any()).Return(sqlc.Order{}, errors.New("insert failed"))

		failed := send(t, h, http.MethodPost, "/api/v2/orders", validBody)
		tooShort := send(t, h, http.MethodPost, "/api/v2/orders",
			`{"vendor_id":"ven_001","category":"electronics","delivery_location":{"lat":-1.2635,"lng":36.803},"value_kes":10,"delivery_fee_kes":10}`)

		assert.Equal(t, http.StatusInternalServerError, failed.Code)
		assert.Equal(t, http.StatusUnprocessableEntity, tooShort.Code)
		assert.Equal(t, 0.0, number(t, vars(t, h), "orders_created"))
	})
}

func TestMetrics_IdempotentReplaysCounted(t *testing.T) {
	h, store := newMetricsHandler(t, nil) // GetVendor and CreateOrder are not expected: a replay never runs the handler
	store.EXPECT().DeleteExpiredIdempotencyKeys(gomock.Any(), gomock.Any()).Return(nil).Times(2)
	store.EXPECT().ClaimIdempotencyKey(gomock.Any(), gomock.Any()).Return(int64(0), nil).Times(2)
	store.EXPECT().GetIdempotencyKey(gomock.Any(), gomock.Any()).Return(sqlc.IdempotencyKey{
		RequestHash: service.HashBody([]byte(orderJSON)), ResponseStatus: ptrTo(int32(201)), ResponseBody: ptrTo(`{"order":{"id":"ord_X1"}}`),
		ResponseHeaders: []byte(`{}`), CreatedAt: testNow,
	}, nil).Times(2)

	first := send(t, h, http.MethodPost, orderPath, orderJSON, "Idempotency-Key", "abc-123")
	second := send(t, h, http.MethodPost, orderPath, orderJSON, "Idempotency-Key", "abc-123")

	assert.Equal(t, "true", first.Header().Get("Idempotent-Replay"))
	assert.Equal(t, "true", second.Header().Get("Idempotent-Replay"))
	v := vars(t, h)
	assert.Equal(t, 2.0, number(t, v, "idempotent_replays"))
	assert.Equal(t, 0.0, number(t, v, "orders_created"), "a replay creates nothing")
}

func TestMetrics_BatchAcceptCounters(t *testing.T) {
	freshlyAccepted := acceptedBatch("rider_002")
	at := testNow
	freshlyAccepted.AcceptedAt = &at

	tests := []struct {
		name string
		// result of the store call, and what each counter should read afterwards
		batch                         domain.Batch
		err                           error
		wantStatus                    int
		accepted, conflicts, capacity float64
	}{
		{"a win counts as accepted", freshlyAccepted, nil, http.StatusOK, 1, 0, 0},
		{"an idempotent retry by the winner is not a second win", acceptedBatch("rider_002"), nil, http.StatusOK, 0, 0, 0},
		{"a lost race counts as a conflict, not as accepted", domain.Batch{}, fmt.Errorf("accept batch: %w", domain.ErrBatchTaken), http.StatusConflict, 0, 1, 0},
		{"no room counts as a capacity rejection", domain.Batch{}, fmt.Errorf("accept batch: %w", domain.ErrRiderAtCapacity), http.StatusUnprocessableEntity, 0, 0, 1},
		{"an unknown batch counts as nothing", domain.Batch{}, fmt.Errorf("accept batch: %w", domain.ErrNotFound), http.StatusNotFound, 0, 0, 0},
		{"an unavailable rider counts as nothing", domain.Batch{}, fmt.Errorf("accept batch: %w", domain.ErrRiderUnavailable), http.StatusUnprocessableEntity, 0, 0, 0},
		{"a server error counts as nothing", domain.Batch{}, fmt.Errorf("accept batch: boom"), http.StatusInternalServerError, 0, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, store := newMetricsHandler(t, nil)
			store.EXPECT().AcceptBatchTx(gomock.Any(), gomock.Any()).Return(tc.batch, tc.err)

			rec := send(t, h, http.MethodPost, acceptPath("batch_X1"), `{"rider_id":"rider_002"}`)

			require.Equal(t, tc.wantStatus, rec.Code, rec.Body.String())
			v := vars(t, h)
			assert.Equal(t, tc.accepted, number(t, v, "batches_accepted"))
			assert.Equal(t, tc.conflicts, number(t, v, "batch_accept_conflicts"))
			assert.Equal(t, tc.capacity, number(t, v, "batch_capacity_rejections"))
		})
	}
}

func TestMetrics_MatchCountersCountServedMatchesOnly(t *testing.T) {
	t.Run("a served cone match counts, a rejected one does not", func(t *testing.T) {
		h, store := newMetricsHandler(t, nil)
		store.EXPECT().GetRider(gomock.Any(), "rider_001").Return(matchRiderRow(2, 5, true), nil)
		store.EXPECT().UpdateRiderTelemetry(gomock.Any(), gomock.Any()).Return(nil)
		store.EXPECT().ExpireBatchesTx(gomock.Any(), gomock.Any()).Return(0, nil)
		store.EXPECT().ListPendingOrdersInBox(gomock.Any(), gomock.Any()).Return(nil, nil)

		served := send(t, h, http.MethodPost, matchPath, validMatchBody)
		invalid := send(t, h, http.MethodPost, matchPath, `{}`)

		require.Equal(t, http.StatusOK, served.Code, served.Body.String())
		assert.GreaterOrEqual(t, invalid.Code, 400)
		assert.Equal(t, 1.0, number(t, vars(t, h), "cone_matches_served"))
	})

	t.Run("a served rider match counts, an unknown category does not", func(t *testing.T) {
		h, store := newMetricsHandler(t, nil)
		store.EXPECT().GetCategory(gomock.Any(), "electronics").Return(sqlc.Category{Code: "electronics"}, nil)
		store.EXPECT().ListMatchCandidateRiders(gomock.Any()).Return(nil, nil)

		served := send(t, h, http.MethodPost, matchRidersPath, detailsBody)
		require.Equal(t, http.StatusOK, served.Code, served.Body.String())
		v := vars(t, h)
		assert.Equal(t, 1.0, number(t, v, "rider_matches_served"))
		assert.Equal(t, 0.0, number(t, v, "cone_matches_served"))
	})
}
