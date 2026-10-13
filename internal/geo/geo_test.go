package geo

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codercollo/zemi-fleet/internal/domain"
)

func loc(lat, lng float64) domain.Location { return domain.Location{Lat: lat, Lng: lng} }

// Seeded vendor coordinates (db/seed/seed.sql).
var (
	sarit         = loc(-1.2635, 36.8030)
	westlandsBite = loc(-1.2660, 36.8045)
	yaya          = loc(-1.2917, 36.7878)
	kilimaniMkt   = loc(-1.2975, 36.7860)
)

func TestHaversine(t *testing.T) {
	tests := []struct {
		name       string
		a, b       domain.Location
		wantM      float64
		toleranceM float64
	}{
		{"identical points", sarit, sarit, 0, 0},
		{"1 degree of longitude on the equator", loc(0, 0), loc(0, 1), 111_195.08, 1},
		{"1 degree of latitude", loc(0, 0), loc(1, 0), 111_195.08, 1},
		{"antipodes are half the circumference", loc(0, 0), loc(0, 180), 20_015_114.4, 1},
		{"antipodes off the equator", loc(45, 10), loc(-45, -170), 20_015_114.4, 1},
		{"pole to pole", loc(90, 0), loc(-90, 0), 20_015_114.4, 1},
		{"Sarit to Westlands Bites", sarit, westlandsBite, 325, 1},
		{"Yaya to Kilimani market", yaya, kilimaniMkt, 676, 1},
		{"across the antimeridian", loc(0, 179.5), loc(0, -179.5), 111_195.08, 1},
		{"Nairobi CBD to JKIA (about 13 km)", loc(-1.2864, 36.8172), loc(-1.3192, 36.9278), 12_730, 150},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.InDelta(t, tc.wantM, Haversine(tc.a, tc.b), tc.toleranceM)
			assert.InDelta(t, Haversine(tc.a, tc.b), Haversine(tc.b, tc.a), 1e-6, "distance is symmetric")
		})
	}
}

func TestHaversine_NeverNaN(t *testing.T) {
	// Near-antipodal points can push the intermediate value a hair above 1.
	for _, b := range []domain.Location{loc(-0.0000001, -179.9999999), loc(0, 180), loc(-89.9999999, 180)} {
		d := Haversine(loc(0.0000001, 0), b)
		assert.False(t, math.IsNaN(d), "got NaN for %v", b)
	}
}

func TestBearing(t *testing.T) {
	tests := []struct {
		name     string
		from, to domain.Location
		want     float64
	}{
		{"due north", loc(0, 0), loc(1, 0), 0},
		{"due east", loc(0, 0), loc(0, 1), 90},
		{"due south", loc(0, 0), loc(-1, 0), 180},
		{"due west", loc(0, 0), loc(0, -1), 270},
		{"north-east on the equator", loc(0, 0), loc(1, 1), 45},
		{"identical points have no direction", sarit, sarit, 0},
		{"Sarit to Westlands Bites heads south-east", sarit, westlandsBite, 149.04},
		{"west is never negative", loc(-1.28, 36.8), loc(-1.28, 36.7), 270.0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Bearing(tc.from, tc.to)

			assert.InDelta(t, tc.want, got, 0.2)
			assert.GreaterOrEqual(t, got, 0.0)
			assert.Less(t, got, 360.0)
		})
	}
}

func TestBearing_ExactCardinals(t *testing.T) {
	// Cardinal directions should be exact enough for 1-decimal storage.
	assert.InDelta(t, 0, Bearing(sarit, loc(sarit.Lat+0.01, sarit.Lng)), 1e-9)
	assert.InDelta(t, 180, Bearing(sarit, loc(sarit.Lat-0.01, sarit.Lng)), 1e-9)
	assert.InDelta(t, 90, Bearing(loc(0, 10), loc(0, 11)), 1e-9)
	assert.InDelta(t, 270, Bearing(loc(0, 10), loc(0, 9)), 1e-9)
}

func TestNormalizeDeg(t *testing.T) {
	tests := []struct{ in, want float64 }{
		{0, 0}, {359.5, 359.5}, {360, 0}, {720, 0}, {-90, 270}, {-360, 0}, {-1e-18, 0}, {450, 90}, {-450, 270},
	}
	for _, tc := range tests {
		assert.InDelta(t, tc.want, normalizeDeg(tc.in), 1e-9, "normalizeDeg(%v)", tc.in)
	}
}

