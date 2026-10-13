package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clearEnv blanks every setting so tests never see values exported by the shell
// or by `make` (which exports app.env). Viper treats an empty variable as unset.
func clearEnv(t *testing.T) {
	t.Helper()
	for key := range defaults {
		t.Setenv(key, "")
	}
}

// Helper to write env file for tests
func writeEnvFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.env")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// Test loading config from file with defaults
func TestLoadFromFileWithDefaults(t *testing.T) {
	clearEnv(t)
	path := writeEnvFile(t, "DATABASE_URL=postgres://u:p@localhost:5432/db\n")

	cfg, err := Load(path)

	require.NoError(t, err)
	assert.Equal(t, EnvDevelopment, cfg.Env)
	assert.Equal(t, 4000, cfg.Port)
	assert.Equal(t, int32(25), cfg.DB.MaxConns)
	assert.Equal(t, 15*time.Minute, cfg.DB.MaxConnIdleTime)
}

// Test environment variables override file values
func TestEnvironmentOverridesFile(t *testing.T) {
	clearEnv(t)
	path := writeEnvFile(t, "DATABASE_URL=postgres://file\nPORT=4000\n")
	t.Setenv("PORT", "9090")
	t.Setenv("APP_ENV", EnvStaging)

	cfg, err := Load(path)

	require.NoError(t, err)
	assert.Equal(t, 9090, cfg.Port)
	assert.Equal(t, EnvStaging, cfg.Env)
}

// Test missing file uses environment variables only.
func TestMissingFileUsesEnvironmentOnly(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_URL", "postgres://from-env")

	cfg, err := Load(filepath.Join(t.TempDir(), "does-not-exist.env"))

	require.NoError(t, err)
	assert.Equal(t, "postgres://from-env", cfg.DB.DSN)
}

// Test configuration validation errors
func TestValidationErrors(t *testing.T) {
	clearEnv(t)
	path := writeEnvFile(t, "APP_ENV=prod\nPORT=0\nDB_MIN_CONNS=50\n")

	_, err := Load(path)

	require.Error(t, err)
	for _, want := range []string{"APP_ENV", "PORT", "DATABASE_URL", "DB_MIN_CONNS"} {
		assert.Contains(t, err.Error(), want)
	}
}

func TestRiderPayoutBPS(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		want    int
		wantErr string
	}{
		{"default is 90%", "", 9000, ""},
		{"custom value", "RIDER_PAYOUT_BPS=8500\n", 8500, ""},
		{"zero is allowed", "RIDER_PAYOUT_BPS=0\n", 0, ""},
		{"above 100% is rejected", "RIDER_PAYOUT_BPS=10001\n", 0, "RIDER_PAYOUT_BPS"},
		{"negative is rejected", "RIDER_PAYOUT_BPS=-1\n", 0, "RIDER_PAYOUT_BPS"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			path := writeEnvFile(t, "DATABASE_URL=postgres://u:p@localhost:5432/db\n"+tc.env)

			cfg, err := Load(path)

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, cfg.RiderPayoutBPS)
		})
	}
}

func TestQueryTimeout(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		want    time.Duration
		wantErr bool
	}{
		{"default is 3s", "", 3 * time.Second, false},
		{"custom", "DB_QUERY_TIMEOUT=500ms\n", 500 * time.Millisecond, false},
		{"zero is rejected", "DB_QUERY_TIMEOUT=0s\n", 0, true},
		{"negative is rejected", "DB_QUERY_TIMEOUT=-1s\n", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			path := writeEnvFile(t, "DATABASE_URL=postgres://u:p@localhost:5432/db\n"+tc.env)

			cfg, err := Load(path)

			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "DB_QUERY_TIMEOUT")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, cfg.DB.QueryTimeout)
		})
	}
}
