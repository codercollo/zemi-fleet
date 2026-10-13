package clock

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReal_NowIsUTCAndCurrent(t *testing.T) {
	var c Clock = Real{}

	first := c.Now()
	second := c.Now()

	assert.Equal(t, time.UTC, first.Location())
	assert.False(t, second.Before(first), "time must not run backwards")
	assert.WithinDuration(t, first, second, time.Second)
}

func TestTest_NowSetAdvance(t *testing.T) {
	nairobi := time.FixedZone("EAT", 3*60*60)
	start := time.Date(2026, 10, 13, 12, 0, 0, 0, nairobi) // 09:00 UTC

	tests := []struct {
		name string
		act  func(c *Test)
		want time.Time
	}{
		{"starts where told, in UTC", func(*Test) {}, time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)},
		{"advance", func(c *Test) { c.Advance(90 * time.Minute) }, time.Date(2026, 10, 13, 10, 30, 0, 0, time.UTC)},
		{"advance backwards", func(c *Test) { c.Advance(-time.Hour) }, time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)},
		{"set normalises to UTC", func(c *Test) { c.Set(time.Date(2027, 1, 1, 3, 0, 0, 0, nairobi)) }, time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"advance twice", func(c *Test) { c.Advance(time.Second); c.Advance(time.Second) }, time.Date(2026, 10, 13, 9, 0, 2, 0, time.UTC)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := NewTest(start)

			tc.act(c)

			got := c.Now()
			assert.True(t, tc.want.Equal(got), "want %s got %s", tc.want, got)
			assert.Equal(t, time.UTC, got.Location())
		})
	}
}

func TestTest_ConcurrentUse(t *testing.T) {
	c := NewTest(time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC))
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Advance(time.Second)
			_ = c.Now()
		}()
	}
	wg.Wait()

	require.True(t, c.Now().Equal(time.Date(2026, 10, 13, 9, 0, 50, 0, time.UTC)))
}
