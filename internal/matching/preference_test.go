package matching

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/geo"
)

var (
	// A Kilimani pickup (Yaya Centre) for an electronics order worth KES 5,000.
	pickup    = domain.Location{Lat: -1.2917, Lng: 36.7878}
	elecOrder = PrefOrder{Category: "electronics", Pickup: pickup, ValueCents: 500000}

	prefParams = PrefParams{
		MaxDistanceM: 10_000, StaleAfter: 30 * time.Minute, MaxResults: 10,
		Weights: Weights{Category: 0.25, Area: 0.20, Proximity: 0.35, Acceptance: 0.20},
	}
)

// kilimani: centre -1.2905, 36.7870, radius 2500 m (as seeded).
// mPerDeg is the length of one degree of latitude on the sphere geo uses.
const mPerDeg = geo.EarthRadiusM * math.Pi / 180

var kilimani = PrefArea{Name: "Kilimani", Center: domain.Location{Lat: -1.2905, Lng: 36.7870}, RadiusM: 2500}

// rider builds an available, empty rider with a fresh location dKm east... of the pickup (nil = unknown).
func rider(id string, mods ...func(*PrefRider)) PrefRider {
	r := PrefRider{ID: id, Available: true, CurrentOrders: 0, MaxCapacity: 5}
	for _, m := range mods {
		m(&r)
	}
	return r
}

func at(distM float64) func(*PrefRider) { return atAge(distM, time.Minute) }

// atAge places the rider distM north of the pickup, last seen age ago.
func atAge(distM float64, age time.Duration) func(*PrefRider) {
	return func(r *PrefRider) {
		loc := domain.Location{Lat: pickup.Lat + distM/mPerDeg, Lng: pickup.Lng}
		seen := now.Add(-age)
		r.Location, r.LocationUpdatedAt = &loc, &seen
	}
}

// history gives the rider offers/accepted counts (and the raw rate that goes with them).
func history(offers, accepted int) func(*PrefRider) {
	return func(r *PrefRider) {
		r.OffersCount, r.AcceptedCount = offers, accepted
		if offers > 0 {
			r.AcceptanceRate = float64(accepted) / float64(offers)
		}
	}
}
func cats(c ...string) func(*PrefRider)    { return func(r *PrefRider) { r.Categories = c } }
func areas(a ...PrefArea) func(*PrefRider) { return func(r *PrefRider) { r.Areas = a } }
func minValue(cents int64) func(*PrefRider) {
	return func(r *PrefRider) { r.MinOrderValueCents = cents }
}
func busy(n, capacity int) func(*PrefRider) {
	return func(r *PrefRider) { r.CurrentOrders, r.MaxCapacity = n, capacity }
}
func offShift() func(*PrefRider) { return func(r *PrefRider) { r.Available = false } }

func riderIDs(ms []RiderMatch) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.RiderID)
	}
	return out
}

func TestMatchRiders_EachHardFilterOnItsOwn(t *testing.T) {
	tests := []struct {
		name string
		r    PrefRider
		want bool
	}{
		// 1. availability
		{"available", rider("r"), true},
		{"not available", rider("r", offShift()), false},
		// 2. capacity
		{"one slot left", rider("r", busy(4, 5)), true},
		{"at capacity", rider("r", busy(5, 5)), false},
		{"at a lower personal capacity", rider("r", busy(3, 3)), false},
		// 3. minimum order value
		{"value exactly the minimum is in", rider("r", minValue(500000)), true},
		{"1 KES below the minimum is out", rider("r", minValue(500100)), false},
		{"a minimum far below the value is in", rider("r", minValue(100)), true},
		// 4. category
		{"prefers this category", rider("r", cats("electronics")), true},
		{"prefers this among several", rider("r", cats("food", "electronics", "fragile")), true},
		{"prefers other categories only", rider("r", cats("food", "groceries")), false},
		// 5. area
		{"pickup inside the preferred area", rider("r", areas(kilimani)), true},
		{"pickup outside every preferred area", rider("r", areas(PrefArea{Name: "CBD", Center: domain.Location{Lat: -1.2864, Lng: 36.8172}, RadiusM: 2000})), false},
		{"inside one of several areas", rider("r", areas(PrefArea{Name: "CBD", Center: domain.Location{Lat: -1.2864, Lng: 36.8172}, RadiusM: 2000}, kilimani)), true},
		// 6. distance (only for a known, fresh location)
		{"fresh location 5 km away", rider("r", at(5000)), true},
		{"fresh location exactly at the limit", rider("r", at(10_000)), true},
		{"fresh location 10.1 km away", rider("r", at(10_100)), false},
		{"stale location 50 km away is treated as unknown, not filtered", rider("r", atAge(50_000, time.Hour)), true},
		{"no location at all is not filtered", rider("r"), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := MatchRiders(elecOrder, []PrefRider{tc.r}, prefParams, now)

			assert.Equal(t, tc.want, len(got) == 1, "got %v", riderIDs(got))
		})
	}
}

