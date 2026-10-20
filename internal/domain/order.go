package domain

import "time"

// OrderStatus mirrors the order_status enum.
type OrderStatus string

const (
	OrderPending   OrderStatus = "pending"
	OrderBatched   OrderStatus = "batched"
	OrderAssigned  OrderStatus = "assigned"
	OrderPickedUp  OrderStatus = "picked_up"
	OrderDelivered OrderStatus = "delivered"
	OrderReturning OrderStatus = "returning"
	OrderReturned  OrderStatus = "returned"
	OrderCancelled OrderStatus = "cancelled"
)

// Order is a delivery request. Money is KES cents; distances are metres; bearings are degrees in [0, 360).
type Order struct {
	ID                     string
	VendorID               string
	CategoryCode           string
	Status                 OrderStatus
	Pickup                 Location
	Delivery               Location
	TripDistanceM          int
	TripBearingDeg         float64
	ValueCents             int64
	DeliveryFeeCents       int64
	RiderPayoutBPS         int
	RiderEarningsCents     int64
	PlatformFeeCents       int64
	DeliveryAttempts       int
	CancelledAt            *time.Time
	CancelReason           *string
	RiderCompensationCents int64
	BatchID                *string
	RiderID                *string
	CreatedAt              time.Time
	UpdatedAt              time.Time
	Version                int
}
