// Package batching groups pending orders into batches a rider can take together, sequences the
// stops, and scores the result. It is pure: no database, config, clock or HTTP imports. Every
// tunable arrives in Params, and the geometry comes from internal/geo.
package batching

import (
	"math"
	"slices"
	"strings"
	"time"

	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/geo"
)

// Order is a pending order as the batcher sees it.
type Order struct {
	ID                 string
	Pickup             domain.Location
	Delivery           domain.Location
	CreatedAt          time.Time
	DeliveryFeeCents   int64
	RiderEarningsCents int64
	ValueCents         int64
	VendorRating       float64
	Urgency            int
}

// Params are the batching rules; in production they come from config (and the request).
type Params struct {
	// MaxOrders caps a batch's size.
	MaxOrders int
	// PickupRadiusM is the largest allowed distance between ANY two pickups in a batch.
	PickupRadiusM float64
	// DirectionToleranceDeg is the largest allowed difference between a trip's bearing and the seed's.
	DirectionToleranceDeg float64
	// AvgSpeedKMH and StopDwellMin turn the route into time estimates (see scoring.go).
	AvgSpeedKMH                                                 float64
	StopDwellMin                                                float64
	Ordering                                                    string
	Now                                                         time.Time
	WeightAge, WeightValue, WeightRating, WeightUrgency         float64
	RadiusMode                                                  string
	DensityRadiusM                                              float64
	DensityLowThreshold, DensityHighThreshold                   int
	DensityLowRadiusM, DensityMediumRadiusM, DensityHighRadiusM float64
}

// Form groups the orders into batches. It is deterministic.
//
//  1. Sort by priority when enabled, then created_at and id (FIFO).
//  2. The seed is the oldest order not yet in a batch.
//  3. Walk the remaining unused orders in FIFO order and add one when
//     (a) its pickup is within PickupRadiusM of EVERY pickup already in the batch, not just the
//     seed's (the brief says pickups are "within 2 km of each other": A-B 1.5 km and B-C 1.5 km
//     with A-C 3 km must not all join), and
//     (b) its trip bearing is within DirectionToleranceDeg of the seed's trip bearing;
//     stop at MaxOrders.
//  4. An order with no partners becomes a batch of one, so it is still offered.
//  5. Repeat until every order is in exactly one batch.
//
// Each returned batch has its stops sequenced and its metrics computed.
func Form(orders []Order, p Params) []Batch {
	batches := []Batch{}
	if len(orders) == 0 || p.MaxOrders < 1 {
		return batches
	}

	fifo := slices.Clone(orders)
	slices.SortStableFunc(fifo, func(a, b Order) int {
		if p.Ordering == "priority" {
			sa, sb := PriorityScore(a, p.Now, p), PriorityScore(b, p.Now, p)
			if math.Abs(sa-sb) > 1e-9 {
				if sa > sb {
					return -1
				}
				return 1
			}
		}
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})

	used := make([]bool, len(fifo))
	for i, seed := range fifo {
		if used[i] {
			continue
		}
		used[i] = true
		members := []Order{seed}
		radius := p.PickupRadiusM
		if p.RadiusMode == "adaptive" {
			radius = adaptiveRadius(seed.Pickup, fifo, p)
		}
		formParams := p
		formParams.PickupRadiusM = radius

		for j := i + 1; j < len(fifo) && len(members) < p.MaxOrders; j++ {
			if used[j] {
				continue
			}
			if fits(fifo[j], seed, members, formParams) {
				used[j] = true
				members = append(members, fifo[j])
			}
		}
		batches = append(batches, build(members, p))
	}
	return batches
}

func adaptiveRadius(seed domain.Location, orders []Order, p Params) float64 {
	if p.DensityRadiusM <= 0 {
		return p.PickupRadiusM
	}
	points := make([]domain.Location, 0, len(orders))
	for _, order := range orders {
		points = append(points, order.Pickup)
	}
	density := geo.Density(seed, points, p.DensityRadiusM)
	radius := p.DensityLowRadiusM
	if density >= p.DensityHighThreshold {
		radius = p.DensityHighRadiusM
	} else if density >= p.DensityLowThreshold {
		radius = p.DensityMediumRadiusM
	}
	if radius <= 0 {
		return p.PickupRadiusM
	}
	return math.Min(radius, p.PickupRadiusM)
}

// fits reports whether candidate may join the batch of members (whose first element is the seed).
func fits(candidate, seed Order, members []Order, p Params) bool {
	for _, m := range members {
		if geo.Haversine(m.Pickup, candidate.Pickup) > p.PickupRadiusM {
			return false
		}
	}
	return directionCompatible(seed, candidate, p.DirectionToleranceDeg)
}

// directionCompatible reports whether two trips head in similar directions. Order creation rejects
// trips shorter than ORDER_MIN_TRIP_M, so every trip has a real direction; should a zero-length trip
// ever get here, geo.Bearing returns 0 for it rather than failing.
func directionCompatible(a, b Order, toleranceDeg float64) bool {
	diff := geo.AngleDiff(geo.Bearing(a.Pickup, a.Delivery), geo.Bearing(b.Pickup, b.Delivery))
	return diff <= toleranceDeg && diff >= -toleranceDeg
}
