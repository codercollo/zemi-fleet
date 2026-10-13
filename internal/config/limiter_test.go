package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLimiterDefaults(t *testing.T) {
	clearEnv(t)
	path := writeEnvFile(t, "DATABASE_URL=postgres://u:p@localhost:5432/db\n")

	cfg, err := Load(path)

	require.NoError(t, err)
	assert.Equal(t, LimiterConfig{Enabled: true, RPS: 10, Burst: 20, TrustedProxies: []string{}}, cfg.Limiter)
	assert.Equal(t, 30*time.Second, cfg.ShutdownTimeout)
}

func TestLimiterFromFile(t *testing.T) {
	clearEnv(t)
	path := writeEnvFile(t, "DATABASE_URL=postgres://u:p@localhost:5432/db\nLIMITER_ENABLED=false\nLIMITER_RPS=2.5\nLIMITER_BURST=7\nTRUSTED_PROXIES=10.0.0.1,192.168.0.0/16,2001:db8::/32\nSHUTDOWN_TIMEOUT=45s\n")

	cfg, err := Load(path)

	require.NoError(t, err)
	assert.False(t, cfg.Limiter.Enabled)
	assert.Equal(t, 2.5, cfg.Limiter.RPS)
	assert.Equal(t, 7, cfg.Limiter.Burst)
	assert.Equal(t, []string{"10.0.0.1", "192.168.0.0/16", "2001:db8::/32"}, cfg.Limiter.TrustedProxies)
	assert.Equal(t, 45*time.Second, cfg.ShutdownTimeout)
}

func TestLimiterValidation(t *testing.T) {
	tests := []struct{ name, env, want string }{
		{"zero rps", "LIMITER_RPS=0", "LIMITER_RPS"},
		{"negative rps", "LIMITER_RPS=-1", "LIMITER_RPS"},
		{"absurd rps", "LIMITER_RPS=1e9", "LIMITER_RPS"},
		{"zero burst", "LIMITER_BURST=0", "LIMITER_BURST"},
		{"absurd burst", "LIMITER_BURST=999999999", "LIMITER_BURST"},
		{"a hostname is not an IP", "TRUSTED_PROXIES=proxy.example.com", "TRUSTED_PROXIES"},
		{"a bad CIDR", "TRUSTED_PROXIES=10.0.0.0/99", "TRUSTED_PROXIES"},
		{"one bad entry among good ones", "TRUSTED_PROXIES=10.0.0.1,nonsense", "nonsense"},
		{"zero shutdown timeout", "SHUTDOWN_TIMEOUT=0s", "SHUTDOWN_TIMEOUT"},
		{"negative shutdown timeout", "SHUTDOWN_TIMEOUT=-5s", "SHUTDOWN_TIMEOUT"},
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

	t.Run("a disabled limiter is still validated, so enabling it later cannot surprise anyone", func(t *testing.T) {
		clearEnv(t)
		path := writeEnvFile(t, "DATABASE_URL=postgres://u:p@localhost:5432/db\nLIMITER_ENABLED=false\nLIMITER_RPS=0\n")

		_, err := Load(path)

		assert.Error(t, err)
	})
}
