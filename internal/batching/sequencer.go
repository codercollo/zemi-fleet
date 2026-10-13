package batching

import (
	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/geo"
)

// StopType says whether a stop collects or drops off an order.
type StopType string

const (
	StopPickup   StopType = "pickup"
	StopDelivery StopType = "delivery"
)

// Stop is one step of a batch's suggested route.
type Stop struct {
	Seq          int // 1-based
	Type         StopType
	OrderID      string
	Location     domain.Location
	LegDistanceM float64 // from the previous stop; 0 for the first stop
	// ETASeconds is the time from the first stop to arriving at this stop (filled in by scoring).
	ETASeconds float64
}

// Sequence orders the stops of a batch by nearest neighbour:
//
//   - the route starts at the seed's pickup (orders[0], the oldest order);
//   - from the current position, go to the nearest FEASIBLE stop: the pickup of any order not yet
//     collected, or the delivery of an order already collected and not yet delivered. A delivery is
//     never feasible before its own pickup, so every pickup precedes its delivery;
//   - ties go to a pickup before a delivery, then to the older order, so the result is deterministic.
//
// Nearest neighbour is a fast heuristic, not an optimal tour; see "Limitations" in docs/DOCUMENTATION.md.
// orders must be in FIFO order with the seed first, as Form provides.
func Sequence(orders []Order) []Stop {
	if len(orders) == 0 {
		return []Stop{}
	}

	type candidate struct {
		idx  int // index into orders: FIFO rank, used for tie-breaking
		kind StopType
		at   domain.Location
	}

	picked := make([]bool, len(orders))
	delivered := make([]bool, len(orders))
	stops := make([]Stop, 0, 2*len(orders))

	add := func(idx int, kind StopType, at domain.Location, leg float64) {
		stops = append(stops, Stop{Seq: len(stops) + 1, Type: kind, OrderID: orders[idx].ID, Location: at, LegDistanceM: leg})
	}

	current := orders[0].Pickup
	picked[0] = true
	add(0, StopPickup, current, 0)

	for len(stops) < 2*len(orders) {
		var best candidate
		bestDist, found := 0.0, false
		consider := func(c candidate) {
			d := geo.Haversine(current, c.at)
			if !found || d < bestDist || (d == bestDist && tieBreak(c.kind, c.idx, best.kind, best.idx)) {
				best, bestDist, found = c, d, true
			}
		}
		for i, o := range orders {
			switch {
			case !picked[i]:
				consider(candidate{i, StopPickup, o.Pickup})
			case !delivered[i]:
				consider(candidate{i, StopDelivery, o.Delivery})
			}
		}

		add(best.idx, best.kind, best.at, bestDist)
		current = best.at
		if best.kind == StopPickup {
			picked[best.idx] = true
		} else {
			delivered[best.idx] = true
		}
	}
	return stops
}

// tieBreak reports whether (kind, idx) beats (otherKind, otherIdx) at equal distance:
// pickups before deliveries, then the older (lower FIFO index) order.
func tieBreak(kind StopType, idx int, otherKind StopType, otherIdx int) bool {
	if kind != otherKind {
		return kind == StopPickup
	}
	return idx < otherIdx
}
