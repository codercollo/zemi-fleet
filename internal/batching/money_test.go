package batching

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAddCents(t *testing.T) {
	tests := []struct {
		name string
		a, b int64
		want int64
	}{
		{"ordinary", 1999, 1, 2000},
		{"zero", 0, 0, 0},
		{"the maximum order fee, five times, is nowhere near the limit", 1_000_000_000 * 4, 1_000_000_000, 5_000_000_000},
		{"up to the limit is exact", math.MaxInt64 - 5, 5, math.MaxInt64},
		{"one past the limit saturates instead of wrapping negative", math.MaxInt64, 1, math.MaxInt64},
		{"far past the limit saturates", math.MaxInt64 - 10, math.MaxInt64 - 10, math.MaxInt64},
		{"negative amounts add", -500, -250, -750},
		{"negative overflow saturates", math.MinInt64, -1, math.MinInt64},
		{"mixed signs never overflow", math.MaxInt64, math.MinInt64, -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, addCents(tc.a, tc.b))
		})
	}
}

// Even absurd per-order amounts cannot make a batch total wrap around to a negative number, and the
// earnings total stays at or below the fee total (the batches table CHECKs that).
func TestBatchTotalsAreOverflowSafe(t *testing.T) {
	huge := int64(math.MaxInt64 / 3)
	var orders []Order
	for i := range 5 {
		o := northTrip(string(rune('a'+i)), at(float64(i)*50, 0), i)
		o.DeliveryFeeCents, o.RiderEarningsCents = huge, huge/10*9
		orders = append(orders, o)
	}

	b := Form(orders, params)[0]

	assert.EqualValues(t, math.MaxInt64, b.FeeCents, "5 x MaxInt64/3 saturates")
	assert.Greater(t, b.EarningsCents, int64(0), "never negative")
	assert.LessOrEqual(t, b.EarningsCents, b.FeeCents)
}

func TestBatchEarningsAreTheExactSumOfCents(t *testing.T) {
	fees := []int64{1999, 3, 1_000_000_000, 30}
	var orders []Order
	var wantFee, wantEarn int64
	for i, fee := range fees {
		o := northTrip(string(rune('a'+i)), at(float64(i)*50, 0), i)
		o.DeliveryFeeCents, o.RiderEarningsCents = fee, fee*9000/10000
		wantFee += fee
		wantEarn += fee * 9000 / 10000
		orders = append(orders, o)
	}

	b := Form(orders, params)[0]

	assert.Equal(t, wantFee, b.FeeCents)
	assert.Equal(t, wantEarn, b.EarningsCents)
}
