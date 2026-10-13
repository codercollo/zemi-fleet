package matching

import (
	"math"
	"slices"
	"strings"
	"time"

	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/geo"
)

// scoreDecimals rounds match_score to 2 decimals.
const scoreDecimals = 100.0

// neutral is the category/area score of a rider with no preference in that dimension: they accept
// everything, which is neither a preference for this order nor against it.
const neutral = 0.5

// PrefOrder is the order a rider is being matched for. It need not be saved yet.
type PrefOrder struct {
	Category   string
	Pickup     domain.Location
	ValueCents int64
}

// PrefArea is a preferred area: a centre plus a coverage radius.
type PrefArea struct {
	Name    string
	Center  domain.Location
	RadiusM float64
}

// PrefRider is a rider as the preference matcher sees them.
type PrefRider struct {
	ID            string
	Available     bool
	CurrentOrders int
	MaxCapacity   int
	// MinOrderValueCents is 0 when the rider has no minimum.
	MinOrderValueCents int64
	// Categories and Areas are the explicit preferences; empty means "accepts everything".
	Categories []string
	Areas      []PrefArea
	// Location and LocationUpdatedAt are the rider's last ping; either may be nil (never reported).
	Location          *domain.Location
	LocationUpdatedAt *time.Time
	AcceptanceRate    float64
}

// Weights are the score weights. Config validates that they sum to 1.0.
type Weights struct {
	Category, Area, Proximity, Acceptance float64
}

// PrefParams are the matching rules; they come from config in production.
type PrefParams struct {
	MaxDistanceM float64
	StaleAfter   time.Duration
	Weights      Weights
	MaxResults   int
}

// RiderMatch is one ranked rider.
type RiderMatch struct {
	RiderID string
	// Score is the weighted score rounded to 2 decimals (the value that is ranked on and shown).
	Score float64
	// MatchingPreferences lists only the EXPLICIT matches: the category code if the rider prefers this
	// category, then the names of the preferred areas that contain the pickup. Never nil.
	MatchingPreferences []string
	CurrentOrders       int
	// DistanceM is rider to pickup; nil when the rider's location is unknown (missing or stale).
	DistanceM      *float64
	AcceptanceRate float64
}

// MatchRiders ranks the riders who suit an order.
//
// Preferences are HARD FILTERS: a rider failing any is excluded, however well they would otherwise score.
// A preference left empty means "accepts everything". The filters:
//
//	not available                                             -> out
//	current orders >= max capacity                            -> out
//	order value < the rider's minimum order value             -> out (equal is in)
//	rider has category preferences, order's category not one  -> out
//	rider has area preferences, pickup in none of them        -> out (on the boundary is in)
//	rider's location is known AND fresh AND farther than MaxDistanceM -> out
//
// A location is known and fresh when it exists and was updated within StaleAfter of now. A stale or
// missing location is "unknown": the rider is not filtered by distance, scores 0 for proximity, and has
// no distance.
//
// The SCORE only ranks the riders who passed:
//
//	category   = 1.0 if the rider explicitly prefers this category, 0.5 if they have no category preference
//	area       = 1.0 if the pickup is inside an explicitly preferred area, 0.5 if they have no area preference
//	proximity  = 1 - distance / MaxDistanceM (0 when the location is unknown)
//	acceptance = the rider's acceptance rate
//	score      = round2(wCategory*category + wArea*area + wProximity*proximity + wAcceptance*acceptance)
//
// Sorted by the (rounded) score descending, then distance ascending with unknown distances last, then
// rider ID, and capped at MaxResults. The result is never nil.
func MatchRiders(order PrefOrder, riders []PrefRider, p PrefParams, now time.Time) []RiderMatch {
	matches := []RiderMatch{}

	for _, r := range riders {
		if !r.Available || r.CurrentOrders >= r.MaxCapacity || order.ValueCents < r.MinOrderValueCents {
			continue
		}

		prefs := []string{}
		categoryScore := neutral
		if len(r.Categories) > 0 {
			if !slices.Contains(r.Categories, order.Category) {
				continue
			}
			categoryScore = 1
			prefs = append(prefs, order.Category)
		}

		areaScore := neutral
		if len(r.Areas) > 0 {
			var inside []string
			for _, a := range r.Areas {
				if geo.Haversine(a.Center, order.Pickup) <= a.RadiusM+distanceEpsilonM {
					inside = append(inside, a.Name)
				}
			}
			if len(inside) == 0 {
				continue
			}
			areaScore = 1
			prefs = append(prefs, inside...)
		}

		var distance *float64
		proximity := 0.0
		if r.Location != nil && r.LocationUpdatedAt != nil && now.Sub(*r.LocationUpdatedAt) <= p.StaleAfter {
			d := geo.Haversine(*r.Location, order.Pickup)
			if d > p.MaxDistanceM+distanceEpsilonM {
				continue
			}
			distance = &d
			proximity = math.Max(0, 1-d/p.MaxDistanceM)
		}

		w := p.Weights
		score := w.Category*categoryScore + w.Area*areaScore + w.Proximity*proximity + w.Acceptance*r.AcceptanceRate
		matches = append(matches, RiderMatch{
			RiderID:             r.ID,
			Score:               math.Round(score*scoreDecimals) / scoreDecimals,
			MatchingPreferences: prefs,
			CurrentOrders:       r.CurrentOrders,
			DistanceM:           distance,
			AcceptanceRate:      r.AcceptanceRate,
		})
	}

	slices.SortFunc(matches, func(a, b RiderMatch) int {
		if a.Score != b.Score {
			if a.Score > b.Score {
				return -1
			}
			return 1
		}
		switch {
		case a.DistanceM != nil && b.DistanceM == nil:
			return -1 // known distances before unknown
		case a.DistanceM == nil && b.DistanceM != nil:
			return 1
		case a.DistanceM != nil && b.DistanceM != nil && *a.DistanceM != *b.DistanceM:
			if *a.DistanceM < *b.DistanceM {
				return -1
			}
			return 1
		}
		return strings.Compare(a.RiderID, b.RiderID)
	})

	if p.MaxResults >= 0 && len(matches) > p.MaxResults {
		matches = matches[:p.MaxResults]
	}
	return matches
}
