package batching

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/geo"
)

var (
	origin = domain.Location{Lat: -1.2635, Lng: 36.8030} // Sarit Centre
	t0     = time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)

	params = Params{MaxOrders: 5, PickupRadiusM: 2000, DirectionToleranceDeg: 45, AvgSpeedKMH: 20, StopDwellMin: 2}
)

// at returns the point reached from origin after eastM metres east and northM metres north.
func at(eastM, northM float64) domain.Location {
	const mPerDeg = geo.EarthRadiusM * math.Pi / 180
	return domain.Location{
		Lat: origin.Lat + northM/mPerDeg,
		Lng: origin.Lng + eastM/(mPerDeg*math.Cos(origin.Lat*math.Pi/180)),
	}
}

// ord builds an order created ageMin minutes after t0 (so a larger number is a newer order).
func ord(id string, pickup, delivery domain.Location, ageMin int) Order {
	return Order{
		ID: id, Pickup: pickup, Delivery: delivery,
		CreatedAt:        t0.Add(time.Duration(ageMin) * time.Minute),
		DeliveryFeeCents: 20000, RiderEarningsCents: 18000,
	}
}

// northTrip is an order picked up at pickup and delivered 4 km due north of it.
func northTrip(id string, pickup domain.Location, ageMin int) Order {
	d := domain.Location{Lat: pickup.Lat + 4000/(geo.EarthRadiusM*math.Pi/180), Lng: pickup.Lng}
	return ord(id, pickup, d, ageMin)
}

func batchIDs(bs []Batch) [][]string {
	out := make([][]string, 0, len(bs))
	for _, b := range bs {
		ids := make([]string, 0, len(b.Orders))
		for _, o := range b.Orders {
			ids = append(ids, o.ID)
		}
		out = append(out, ids)
	}
	return out
}

func TestForm_EmptyAndSingle(t *testing.T) {
	t.Run("no orders gives an empty, non-nil list", func(t *testing.T) {
		for _, in := range [][]Order{nil, {}} {
			got := Form(in, params)

			assert.NotNil(t, got)
			assert.Empty(t, got)
		}
	})

	t.Run("max orders below 1 gives nothing", func(t *testing.T) {
		p := params
		p.MaxOrders = 0

		assert.Empty(t, Form([]Order{northTrip("a", origin, 0)}, p))
	})

	t.Run("one order is one batch of one, scored 1.0", func(t *testing.T) {
		got := Form([]Order{northTrip("a", origin, 0)}, params)

		require.Len(t, got, 1)
		b := got[0]
		assert.Equal(t, 1.0, b.Score)
		require.Len(t, b.Stops, 2)
		assert.Equal(t, StopPickup, b.Stops[0].Type)
		assert.Equal(t, StopDelivery, b.Stops[1].Type)
		assert.Equal(t, 0.0, b.Stops[0].LegDistanceM)
		assert.InDelta(t, 4000, b.RouteDistanceM, 1)
		assert.Equal(t, b.TripDistanceM, b.RouteDistanceM)
		assert.EqualValues(t, 18000, b.EarningsCents)
		assert.Equal(t, 0.0, b.PickupSpreadM)
		assert.Equal(t, origin, b.Centroid)
	})
}

