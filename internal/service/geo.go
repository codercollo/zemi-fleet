package service

import (
	"math"

	"github.com/codercollo/zemi-fleet/internal/domain"
)

// TODO(Phase 5): delete this file and use internal/geo (Haversine, Bearing). It exists
// only so order creation can precompute trip_distance_m and trip_bearing_deg now.

// earthRadiusM is the mean Earth radius used by the haversine formula.
const earthRadiusM = 6_371_000.0

// haversineM returns the great-circle distance between a and b in metres.
func haversineM(a, b domain.Location) float64 {
	lat1, lat2 := radians(a.Lat), radians(b.Lat)
	dLat := lat2 - lat1
	dLng := radians(b.Lng - a.Lng)

	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1)*math.Cos(lat2)*math.Sin(dLng/2)*math.Sin(dLng/2)
	return 2 * earthRadiusM * math.Asin(math.Sqrt(h))
}

// bearingDeg returns the initial compass bearing from a to b in [0, 360).
// Identical points have no direction and return 0.
func bearingDeg(a, b domain.Location) float64 {
	lat1, lat2 := radians(a.Lat), radians(b.Lat)
	dLng := radians(b.Lng - a.Lng)

	y := math.Sin(dLng) * math.Cos(lat2)
	x := math.Cos(lat1)*math.Sin(lat2) - math.Sin(lat1)*math.Cos(lat2)*math.Cos(dLng)
	return math.Mod(math.Atan2(y, x)*180/math.Pi+360, 360)
}

func radians(deg float64) float64 { return deg * math.Pi / 180 }