func TestMatchRiders_NoPreferencesMatchesEverythingAtNeutral(t *testing.T) {
	cases := []PrefOrder{
		elecOrder,
		{Category: "food", Pickup: domain.Location{Lat: -1.30, Lng: 36.83}, ValueCents: 1},
		{Category: "anything-at-all", Pickup: domain.Location{Lat: 0, Lng: 0}, ValueCents: 99999999},
	}
	for _, o := range cases {
		got := MatchRiders(o, []PrefRider{rider("r")}, prefParams, now)

		require.Len(t, got, 1, o.Category)
		assert.Empty(t, got[0].MatchingPreferences)
		assert.NotNil(t, got[0].MatchingPreferences, "an empty array, not null")
		// neutral category and area (0.5 each), unknown location (0), acceptance 0:
		assert.InDelta(t, 0.325, got[0].Score, 0.0051, "neutral category, area and a neutral newcomer acceptance of 0.5")
		assert.Nil(t, got[0].DistanceM)
	}
}

func TestMatchRiders_AreaBoundaryIsIncluded(t *testing.T) {
	// A pickup exactly radius metres from the area centre.
	centre := domain.Location{Lat: -1.30, Lng: 36.80}
	edge := PrefArea{Name: "Edge", Center: centre, RadiusM: 1500}
	pickupOnEdge := domain.Location{Lat: centre.Lat + 1500/mPerDeg, Lng: centre.Lng}
	inside := PrefOrder{Category: "food", Pickup: pickupOnEdge, ValueCents: 100}
	outside := PrefOrder{Category: "food", Pickup: domain.Location{Lat: centre.Lat + 1501/mPerDeg, Lng: centre.Lng}, ValueCents: 100}

	assert.Len(t, MatchRiders(inside, []PrefRider{rider("r", areas(edge))}, prefParams, now), 1, "on the boundary is in")
	assert.Empty(t, MatchRiders(outside, []PrefRider{rider("r", areas(edge))}, prefParams, now), "1 m beyond is out")
}

func TestMatchRiders_StaleAndMissingLocationsAreUnknown(t *testing.T) {
	tests := []struct {
		name      string
		r         PrefRider
		wantKnown bool
	}{
		{"fresh", rider("r", at(2000)), true},
		{"seen exactly StaleAfter ago is still fresh", rider("r", atAge(2000, 30*time.Minute)), true},
		{"seen 30m01s ago is stale", rider("r", atAge(2000, 30*time.Minute+time.Second)), false},
		{"seen days ago is stale", rider("r", atAge(2000, 72*time.Hour)), false},
		{"never reported a location", rider("r"), false},
		{"a location without a timestamp cannot be trusted", rider("r", func(r *PrefRider) { l := pickup; r.Location = &l }), false},
		{"a timestamp in the future (clock skew) counts as fresh", rider("r", atAge(2000, -5*time.Minute)), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := MatchRiders(elecOrder, []PrefRider{tc.r}, prefParams, now)

			require.Len(t, got, 1, "unknown location is never a reason to exclude")
			if tc.wantKnown {
				require.NotNil(t, got[0].DistanceM)
				assert.InDelta(t, 2000, *got[0].DistanceM, 5)
				assert.InDelta(t, 0.25*0.5+0.20*0.5+0.20*0.5+0.35*0.8, got[0].Score, 0.0051)
			} else {
				assert.Nil(t, got[0].DistanceM, "distance is null when unknown")
				assert.InDelta(t, 0.325, got[0].Score, 0.0051, "proximity scores 0; the rest is neutral")
			}
		})
	}
}

