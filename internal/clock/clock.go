// Package clock is the only place the application asks for the current time. Business
// timestamps (created_at, location_updated_at, expires_at, ...) come from an injected
// Clock, always in UTC, so tests can control time and every layer agrees on one standard.
package clock

import (
	"sync"
	"time"
)

// Clock tells the time.
type Clock interface {
	Now() time.Time
}

// Real is the production clock. It always returns UTC.
type Real struct{}

// Now returns the current time in UTC.
func (Real) Now() time.Time { return time.Now().UTC() }

// Test is a clock for tests: it stands still until Set or Advance moves it.
// It is safe for concurrent use.
type Test struct {
	mu  sync.Mutex
	now time.Time
}

// NewTest returns a Test clock starting at t (normalised to UTC).
func NewTest(t time.Time) *Test { return &Test{now: t.UTC()} }

// Now returns the current fake time in UTC.
func (c *Test) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Set jumps the clock to t (normalised to UTC).
func (c *Test) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t.UTC()
}

// Advance moves the clock forward (or backward, if d is negative) by d.
func (c *Test) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

var (
	_ Clock = Real{}
	_ Clock = (*Test)(nil)
)
