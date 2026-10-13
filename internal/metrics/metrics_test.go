package metrics

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCountersIncrement(t *testing.T) {
	c := New()
	c.OrderCreated()
	c.BatchesFormed(3)
	c.BatchesFormed(0)
	c.BatchAccepted()
	c.BatchAcceptConflict()
	c.BatchCapacityRejection()
	c.ConeMatchServed()
	c.RiderMatchServed()
	c.IdempotentReplay()

	want := map[string]string{
		"orders_created": "1", "batches_formed": "3", "batches_accepted": "1", "batch_accept_conflicts": "1",
		"batch_capacity_rejections": "1", "cone_matches_served": "1", "rider_matches_served": "1", "idempotent_replays": "1",
	}
	got := map[string]string{}
	for name, v := range c.Vars() {
		got[name] = v.String()
	}
	assert.Equal(t, want, got)
}

func TestNilCountersAreNoOps(t *testing.T) {
	var c *Counters
	assert.NotPanics(t, func() {
		c.OrderCreated()
		c.BatchesFormed(2)
		c.BatchAccepted()
		c.BatchAcceptConflict()
		c.BatchCapacityRejection()
		c.ConeMatchServed()
		c.RiderMatchServed()
		c.IdempotentReplay()
	})
}
