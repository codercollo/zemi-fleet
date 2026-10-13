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

// Server holds the HTTP layer's dependencies (the book's `application` struct).
// Let's Go Further ch. 2.3.
type Server struct {
	cfg      config.Config
	logger   *slog.Logger
	db       Pinger
	orders   *service.OrderService
	riders   *service.RiderService
	matching *service.MatchingService
	version  string
}

// NewServer wires the dependencies together.
func NewServer(cfg config.Config, logger *slog.Logger, db Pinger, orders *service.OrderService, riders *service.RiderService, matching *service.MatchingService, version string) *Server {
	return &Server{cfg: cfg, logger: logger, db: db, orders: orders, riders: riders, matching: matching, version: version}
}
