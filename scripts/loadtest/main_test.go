package main

import (
	"testing"
	"time"
)

func TestPercentileUsesNearestRank(t *testing.T) {
	values := []time.Duration{5 * time.Millisecond, 1 * time.Millisecond, 3 * time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond}
	if got := percentile(values, 0.50); got != 3*time.Millisecond {
		t.Fatalf("p50 = %s, want 3ms", got)
	}
	if got := percentile(values, 0.95); got != 5*time.Millisecond {
		t.Fatalf("p95 = %s, want 5ms", got)
	}
}

func TestErrorRateHasNoNaNForEmptyRun(t *testing.T) {
	if got := errorRate(&counters{}); got != 0 {
		t.Fatalf("empty error rate = %f, want 0", got)
	}
}
