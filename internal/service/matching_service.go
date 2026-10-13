package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/codercollo/zemi-fleet/internal/clock"
	"github.com/codercollo/zemi-fleet/internal/config"
	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/geo"
	"github.com/codercollo/zemi-fleet/internal/matching"
	"github.com/codercollo/zemi-fleet/internal/repository"
	"github.com/codercollo/zemi-fleet/internal/repository/sqlc"
)

const metresPerKM = 1000

// MatchingService finds pending pickups for a moving rider.
type MatchingService struct {
	store  repository.Store
	clock  clock.Clock
	logger *slog.Logger
	params matching.ConeParams
}

// NewMatchingService wires the service. The cone settings come from config (kilometres there,
// metres in the algorithm).
func NewMatchingService(store repository.Store, clk clock.Clock, logger *slog.Logger, cone config.ConeConfig) *MatchingService {
	return &MatchingService{
		store:  store,
		clock:  clk,
		logger: logger,
		params: matching.ConeParams{
			RadiusM:      cone.RadiusKM * metresPerKM,
			HalfAngleDeg: cone.HalfAngleDeg,
			MinSpeedKMH:  cone.MinSpeedKMH,
			MaxResults:   cone.MaxResults,
		},
	}
}

// MatchOrdersInput is a validated match-orders request.
type MatchOrdersInput struct {
	RiderID  string
	Position domain.Location
	// HeadingDeg and SpeedKMH are nil when the app could not read them.
	HeadingDeg *float64
	SpeedKMH   *float64
	// ReportedOrdersCount is the count the app believes it has. It is NOT used for any decision:
	// see MatchOrders.
	ReportedOrdersCount int
}

// MatchOrdersResult is the outcome of a match-orders call.
type MatchOrdersResult struct {
	Matches                []matching.Match // never nil
	CanAcceptMore          bool
	MaxAdditional          int
	DirectionFilterApplied bool
}

// MatchOrders records the rider's position and returns the pending pickups ahead of them.
//
// The database is the source of truth for capacity: can_accept_more_orders and
// max_additional_orders (= max_capacity - current_orders_count) use the stored counts, because
// the server is what assigns orders, so its count cannot be stale the way a phone's can. The
// count in the request is validated by the handler but only logged here when it disagrees,
// which flags an app that is out of sync without trusting it.
//
// A rider who is unavailable or already at capacity gets an empty list and the candidate
// query is not run.
func (s *MatchingService) MatchOrders(ctx context.Context, in MatchOrdersInput) (MatchOrdersResult, error) {
	rider, err := s.store.GetRider(ctx, in.RiderID)
	if err != nil {
		return MatchOrdersResult{}, fmt.Errorf("get rider: %w", err)
	}

	if in.ReportedOrdersCount != int(rider.CurrentOrdersCount) {
		s.logger.WarnContext(ctx, "rider reported a different order count than the server holds; using the server's",
			"rider_id", rider.ID, "reported", in.ReportedOrdersCount, "server", rider.CurrentOrdersCount)
	}

	// Same rules as PATCH /location for the reading itself (an omitted bearing or speed is stored
	// as NULL; the timestamp is the clock's), but with no optimistic version check and no version
	// bump: telemetry is last-write-wins, so it can never fail with a 409 and never makes the
	// rider's own versioned writes conflict.
	if err := s.store.UpdateRiderTelemetry(ctx, sqlc.UpdateRiderTelemetryParams{
		LastLat:           &in.Position.Lat,
		LastLng:           &in.Position.Lng,
		LastBearingDeg:    in.HeadingDeg,
		LastSpeedKmh:      in.SpeedKMH,
		LocationUpdatedAt: ptr(s.clock.Now()),
		ID:                rider.ID,
	}); err != nil {
		return MatchOrdersResult{}, fmt.Errorf("store rider position: %w", err)
	}

	spare := int(rider.MaxCapacity) - int(rider.CurrentOrdersCount)
	if !rider.IsAvailable || spare <= 0 {
		return MatchOrdersResult{Matches: []matching.Match{}}, nil
	}

	// A batch nobody accepted in time still holds its orders as 'batched'. Release them now, so the
	// rider is offered them instead of them staying invisible until someone next asks for batches.
	if _, err := s.store.ExpireBatchesTx(ctx, s.clock.Now()); err != nil {
		return MatchOrdersResult{}, fmt.Errorf("expire stale batches: %w", err)
	}

	minLat, maxLat, minLng, maxLng := geo.BoundingBox(in.Position, s.params.RadiusM)
	rows, err := s.store.ListPendingOrdersInBox(ctx, sqlc.ListPendingOrdersInBoxParams{
		MinLat: minLat, MaxLat: maxLat, MinLng: minLng, MaxLng: maxLng,
	})
	if err != nil {
		return MatchOrdersResult{}, fmt.Errorf("list pending orders: %w", err)
	}

	candidates := make([]matching.Candidate, 0, len(rows))
	for _, r := range rows {
		candidates = append(candidates, matching.Candidate{
			OrderID:            r.ID,
			Pickup:             domain.Location{Lat: r.PickupLat, Lng: r.PickupLng},
			CreatedAt:          r.CreatedAt,
			RiderEarningsCents: deref(r.RiderEarningsCents),
		})
	}

	cone := matching.MatchCone(matching.Rider{
		Position:   in.Position,
		HeadingDeg: in.HeadingDeg,
		SpeedKMH:   in.SpeedKMH,
	}, candidates, s.params, s.clock.Now())

	return MatchOrdersResult{
		Matches:                cone.Matches,
		CanAcceptMore:          true,
		MaxAdditional:          spare,
		DirectionFilterApplied: cone.DirectionFilterApplied,
	}, nil
}
