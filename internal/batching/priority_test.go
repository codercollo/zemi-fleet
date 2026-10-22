package batching

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestPriorityOrderingKeepsOldOrdersAheadOfFiniteAdvantages(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	p := params
	p.Ordering = "priority"
	p.Now = now
	p.WeightAge, p.WeightValue, p.WeightRating, p.WeightUrgency = .8, .1, .05, .05
	old := northTrip("old", origin, 0)
	old.CreatedAt = now.Add(-24 * time.Hour)
	old.ValueCents = 1
	newHighValue := northTrip("new", origin, 1)
	newHighValue.CreatedAt = now.Add(-time.Minute)
	newHighValue.ValueCents = 10_000_000

	got := Form([]Order{newHighValue, old}, p)
	assert.Equal(t, []string{"old", "new"}, []string{got[0].Orders[0].ID, got[0].Orders[1].ID})
}

func TestPriorityTieUsesFIFOAndID(t *testing.T) {
	p := params
	p.Ordering = "priority"
	p.Now = t0.Add(time.Hour)
	p.WeightAge, p.WeightValue, p.WeightRating, p.WeightUrgency = 1, 0, 0, 0
	a := northTrip("b", origin, 0)
	b := northTrip("a", origin, 0)
	got := Form([]Order{a, b}, p)
	assert.Equal(t, []string{"a", "b"}, []string{got[0].Orders[0].ID, got[0].Orders[1].ID})
}

func TestAdaptiveRadiusOnlyNarrowsDenseClusters(t *testing.T) {
	p := params
	p.RadiusMode = "adaptive"
	p.DensityRadiusM = 3000
	p.DensityLowThreshold = 0
	p.DensityHighThreshold = 3
	p.DensityHighRadiusM = 1000
	p.DensityMediumRadiusM = 2000
	p.DensityLowRadiusM = 2000
	orders := []Order{
		northTrip("a", origin, 0),
		northTrip("b", at(500, 0), 1),
		northTrip("c", at(1500, 0), 2),
	}

	got := Form(orders, p)
	assert.Equal(t, [][]string{{"a", "b"}, {"c"}}, batchIDs(got))
	for _, batch := range got {
		for i := range batch.Orders {
			for j := i + 1; j < len(batch.Orders); j++ {
				assert.LessOrEqual(t, batch.PickupSpreadM, 1000.0+1e-6)
			}
		}
	}
}

func TestPriorityScoreClampsOptionalInputs(t *testing.T) {
	o := Order{CreatedAt: t0, ValueCents: -1, VendorRating: 99, Urgency: 999}
	p := Params{WeightAge: 0, WeightValue: 0, WeightRating: .5, WeightUrgency: .5}
	assert.Equal(t, 1.0, PriorityScore(o, t0, p))
}
