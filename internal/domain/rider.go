package domain

import "time"

// MaxRiderCapacity is the largest max_capacity a rider can have (the riders table CHECKs it).
const MaxRiderCapacity = 5

// Preferences are a rider's matching preferences. An empty Categories or AreaIDs
// list means "no preference", which matches everything.
type Preferences struct {
	MinOrderValueCents int64
	Categories         []string
	AreaIDs            []string
}

// Rider is a courier with their last known telemetry. Location, BearingDeg and
// SpeedKmh are nil until the rider's first location ping.
type Rider struct {
	ID                string
	FullName          string
	Phone             string
	IsAvailable       bool
	MaxCapacity       int
	CurrentOrders     int
	Location          *Location
	BearingDeg        *float64
	SpeedKmh          *float64
	LocationUpdatedAt *time.Time
	OfflineFlaggedAt  *time.Time
	OffersCount       int
	AcceptedCount     int
	AcceptanceRate    float64
	Version           int
	CreatedAt         time.Time
	UpdatedAt         time.Time
	Preferences       Preferences
}
