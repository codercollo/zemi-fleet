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
	"github.com/codercollo/zemi-fleet/internal/repository"
	"github.com/codercollo/zemi-fleet/internal/service"
)

// Build returns the HTTP handler for the whole API on top of an open pool. The clock is injected so tests can
// control time; production passes clock.Real{}.
func Build(cfg config.Config, logger *slog.Logger, pool *pgxpool.Pool, clk clock.Clock, version string) http.Handler {
	store := repository.NewStore(pool, cfg.DB.QueryTimeout)

	server := api.NewServer(cfg, logger, pool, api.Services{
		Orders:      service.NewOrderService(store, clk, cfg.RiderPayoutBPS, cfg.OrderMinTripM),
		Riders:      service.NewRiderService(store, clk),
		Matching:    service.NewMatchingService(store, clk, logger, cfg.Cone),
		Batches:     service.NewBatchService(store, clk, cfg.Batch),
		Preferences: service.NewPreferenceService(store, clk, cfg.Match, cfg.OrderMinTripM),
		Idempotency: service.NewIdempotencyService(store, clk, cfg.IdempotencyTTL, cfg.IdempotencyProcessingTimeout),
	}, version)
	return server.Routes()
}
