// Package geo holds the pure geometry used by matching and batching: great-circle
// distance, bearings, angle differences and bounding boxes. It has no config, database
// or HTTP dependencies; every parameter is an argument.
package geo

import (
	"math"

	"github.com/codercollo/zemi-fleet/internal/domain"
)

// EarthRadiusM is the mean Earth radius in metres (IUGG: (2a + b) / 3 for WGS84).
// A single spherical radius is accurate to about 0.3% (roughly 3 m per km), which is
// far inside what 1 km and 2 km matching rules need.
const EarthRadiusM = 6_371_008.8

const fullCircleDeg = 360.0

// Density counts points no farther than radiusM from center, inclusively.
// It is intentionally small and pure so adaptive batching can be tested
// independently of configuration and persistence.
func Density(center domain.Location, points []domain.Location, radiusM float64) int {
	if radiusM < 0 {
		return 0
	}
	n := 0
	for _, point := range points {
		if Haversine(center, point) <= radiusM {
			n++
		}
	}
	return n
}

func radians(deg float64) float64 { return deg * math.Pi / 180 }
func degrees(rad float64) float64 { return rad * 180 / math.Pi }

// Haversine returns the great-circle distance between a and b in metres.
func Haversine(a, b domain.Location) float64 {
	lat1, lat2 := radians(a.Lat), radians(b.Lat)
	dLat := lat2 - lat1
	dLng := radians(b.Lng - a.Lng)

	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1)*math.Cos(lat2)*math.Sin(dLng/2)*math.Sin(dLng/2)
	// Rounding can push h a hair above 1 for near-antipodal points; clamp so Asin stays defined.
	return 2 * EarthRadiusM * math.Asin(math.Sqrt(math.Min(1, h)))
}

// Bearing returns the initial compass bearing from one point to another in degrees,
// normalised to [0, 360) with 0 = north and 90 = east. Identical points have no
// direction and return 0.
func Bearing(from, to domain.Location) float64 {
	lat1, lat2 := radians(from.Lat), radians(to.Lat)
	dLng := radians(to.Lng - from.Lng)

	y := math.Sin(dLng) * math.Cos(lat2)
	x := math.Cos(lat1)*math.Sin(lat2) - math.Sin(lat1)*math.Cos(lat2)*math.Cos(dLng)
	return normalizeDeg(degrees(math.Atan2(y, x)))
}

// normalizeDeg maps any angle into [0, 360).
func normalizeDeg(deg float64) float64 {
	n := math.Mod(deg, fullCircleDeg)
	if n < 0 {
		n += fullCircleDeg
	}
	// -1e-18 + 360 rounds to exactly 360 in float64; fold it back.
	if n >= fullCircleDeg {
		return 0
	}
	return n
}

// AngleDiff returns the signed smallest rotation from bearing a to bearing b, in
// (-180, 180]: positive is clockwise. It handles wraparound, so AngleDiff(350, 10) = 20
// and AngleDiff(10, 350) = -20.
//
// The formula mod(b - a + 540, 360) - 180 yields [-180, 180), so exactly opposite
// bearings would come out as -180; they are folded to +180 to honour the half-open
// interval above.
func AngleDiff(a, b float64) float64 {
	d := math.Mod(b-a+540, fullCircleDeg)
	if d < 0 {
		d += fullCircleDeg // Go's Mod keeps the sign of the dividend
	}
	d -= 180
	if d == -180 {
		return 180
	}
	return d
}

// WithinCone reports whether target lies within +/-halfAngle degrees of heading.
// The edge is inclusive: a target exactly halfAngle away is inside.
func WithinCone(heading, target, halfAngle float64) bool {
	return math.Abs(AngleDiff(heading, target)) <= halfAngle
}

// BoundingBox returns the smallest lat/lng rectangle that contains every point within
// radiusM of center. It is a cheap SQL prefilter (indexed BETWEEN on lat and lng); apply
// Haversine afterwards for the exact test.
//
// The longitude half-width is asin(sin(r/R) / cos(lat)), the exact extent of a circle on a
// sphere, which is slightly wider than the common radius/(R*cos(lat)) approximation, so the
// box never clips the circle. Two cases cannot be expressed as one lat/lng rectangle and
// are widened conservatively (the box then contains more than the circle, never less):
//   - the circle reaches a pole: latitude is clamped to +/-90 and longitude spans -180..180;
//   - the circle crosses the antimeridian: longitude spans -180..180.
func BoundingBox(center domain.Location, radiusM float64) (minLat, maxLat, minLng, maxLng float64) {
	angular := math.Max(radiusM, 0) / EarthRadiusM // radians of arc
	dLatDeg := degrees(angular)

	minLat = math.Max(center.Lat-dLatDeg, -90)
	maxLat = math.Min(center.Lat+dLatDeg, 90)

	reachesPole := center.Lat+dLatDeg >= 90 || center.Lat-dLatDeg <= -90
	if reachesPole || angular >= math.Pi/2 {
		return minLat, maxLat, -180, 180
	}

	dLngDeg := degrees(math.Asin(math.Min(1, math.Sin(angular)/math.Cos(radians(center.Lat)))))
	minLng, maxLng = center.Lng-dLngDeg, center.Lng+dLngDeg
	if minLng < -180 || maxLng > 180 {
		return minLat, maxLat, -180, 180
	}
	return minLat, maxLat, minLng, maxLng
}
