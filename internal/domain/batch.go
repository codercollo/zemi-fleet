package domain

import "time"

// BatchStatus mirrors the batch_status enum.
type BatchStatus string

const (
	BatchOpen       BatchStatus = "open"
	BatchAccepted   BatchStatus = "accepted"
	BatchInProgress BatchStatus = "in_progress"
	BatchCompleted  BatchStatus = "completed"
	BatchCancelled  BatchStatus = "cancelled"
	BatchExpired    BatchStatus = "expired"
)

// StopType says whether a stop collects or drops off an order.
type StopType string

const (
	StopPickup   StopType = "pickup"
	StopDelivery StopType = "delivery"
)

// BatchOrder is an order as it sits inside a batch.
type BatchOrder struct {
	ID                 string
	Pickup             Location
	Delivery           Location
	TripDistanceM      int
	CreatedAt          time.Time
	RiderEarningsCents int64
	// Seq is the order's FIFO rank in the batch (1 = the oldest, the seed).
	Seq int
}

// BatchStop is one step of the suggested route.
type BatchStop struct {
	Seq          int
	Type         StopType
	OrderID      string
	Location     Location
	LegDistanceM int
	// ETAOffsetS is the time from the first stop to this one.
	ETAOffsetS int
}

// Batch is a group of orders offered to riders together. It stays open (assigned to nobody) until a
// rider accepts it, and expires at ExpiresAt if nobody does.
type Batch struct {
	ID                     string
	Status                 BatchStatus
	PickupCentroid         Location
	PickupSpreadM          int
	OrdersCount            int
	TotalDistanceM         int // the route: the sum of the stop legs
	EstimatedDurationS     int
	TotalDeliveryFeeCents  int64
	EstimatedEarningsCents int64
	RouteOptimizationScore float64
	ExpiresAt              time.Time
	CreatedAt              time.Time
	// RiderID and AcceptedAt are set once a rider has accepted the batch.
	RiderID    *string
	AcceptedAt *time.Time
	Version    int
	Orders     []BatchOrder // FIFO order
	Stops      []BatchStop  // route order
}

// DistanceSavedM is the sum of the orders' direct trip distances minus the batch's route: how much
// shorter the batch is than doing each trip alone. It is negative when the batch is longer.
func (b Batch) DistanceSavedM() int {
	solo := 0
	for _, o := range b.Orders {
		solo += o.TripDistanceM
	}
	return solo - b.TotalDistanceM
}

// FirstPickup is where the suggested route starts.
func (b Batch) FirstPickup() Location {
	if len(b.Stops) == 0 {
		return b.PickupCentroid
	}
	return b.Stops[0].Location
}

// OldestOrderAt is the creation time of the batch's oldest order (its seed), or the zero time for an empty batch.
func (b Batch) OldestOrderAt() time.Time {
	var oldest time.Time
	for _, o := range b.Orders {
		if oldest.IsZero() || o.CreatedAt.Before(oldest) {
			oldest = o.CreatedAt
		}
	}
	return oldest
}
