package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codercollo/zemi-fleet/internal/domain"
)

// API timestamps are RFC 3339 in UTC ("...Z") whatever zone the time carries internally.
func TestDTOTimestampsAreUTC(t *testing.T) {
	eat := time.FixedZone("EAT", 3*60*60)
	local := time.Date(2026, 10, 13, 12, 30, 15, 0, eat) // 09:30:15Z

	t.Run("order created_at", func(t *testing.T) {
		js, err := json.Marshal(newOrderDTO(domain.Order{ID: "ord_1", CreatedAt: local}))
		require.NoError(t, err)

		var out map[string]any
		require.NoError(t, json.Unmarshal(js, &out))
		assert.Equal(t, "2026-10-13T09:30:15Z", out["created_at"])
	})

	t.Run("rider location_updated_at", func(t *testing.T) {
		js, err := json.Marshal(newRiderDTO(domain.Rider{ID: "rider_1", LocationUpdatedAt: &local}))
		require.NoError(t, err)

		var out map[string]any
		require.NoError(t, json.Unmarshal(js, &out))
		assert.Equal(t, "2026-10-13T09:30:15Z", out["location_updated_at"])
		assert.Equal(t, eat, local.Location(), "the input time must not be mutated")
	})

	t.Run("missing location_updated_at is null", func(t *testing.T) {
		js, err := json.Marshal(newRiderDTO(domain.Rider{ID: "rider_1"}))
		require.NoError(t, err)

		assert.Contains(t, string(js), `"location_updated_at":null`)
	})
}

func TestKESConversion(t *testing.T) {
	tests := []struct {
		kes       float64
		wantCents int64
		wantOK    bool
	}{
		{4500.50, 450050, true}, {0.1, 10, true}, {19.99, 1999, true},
		{10.005, 0, false}, {1e300, 0, false},
	}
	for _, tc := range tests {
		cents, ok := kesToCents(tc.kes)
		assert.Equal(t, tc.wantOK, ok, "%v", tc.kes)
		if tc.wantOK {
			assert.Equal(t, tc.wantCents, cents)
		}
	}
}
