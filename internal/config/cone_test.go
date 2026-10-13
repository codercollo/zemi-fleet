package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConeDefaults(t *testing.T) {
	clearEnv(t)
	path := writeEnvFile(t, "DATABASE_URL=postgres://u:p@localhost:5432/db\n")

	cfg, err := Load(path)

	require.NoError(t, err)
	assert.Equal(t, ConeConfig{RadiusKM: 1, HalfAngleDeg: 30, MinSpeedKMH: 3, MaxResults: 20}, cfg.Cone)
}

func TestConeFromFile(t *testing.T) {
	clearEnv(t)
	path := writeEnvFile(t, "DATABASE_URL=postgres://u:p@localhost:5432/db\nCONE_RADIUS_KM=2.5\nCONE_HALF_ANGLE_DEG=45\nCONE_MIN_SPEED_KMH=0\nCONE_MAX_RESULTS=5\n")

	cfg, err := Load(path)

	require.NoError(t, err)
	assert.Equal(t, ConeConfig{RadiusKM: 2.5, HalfAngleDeg: 45, MinSpeedKMH: 0, MaxResults: 5}, cfg.Cone)
}

func TestConeValidation(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want string
	}{
		{"zero radius", "CONE_RADIUS_KM=0", "CONE_RADIUS_KM"},
		{"negative radius", "CONE_RADIUS_KM=-1", "CONE_RADIUS_KM"},
		{"huge radius", "CONE_RADIUS_KM=500", "CONE_RADIUS_KM"},
		{"zero angle", "CONE_HALF_ANGLE_DEG=0", "CONE_HALF_ANGLE_DEG"},
		{"angle above 180", "CONE_HALF_ANGLE_DEG=181", "CONE_HALF_ANGLE_DEG"},
		{"negative min speed", "CONE_MIN_SPEED_KMH=-1", "CONE_MIN_SPEED_KMH"},
		{"absurd min speed", "CONE_MIN_SPEED_KMH=500", "CONE_MIN_SPEED_KMH"},
		{"zero max results", "CONE_MAX_RESULTS=0", "CONE_MAX_RESULTS"},
		{"too many results", "CONE_MAX_RESULTS=100000", "CONE_MAX_RESULTS"},
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

	t.Run("all problems are reported together", func(t *testing.T) {
		clearEnv(t)
		path := writeEnvFile(t, "DATABASE_URL=postgres://u:p@localhost:5432/db\nCONE_RADIUS_KM=0\nCONE_MAX_RESULTS=0\n")

		_, err := Load(path)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "CONE_RADIUS_KM")
		assert.Contains(t, err.Error(), "CONE_MAX_RESULTS")
	})
}
