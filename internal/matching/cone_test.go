package matching

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/geo"
)

var (
	origin = domain.Location{Lat: -1.2635, Lng: 36.8030} // Sarit Centre
	now    = time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)

	params = ConeParams{RadiusM: 1000, HalfAngleDeg: 30, MinSpeedKMH: 3, MaxResults: 20}
)

func ptr[T any](v T) *T { return &v }

// destination returns the point reached from start after distM along bearingDeg.
func destination(start domain.Location, bearingDeg, distM float64) domain.Location {
	d := distM / geo.EarthRadiusM
	theta := bearingDeg * math.Pi / 180
	lat1, lng1 := start.Lat*math.Pi/180, start.Lng*math.Pi/180

	lat2 := math.Asin(math.Sin(lat1)*math.Cos(d) + math.Cos(lat1)*math.Sin(d)*math.Cos(theta))
	lng2 := lng1 + math.Atan2(math.Sin(theta)*math.Sin(d)*math.Cos(lat1), math.Cos(d)-math.Sin(lat1)*math.Sin(lat2))
	return domain.Location{Lat: lat2 * 180 / math.Pi, Lng: lng2 * 180 / math.Pi}
}

// order builds a candidate at the given bearing and distance from origin.
func order(id string, bearing, distM float64, ageMin int) Candidate {
	return Candidate{
		OrderID:            id,
		Pickup:             destination(origin, bearing, distM),
		CreatedAt:          now.Add(-time.Duration(ageMin) * time.Minute),
		RiderEarningsCents: 18000,
	}
}

// moving is a rider travelling fast enough for the heading to count.
func moving(heading float64) Rider {
	return Rider{Position: origin, HeadingDeg: ptr(heading), SpeedKMH: ptr(25.0)}
}

func ids(r ConeResult) []string {
	out := make([]string, 0, len(r.Matches))
	for _, m := range r.Matches {
		out = append(out, m.OrderID)
	}
	return out
}

func TestMatchCone_ConeEdges(t *testing.T) {
	tests := []struct {
		name    string
		heading float64
		bearing float64
		want    bool
	}{
		{"dead ahead", 0, 0, true},
		{"exactly +30 is in", 0, 30, true},
		{"exactly -30 is in", 0, 330, true},
		{"30.1 is out", 0, 30.1, false},
		{"-30.1 is out", 0, 329.9, false},
		{"behind is out", 0, 180, false},
		{"wraparound: heading 350, order at 10 is in", 350, 10, true},
		{"wraparound: heading 10, order at 340 is exactly 30 away, so in", 10, 340, true},
		{"wraparound: heading 10, order at 339 is 31 away, so out", 10, 339, false},
		{"wraparound: heading 355, order at 25 is exactly 30 away, so in", 355, 25, true},
		{"wraparound: heading 355, order at 26 is out", 355, 26, false},
		{"heading 0 and 360 are the same", 360, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := MatchCone(moving(tc.heading), []Candidate{order("ord_1", tc.bearing, 500, 5)}, params, now)

			assert.True(t, res.DirectionFilterApplied)
			assert.Equal(t, tc.want, len(res.Matches) == 1, "matches: %v", ids(res))
			if tc.want {
				require.NotNil(t, res.Matches[0].AngleDeg)
				assert.LessOrEqual(t, *res.Matches[0].AngleDeg, 30.0+1e-6)
				assert.GreaterOrEqual(t, *res.Matches[0].AngleDeg, 0.0)
			}
		})
	}
}

func TestMatchCone_Angle(t *testing.T) {
	res := MatchCone(moving(350), []Candidate{order("ord_1", 10, 500, 5)}, params, now)

	require.Len(t, res.Matches, 1)
	assert.InDelta(t, 20, *res.Matches[0].AngleDeg, 1e-6, "absolute angle across the 0/360 seam")
}

