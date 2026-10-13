// Package service holds the business rules. It calls the repository and never touches HTTP.
package service

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/repository"
	"github.com/codercollo/zemi-fleet/internal/repository/sqlc"
	"github.com/codercollo/zemi-fleet/internal/util"
)

// OrderService creates and reads orders.
type OrderService struct {
	store repository.Store
	// payoutBPS is the rider's share of the delivery fee (RIDER_PAYOUT_BPS). It is
	// copied onto each order so later config changes never alter past earnings.
	payoutBPS int
}

// NewOrderService wires the service to its store and the configured payout share.
func NewOrderService(store repository.Store, payoutBPS int) *OrderService {
	return &OrderService{store: store, payoutBPS: payoutBPS}
}

// CreateOrderInput is a validated order request. Money is in cents.
type CreateOrderInput struct {
	VendorID         string
	CategoryCode     string
	Delivery         domain.Location
	ValueCents       int64
	DeliveryFeeCents int64
}

// Create stores a new pending order. The pickup point is snapshotted from the
// vendor, and trip distance and bearing are computed once here because batching
// reads them for every pending order.
func (s *OrderService) Create(ctx context.Context, in CreateOrderInput) (domain.Order, error) {
	fields := map[string]string{}

	vendor, err := s.store.GetVendor(ctx, in.VendorID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		fields["vendor_id"] = "vendor does not exist"
	case err != nil:
		return domain.Order{}, fmt.Errorf("get vendor: %w", err)
	}

	if _, err := s.store.GetCategory(ctx, in.CategoryCode); errors.Is(err, domain.ErrNotFound) {
		fields["category"] = "category does not exist"
	} else if err != nil {
		return domain.Order{}, fmt.Errorf("get category: %w", err)
	}

	if len(fields) > 0 {
		return domain.Order{}, &domain.ValidationError{Fields: fields}
	}

	pickup := domain.Location{Lat: vendor.Lat, Lng: vendor.Lng}
	row, err := s.store.CreateOrder(ctx, sqlc.CreateOrderParams{
		ID:               util.NewOrderID(),
		VendorID:         vendor.ID,
		CategoryCode:     in.CategoryCode,
		PickupLat:        pickup.Lat,
		PickupLng:        pickup.Lng,
		DeliveryLat:      in.Delivery.Lat,
		DeliveryLng:      in.Delivery.Lng,
		TripDistanceM:    int32(math.Round(haversineM(pickup, in.Delivery))),
		TripBearingDeg:   float32(roundBearing(bearingDeg(pickup, in.Delivery))),
		ValueCents:       in.ValueCents,
		DeliveryFeeCents: in.DeliveryFeeCents,
		RiderPayoutBps:   int32(s.payoutBPS),
	})
	if err != nil {
		return domain.Order{}, fmt.Errorf("create order: %w", err)
	}
	return orderFromRow(row), nil
}

// Get returns one order, or domain.ErrNotFound.
func (s *OrderService) Get(ctx context.Context, id string) (domain.Order, error) {
	row, err := s.store.GetOrder(ctx, id)
	if err != nil {
		return domain.Order{}, fmt.Errorf("get order: %w", err)
	}
	return orderFromRow(row), nil
}

// roundBearing rounds to one decimal and wraps 360.0 back to 0, because the
// column is REAL and its CHECK requires [0, 360).
func roundBearing(deg float64) float64 {
	return math.Mod(math.Round(deg*10)/10, 360)
}
