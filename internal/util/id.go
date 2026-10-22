// Package util holds small helpers with no project dependencies.
package util

import (
	"crypto/rand"
	"fmt"
	"time"
)

// Prefixes of the IDs the app generates.
const (
	PrefixOrder  = "ord_"
	PrefixRider  = "rider_"
	PrefixBatch  = "batch_"
	PrefixOutbox = "out_"
)

// crockford is Crockford's base32 alphabet. Its characters are in ascending ASCII
// order, so encoded strings of equal length sort the same way as the numbers they encode.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

const (
	timeChars   = 10 // 50 bits of milliseconds since the Unix epoch, good for thousands of years
	randomChars = 10 // 50 random bits, so two IDs in the same millisecond rarely collide
)

// NewOrderID, NewRiderID and NewBatchID return IDs like "ord_01HX9Z3K4M7Q2R8T5V".
func NewOrderID(now time.Time) string  { return NewID(PrefixOrder, now) }
func NewRiderID(now time.Time) string  { return NewID(PrefixRider, now) }
func NewBatchID(now time.Time) string  { return NewID(PrefixBatch, now) }
func NewOutboxID(now time.Time) string { return NewID(PrefixOutbox, now) }

// NewID returns prefix + a time-sortable suffix: IDs created later sort after
// earlier ones (millisecond resolution), which keeps primary-key inserts mostly
// append-only and matches the brief's "rider_123" style. The caller passes the time
// (from the injected clock.Clock) so this package never reads the system clock.
func NewID(prefix string, now time.Time) string {
	id, err := newID(prefix, now)
	if err != nil {
		// crypto/rand failing means the OS entropy source is broken; nothing sensible can continue.
		panic(fmt.Errorf("generate id: %w", err))
	}
	return id
}

func newID(prefix string, now time.Time) (string, error) {
	buf := make([]byte, timeChars+randomChars)

	ms := uint64(now.UnixMilli())
	for i := timeChars - 1; i >= 0; i-- {
		buf[i] = crockford[ms&31]
		ms >>= 5
	}

	var rnd [randomChars]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", err
	}
	for i, b := range rnd {
		buf[timeChars+i] = crockford[b&31]
	}
	return prefix + string(buf), nil
}