func TestForm_Grouping(t *testing.T) {
	cluster := func(n int) []Order {
		var os []Order
		for i := range n {
			os = append(os, northTrip(fmt.Sprintf("o%02d", i), at(float64(i)*50, 0), i))
		}
		return os
	}
	tests := []struct {
		name   string
		orders []Order
		want   [][]string
	}{
		{"5 in one cluster make one batch", cluster(5), [][]string{{"o00", "o01", "o02", "o03", "o04"}}},
		{"6 in one cluster make 5 + 1, the 5 oldest first", cluster(6), [][]string{{"o00", "o01", "o02", "o03", "o04"}, {"o05"}}},
		{"8 in one cluster make 5 + 3 in FIFO order", cluster(8), [][]string{{"o00", "o01", "o02", "o03", "o04"}, {"o05", "o06", "o07"}}},
		{"opposite directions are separate batches", []Order{
			northTrip("north", origin, 0),
			ord("south", at(100, 0), at(100, -4000), 1),
		}, [][]string{{"north"}, {"south"}}},
		{"pickups 2.5 km apart are separate", []Order{
			northTrip("a", origin, 0), northTrip("b", at(2500, 0), 1),
		}, [][]string{{"a"}, {"b"}}},
		{"pickups exactly at the radius join (inclusive)", []Order{
			northTrip("a", origin, 0), northTrip("b", at(1999.9, 0), 1),
		}, [][]string{{"a", "b"}}},
		{"pairwise rule: A-B 1.5 km, B-C 1.5 km, A-C 3 km cannot all join", []Order{
			northTrip("A", origin, 0), northTrip("B", at(1500, 0), 1), northTrip("C", at(3000, 0), 2),
		}, [][]string{{"A", "B"}, {"C"}}},
		{"pairwise rule uses every member, not just the seed", []Order{
			northTrip("A", origin, 0), northTrip("B", at(-1500, 0), 1), northTrip("C", at(1500, 0), 2),
		}, [][]string{{"A", "B"}, {"C"}}}, // C is 1.5 km from the seed but 3 km from B
		{"a far order in the middle does not block later near ones", []Order{
			northTrip("a", origin, 0), northTrip("far", at(9000, 0), 1), northTrip("c", at(300, 0), 2),
		}, [][]string{{"a", "c"}, {"far"}}},
		{"input order does not matter: FIFO is by created_at", []Order{
			northTrip("late", at(100, 0), 30), northTrip("early", origin, 0), northTrip("mid", at(50, 0), 10),
		}, [][]string{{"early", "mid", "late"}}},
		{"equal created_at is broken by id", []Order{
			northTrip("b", origin, 0), northTrip("a", at(10, 0), 0),
		}, [][]string{{"a", "b"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, batchIDs(Form(tc.orders, params)))
		})
	}
}

// rotatedTrip is a 4 km trip heading bearingDeg from pickup.
func rotatedTrip(id string, pickup domain.Location, bearingDeg float64, ageMin int) Order {
	rad := bearingDeg * math.Pi / 180
	const mPerDeg = geo.EarthRadiusM * math.Pi / 180
	d := domain.Location{
		Lat: pickup.Lat + 4000*math.Cos(rad)/mPerDeg,
		Lng: pickup.Lng + 4000*math.Sin(rad)/(mPerDeg*math.Cos(pickup.Lat*math.Pi/180)),
	}
	return ord(id, pickup, d, ageMin)
}

func TestForm_DirectionTolerance(t *testing.T) {
	tests := []struct {
		name         string
		seed, other  float64
		wantTogether bool
	}{
		{"same direction", 90, 90, true},
		{"40 degrees apart is inside a 45 tolerance", 90, 130, true},
		{"50 degrees apart is outside", 90, 140, false},
		{"opposite", 0, 180, false},
		{"wraps across north: 350 and 20 are 30 apart", 350, 20, true},
		{"wraps across north the other way: 20 and 350", 20, 350, true},
		{"wraps across north but too far: 340 and 30", 340, 30, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			orders := []Order{rotatedTrip("seed", origin, tc.seed, 0), rotatedTrip("other", at(100, 0), tc.other, 1)}

			got := Form(orders, params)

			if tc.wantTogether {
				assert.Equal(t, [][]string{{"seed", "other"}}, batchIDs(got))
			} else {
				assert.Equal(t, [][]string{{"seed"}, {"other"}}, batchIDs(got))
			}
		})
	}

	t.Run("direction is measured against the seed, not the latest member", func(t *testing.T) {
		// 0, 40 and 80 degrees: each neighbour is within 45 of the next, but 80 is 80 from the seed.
		orders := []Order{
			rotatedTrip("s", origin, 0, 0), rotatedTrip("m", at(50, 0), 40, 1), rotatedTrip("e", at(100, 0), 80, 2),
		}

		assert.Equal(t, [][]string{{"s", "m"}, {"e"}}, batchIDs(Form(orders, params)))
	})

	t.Run("a zero-length trip has no direction and joins anything", func(t *testing.T) {
		orders := []Order{rotatedTrip("seed", origin, 0, 0), ord("here", at(100, 0), at(100, 0), 1)}

		assert.Equal(t, [][]string{{"seed", "here"}}, batchIDs(Form(orders, params)))
	})
}