func TestMatchCone_Distance(t *testing.T) {
	tests := []struct {
		name  string
		distM float64
		want  bool
	}{
		{"well inside", 400, true},
		{"999 m is in", 999, true},
		{"exactly 1000 m is in (inclusive)", 1000, true},
		{"1001 m is out", 1001, false},
		{"far away", 5000, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := MatchCone(moving(0), []Candidate{order("ord_1", 0, tc.distM, 5)}, params, now)

			assert.Equal(t, tc.want, len(res.Matches) == 1)
		})
	}
}

func TestMatchCone_FallsBackToFullCircle(t *testing.T) {
	behind := order("ord_behind", 180, 500, 5)
	ahead := order("ord_ahead", 0, 700, 5)
	tests := []struct {
		name  string
		rider Rider
	}{
		{"speed 0", Rider{Position: origin, HeadingDeg: ptr(0.0), SpeedKMH: ptr(0.0)}},
		{"just below the minimum speed", Rider{Position: origin, HeadingDeg: ptr(0.0), SpeedKMH: ptr(2.99)}},
		{"heading unknown", Rider{Position: origin, SpeedKMH: ptr(30.0)}},
		{"heading unknown (fast)", Rider{Position: origin, SpeedKMH: ptr(80.0)}},
		{"both unknown", Rider{Position: origin}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := MatchCone(tc.rider, []Candidate{behind, ahead}, params, now)

			assert.False(t, res.DirectionFilterApplied)
			assert.Equal(t, []string{"ord_behind", "ord_ahead"}, ids(res), "both directions, nearest first")
			for _, m := range res.Matches {
				assert.Nil(t, m.AngleDeg, "no heading in use, so no angle")
			}
		})
	}

	t.Run("full circle still honours the radius", func(t *testing.T) {
		res := MatchCone(Rider{Position: origin}, []Candidate{order("far", 90, 1500, 1)}, params, now)

		assert.Empty(t, res.Matches)
	})

	t.Run("a heading with no speed uses the cone", func(t *testing.T) {
		rider := Rider{Position: origin, HeadingDeg: ptr(0.0)}

		res := MatchCone(rider, []Candidate{behind, ahead}, params, now)

		assert.True(t, res.DirectionFilterApplied)
		assert.Equal(t, []string{"ord_ahead"}, ids(res))
		require.NotNil(t, res.Matches[0].AngleDeg)
	})

	t.Run("exactly the minimum speed applies the cone", func(t *testing.T) {
		rider := Rider{Position: origin, HeadingDeg: ptr(0.0), SpeedKMH: ptr(3.0)}

		res := MatchCone(rider, []Candidate{behind, ahead}, params, now)

		assert.True(t, res.DirectionFilterApplied)
		assert.Equal(t, []string{"ord_ahead"}, ids(res))
	})
}

func TestMatchCone_CoincidentPickupIsInTheCone(t *testing.T) {
	here := Candidate{OrderID: "ord_here", Pickup: origin, CreatedAt: now, RiderEarningsCents: 1}

	res := MatchCone(moving(123), []Candidate{here}, params, now)

	require.Len(t, res.Matches, 1)
	assert.Equal(t, 0.0, *res.Matches[0].AngleDeg)
	assert.Equal(t, 0.0, res.Matches[0].DistanceM)
}

