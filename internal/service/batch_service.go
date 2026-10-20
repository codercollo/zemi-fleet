package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/codercollo/zemi-fleet/internal/batching"
	"github.com/codercollo/zemi-fleet/internal/clock"
	"github.com/codercollo/zemi-fleet/internal/config"
	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/geo"
	"github.com/codercollo/zemi-fleet/internal/metrics"
	"github.com/codercollo/zemi-fleet/internal/repository"
)

// BatchService forms and offers batches of pending orders.
type BatchService struct {
	store   repository.Store
	clock   clock.Clock
	cfg     config.BatchConfig
	metrics *metrics.Counters
}

// NewBatchService wires the service to its store, clock and batching settings.
func NewBatchService(store repository.Store, clk clock.Clock, cfg config.BatchConfig) *BatchService {
	return &BatchService{store: store, clock: clk, cfg: cfg}
}

// WithMetrics makes the service count business events in c. Without it nothing is counted.
func (s *BatchService) WithMetrics(c *metrics.Counters) *BatchService {
	s.metrics = c
	return s
}

// ExpireBatches runs one expiry sweep. The repository reports only committed
// batches, so the metric cannot count work that was rolled back.
func (s *BatchService) ExpireBatches(ctx context.Context) (int, error) {
	n, err := s.store.ExpireBatchesTx(ctx, s.clock.Now())
	if err != nil {
		return 0, err
	}
	if n > 0 {
		s.metrics.BatchesExpired(n)
	}
	return n, nil
}

// CreateBatchesInput is a validated POST /orders/batch request.
type CreateBatchesInput struct {
	RiderID       string
	RiderLocation domain.Location
	// MaxOrders is the largest batch the rider asked for.
	MaxOrders int
	// RadiusKM is the pairwise pickup radius the rider asked for.
	RadiusKM float64
}

// BatchOffer is a batch as offered to one rider.
type BatchOffer struct {
	Batch domain.Batch
	// DistanceToFirstPickupM is rider-specific: straight-line distance from the rider to the route's first stop.
	DistanceToFirstPickupM float64
}

// CreateBatches returns the open batches on offer to the rider, forming new ones from pending orders first.
//
// The effective batch size is min(max_orders, the rider's remaining capacity), read from the
// database. A rider who is off shift or at capacity gets an empty list and no transaction runs.
// The result is sorted by distance to the first pickup, then by the oldest order (FIFO).
func (s *BatchService) CreateBatches(ctx context.Context, in CreateBatchesInput) ([]BatchOffer, error) {
	rider, err := s.store.GetRider(ctx, in.RiderID)
	if err != nil {
		return nil, fmt.Errorf("get rider: %w", err)
	}

	spare := int(rider.MaxCapacity) - int(rider.CurrentOrdersCount)
	if !rider.IsAvailable || spare <= 0 {
		return []BatchOffer{}, nil
	}
	size := min(in.MaxOrders, spare)

	now := s.clock.Now()
	batches, formed, err := s.store.CreateBatchesTx(ctx, repository.CreateBatchesParams{
		RiderID:       rider.ID,
		RiderLocation: in.RiderLocation,
		Now:           now,
		TTL:           s.cfg.TTL,
		SearchRadiusM: s.cfg.SearchRadiusKM * metresPerKM,
		Batching: batching.Params{
			MaxOrders:             size,
			PickupRadiusM:         in.RadiusKM * metresPerKM,
			DirectionToleranceDeg: s.cfg.DirectionToleranceDeg,
			AvgSpeedKMH:           s.cfg.AvgSpeedKMH,
			StopDwellMin:          s.cfg.StopDwellMin,
			Ordering:              s.cfg.Ordering,
			Now:                   now,
			WeightAge:             s.cfg.WeightAge,
			WeightValue:           s.cfg.WeightValue,
			WeightRating:          s.cfg.WeightRating,
			WeightUrgency:         s.cfg.WeightUrgency,
			RadiusMode:            s.cfg.RadiusMode,
			DensityRadiusM:        s.cfg.DensityRadiusKM * metresPerKM,
			DensityLowThreshold:   s.cfg.DensityLowThreshold,
			DensityHighThreshold:  s.cfg.DensityHighThreshold,
			DensityLowRadiusM:     s.cfg.DensityLowRadiusKM * metresPerKM,
			DensityMediumRadiusM:  s.cfg.DensityMediumRadiusKM * metresPerKM,
			DensityHighRadiusM:    s.cfg.DensityHighRadiusKM * metresPerKM,
		},
	})
	if err != nil {
		return nil, err
	}

	// The transaction returns every open batch on offer, new or not; formed counts only the ones it just created.
	s.metrics.BatchesFormed(formed)

	offers := make([]BatchOffer, 0, len(batches))
	for _, b := range batches {
		offers = append(offers, BatchOffer{Batch: b, DistanceToFirstPickupM: geo.Haversine(in.RiderLocation, b.FirstPickup())})
	}
	slices.SortFunc(offers, func(a, b BatchOffer) int {
		if a.DistanceToFirstPickupM != b.DistanceToFirstPickupM {
			if a.DistanceToFirstPickupM < b.DistanceToFirstPickupM {
				return -1
			}
			return 1
		}
		if c := a.Batch.OldestOrderAt().Compare(b.Batch.OldestOrderAt()); c != 0 {
			return c
		}
		return strings.Compare(a.Batch.ID, b.Batch.ID)
	})
	return offers, nil
}

