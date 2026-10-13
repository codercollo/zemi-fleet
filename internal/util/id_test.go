package util

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewID_Prefix(t *testing.T) {
	tests := []struct {
		name   string
		gen    func() string
		prefix string
	}{
		{"order", NewOrderID, "ord_"},
		{"rider", NewRiderID, "rider_"},
		{"batch", NewBatchID, "batch_"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := tc.gen()

			assert.True(t, strings.HasPrefix(id, tc.prefix))
			assert.Len(t, id, len(tc.prefix)+timeChars+randomChars)
			for _, r := range strings.TrimPrefix(id, tc.prefix) {
				assert.Contains(t, crockford, string(r))
			}
		})
	}
}

func TestNewID_SortsByTime(t *testing.T) {
	start := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	var prev string
	for i := range 50 {
		id, err := newID("ord_", start.Add(time.Duration(i)*time.Millisecond*37))
		require.NoError(t, err)
		if prev != "" {
			assert.Less(t, prev, id)
		}
		prev = id
	}
}

func TestNewID_Unique(t *testing.T) {
	seen := make(map[string]struct{}, 10000)
	for range 10000 {
		id := NewOrderID()
		_, dup := seen[id]
		require.False(t, dup, "duplicate id %s", id)
		seen[id] = struct{}{}
	}
}
