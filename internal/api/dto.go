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

// kesToCents converts a JSON amount to cents. ok is false when the amount has
// more than two decimal places or is too large to hold, so no fraction of a cent is silently lost.
func kesToCents(kes float64) (cents int64, ok bool) {
	scaled := kes * centsPerKES
	if math.IsNaN(scaled) || math.IsInf(scaled, 0) || math.Abs(scaled) > math.MaxInt64/2 {
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