func TestAngleDiff(t *testing.T) {
	tests := []struct {
		name string
		a, b float64
		want float64
	}{
		{"350 to 10 wraps clockwise", 350, 10, 20},
		{"10 to 350 wraps anticlockwise", 10, 350, -20},
		{"same bearing", 123, 123, 0},
		{"quarter turn", 0, 90, 90},
		{"quarter turn back", 90, 0, -90},
		{"opposite is +180, never -180", 0, 180, 180},
		{"opposite the other way", 180, 0, 180},
		{"just under opposite", 0, 179, 179},
		{"just over opposite goes the short way", 0, 181, -179},
		{"inputs beyond a full turn", 720, 10, 10},
		{"negative inputs", -10, 10, 20},
		{"both negative", -350, -10, -20},
		{"360 equals 0", 360, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AngleDiff(tc.a, tc.b)

			assert.InDelta(t, tc.want, got, 1e-9)
			assert.Greater(t, got, -180.0)
			assert.LessOrEqual(t, got, 180.0)
		})
	}
}

func TestAngleDiff_RangeProperty(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for range 10_000 {
		a, b := r.Float64()*1440-720, r.Float64()*1440-720
		d := AngleDiff(a, b)

		require.Greater(t, d, -180.0)
		require.LessOrEqual(t, d, 180.0)
		// Rotating a by d must land on b (mod 360).
		require.InDelta(t, 0, math.Abs(AngleDiff(a+d, b)), 1e-6)
	}
}

func TestWithinCone(t *testing.T) {
	const half = 30.0
	tests := []struct {
		name            string
		heading, target float64
		want            bool
	}{
		{"dead ahead", 90, 90, true},
		{"exactly +30 is inside", 0, 30, true},
		{"exactly -30 is inside", 0, 330, true},
		{"30.01 is outside", 0, 30.01, false},
		{"-30.01 is outside", 0, 329.99, false},
		{"behind", 0, 180, false},
		{"wraps across north (350 vs 10)", 350, 10, true},
		{"wraps across north, outside (340 vs 15)", 340, 15, false},
		{"wraps the other way (10 vs 350)", 10, 350, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, WithinCone(tc.heading, tc.target, half))
		})
	}
}

func TestBoundingBox(t *testing.T) {
	t.Run("nairobi 1 km box is symmetric and tight", func(t *testing.T) {
		minLat, maxLat, minLng, maxLng := BoundingBox(sarit, 1000)

		dLat := 1000 / EarthRadiusM * 180 / math.Pi
		assert.InDelta(t, sarit.Lat-dLat, minLat, 1e-9)
		assert.InDelta(t, sarit.Lat+dLat, maxLat, 1e-9)
		assert.InDelta(t, sarit.Lng, (minLng+maxLng)/2, 1e-9)
		// Near the equator a degree of longitude is about as long as one of latitude.
		assert.InDelta(t, maxLat-minLat, maxLng-minLng, 1e-4)
	})

	t.Run("longitude widens with latitude (scaled by 1/cos)", func(t *testing.T) {
		_, _, eqMin, eqMax := BoundingBox(loc(0, 0), 10_000)
		_, _, hiMin, hiMax := BoundingBox(loc(60, 0), 10_000)

		assert.InDelta(t, 2.0, (hiMax-hiMin)/(eqMax-eqMin), 0.01, "cos(60 deg) = 0.5, so twice as wide")
	})

	t.Run("zero and negative radius collapse to the point", func(t *testing.T) {
		for _, r := range []float64{0, -500} {
			minLat, maxLat, minLng, maxLng := BoundingBox(sarit, r)
			assert.InDelta(t, sarit.Lat, minLat, 1e-12)
			assert.InDelta(t, sarit.Lat, maxLat, 1e-12)
			assert.InDelta(t, sarit.Lng, minLng, 1e-12)
			assert.InDelta(t, sarit.Lng, maxLng, 1e-12)
		}
	})

	t.Run("a circle over the north pole is clamped and spans all longitudes", func(t *testing.T) {
		minLat, maxLat, minLng, maxLng := BoundingBox(loc(89.5, 20), 100_000)

		assert.Equal(t, 90.0, maxLat)
		assert.Less(t, minLat, 89.5)
		assert.Equal(t, -180.0, minLng)
		assert.Equal(t, 180.0, maxLng)
	})

	t.Run("a circle over the south pole is clamped too", func(t *testing.T) {
		minLat, maxLat, minLng, maxLng := BoundingBox(loc(-89.9, -45), 50_000)

		assert.Equal(t, -90.0, minLat)
		assert.Greater(t, maxLat, -89.9)
		assert.Equal(t, [2]float64{-180, 180}, [2]float64{minLng, maxLng})
	})

	t.Run("crossing the antimeridian widens to every longitude", func(t *testing.T) {
		_, _, minLng, maxLng := BoundingBox(loc(0, 179.9), 50_000)

		assert.Equal(t, [2]float64{-180, 180}, [2]float64{minLng, maxLng})
	})

	t.Run("a huge radius covers the whole globe", func(t *testing.T) {
		minLat, maxLat, minLng, maxLng := BoundingBox(loc(10, 10), 15_000_000)

		assert.Equal(t, [4]float64{-90, 90, -180, 180}, [4]float64{minLat, maxLat, minLng, maxLng})
	})
}

