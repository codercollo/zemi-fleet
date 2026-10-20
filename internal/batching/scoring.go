package batching

import (
	"math"
	"time"

	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/geo"
)

// PriorityScore ranks an order for priority batching. Age is deliberately
// unbounded, so a waiting order eventually outranks any finite value/rating/
// urgency advantage. FIFO remains the deterministic tie-break in Form.
func PriorityScore(o Order, now time.Time, p Params) float64 {
	ageHours := math.Max(now.Sub(o.CreatedAt).Hours(), 0)
	value := math.Log1p(math.Max(float64(o.ValueCents), 0))
	rating := math.Max(0, math.Min(5, o.VendorRating)) / 5
	urgency := math.Max(0, math.Min(100, float64(o.Urgency))) / 100
	return p.WeightAge*ageHours + p.WeightValue*value + p.WeightRating*rating + p.WeightUrgency*urgency
}

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
	// TripDistanceM is the sum of each order's own pickup-to-delivery distance (what riders
	// would cover doing the orders one by one, loaded).
	TripDistanceM float64
	// DistanceSavedM is TripDistanceM - RouteDistanceM. It is negative for a batch whose route is longer
	// than doing the trips separately.
	DistanceSavedM float64
	// Score is route_optimization_score, the mean detour score, in (0, 1].
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
//	distance_saved            = sum(direct trip distances) - route_distance
//	route_optimization_score  = mean over orders of the detour score (see detourScore): direct trip
//	                            distance / distance travelled between that order's pickup stop and its
//	                            delivery stop; 1.0 for a batch of one
//	stop time (seconds)       = cumulative distance to the stop / AvgSpeedKMH
//	                            + StopDwellMin * 60 * (number of EARLIER stops)
//	estimated_time_min        = the stop time of that order's delivery stop, in minutes
//	estimated_earnings        = sum(rider_earnings_cents)
func build(members []Order, p Params) Batch {
	stops := SequenceWithDistance(members, p.Distance, p.Sequencer == "two_opt")

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
		b.EarningsCents = addCents(b.EarningsCents, o.RiderEarningsCents)
		b.FeeCents = addCents(b.FeeCents, o.DeliveryFeeCents)
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

	b.DistanceSavedM = b.TripDistanceM - b.RouteDistanceM
	b.Score = detourScore(members, stops)
	return b
}

// detourScore is the mean, over the orders, of
//
//	direct trip distance / distance travelled along the route from the order's pickup stop to its delivery stop
//
// so an order delivered straight after its pickup scores 1.0, and one that rides along while the rider
// visits other stops scores less. The batch of one scores exactly 1.0 (its pickup and delivery are
// adjacent, so both distances are the same haversine). Each ratio is capped at 1 (the route between two
// points can never beat the straight line; this only absorbs float noise). A zero-length trip, which
// order creation rejects, counts as 1 instead of dividing by zero.
func detourScore(members []Order, stops []Stop) float64 {
	if len(members) == 0 {
		return 1
	}

	cum := make([]float64, len(stops)) // distance travelled by the time each stop is reached
	var run float64
	for i, s := range stops {
		run += s.LegDistanceM
		cum[i] = run
	}
	pickupAt, deliveryAt := make(map[string]int, len(members)), make(map[string]int, len(members))
	for i, s := range stops {
		if s.Type == StopPickup {
			pickupAt[s.OrderID] = i
		} else {
			deliveryAt[s.OrderID] = i
		}
	}

	var sum float64
	for _, o := range members {
		direct := geo.Haversine(o.Pickup, o.Delivery)
		along := cum[deliveryAt[o.ID]] - cum[pickupAt[o.ID]]
		if direct <= 0 || along <= 0 {
			sum++
			continue
		}
		sum += math.Min(1, direct/along)
	}
	return sum / float64(len(members))
}

// addCents adds two amounts of cents without wrapping around. On overflow it saturates at the int64 limit instead
// of silently turning a huge total into a negative one. The configured per-order maximums (ORDER_MAX_VALUE_KES and
// ORDER_MAX_DELIVERY_FEE_KES) keep real totals many orders of magnitude below that limit, so this is a guard,
// not a code path that normal data takes. Saturating both the fee total and the earnings total keeps
// earnings <= fees, which the batches table CHECKs.
func addCents(a, b int64) int64 {
	switch {
	case b > 0 && a > math.MaxInt64-b:
		return math.MaxInt64
	case b < 0 && a < math.MinInt64-b:
		return math.MinInt64
	}
	return a + b
}