func TestMatchRiders_ScoreComponents(t *testing.T) {
	tests := []struct {
		name  string
		r     PrefRider
		want  float64
		prefs []string
	}{
		{"nothing known: neutral category, area and newcomer acceptance", rider("r"), 0.325, []string{}},
		{"explicit category", rider("r", cats("electronics")), 0.25 + 0.10 + 0.10, []string{"electronics"}},
		{"explicit area", rider("r", areas(kilimani)), 0.125 + 0.20 + 0.10, []string{"Kilimani"}},
		{"explicit category and area", rider("r", cats("electronics"), areas(kilimani)), 0.25 + 0.20 + 0.10, []string{"electronics", "Kilimani"}},
		{"strong history: 45 of 50 accepted is 46/52", rider("r", history(50, 45)), 0.225 + 0.20*46.0/52, []string{}},
		{"poor history: 1 of 20 accepted is 2/22", rider("r", history(20, 1)), 0.225 + 0.20*2.0/22, []string{}},
		{"one lucky offer is not a perfect score: 1 of 1 is 2/3", rider("r", history(1, 1)), 0.225 + 0.20*2.0/3, []string{}},
		{"at the pickup: full proximity", rider("r", at(0)), 0.325 + 0.35, []string{}},
		{"half the max distance: half proximity", rider("r", at(5000)), 0.325 + 0.175, []string{}},
		{"at the max distance: no proximity", rider("r", at(10_000)), 0.325, []string{}},
		{"everything: explicit, at the pickup, a long perfect record (998/1000 smoothed)", rider("r", cats("electronics"), areas(kilimani), at(0), history(1000, 1000)), 0.25 + 0.20 + 0.35 + 0.20*1001.0/1002, []string{"electronics", "Kilimani"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := MatchRiders(elecOrder, []PrefRider{tc.r}, prefParams, now)

			require.Len(t, got, 1)
			assert.InDelta(t, tc.want, got[0].Score, 0.0051, "rounded to 2 decimals")
			assert.Equal(t, math.Round(got[0].Score*100)/100, got[0].Score, "already rounded")
			assert.Equal(t, tc.prefs, got[0].MatchingPreferences)
		})
	}
}

func TestMatchRiders_MatchingPreferencesListEveryContainingArea(t *testing.T) {
	lavington := PrefArea{Name: "Lavington", Center: domain.Location{Lat: -1.2780, Lng: 36.7690}, RadiusM: 3000} // overlaps Kilimani
	cbd := PrefArea{Name: "CBD", Center: domain.Location{Lat: -1.2864, Lng: 36.8172}, RadiusM: 2000}             // too far

	got := MatchRiders(elecOrder, []PrefRider{rider("r", cats("electronics"), areas(kilimani, cbd, lavington))}, prefParams, now)

	require.Len(t, got, 1)
	assert.Equal(t, []string{"electronics", "Kilimani", "Lavington"}, got[0].MatchingPreferences, "CBD does not contain the pickup")
}

