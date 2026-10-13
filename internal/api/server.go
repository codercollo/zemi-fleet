// Package api is the HTTP layer: routes, handlers, JSON helpers and error responses.
package api

import (
	"context"
	"log/slog"

	"github.com/codercollo/zemi-fleet/internal/config"
	"github.com/codercollo/zemi-fleet/internal/service"
)

// Pinger is the slice of the DB pool the HTTP layer needs. Declaring it here
// lets tests fake the database without a running Postgres.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Services are the business-logic services the handlers call.
type Services struct {
	Orders      *service.OrderService
	Riders      *service.RiderService
	Matching    *service.MatchingService
	Batches     *service.BatchService
	Preferences *service.PreferenceService
	Idempotency *service.IdempotencyService
}

// Server holds the HTTP layer's dependencies (the book's `application` struct).
// Let's Go Further ch. 2.3.
type Server struct {
	cfg         config.Config
	logger      *slog.Logger
	db          Pinger
	orders      *service.OrderService
	riders      *service.RiderService
	matching    *service.MatchingService
	batches     *service.BatchService
	preferences *service.PreferenceService
	idempotency *service.IdempotencyService
	version     string
}

// NewServer wires the dependencies together.
func NewServer(cfg config.Config, logger *slog.Logger, db Pinger, svc Services, version string) *Server {
	return &Server{
		cfg: cfg, logger: logger, db: db, version: version,
		orders: svc.Orders, riders: svc.Riders, matching: svc.Matching, batches: svc.Batches,
		preferences: svc.Preferences, idempotency: svc.Idempotency,
	}
}
