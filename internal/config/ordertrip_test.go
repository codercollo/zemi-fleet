package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOrderMinTripAndVisibilityDefaults(t *testing.T) {
	clearEnv(t)
	path := writeEnvFile(t, "DATABASE_URL=postgres://u:p@localhost:5432/db\n")

	cfg, err := Load(path)

	require.NoError(t, err)
	assert.Equal(t, 50, cfg.OrderMinTripM)
	assert.Equal(t, 3.0, cfg.Batch.VisibilityRadiusKM)
}

func TestOrderMinTripAndVisibilityValidation(t *testing.T) {
	tests := []struct{ name, env, want string }{
		{"negative min trip", "ORDER_MIN_TRIP_M=-1", "ORDER_MIN_TRIP_M"},
		{"absurd min trip", "ORDER_MIN_TRIP_M=50000", "ORDER_MIN_TRIP_M"},
		{"zero visibility radius", "BATCH_VISIBILITY_RADIUS_KM=0", "BATCH_VISIBILITY_RADIUS_KM"},
		{"visibility above the search radius", "BATCH_VISIBILITY_RADIUS_KM=6", "BATCH_VISIBILITY_RADIUS_KM"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			path := writeEnvFile(t, "DATABASE_URL=postgres://u:p@localhost:5432/db\n"+tc.env+"\n")

			_, err := Load(path)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}

	t.Run("min trip 0 is allowed, visibility equal to the search radius is allowed", func(t *testing.T) {
		clearEnv(t)
		path := writeEnvFile(t, "DATABASE_URL=postgres://u:p@localhost:5432/db\nORDER_MIN_TRIP_M=0\nBATCH_VISIBILITY_RADIUS_KM=5\n")

		cfg, err := Load(path)

		require.NoError(t, err)
		assert.Equal(t, 0, cfg.OrderMinTripM)
	})
}
