package capacity

import "testing"

func TestEffectiveCapacityBounds(t *testing.T) {
	p := DefaultParams(ModeDynamic)
	for max := -2; max <= 7; max++ {
		for current := -2; current <= 7; current++ {
			got := EffectiveCapacity(Inputs{MaxCapacity: max, CurrentOrders: current}, p)
			upper := max
			if upper < 0 {
				upper = 0
			}
			if got < 0 || got > upper {
				t.Fatalf("max=%d current=%d got=%d", max, current, got)
			}
		}
	}
}

func TestCountModeIgnoresDynamicInputs(t *testing.T) {
	got := EffectiveCapacity(Inputs{MaxCapacity: 5, CurrentOrders: 4, ActiveWeightG: 99999999, Hour: 2}, DefaultParams(ModeCount))
	if got != 5 {
		t.Fatalf("got %d, want 5", got)
	}
}

func TestDynamicCapacityRespondsToLoad(t *testing.T) {
	p := DefaultParams(ModeDynamic)
	if light, heavy := EffectiveCapacity(Inputs{MaxCapacity: 5}, p), EffectiveCapacity(Inputs{MaxCapacity: 5, ActiveWeightG: 125_000}, p); heavy >= light {
		t.Fatalf("light=%d heavy=%d", light, heavy)
	}
}
