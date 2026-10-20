// Package app wires the application together: the Store, the services and the HTTP handler. cmd/api and the
// end-to-end tests both build the server through Build, so the e2e tests exercise the real wiring.
package app

import (
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/codercollo/zemi-fleet/internal/api"
	"github.com/codercollo/zemi-fleet/internal/clock"
	"github.com/codercollo/zemi-fleet/internal/config"
	"github.com/codercollo/zemi-fleet/internal/metrics"
	"github.com/codercollo/zemi-fleet/internal/repository"
	"github.com/codercollo/zemi-fleet/internal/routing"
	"github.com/codercollo/zemi-fleet/internal/service"
	"github.com/codercollo/zemi-fleet/internal/webhooks"
)

// Build returns the HTTP handler for the whole API on top of an open pool (tests serve it with httptest). The clock
// is injected so tests can control time; production passes clock.Real{}.
func Build(cfg config.Config, logger *slog.Logger, pool *pgxpool.Pool, clk clock.Clock, version string) http.Handler {
	return New(cfg, logger, pool, clk, version).Routes()
}

// New returns the API server itself, which cmd/api runs with Serve so it can shut down gracefully.
func New(cfg config.Config, logger *slog.Logger, pool *pgxpool.Pool, clk clock.Clock, version string) *api.Server {
	store := repository.NewStore(pool, cfg.DB.QueryTimeout)
	counters := metrics.New()
	var router routing.Router = routing.Haversine{}
	if cfg.Routing.Provider == "osrm" {
		router = routing.NewResilient(routing.OSRM{BaseURL: cfg.Routing.BaseURL}, routing.Haversine{}, routing.Config{
			Timeout: cfg.Routing.Timeout, Retries: cfg.Routing.Retries, BreakerFailures: cfg.Routing.BreakerFailures,
			BreakerOpen: cfg.Routing.BreakerOpen, CacheTTL: cfg.Routing.CacheTTL, RoundDigits: 5,
		}, clk)
	}

	dispatcher := &webhooks.Dispatcher{Store: store, Sender: webhooks.HTTPSender{}, Now: clk.Now, Enabled: cfg.Webhooks.Enabled}
	// The server owns the dispatcher lifecycle; the store remains the same pool-backed adapter.
	server := api.NewServer(cfg, logger, pool, api.Services{
		Orders: service.NewOrderService(store, clk, cfg.RiderPayoutBPS, cfg.OrderMinTripM).WithMetrics(counters), Riders: service.NewRiderService(store, clk),
		Matching: service.NewMatchingService(store, clk, logger, cfg.Cone, cfg.Capacity).WithMetrics(counters), Batches: service.NewBatchService(store, clk, cfg.Batch, cfg.Capacity).WithRouting(router, cfg.Routing).WithMetrics(counters),
		Preferences: service.NewPreferenceService(store, clk, cfg.Match, cfg.OrderMinTripM, cfg.Capacity).WithMetrics(counters), Idempotency: service.NewIdempotencyService(store, clk, cfg.IdempotencyTTL, cfg.IdempotencyProcessingTimeout).WithMetrics(counters), Lifecycle: service.NewLifecycleService(store, clk, cfg.CancelCompensationBPS, service.NoopEventSink{}), Offline: service.NewOfflineService(store, clk, service.NoopEventSink{}), Metrics: counters, Clock: clk, WebhookStore: store, WebhookDispatcher: dispatcher,
	}, version)
	return server
}