func TestMatchCone_Sorting(t *testing.T) {
	t.Run("nearest first", func(t *testing.T) {
		cands := []Candidate{order("far", 0, 800, 1), order("near", 5, 200, 1), order("mid", -5, 500, 1)}

		res := MatchCone(moving(0), cands, params, now)

		assert.Equal(t, []string{"near", "mid", "far"}, ids(res))
	})

	t.Run("distance tie goes to the older order (FIFO)", func(t *testing.T) {
		same := destination(origin, 0, 400)
		cands := []Candidate{
			{OrderID: "ord_new", Pickup: same, CreatedAt: now.Add(-2 * time.Minute)},
			{OrderID: "ord_old", Pickup: same, CreatedAt: now.Add(-90 * time.Minute)},
			{OrderID: "ord_mid", Pickup: same, CreatedAt: now.Add(-30 * time.Minute)},
		}

		res := MatchCone(moving(0), cands, params, now)

		assert.Equal(t, []string{"ord_old", "ord_mid", "ord_new"}, ids(res))
	})

	t.Run("full tie goes to order id so the result is deterministic", func(t *testing.T) {
		same := destination(origin, 0, 400)
		at := now.Add(-10 * time.Minute)
		cands := []Candidate{
			{OrderID: "ord_c", Pickup: same, CreatedAt: at},
			{OrderID: "ord_a", Pickup: same, CreatedAt: at},
			{OrderID: "ord_b", Pickup: same, CreatedAt: at},
		}

		first := ids(MatchCone(moving(0), cands, params, now))
		cands[0], cands[2] = cands[2], cands[0] // shuffle the input
		second := ids(MatchCone(moving(0), cands, params, now))

		assert.Equal(t, []string{"ord_a", "ord_b", "ord_c"}, first)
		assert.Equal(t, first, second)
	})

	t.Run("a newer order that is closer still beats an older one that is farther", func(t *testing.T) {
		cands := []Candidate{order("old_far", 0, 900, 120), order("new_near", 0, 100, 1)}

		res := MatchCone(moving(0), cands, params, now)

		assert.Equal(t, []string{"new_near", "old_far"}, ids(res))
	})
}

func TestMatchCone_Cap(t *testing.T) {
	var cands []Candidate
	for i := range 30 {
		cands = append(cands, order("ord_"+string(rune('a'+i%26))+string(rune('a'+i/26)), 0, float64(100+i*10), 1))
	}
	p := params
	p.MaxResults = 5

	res := MatchCone(moving(0), cands, p, now)

	require.Len(t, res.Matches, 5)
	for i := 1; i < len(res.Matches); i++ {
		assert.LessOrEqual(t, res.Matches[i-1].DistanceM, res.Matches[i].DistanceM, "the cap keeps the nearest")
	}
	assert.InDelta(t, 100, res.Matches[0].DistanceM, 0.01)

	p.MaxResults = 0
	assert.Empty(t, MatchCone(moving(0), cands, p, now).Matches)
}

func TestMatchCone_EmptyInput(t *testing.T) {
	for _, cands := range [][]Candidate{nil, {}} {
		res := MatchCone(moving(0), cands, params, now)

		assert.NotNil(t, res.Matches, "an empty list, never nil (it must encode as [])")
		assert.Empty(t, res.Matches)
		assert.True(t, res.DirectionFilterApplied)
	}
}

func TestMatchCone_AgeMinutes(t *testing.T) {
	tests := []struct {
		name      string
		createdAt time.Time
		want      int
	}{
		{"just created", now, 0},
		{"59 seconds is still 0", now.Add(-59 * time.Second), 0},
		{"exactly one minute", now.Add(-time.Minute), 1},
		{"89 seconds floors to 1", now.Add(-89 * time.Second), 1},
		{"two hours", now.Add(-2 * time.Hour), 120},
		{"clock skew (created in the future) is 0", now.Add(30 * time.Second), 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := order("ord_1", 0, 300, 0)
			c.CreatedAt = tc.createdAt

			res := MatchCone(moving(0), []Candidate{c}, params, now)

			require.Len(t, res.Matches, 1)
			assert.Equal(t, tc.want, res.Matches[0].AgeMinutes)
		})
	}
}

func TestMatchCone_CarriesCandidateData(t *testing.T) {
	c := order("ord_1", 10, 640, 12)
	c.RiderEarningsCents = 13500

	res := MatchCone(moving(0), []Candidate{c}, params, now)

	require.Len(t, res.Matches, 1)
	m := res.Matches[0]
	assert.Equal(t, "ord_1", m.OrderID)
	assert.EqualValues(t, 13500, m.RiderEarningsCents)
	assert.InDelta(t, 640, m.DistanceM, 0.01)
	assert.InDelta(t, 10, *m.AngleDeg, 1e-6)
	assert.Equal(t, 12, m.AgeMinutes)
}