func TestMatchRiders_Ranking(t *testing.T) {
	t.Run("higher score first", func(t *testing.T) {
		riders := []PrefRider{
			rider("plain"),
			rider("best", cats("electronics"), areas(kilimani), at(500), history(100, 90)),
			rider("mid", cats("electronics"), at(4000)),
		}

		got := MatchRiders(elecOrder, riders, prefParams, now)

		assert.Equal(t, []string{"best", "mid", "plain"}, riderIDs(got))
	})

	t.Run("equal score: nearer first, unknown distance last", func(t *testing.T) {
		// Zero out the proximity weight so only the distance tie-break can separate them.
		p := prefParams
		p.Weights = Weights{Category: 0.4, Area: 0.3, Proximity: 0, Acceptance: 0.3}
		riders := []PrefRider{
			rider("unknown"), rider("far", at(9000)), rider("near", at(100)), rider("mid", at(3000)),
		}

		got := MatchRiders(elecOrder, riders, p, now)

		assert.Equal(t, []string{"near", "mid", "far", "unknown"}, riderIDs(got))
		assert.Equal(t, got[0].Score, got[3].Score)
	})

	t.Run("equal score and distance: rider id", func(t *testing.T) {
		riders := []PrefRider{rider("rider_c", at(1000)), rider("rider_a", at(1000)), rider("rider_b", at(1000))}

		first := riderIDs(MatchRiders(elecOrder, riders, prefParams, now))
		riders[0], riders[2] = riders[2], riders[0]
		second := riderIDs(MatchRiders(elecOrder, riders, prefParams, now))

		assert.Equal(t, []string{"rider_a", "rider_b", "rider_c"}, first)
		assert.Equal(t, first, second, "input order does not matter")
	})

	t.Run("ties are decided on the rounded score the client sees", func(t *testing.T) {
		// 0.801 and 0.804 both display as 0.80, so the nearer rider wins even with the lower raw score.
		p := prefParams
		p.Weights = Weights{Category: 0, Area: 0, Proximity: 0, Acceptance: 1}
		riders := []PrefRider{rider("far_higher_raw", at(8000), history(998, 803)), rider("near_lower_raw", at(100), history(998, 800))} // smoothed 0.804 and 0.801

		got := MatchRiders(elecOrder, riders, p, now)

		assert.Equal(t, []string{"near_lower_raw", "far_higher_raw"}, riderIDs(got))
		assert.Equal(t, 0.80, got[0].Score)
		assert.Equal(t, 0.80, got[1].Score)
	})
}

func TestMatchRiders_CapAndEmpty(t *testing.T) {
	var riders []PrefRider
	for i := range 15 {
		riders = append(riders, rider(fmt.Sprintf("r%02d", i), history(20, i)))
	}
	p := prefParams
	p.MaxResults = 10

	got := MatchRiders(elecOrder, riders, p, now)

	require.Len(t, got, 10)
	assert.Equal(t, "r14", got[0].RiderID, "the cap keeps the best")
	assert.Equal(t, "r05", got[9].RiderID)

	p.MaxResults = 0
	assert.Empty(t, MatchRiders(elecOrder, riders, p, now))

	for _, in := range [][]PrefRider{nil, {}} {
		out := MatchRiders(elecOrder, in, prefParams, now)
		assert.NotNil(t, out, "no riders is [], never nil")
		assert.Empty(t, out)
	}
	assert.Empty(t, MatchRiders(elecOrder, []PrefRider{rider("a", offShift()), rider("b", busy(5, 5))}, prefParams, now), "everyone filtered out is []")
}

func TestMatchRiders_CarriesRiderFields(t *testing.T) {
	got := MatchRiders(elecOrder, []PrefRider{rider("rider_007", busy(3, 5), history(50, 31), at(1234))}, prefParams, now)

	require.Len(t, got, 1)
	assert.Equal(t, "rider_007", got[0].RiderID)
	assert.Equal(t, 3, got[0].CurrentOrders)
	assert.Equal(t, 0.62, got[0].AcceptanceRate, "the raw stored rate is echoed back, not the smoothed one")
	assert.InDelta(t, 1234, *got[0].DistanceM, 3)
}

func TestSmoothedAcceptance(t *testing.T) {
	tests := []struct {
		name              string
		accepted, offered int
		want              float64
	}{
		{"a newcomer is neutral", 0, 0, 0.5},
		{"one offer accepted", 1, 1, 2.0 / 3},
		{"one offer declined", 0, 1, 1.0 / 3},
		{"45 of 50", 45, 50, 46.0 / 52},
		{"never accepts", 0, 100, 1.0 / 102},
		{"always accepts", 100, 100, 101.0 / 102},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.InDelta(t, tc.want, SmoothedAcceptance(tc.accepted, tc.offered), 1e-12)
		})
	}

	t.Run("a newcomer outranks a rider with a bad record, and loses to a good one", func(t *testing.T) {
		riders := []PrefRider{
			rider("newcomer"),
			rider("bad", history(40, 4)),
			rider("good", history(40, 36)),
		}

		got := MatchRiders(elecOrder, riders, prefParams, now)

		assert.Equal(t, []string{"good", "newcomer", "bad"}, riderIDs(got))
	})
}
