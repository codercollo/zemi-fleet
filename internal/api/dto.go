package api

import (
	"math"
	"time"

	"github.com/codercollo/zemi-fleet/internal/domain"
)

// Money is stored as KES cents and shown as KES only in these DTOs.
const (
	centsPerKES = 100
	metresPerKM = 1000.0
)

// centsToKES converts for output only; never do arithmetic on the result.
func centsToKES(cents int64) float64 { return float64(cents) / centsPerKES }

// maxScaledKES is the largest value (in cents, as a float) kesToCents will convert. Far below float64's 2^53
// exact-integer range and int64's maximum, so the conversion can neither lose cents nor overflow.
const maxScaledKES = 1e15

// kesToCents converts a JSON amount in KES to int64 cents. The conversion rounds with math.Round (never
// truncates: 19.99 * 100 is 1998.9999999999998 in float64, and int64() of that would lose a cent). ok is false when
// the amount has more than two decimal places (10.005), is not finite, or is too large to convert: a fraction of
// a cent is rejected instead of silently rounded. Float noise of up to a millionth of a cent is tolerated, so
// 0.1 + 0.2 (0.30000000000000004) is 30 cents.
func kesToCents(kes float64) (cents int64, ok bool) {
	scaled := kes * centsPerKES
	if math.IsNaN(scaled) || math.IsInf(scaled, 0) || math.Abs(scaled) > maxScaledKES {
		return 0, false
	}
	rounded := math.Round(scaled)
	return int64(rounded), math.Abs(scaled-rounded) < 1e-6
}

type locationDTO struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

func newLocationDTO(l domain.Location) locationDTO { return locationDTO{Lat: l.Lat, Lng: l.Lng} }

type orderDTO struct {
	ID                   string      `json:"id"`
	VendorID             string      `json:"vendor_id"`
	Category             string      `json:"category"`
	Status               string      `json:"status"`
	PickupLocation       locationDTO `json:"pickup_location"`
	DeliveryLocation     locationDTO `json:"delivery_location"`
	TripDistanceKM       float64     `json:"trip_distance_km"`
	TripBearingDegrees   float64     `json:"trip_bearing_degrees"`
	ValueKES             float64     `json:"value_kes"`
	DeliveryFeeKES       float64     `json:"delivery_fee_kes"`
	RiderPayoutPercent   float64     `json:"rider_payout_percent"`
	EstimatedEarningsKES float64     `json:"estimated_earnings_kes"`
	PlatformFeeKES       float64     `json:"platform_fee_kes"`
	BatchID              *string     `json:"batch_id"`
	RiderID              *string     `json:"rider_id"`
	CreatedAt            time.Time   `json:"created_at"`
	Version              int         `json:"version"`
}

func newOrderDTO(o domain.Order) orderDTO {
	return orderDTO{
		ID:                   o.ID,
		VendorID:             o.VendorID,
		Category:             o.CategoryCode,
		Status:               string(o.Status),
		PickupLocation:       newLocationDTO(o.Pickup),
		DeliveryLocation:     newLocationDTO(o.Delivery),
		TripDistanceKM:       float64(o.TripDistanceM) / metresPerKM,
		TripBearingDegrees:   o.TripBearingDeg,
		ValueKES:             centsToKES(o.ValueCents),
		DeliveryFeeKES:       centsToKES(o.DeliveryFeeCents),
		RiderPayoutPercent:   float64(o.RiderPayoutBPS) / 100,
		EstimatedEarningsKES: centsToKES(o.RiderEarningsCents),
		PlatformFeeKES:       centsToKES(o.PlatformFeeCents),
		BatchID:              o.BatchID,
		RiderID:              o.RiderID,
		CreatedAt:            o.CreatedAt.UTC(),
		Version:              o.Version,
	}
}

type preferencesDTO struct {
	MinValueKES         float64  `json:"min_order_value_kes"`
	PreferredCategories []string `json:"preferred_categories"`
	PreferredAreas      []string `json:"preferred_areas"`
}

func newPreferencesDTO(p domain.Preferences) preferencesDTO {
	// Lists are always arrays in JSON, never null.
	return preferencesDTO{
		MinValueKES:         centsToKES(p.MinOrderValueCents),
		PreferredCategories: nonNil(p.Categories),
		PreferredAreas:      nonNil(p.AreaIDs),
	}
}

type riderDTO struct {
	ID                  string         `json:"id"`
	FullName            string         `json:"full_name"`
	Phone               string         `json:"phone"`
	IsAvailable         bool           `json:"is_available"`
	MaxCapacity         int            `json:"max_capacity"`
	CurrentOrdersCount  int            `json:"current_orders_count"`
	MaxAdditionalOrders int            `json:"max_additional_orders"`
	CurrentLocation     *locationDTO   `json:"current_location"`
	BearingDegrees      *float64       `json:"bearing_degrees"`
	SpeedKMH            *float64       `json:"speed_kmh"`
	LocationUpdatedAt   *time.Time     `json:"location_updated_at"`
	AcceptanceRate      float64        `json:"acceptance_rate"`
	Preferences         preferencesDTO `json:"preferences"`
	Version             int            `json:"version"`
}

func newRiderDTO(r domain.Rider) riderDTO {
	dto := riderDTO{
		ID:                  r.ID,
		FullName:            r.FullName,
		Phone:               r.Phone,
		IsAvailable:         r.IsAvailable,
		MaxCapacity:         r.MaxCapacity,
		CurrentOrdersCount:  r.CurrentOrders,
		MaxAdditionalOrders: max(r.MaxCapacity-r.CurrentOrders, 0),
		BearingDegrees:      r.BearingDeg,
		SpeedKMH:            r.SpeedKmh,
		AcceptanceRate:      r.AcceptanceRate,
		Preferences:         newPreferencesDTO(r.Preferences),
		Version:             r.Version,
	}
	if r.Location != nil {
		loc := newLocationDTO(*r.Location)
		dto.CurrentLocation = &loc
	}
	if r.LocationUpdatedAt != nil {
		t := r.LocationUpdatedAt.UTC()
		dto.LocationUpdatedAt = &t
	}
	return dto
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
