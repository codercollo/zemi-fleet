package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBatchDefaults(t *testing.T) {
	clearEnv(t)
	path := writeEnvFile(t, "DATABASE_URL=postgres://u:p@localhost:5432/db\n")

	cfg, err := Load(path)

	require.NoError(t, err)
	assert.Equal(t, BatchConfig{
		MaxOrders: 5, PickupRadiusKM: 2, DirectionToleranceDeg: 45, SearchRadiusKM: 5,
		TTL: 10 * time.Minute, AvgSpeedKMH: 20, StopDwellMin: 2,
	}, cfg.Batch)
}

func TestBatchFromFile(t *testing.T) {
	clearEnv(t)
	path := writeEnvFile(t, "DATABASE_URL=postgres://u:p@localhost:5432/db\nBATCH_MAX_ORDERS=3\nBATCH_PICKUP_RADIUS_KM=1.5\n"+
		"BATCH_DIRECTION_TOLERANCE_DEG=30\nBATCH_SEARCH_RADIUS_KM=8\nBATCH_TTL=90s\nAVG_SPEED_KMH=25\nSTOP_DWELL_MIN=0\n")

	cfg, err := Load(path)

	require.NoError(t, err)
	assert.Equal(t, BatchConfig{
		MaxOrders: 3, PickupRadiusKM: 1.5, DirectionToleranceDeg: 30, SearchRadiusKM: 8,
		TTL: 90 * time.Second, AvgSpeedKMH: 25, StopDwellMin: 0,
	}, cfg.Batch)
}

func TestBatchValidation(t *testing.T) {
	tests := []struct{ name, env, want string }{
		{"zero max orders", "BATCH_MAX_ORDERS=0", "BATCH_MAX_ORDERS"},
		{"max orders above the database limit", "BATCH_MAX_ORDERS=6", "BATCH_MAX_ORDERS"},
		{"zero pickup radius", "BATCH_PICKUP_RADIUS_KM=0", "BATCH_PICKUP_RADIUS_KM"},
		{"negative pickup radius", "BATCH_PICKUP_RADIUS_KM=-2", "BATCH_PICKUP_RADIUS_KM"},
		{"huge pickup radius", "BATCH_PICKUP_RADIUS_KM=500", "BATCH_PICKUP_RADIUS_KM"},
		{"zero tolerance", "BATCH_DIRECTION_TOLERANCE_DEG=0", "BATCH_DIRECTION_TOLERANCE_DEG"},
		{"tolerance above 180", "BATCH_DIRECTION_TOLERANCE_DEG=181", "BATCH_DIRECTION_TOLERANCE_DEG"},
		{"zero search radius", "BATCH_SEARCH_RADIUS_KM=0", "BATCH_SEARCH_RADIUS_KM"},
		{"huge search radius", "BATCH_SEARCH_RADIUS_KM=51", "BATCH_SEARCH_RADIUS_KM"},
		{"zero ttl", "BATCH_TTL=0s", "BATCH_TTL"},
		{"negative ttl", "BATCH_TTL=-1m", "BATCH_TTL"},
		{"zero speed", "AVG_SPEED_KMH=0", "AVG_SPEED_KMH"},
		{"absurd speed", "AVG_SPEED_KMH=500", "AVG_SPEED_KMH"},
		{"negative dwell", "STOP_DWELL_MIN=-1", "STOP_DWELL_MIN"},
		{"absurd dwell", "STOP_DWELL_MIN=600", "STOP_DWELL_MIN"},
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
}
