package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const baseEnv = "DATABASE_URL=postgres://u:p@localhost:5432/db\n"

func TestCORSDefaultsToNoOrigins(t *testing.T) {
	clearEnv(t)

	cfg, err := Load(writeEnvFile(t, baseEnv))

	require.NoError(t, err)
	assert.Empty(t, cfg.CORS.TrustedOrigins)
}

func TestCORSTrustedOriginsFromFile(t *testing.T) {
	clearEnv(t)

	cfg, err := Load(writeEnvFile(t, baseEnv+"CORS_TRUSTED_ORIGINS=http://localhost:3000,https://app.zemi.africa\n"))

	require.NoError(t, err)
	assert.Equal(t, []string{"http://localhost:3000", "https://app.zemi.africa"}, cfg.CORS.TrustedOrigins)
}

func TestCORSValidation(t *testing.T) {
	tests := []struct{ name, value, want string }{
		{"a wildcard", "*", "wildcard"},
		{"a wildcard subdomain", "https://*.zemi.africa", "wildcard"},
		{"a path", "https://app.zemi.africa/app", "no path"},
		{"a trailing slash", "https://app.zemi.africa/", "no path"},
		{"a query", "https://app.zemi.africa?x=1", "no path"},
		{"no scheme", "app.zemi.africa", "http:// or https://"},
		{"a non-web scheme", "ftp://app.zemi.africa", "http:// or https://"},
		{"no host", "https://", "host"},
		{"user info", "https://user@app.zemi.africa", "user info"},
		{"uppercase", "https://App.Zemi.africa", "lowercase"},
		{"the literal null origin", "null", "http:// or https://"},
		{"one bad entry among good ones", "http://localhost:3000,*", "wildcard"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)

			_, err := Load(writeEnvFile(t, baseEnv+"CORS_TRUSTED_ORIGINS="+tc.value+"\n"))

			require.Error(t, err)
			assert.Contains(t, err.Error(), "CORS_TRUSTED_ORIGINS")
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestMetricsEnabledDefaultsByEnvironment(t *testing.T) {
	tests := []struct {
		name, extra string
		want        bool
	}{
		{"development", "APP_ENV=development\n", true},
		{"staging", "APP_ENV=staging\nAPI_KEYS=tests=config-test-key-0123456789abcdef0123\n", true},
		{"production", "APP_ENV=production\nAPI_KEYS=tests=config-test-key-0123456789abcdef0123\n", false},
		{"production, switched on", "APP_ENV=production\nMETRICS_ENABLED=true\n", true},
		{"development, switched off", "APP_ENV=development\nMETRICS_ENABLED=false\n", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)

			cfg, err := Load(writeEnvFile(t, baseEnv+tc.extra))

			require.NoError(t, err)
			assert.Equal(t, tc.want, cfg.MetricsEnabled)
		})
	}
}

func TestMetricsEnabledFromEnvironmentVariable(t *testing.T) {
	clearEnv(t)
	t.Setenv("METRICS_ENABLED", "false")

	cfg, err := Load(writeEnvFile(t, baseEnv))

	require.NoError(t, err)
	assert.False(t, cfg.MetricsEnabled)
}

func TestDocsEnabledDefaultsByEnvironment(t *testing.T) {
	const key = "API_KEYS=tests=config-test-key-0123456789abcdef0123\n"
	tests := []struct {
		name, extra string
		want        bool
	}{
		{"development", "APP_ENV=development\n", true},
		{"staging", "APP_ENV=staging\n" + key, true},
		{"production", "APP_ENV=production\n" + key, false},
		{"production, switched on", "APP_ENV=production\nDOCS_ENABLED=true\n" + key, true},
		{"development, switched off", "DOCS_ENABLED=false\n", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)

			cfg, err := Load(writeEnvFile(t, baseEnv+tc.extra))

			require.NoError(t, err)
			assert.Equal(t, tc.want, cfg.DocsEnabled)
		})
	}
}
