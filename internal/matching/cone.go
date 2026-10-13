// Package matching holds the pure matching algorithms. It imports no database, HTTP or
// config code: every tunable arrives as an argument, so the rules are testable with plain values.
package matching

import (
	"math"
	"slices"
	"strings"
	"time"

	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/geo"
)

// Float tolerances so that "inclusive" really is inclusive. A pickup constructed to sit exactly
// on the radius or exactly on the cone edge comes out of the trigonometry a few ULPs over;
// without a tolerance it would flip to "outside". Both are far below GPS precision.
const (
	distanceEpsilonM = 1e-6 // a micrometre
	angleEpsilonDeg  = 1e-6 // about 0.1 mm of arc at 1 km

	// coincidentM: closer than this to the pickup, the bearing to it is meaningless (it flips
	// with GPS noise), so the pickup counts as inside the cone with angle 0. A rider standing
	// at the vendor should not lose that order because of the direction they happen to face.
	coincidentM = 1.0
)

// Rider is the rider's reading at the time of the request. HeadingDeg and SpeedKMH are nil when unknown.
type Rider struct {
	Position   domain.Location
	HeadingDeg *float64
	SpeedKMH   *float64
}

// Candidate is a pending order that could be offered to the rider.
type Candidate struct {
	OrderID            string
	Pickup             domain.Location
	CreatedAt          time.Time
	RiderEarningsCents int64
}

// ConeParams are the matching rules; they come from config in production.
type ConeParams struct {
	RadiusM      float64 // pickups farther than this are out (inclusive)
	HalfAngleDeg float64 // half the cone's opening (30 = a 60 degree cone)
	MinSpeedKMH  float64 // the cone only applies at or above this speed
	MaxResults   int     // cap on the result list
}

// Match is one pickup offered to the rider.
type Match struct {
	Candidate
	DistanceM float64
	// AngleDeg is the absolute angle between the rider's heading and the pickup, 0..HalfAngleDeg.
	// It is nil when the cone was not applied, because there is no heading to measure from.
	AngleDeg *float64
	// AgeMinutes is whole minutes since the order was created, never negative.
	AgeMinutes int
}

// ConeResult is what MatchCone returns.
type ConeResult struct {
	// Matches is never nil, so it encodes as [] rather than null.
	Matches []Match
	// DirectionFilterApplied is true when the cone was used and false when the full circle was.
	DirectionFilterApplied bool
}

// MatchCone picks the pending pickups worth offering to a moving rider:
//
//  1. keep pickups within RadiusM of the rider (inclusive);
//  2. if the heading is known AND the speed is known and at least MinSpeedKMH, also keep only
//     pickups whose bearing from the rider is within HalfAngleDeg of the heading (inclusive).
//     A stationary or slow rider's GPS heading is noise, and an unknown heading or speed gives
//     nothing to filter on, so all of those fall back to the full circle;
//  3. sort by distance, then oldest order first (FIFO), then order ID, so the order is deterministic;
//  4. cap at MaxResults.
//
// now is passed in (from the injected clock) so ages are exact in tests.
func MatchCone(rider Rider, candidates []Candidate, p ConeParams, now time.Time) ConeResult {
	applyCone := rider.HeadingDeg != nil && rider.SpeedKMH != nil && *rider.SpeedKMH >= p.MinSpeedKMH
	result := ConeResult{Matches: []Match{}, DirectionFilterApplied: applyCone}

	for _, c := range candidates {
		distance := geo.Haversine(rider.Position, c.Pickup)
		if distance > p.RadiusM+distanceEpsilonM {
			continue
		}

		var angle *float64
		if applyCone {
			a := 0.0
			if distance >= coincidentM {
				bearing := geo.Bearing(rider.Position, c.Pickup)
				if !geo.WithinCone(*rider.HeadingDeg, bearing, p.HalfAngleDeg+angleEpsilonDeg) {
					continue
				}
				a = math.Abs(geo.AngleDiff(*rider.HeadingDeg, bearing))
			}
			angle = &a
		}

		result.Matches = append(result.Matches, Match{
			Candidate:  c,
			DistanceM:  distance,
			AngleDeg:   angle,
			AgeMinutes: ageMinutes(c.CreatedAt, now),
		})
	}

	slices.SortFunc(result.Matches, func(a, b Match) int {
		if a.DistanceM != b.DistanceM {
			if a.DistanceM < b.DistanceM {
				return -1
			}
			return 1
		}
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.OrderID, b.OrderID)
	})

	if p.MaxResults >= 0 && len(result.Matches) > p.MaxResults {
		result.Matches = result.Matches[:p.MaxResults]
	}
	return result
}

// ageMinutes is the whole number of minutes between createdAt and now. Clock skew (an order
// stamped slightly in the future) reads as 0 rather than a negative age.
func ageMinutes(createdAt, now time.Time) int {
	age := now.Sub(createdAt)
	if age <= 0 {
		return 0
	}
	return int(age / time.Minute)
}