// Accept lets a rider claim an open batch exclusively. The whole decision happens in one repository
// transaction (see repository.AcceptBatchTx); this only supplies the clock's time.
// Errors: domain.ErrNotFound (rider or batch), ErrRiderUnavailable, ErrRiderAtCapacity, ErrBatchTaken.
func (s *BatchService) Accept(ctx context.Context, batchID, riderID string) (domain.Batch, error) {
	now := s.clock.Now()
	batch, alreadyHeld, err := s.store.AcceptBatchTx(ctx, repository.AcceptBatchParams{BatchID: batchID, RiderID: riderID, Now: now})
	switch {
	case errors.Is(err, domain.ErrBatchTaken):
		s.metrics.BatchAcceptConflict()
	case errors.Is(err, domain.ErrRiderAtCapacity):
		s.metrics.BatchCapacityRejection()
	case err == nil && !alreadyHeld:
		// An idempotent retry by the winner also returns 200, but it is not a second win.
		s.metrics.BatchAccepted()
	}
	return batch, err
}

// ListBatchesInput is a validated GET /batches request.
type ListBatchesInput struct {
	Point    domain.Location
	RadiusKM float64
	Page     int
	PageSize int
}

// BatchPage is one page of batches plus the total number of matches.
type BatchPage struct {
	Offers []BatchOffer
	Total  int
}

// ListBatches pages through the open, unexpired batches whose first pickup is near the given point.
// It is read-only: no offers are recorded and nothing is expired (expired batches are just not listed).
func (s *BatchService) ListBatches(ctx context.Context, in ListBatchesInput) (BatchPage, error) {
	batches, total, err := s.store.ListOpenBatches(ctx, repository.ListOpenBatchesParams{
		Point:            in.Point,
		RadiusM:          in.RadiusKM * metresPerKM,
		Now:              s.clock.Now(),
		Page:             in.Page,
		PageSize:         in.PageSize,
		SpatialQueryMode: s.cfg.SpatialQueryMode,
	})
	if err != nil {
		return BatchPage{}, err
	}
	offers := make([]BatchOffer, 0, len(batches))
	for _, b := range batches {
		offers = append(offers, BatchOffer{Batch: b, DistanceToFirstPickupM: geo.Haversine(in.Point, b.FirstPickup())})
	}
	return BatchPage{Offers: offers, Total: total}, nil
}