func TestSequence(t *testing.T) {
	t.Run("nothing to sequence", func(t *testing.T) {
		assert.Empty(t, Sequence(nil))
	})

	t.Run("starts at the seed's pickup and interleaves when a pickup is nearer than a delivery", func(t *testing.T) {
		o1 := ord("o1", at(0, 0), at(0, 5000), 0)
		o2 := ord("o2", at(0, 1000), at(0, 6000), 1)

		stops := Sequence([]Order{o1, o2})

		require.Len(t, stops, 4)
		var got []string
		for _, s := range stops {
			got = append(got, fmt.Sprintf("%s:%s", s.Type, s.OrderID))
		}
		assert.Equal(t, []string{"pickup:o1", "pickup:o2", "delivery:o1", "delivery:o2"}, got)
		for i, s := range stops {
			assert.Equal(t, i+1, s.Seq)
		}
		assert.Equal(t, 0.0, stops[0].LegDistanceM)
		assert.InDelta(t, 1000, stops[1].LegDistanceM, 1)
		assert.InDelta(t, 4000, stops[2].LegDistanceM, 1)
		assert.InDelta(t, 1000, stops[3].LegDistanceM, 1)
	})

	t.Run("a delivery is never feasible before its own pickup", func(t *testing.T) {
		// o2's delivery is right next to o1's pickup, tempting nearest-neighbour to go there first.
		o1 := ord("o1", at(0, 0), at(0, 3000), 0)
		o2 := ord("o2", at(0, 2000), at(10, 5), 1)

		stops := Sequence([]Order{o1, o2})

		pos := map[string]int{}
		for i, s := range stops {
			pos[string(s.Type)+s.OrderID] = i
		}
		assert.Less(t, pos["pickupo2"], pos["deliveryo2"])
		assert.Less(t, pos["pickupo1"], pos["deliveryo1"])
	})

	t.Run("ties go to a pickup before a delivery, then to the older order", func(t *testing.T) {
		same := at(500, 0)
		o1 := ord("o1", at(0, 0), same, 0) // delivery exactly where o2 and o3 are picked up
		o2 := ord("o2", same, at(900, 0), 1)
		o3 := ord("o3", same, at(950, 0), 2)

		stops := Sequence([]Order{o1, o2, o3})

		var got []string
		for _, s := range stops[:4] {
			got = append(got, fmt.Sprintf("%s:%s", s.Type, s.OrderID))
		}
		assert.Equal(t, []string{"pickup:o1", "pickup:o2", "pickup:o3", "delivery:o1"}, got)
	})

	t.Run("identical coordinates for everything still terminates and is deterministic", func(t *testing.T) {
		p := at(0, 0)
		orders := []Order{ord("a", p, p, 0), ord("b", p, p, 1), ord("c", p, p, 2)}

		first, second := Sequence(orders), Sequence(orders)

		assert.Equal(t, first, second)
		assert.Len(t, first, 6)
	})
}

