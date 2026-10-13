package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatchDefaults(t *testing.T) {
	clearEnv(t)
	path := writeEnvFile(t, "DATABASE_URL=postgres://u:p@localhost:5432/db\n")

	cfg, err := Load(path)

	require.NoError(t, err)
	assert.Equal(t, MatchConfig{
		MaxDistanceKM: 10, LocationStaleAfter: 30 * time.Minute,
		WeightCategory: 0.25, WeightArea: 0.20, WeightProximity: 0.35, WeightAcceptance: 0.20, MaxResults: 10,
	}, cfg.Match)
}

func TestMatchCustomValidWeights(t *testing.T) {
	clearEnv(t)
	path := writeEnvFile(t, "DATABASE_URL=postgres://u:p@localhost:5432/db\nMATCH_WEIGHT_CATEGORY=0.1\nMATCH_WEIGHT_AREA=0.1\n"+
		"MATCH_WEIGHT_PROXIMITY=0.5\nMATCH_WEIGHT_ACCEPTANCE=0.3\nRIDER_LOCATION_STALE_AFTER=5m\nMATCH_MAX_DISTANCE_KM=2.5\nMATCH_MAX_RESULTS=3\n")

	cfg, err := Load(path)

	require.NoError(t, err)
	assert.Equal(t, 0.5, cfg.Match.WeightProximity)
	assert.Equal(t, 5*time.Minute, cfg.Match.LocationStaleAfter)
	assert.Equal(t, 3, cfg.Match.MaxResults)
}

func TestMatchValidation(t *testing.T) {
	tests := []struct{ name, env, want string }{
		{"weights sum to less than 1", "MATCH_WEIGHT_CATEGORY=0.2", "sum to 1.0"},
		{"weights sum to more than 1", "MATCH_WEIGHT_ACCEPTANCE=0.3", "sum to 1.0"},
		{"all weights zero", "MATCH_WEIGHT_CATEGORY=0\nMATCH_WEIGHT_AREA=0\nMATCH_WEIGHT_PROXIMITY=0\nMATCH_WEIGHT_ACCEPTANCE=0", "sum to 1.0"},
		{"negative weight", "MATCH_WEIGHT_CATEGORY=-0.25\nMATCH_WEIGHT_AREA=0.7", "MATCH_WEIGHT_CATEGORY"},
		{"weight above 1", "MATCH_WEIGHT_PROXIMITY=1.35\nMATCH_WEIGHT_CATEGORY=0\nMATCH_WEIGHT_AREA=0\nMATCH_WEIGHT_ACCEPTANCE=-0.35", "MATCH_WEIGHT"},
		{"zero max distance", "MATCH_MAX_DISTANCE_KM=0", "MATCH_MAX_DISTANCE_KM"},
		{"huge max distance", "MATCH_MAX_DISTANCE_KM=1000", "MATCH_MAX_DISTANCE_KM"},
		{"zero stale after", "RIDER_LOCATION_STALE_AFTER=0s", "RIDER_LOCATION_STALE_AFTER"},
		{"negative stale after", "RIDER_LOCATION_STALE_AFTER=-5m", "RIDER_LOCATION_STALE_AFTER"},
		{"zero max results", "MATCH_MAX_RESULTS=0", "MATCH_MAX_RESULTS"},
		{"too many results", "MATCH_MAX_RESULTS=1000", "MATCH_MAX_RESULTS"},
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

	t.Run("floating point noise in a valid sum is tolerated", func(t *testing.T) {
		clearEnv(t)
		path := writeEnvFile(t, "DATABASE_URL=postgres://u:p@localhost:5432/db\nMATCH_WEIGHT_CATEGORY=0.1\nMATCH_WEIGHT_AREA=0.2\nMATCH_WEIGHT_PROXIMITY=0.3\nMATCH_WEIGHT_ACCEPTANCE=0.4\n")

		_, err := Load(path)

		assert.NoError(t, err)
	})
}
