package batching

import (
	"math"

	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/geo"
)

const (
	secondsPerMinute = 60.0
	metresPerKM      = 1000.0
	minutesPerHour   = 60.0
)

// Batch is a group of orders with its suggested route and metrics.
type Batch struct {
	// Orders are in FIFO order; Orders[0] is the seed.
	Orders []Order
	// Stops is the suggested pickup/delivery sequence.
	Stops []Stop

	// RouteDistanceM is the sum of the legs of Stops.
	RouteDistanceM float64
	// TripDistanceM is the sum of each order's own pickup-to-delivery distance (what the riders
	// would cover doing the orders one by one, loaded).
	TripDistanceM float64
	// Score is route_optimization_score, in (0, 1].
	Score float64
	// DurationS is the time from the first stop to the last.
	DurationS float64
	// DeliveryMinutes maps each order ID to the minutes from the first stop to that order's delivery.
	DeliveryMinutes map[string]float64
	// EarningsCents is the rider's total for the batch (sum of the orders' rider earnings, i.e. 90% of fees).
	EarningsCents int64
	// FeeCents is the total delivery fee.
	FeeCents int64
	// Centroid is the mean of the pickups; PickupSpreadM is the largest distance between two pickups.
	Centroid      domain.Location
	PickupSpreadM float64
}

// build sequences the stops and computes every metric for one batch.
//
// Formulas (also in docs/DOCUMENTATION.md):
//
//	route_distance            = sum of the stop-to-stop legs (the first stop has leg 0)
//	route_optimization_score  = min(1, sum(order trip distances) / route_distance)
//	                            "useful loaded km per km driven"; 1.0 for a batch of one, and 1.0 when
//	                            the route has no length at all (it is 0 only for degenerate batches
//	                            whose trips all have zero length)
//	stop time (seconds)       = cumulative distance to the stop / AvgSpeedKMH
//	                            + StopDwellMin * 60 * (number of EARLIER stops)
//	estimated_time_min        = the stop time of that order's delivery stop, in minutes
//	estimated_earnings        = sum(rider_earnings_cents)
func build(members []Order, p Params) Batch {
	stops := Sequence(members)

	speedMS := p.AvgSpeedKMH * metresPerKM / (minutesPerHour * secondsPerMinute)
	dwellS := p.StopDwellMin * secondsPerMinute

	var route float64
	for i := range stops {
		route += stops[i].LegDistanceM
		stops[i].ETASeconds = route/speedMS + dwellS*float64(i)
	}

	b := Batch{
		Orders:          members,
		Stops:           stops,
		RouteDistanceM:  route,
		DeliveryMinutes: make(map[string]float64, len(members)),
	}
	if len(stops) > 0 {
		b.DurationS = stops[len(stops)-1].ETASeconds
	}
	for _, s := range stops {
		if s.Type == StopDelivery {
			b.DeliveryMinutes[s.OrderID] = s.ETASeconds / secondsPerMinute
		}
	}

	var latSum, lngSum float64
	for _, o := range members {
		b.TripDistanceM += geo.Haversine(o.Pickup, o.Delivery)
		b.EarningsCents += o.RiderEarningsCents
		b.FeeCents += o.DeliveryFeeCents
		latSum += o.Pickup.Lat
		lngSum += o.Pickup.Lng
	}
	n := float64(len(members))
	b.Centroid = domain.Location{Lat: latSum / n, Lng: lngSum / n}
	for i := range members {
		for j := i + 1; j < len(members); j++ {
			b.PickupSpreadM = math.Max(b.PickupSpreadM, geo.Haversine(members[i].Pickup, members[j].Pickup))
		}
	}

	b.Score = score(b.TripDistanceM, b.RouteDistanceM)
	return b
}

// score is min(1, trip / route), and 1 when there is no route to speak of.
func score(trip, route float64) float64 {
	if route <= 0 {
		return 1
	}
	return math.Min(1, trip/route)
}