func TestScoring(t *testing.T) {
	t.Run("route distance is the sum of the legs and the score is trips divided by route", func(t *testing.T) {
		// o2 travels the opposite way, so the combined route is much longer than the trips alone.
		o1 := ord("o1", at(0, 0), at(0, 4000), 0)
		o2 := ord("o2", at(0, 500), at(0, -3500), 1)

		b := build([]Order{o1, o2}, params)

		assert.InDelta(t, 0.5+3.5+7.5, b.RouteDistanceM/1000, 0.01)
		assert.InDelta(t, 8000, b.TripDistanceM, 2)
		assert.InDelta(t, 8000.0/11500, b.Score, 0.001)
		assert.Greater(t, b.Score, 0.0)
		assert.Less(t, b.Score, 1.0)
	})

	t.Run("a route shorter than the sum of the trips is capped at 1", func(t *testing.T) {
		o1 := ord("o1", at(0, 0), at(0, 5000), 0)
		o2 := ord("o2", at(0, 1000), at(0, 6000), 1) // route 6 km for 10 km of trips

		b := build([]Order{o1, o2}, params)

		assert.Equal(t, 1.0, b.Score)
	})

	t.Run("estimated minutes: distance at the average speed plus dwell per earlier stop", func(t *testing.T) {
		// 5 km at 20 km/h = 15 min; the delivery is the 2nd stop, so one earlier stop = +2 min dwell.
		o := ord("o1", at(0, 0), at(0, 5000), 0)

		b := build([]Order{o}, params)

		assert.InDelta(t, 17.0, b.DeliveryMinutes["o1"], 0.01)
		assert.InDelta(t, 0.0, b.Stops[0].ETASeconds, 1e-9)
		assert.InDelta(t, 17.0*60, b.Stops[1].ETASeconds, 0.5)
		assert.InDelta(t, 17.0*60, b.DurationS, 0.5)
	})

	t.Run("dwell accumulates over every earlier stop", func(t *testing.T) {
		o1 := ord("o1", at(0, 0), at(0, 5000), 0)
		o2 := ord("o2", at(0, 1000), at(0, 6000), 1)

		b := build([]Order{o1, o2}, params) // stops: P1, P2, D1, D2

		// D1 is the 3rd stop (2 earlier): 5 km along the route / 20 km/h = 15 min + 2*2 = 19 min.
		assert.InDelta(t, 19.0, b.DeliveryMinutes["o1"], 0.01)
		// D2 is the 4th stop (3 earlier): 6 km = 18 min + 3*2 = 24 min.
		assert.InDelta(t, 24.0, b.DeliveryMinutes["o2"], 0.01)
	})

	t.Run("zero dwell and a different speed", func(t *testing.T) {
		p := params
		p.StopDwellMin, p.AvgSpeedKMH = 0, 30
		o := ord("o1", at(0, 0), at(0, 15000), 0) // 15 km at 30 km/h = 30 min

		b := build([]Order{o}, p)

		assert.InDelta(t, 30.0, b.DeliveryMinutes["o1"], 0.01)
	})

	t.Run("earnings are the sum of rider earnings, 90% of the fees", func(t *testing.T) {
		var orders []Order
		for i, fee := range []int64{15000, 22000, 31000} {
			o := northTrip(fmt.Sprintf("o%d", i), at(float64(i)*100, 0), i)
			o.DeliveryFeeCents = fee
			o.RiderEarningsCents = fee * 9000 / 10000
			orders = append(orders, o)
		}

		b := Form(orders, params)[0]

		assert.EqualValues(t, 68000, b.FeeCents)
		assert.EqualValues(t, 61200, b.EarningsCents)
		assert.Equal(t, float64(b.FeeCents)*0.9, float64(b.EarningsCents))
	})

	t.Run("centroid and spread of the pickups", func(t *testing.T) {
		orders := []Order{northTrip("a", at(0, 0), 0), northTrip("b", at(600, 0), 1), northTrip("c", at(1200, 0), 2)}

		b := Form(orders, params)[0]

		assert.InDelta(t, at(600, 0).Lat, b.Centroid.Lat, 1e-9)
		assert.InDelta(t, at(600, 0).Lng, b.Centroid.Lng, 1e-9)
		assert.InDelta(t, 1200, b.PickupSpreadM, 1)
	})

	t.Run("an empty route scores 1", func(t *testing.T) {
		assert.Equal(t, 1.0, score(0, 0))
		assert.Equal(t, 1.0, score(5, 0))
		assert.Equal(t, 0.5, score(1, 2))
	})
}

