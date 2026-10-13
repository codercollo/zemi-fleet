package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSecurityDefaults(t *testing.T) {
	clearEnv(t)
	path := writeEnvFile(t, "DATABASE_URL=postgres://u:p@localhost:5432/db\n")

	cfg, err := Load(path)

	require.NoError(t, err)
	assert.Equal(t, 10_000_000.0, cfg.OrderMaxValueKES)
	assert.Equal(t, 10_000_000.0, cfg.OrderMaxDeliveryFeeKES)
	assert.Equal(t, 24*time.Hour, cfg.IdempotencyTTL)
	assert.Equal(t, 2*time.Minute, cfg.IdempotencyProcessingTimeout)
}

func TestSecurityFromFile(t *testing.T) {
	clearEnv(t)
	path := writeEnvFile(t, "DATABASE_URL=postgres://u:p@localhost:5432/db\nORDER_MAX_VALUE_KES=50000\nORDER_MAX_DELIVERY_FEE_KES=2000\nIDEMPOTENCY_TTL=1h\nIDEMPOTENCY_PROCESSING_TIMEOUT=30s\n")

	cfg, err := Load(path)

	require.NoError(t, err)
	assert.Equal(t, 50_000.0, cfg.OrderMaxValueKES)
	assert.Equal(t, 2_000.0, cfg.OrderMaxDeliveryFeeKES)
	assert.Equal(t, time.Hour, cfg.IdempotencyTTL)
	assert.Equal(t, 30*time.Second, cfg.IdempotencyProcessingTimeout)
}

func TestSecurityValidation(t *testing.T) {
	tests := []struct{ name, env, want string }{
		{"zero max value", "ORDER_MAX_VALUE_KES=0", "ORDER_MAX_VALUE_KES"},
		{"negative max value", "ORDER_MAX_VALUE_KES=-1", "ORDER_MAX_VALUE_KES"},
		{"max value that could overflow cents", "ORDER_MAX_VALUE_KES=1e12", "ORDER_MAX_VALUE_KES"},
		{"zero max fee", "ORDER_MAX_DELIVERY_FEE_KES=0", "ORDER_MAX_DELIVERY_FEE_KES"},
		{"max fee that could overflow cents", "ORDER_MAX_DELIVERY_FEE_KES=5000000000", "ORDER_MAX_DELIVERY_FEE_KES"},
		{"zero ttl", "IDEMPOTENCY_TTL=0s", "IDEMPOTENCY_TTL"},
		{"negative ttl", "IDEMPOTENCY_TTL=-1h", "IDEMPOTENCY_TTL"},
		{"zero processing timeout", "IDEMPOTENCY_PROCESSING_TIMEOUT=0s", "IDEMPOTENCY_PROCESSING_TIMEOUT"},
		{"processing timeout longer than the ttl", "IDEMPOTENCY_TTL=1m\nIDEMPOTENCY_PROCESSING_TIMEOUT=5m", "IDEMPOTENCY_PROCESSING_TIMEOUT"},
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

	t.Run("the largest allowed amount is accepted", func(t *testing.T) {
		clearEnv(t)
		path := writeEnvFile(t, "DATABASE_URL=postgres://u:p@localhost:5432/db\nORDER_MAX_VALUE_KES=1000000000\nORDER_MAX_DELIVERY_FEE_KES=1000000000\n")

		_, err := Load(path)

		assert.NoError(t, err)
	})
}