// destination returns the point reached from start after travelling distM along bearingDeg.
func destination(start domain.Location, bearingDeg, distM float64) domain.Location {
	d := distM / EarthRadiusM
	theta := radians(bearingDeg)
	lat1, lng1 := radians(start.Lat), radians(start.Lng)

	lat2 := math.Asin(math.Sin(lat1)*math.Cos(d) + math.Cos(lat1)*math.Sin(d)*math.Cos(theta))
	lng2 := lng1 + math.Atan2(math.Sin(theta)*math.Sin(d)*math.Cos(lat1), math.Cos(d)-math.Sin(lat1)*math.Sin(lat2))
	lngDeg := math.Mod(degrees(lng2)+540, 360) - 180 // normalise to [-180, 180)
	return loc(degrees(lat2), lngDeg)
}

// Property: every point within the radius lies inside the box, wherever the centre is
// (equator, mid-latitudes, near the poles, near the antimeridian) and whatever the radius.
func TestBoundingBox_ContainsEveryPointWithinRadius(t *testing.T) {
	r := rand.New(rand.NewPCG(42, 7))
	radii := []float64{1, 100, 1_000, 2_000, 50_000, 500_000, 5_000_000}

	for i := range 20_000 {
		center := loc(r.Float64()*180-90, r.Float64()*360-180)
		switch i % 4 { // oversample the awkward places
		case 1:
			center.Lat = 85 + r.Float64()*5
		case 2:
			center.Lng = 179 + r.Float64()
		case 3:
			center.Lat = -1.5 + r.Float64()*0.5 // Nairobi-ish
			center.Lng = 36.7 + r.Float64()*0.3
		}
		radius := radii[r.IntN(len(radii))]
		minLat, maxLat, minLng, maxLng := BoundingBox(center, radius)

		for range 8 {
			// sqrt for uniform area; the last radius is the very edge of the circle.
			dist := radius * math.Sqrt(r.Float64())
			p := destination(center, r.Float64()*360, dist)
			require.LessOrEqual(t, Haversine(center, p), radius*(1+1e-9)+1e-6, "test point must be within the radius")

			const eps = 1e-9
			require.GreaterOrEqual(t, p.Lat, minLat-eps, "center=%v radius=%v p=%v", center, radius, p)
			require.LessOrEqual(t, p.Lat, maxLat+eps, "center=%v radius=%v p=%v", center, radius, p)
			require.GreaterOrEqual(t, p.Lng, minLng-eps, "center=%v radius=%v p=%v", center, radius, p)
			require.LessOrEqual(t, p.Lng, maxLng+eps, "center=%v radius=%v p=%v", center, radius, p)
		}
	}
}

// The cardinal extremes of the circle (due N/E/S/W at exactly the radius) must sit on or inside the box.
func TestBoundingBox_CardinalExtremes(t *testing.T) {
	const radius = 1_000.0
	minLat, maxLat, minLng, maxLng := BoundingBox(sarit, radius)

	north, south := destination(sarit, 0, radius), destination(sarit, 180, radius)
	east, west := destination(sarit, 90, radius), destination(sarit, 270, radius)

	assert.InDelta(t, north.Lat, maxLat, 1e-9)
	assert.InDelta(t, south.Lat, minLat, 1e-9)
	assert.LessOrEqual(t, east.Lng, maxLng+1e-9)
	assert.GreaterOrEqual(t, west.Lng, minLng-1e-9)
}
