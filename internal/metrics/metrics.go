// Package metrics holds the domain counters published at GET /debug/vars (Let's Go Further ch. 19).
//
// The counters are expvar.Int values but are NOT registered in expvar's global registry: expvar.NewInt panics when a
// name is published twice, and every test builds its own server. The API layer publishes them through a per-server
// expvar.Map instead.
package metrics

import "expvar"

// Counters counts business events. Services call its methods after the work has COMMITTED, so a failed or rolled-back
// request never moves a counter. Every method is safe on a nil *Counters (it does nothing), so a service built
// without metrics works unchanged.
type Counters struct {
	ordersCreated          expvar.Int
	batchesFormed          expvar.Int
	batchesAccepted        expvar.Int
	batchAcceptConflicts   expvar.Int
	batchCapacityRejection expvar.Int
	coneMatchesServed      expvar.Int
	riderMatchesServed     expvar.Int
	idempotentReplays      expvar.Int
	batchesExpired         expvar.Int
}

// New returns zeroed counters.
func New() *Counters { return &Counters{} }

// Vars lists every counter under its published name, in the order they appear in the documentation.
func (c *Counters) Vars() map[string]*expvar.Int {
	return map[string]*expvar.Int{
		"orders_created":            &c.ordersCreated,
		"batches_formed":            &c.batchesFormed,
		"batches_accepted":          &c.batchesAccepted,
		"batch_accept_conflicts":    &c.batchAcceptConflicts,
		"batch_capacity_rejections": &c.batchCapacityRejection,
		"cone_matches_served":       &c.coneMatchesServed,
		"rider_matches_served":      &c.riderMatchesServed,
		"idempotent_replays":        &c.idempotentReplays,
		"batches_expired":           &c.batchesExpired,
	}
}

func add(v *expvar.Int, n int64) { v.Add(n) }

// OrderCreated records a stored order.
func (c *Counters) OrderCreated() {
	if c != nil {
		add(&c.ordersCreated, 1)
	}
}

// BatchesFormed records n newly formed batches (not batches that were already open).
func (c *Counters) BatchesFormed(n int) {
	if c != nil && n > 0 {
		add(&c.batchesFormed, int64(n))
	}
}

// BatchAccepted records a rider winning a batch.
func (c *Counters) BatchAccepted() {
	if c != nil {
		add(&c.batchesAccepted, 1)
	}
}

// BatchAcceptConflict records an accept that lost the race (409). A rising value means riders are racing for the
// same batches.
func (c *Counters) BatchAcceptConflict() {
	if c != nil {
		add(&c.batchAcceptConflicts, 1)
	}
}

// BatchCapacityRejection records an accept refused because the rider has no room (422).
func (c *Counters) BatchCapacityRejection() {
	if c != nil {
		add(&c.batchCapacityRejection, 1)
	}
}

// ConeMatchServed records a completed POST /riders/match-orders.
func (c *Counters) ConeMatchServed() {
	if c != nil {
		add(&c.coneMatchesServed, 1)
	}
}

// RiderMatchServed records a completed POST /orders/match-riders.
func (c *Counters) RiderMatchServed() {
	if c != nil {
		add(&c.riderMatchesServed, 1)
	}
}

// IdempotentReplay records a stored response being replayed instead of the work being repeated.
func (c *Counters) IdempotentReplay() {
	if c != nil {
		add(&c.idempotentReplays, 1)
	}
}

// BatchesExpired records batches released by a committed expiry sweep.
func (c *Counters) BatchesExpired(n int) {
	if c != nil && n > 0 {
		add(&c.batchesExpired, int64(n))
	}
}