// Property: for random inputs, Form always returns a valid partition.
func TestForm_Properties(t *testing.T) {
	r := rand.New(rand.NewPCG(2026, 10))

	for trial := range 400 {
		n := r.IntN(40)
		p := Params{
			MaxOrders:             1 + r.IntN(5),
			PickupRadiusM:         500 + r.Float64()*2500,
			DirectionToleranceDeg: 10 + r.Float64()*120,
			AvgSpeedKMH:           5 + r.Float64()*60,
			StopDwellMin:          r.Float64() * 5,
		}
		var orders []Order
		for i := range n {
			pickup := at(r.Float64()*6000-3000, r.Float64()*6000-3000)
			delivery := at(r.Float64()*10000-5000, r.Float64()*10000-5000)
			if r.IntN(10) == 0 {
				delivery = pickup // some zero-length trips
			}
			fee := int64(10000 + r.IntN(40000))
			orders = append(orders, Order{
				ID: fmt.Sprintf("o%03d", i), Pickup: pickup, Delivery: delivery,
				CreatedAt:        t0.Add(time.Duration(r.IntN(120)) * time.Minute), // many equal timestamps
				DeliveryFeeCents: fee, RiderEarningsCents: fee * 9000 / 10000,
			})
		}
		shuffled := slices.Clone(orders)
		r.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })

		batches := Form(shuffled, p)

		seen := map[string]int{}
		var lastSeed Order
		for bi, b := range batches {
			ctx := fmt.Sprintf("trial %d batch %d", trial, bi)
			require.NotEmpty(t, b.Orders, ctx)
			require.LessOrEqual(t, len(b.Orders), p.MaxOrders, ctx)

			// FIFO: members in order, and seeds in order across batches.
			for i := 1; i < len(b.Orders); i++ {
				prev, cur := b.Orders[i-1], b.Orders[i]
				require.True(t, prev.CreatedAt.Before(cur.CreatedAt) || (prev.CreatedAt.Equal(cur.CreatedAt) && prev.ID < cur.ID), ctx)
			}
			if bi > 0 {
				require.True(t, lastSeed.CreatedAt.Before(b.Orders[0].CreatedAt) ||
					(lastSeed.CreatedAt.Equal(b.Orders[0].CreatedAt) && lastSeed.ID < b.Orders[0].ID), ctx+": seeds are oldest-first")
			}
			lastSeed = b.Orders[0]

			// every order exactly once; pairwise pickups within the radius
			for i, o := range b.Orders {
				seen[o.ID]++
				for _, other := range b.Orders[i+1:] {
					require.LessOrEqual(t, geo.Haversine(o.Pickup, other.Pickup), p.PickupRadiusM, ctx)
				}
			}

			// stops: 2 per order, sequential, every pickup before its delivery
			require.Len(t, b.Stops, 2*len(b.Orders), ctx)
			pickupAt, deliveryAt := map[string]int{}, map[string]int{}
			var route float64
			for i, s := range b.Stops {
				require.Equal(t, i+1, s.Seq, ctx)
				route += s.LegDistanceM
				if s.Type == StopPickup {
					pickupAt[s.OrderID] = s.Seq
				} else {
					deliveryAt[s.OrderID] = s.Seq
				}
			}
			require.Equal(t, StopPickup, b.Stops[0].Type, ctx)
			require.Equal(t, b.Orders[0].ID, b.Stops[0].OrderID, ctx+": route starts at the seed")
			for _, o := range b.Orders {
				require.Contains(t, pickupAt, o.ID, ctx)
				require.Contains(t, deliveryAt, o.ID, ctx)
				require.Less(t, pickupAt[o.ID], deliveryAt[o.ID], ctx)
				require.GreaterOrEqual(t, b.DeliveryMinutes[o.ID], 0.0, ctx)
			}
			require.InDelta(t, route, b.RouteDistanceM, 1e-6, ctx)

			// score in (0, 1]
			if b.TripDistanceM > 0 {
				require.Greater(t, b.Score, 0.0, ctx)
			} else {
				require.GreaterOrEqual(t, b.Score, 0.0, ctx+": only degenerate zero-length trips may score 0")
			}
			require.LessOrEqual(t, b.Score, 1.0, ctx)
			if len(b.Orders) == 1 {
				require.Equal(t, 1.0, b.Score, ctx)
			}

			// earnings are 90% of the fees (integer cents, floor per order)
			var fees, earn int64
			for _, o := range b.Orders {
				fees += o.DeliveryFeeCents
				earn += o.DeliveryFeeCents * 9000 / 10000
			}
			require.Equal(t, fees, b.FeeCents, ctx)
			require.Equal(t, earn, b.EarningsCents, ctx)
			require.InDelta(t, float64(fees)*0.9, float64(earn), float64(len(b.Orders)), ctx)
		}

		require.Len(t, seen, n, "trial %d: every order is in a batch", trial)
		for id, c := range seen {
			require.Equal(t, 1, c, "trial %d: order %s appears once", trial, id)
		}
	}
}
