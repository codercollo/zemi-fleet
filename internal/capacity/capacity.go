// Package capacity contains the pure rider-capacity policy shared by matching,
// batching, and batch acceptance.
package capacity

import "math"

type Mode string

const (
	ModeCount   Mode = "count"
	ModeDynamic Mode = "dynamic"
)

// Inputs are the rider state and the load/history used by dynamic capacity.
type Inputs struct {
	MaxCapacity     int
	CurrentOrders   int
	ActiveWeightG   int64
	ActiveVolumeCM3 int64
	Hour            int
	CompletionRate  float64
	DistanceTodayM  int64
}

// Params are deliberately small and deterministic. The defaults model a 25 kg
// and 1 m³ allowance per capacity slot; deployments can tune the policy later.
type Params struct {
	Mode             Mode
	MaxWeightG       int64
	MaxVolumeCM3     int64
	NightReduction   float64
	CompletionWeight float64
	DistanceWeight   float64
	DistanceLimitM   int64
}

func DefaultParams(mode Mode) Params {
	return Params{Mode: mode, MaxWeightG: 25_000, MaxVolumeCM3: 1_000_000, NightReduction: 0.8, CompletionWeight: 0.25, DistanceWeight: 0.2, DistanceLimitM: 100_000}
}

// EffectiveCapacity returns the maximum number of orders the rider may hold.
// It is always in [0, MaxCapacity], and never below the orders already held.
func EffectiveCapacity(in Inputs, p Params) int {
	max := clampInt(in.MaxCapacity, 0, 5)
	current := clampInt(in.CurrentOrders, 0, max)
	if p.Mode != ModeDynamic || max == 0 {
		return max
	}

	load := float64(current) / float64(max)
	if p.MaxWeightG > 0 {
		load = math.Max(load, float64(maxInt64(in.ActiveWeightG, 0))/float64(p.MaxWeightG*int64(max)))
	}
	if p.MaxVolumeCM3 > 0 {
		load = math.Max(load, float64(maxInt64(in.ActiveVolumeCM3, 0))/float64(p.MaxVolumeCM3*int64(max)))
	}
	load = math.Min(load, 1)
	factor := 1 - 0.5*load
	if in.Hour >= 22 || in.Hour < 6 {
		factor *= p.NightReduction
	}
	rate := math.Max(0, math.Min(1, in.CompletionRate))
	factor *= (1 - p.CompletionWeight*(1-rate))
	distance := math.Max(0, float64(in.DistanceTodayM))
	if p.DistanceLimitM > 0 {
		factor *= 1 - p.DistanceWeight*math.Min(1, distance/float64(p.DistanceLimitM))
	}
	effective := int(math.Floor(float64(max) * factor))
	if effective < current {
		effective = current
	}
	return clampInt(effective, 0, max)
}

func clampInt(v, low, high int) int {
	if v < low {
		return low
	}
	if v > high {
		return high
	}
	return v
}

func maxInt64(v, low int64) int64 {
	if v < low {
		return low
	}
	return v
}
