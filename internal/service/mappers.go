package service

import (
	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/repository/sqlc"
)

// orderFromRow converts a sqlc row into the domain type. Generated columns are
// nullable in sqlc's eyes but are always set in Postgres, hence the deref helper.
func orderFromRow(o sqlc.Order) domain.Order {
	return domain.Order{
		ID:                 o.ID,
		VendorID:           o.VendorID,
		CategoryCode:       o.CategoryCode,
		Status:             domain.OrderStatus(o.Status),
		Pickup:             domain.Location{Lat: o.PickupLat, Lng: o.PickupLng},
		Delivery:           domain.Location{Lat: o.DeliveryLat, Lng: o.DeliveryLng},
		TripDistanceM:      int(o.TripDistanceM),
		TripBearingDeg:     o.TripBearingDeg,
		ValueCents:         o.ValueCents,
		DeliveryFeeCents:   o.DeliveryFeeCents,
		RiderPayoutBPS:     int(o.RiderPayoutBps),
		RiderEarningsCents: deref(o.RiderEarningsCents),
		PlatformFeeCents:   deref(o.PlatformFeeCents),
		BatchID:            o.BatchID,
		RiderID:            o.RiderID,
		CreatedAt:          o.CreatedAt,
		UpdatedAt:          o.UpdatedAt,
		Version:            int(o.Version),
	}
}

func riderFromRow(r sqlc.Rider) domain.Rider {
	rider := domain.Rider{
		ID:                r.ID,
		FullName:          r.FullName,
		Phone:             r.Phone,
		IsAvailable:       r.IsAvailable,
		MaxCapacity:       int(r.MaxCapacity),
		CurrentOrders:     int(r.CurrentOrdersCount),
		LocationUpdatedAt: r.LocationUpdatedAt,
		OffersCount:       int(r.OffersCount),
		AcceptedCount:     int(r.AcceptedCount),
		AcceptanceRate:    deref(r.AcceptanceRate),
		Version:           int(r.Version),
		CreatedAt:         r.CreatedAt,
		UpdatedAt:         r.UpdatedAt,
	}
	// The riders table CHECKs that lat and lng are both set or both NULL.
	if r.LastLat != nil && r.LastLng != nil {
		rider.Location = &domain.Location{Lat: *r.LastLat, Lng: *r.LastLng}
	}
	if r.LastBearingDeg != nil {
		v := *r.LastBearingDeg
		rider.BearingDeg = &v
	}
	if r.LastSpeedKmh != nil {
		v := *r.LastSpeedKmh
		rider.SpeedKmh = &v
	}
	return rider
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
