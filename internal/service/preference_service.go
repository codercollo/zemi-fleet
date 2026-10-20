package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/codercollo/zemi-fleet/internal/clock"
	"github.com/codercollo/zemi-fleet/internal/config"
	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/geo"
	"github.com/codercollo/zemi-fleet/internal/matching"
	"github.com/codercollo/zemi-fleet/internal/metrics"
	"github.com/codercollo/zemi-fleet/internal/repository"
	"github.com/codercollo/zemi-fleet/internal/repository/sqlc"
)

// PreferenceService ranks riders for an order by their preferences.
type PreferenceService struct {
	store    repository.Store
	clock    clock.Clock
	params   matching.PrefParams
	minTripM float64
	metrics  *metrics.Counters
	capacity capacityPolicy
}

// NewPreferenceService wires the service. Distances are kilometres in config and metres in the algorithm.
func NewPreferenceService(store repository.Store, clk clock.Clock, cfg config.MatchConfig, minTripM int, capacityCfg ...config.CapacityConfig) *PreferenceService {
	var cc config.CapacityConfig
	if len(capacityCfg) > 0 {
		cc = capacityCfg[0]
	}
	return &PreferenceService{
		store: store,
		clock: clk,
		params: matching.PrefParams{
			MaxDistanceM: cfg.MaxDistanceKM * metresPerKM,
			StaleAfter:   cfg.LocationStaleAfter,
			MaxResults:   cfg.MaxResults,
			Weights: matching.Weights{
				Category: cfg.WeightCategory, Area: cfg.WeightArea,
				Proximity: cfg.WeightProximity, Acceptance: cfg.WeightAcceptance,
			},
		},
		minTripM: float64(minTripM),
		capacity: newCapacityPolicy(cc),
	}
}

// WithMetrics makes the service count business events in c. Without it nothing is counted.
func (s *PreferenceService) WithMetrics(c *metrics.Counters) *PreferenceService {
	s.metrics = c
	return s
}

// OrderDetails describes an order that may not be saved yet.
type OrderDetails struct {
	Category   string
	Pickup     domain.Location
	Delivery   domain.Location
	ValueCents int64
}

// MatchRidersInput is a validated POST /orders/match-riders request. When Details is set it wins, and
// the stored order (if any) is not read; otherwise OrderID names a stored order.
type MatchRidersInput struct {
	OrderID string
	Details *OrderDetails
}

// MatchRiders returns the riders who suit the order, best first (see matching.MatchRiders for the rules).
// Errors: domain.ErrNotFound for an unknown order_id (when no details were sent), and a
// *domain.ValidationError for an unknown category or too short a trip in the details.
func (s *PreferenceService) MatchRiders(ctx context.Context, in MatchRidersInput) ([]matching.RiderMatch, error) {
	order, err := s.resolveOrder(ctx, in)
	if err != nil {
		return nil, err
	}

	rows, err := s.store.ListMatchCandidateRiders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list candidate riders: %w", err)
	}
	if len(rows) == 0 {
		s.metrics.RiderMatchServed()
		return []matching.RiderMatch{}, nil
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}

	catRows, err := s.store.ListPreferredCategoriesForRiders(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list preferred categories: %w", err)
	}
	areaRows, err := s.store.ListPreferredAreasForRiders(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list preferred areas: %w", err)
	}
	categories := make(map[string][]string, len(rows))
	for _, c := range catRows {
		categories[c.RiderID] = append(categories[c.RiderID], c.CategoryCode)
	}
	areas := make(map[string][]matching.PrefArea, len(rows))
	for _, a := range areaRows {
		areas[a.RiderID] = append(areas[a.RiderID], matching.PrefArea{
			Name: a.Name, Center: domain.Location{Lat: a.CenterLat, Lng: a.CenterLng}, RadiusM: float64(a.RadiusM),
		})
	}

	riders := make([]matching.PrefRider, 0, len(rows))
	for _, r := range rows {
		effective, err := s.capacity.effective(ctx, s.store, sqlc.Rider{ID: r.ID, MaxCapacity: r.MaxCapacity, CurrentOrdersCount: r.CurrentOrdersCount}, s.clock.Now())
		if err != nil {
			return nil, fmt.Errorf("calculate rider capacity: %w", err)
		}
		if int(r.CurrentOrdersCount) >= effective {
			continue
		}
		pr := matching.PrefRider{
			ID:                 r.ID,
			Available:          r.IsAvailable,
			CurrentOrders:      int(r.CurrentOrdersCount),
			MaxCapacity:        int(r.MaxCapacity),
			MinOrderValueCents: r.MinOrderValueCents,
			Categories:         categories[r.ID],
			Areas:              areas[r.ID],
			LocationUpdatedAt:  r.LocationUpdatedAt,
			OffersCount:        int(r.OffersCount),
			AcceptedCount:      int(r.AcceptedCount),
			AcceptanceRate:     deref(r.AcceptanceRate),
		}
		// The riders table CHECKs that lat and lng are both set or both NULL.
		if r.LastLat != nil && r.LastLng != nil {
			pr.Location = &domain.Location{Lat: *r.LastLat, Lng: *r.LastLng}
		}
		riders = append(riders, pr)
	}

	s.metrics.RiderMatchServed()
	return matching.MatchRiders(order, riders, s.params, s.clock.Now()), nil
}

// resolveOrder turns the request into the order to match for: the details if sent (validated),
// otherwise the stored order.
func (s *PreferenceService) resolveOrder(ctx context.Context, in MatchRidersInput) (matching.PrefOrder, error) {
	if d := in.Details; d != nil {
		fields := map[string]string{}
		if _, err := s.store.GetCategory(ctx, d.Category); errors.Is(err, domain.ErrNotFound) {
			fields["order_details.category"] = "category does not exist"
		} else if err != nil {
			return matching.PrefOrder{}, fmt.Errorf("get category: %w", err)
		}
		if dist := geo.Haversine(d.Pickup, d.Delivery); dist < s.minTripM {
			fields["order_details.delivery_location"] = fmt.Sprintf("must be at least %.0f m from the pickup; this one is %.0f m", s.minTripM, dist)
		}
		if len(fields) > 0 {
			return matching.PrefOrder{}, &domain.ValidationError{Fields: fields}
		}
		return matching.PrefOrder{Category: d.Category, Pickup: d.Pickup, ValueCents: d.ValueCents}, nil
	}

	o, err := s.store.GetOrder(ctx, in.OrderID)
	if err != nil {
		return matching.PrefOrder{}, fmt.Errorf("get order: %w", err)
	}
	return matching.PrefOrder{
		Category:   o.CategoryCode,
		Pickup:     domain.Location{Lat: o.PickupLat, Lng: o.PickupLng},
		ValueCents: o.ValueCents,
	}, nil
}
