package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	key16 = "0123456789abcdef"                 // 16 characters: enough for development only
	key32 = "0123456789abcdef0123456789abcdef" // 32 characters
	key33 = key32 + "x"
)

// loadWith loads a config whose only deliberate settings are the environment and API_KEYS.
func loadWith(t *testing.T, env, apiKeys string) (Config, error) {
	t.Helper()
	clearEnv(t)
	t.Setenv("APP_ENV", env)
	t.Setenv("API_KEYS", apiKeys)
	return Load(writeEnvFile(t, "DATABASE_URL=postgres://u:p@localhost:5432/db\n"))
}

func TestAPIKeys_Valid(t *testing.T) {
	tests := []struct {
		name, env, raw string
		wantNames      []string
	}{
		{"one key in development", EnvDevelopment, "postman=" + key16, []string{"postman"}},
		{"two keys, spaces around the pairs are trimmed", EnvDevelopment, "postman=" + key16 + " , ops=" + key33, []string{"postman", "ops"}},
		{"a trailing comma is ignored", EnvDevelopment, "postman=" + key16 + ",", []string{"postman"}},
		{"32 characters in staging", EnvStaging, "ops=" + key32, []string{"ops"}},
		{"32 characters in production", EnvProduction, "ops=" + key32, []string{"ops"}},
		{"a key may contain =", EnvProduction, "ops=" + key32 + "==", []string{"ops"}},
		{"name of 32 characters", EnvDevelopment, strings.Repeat("a", 32) + "=" + key16, []string{strings.Repeat("a", 32)}},
		{"name with digits, - and _", EnvDevelopment, "ops_team-2=" + key16, []string{"ops_team-2"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadWith(t, tc.env, tc.raw)

			require.NoError(t, err)
			var names []string
			for _, k := range cfg.APIKeys {
				names = append(names, k.Name)
			}
			assert.Equal(t, tc.wantNames, names)
		})
	}
}

func TestAPIKeys_OnlyHashesAreKept(t *testing.T) {
	cfg, err := loadWith(t, EnvDevelopment, "postman="+key16+",ops="+key33)

	require.NoError(t, err)
	assert.Empty(t, cfg.APIKeysRaw, "the raw string is dropped once parsed")
	require.Len(t, cfg.APIKeys, 2)
	assert.Equal(t, HashAPIKey(key16), cfg.APIKeys[0].Hash)
	assert.Equal(t, HashAPIKey(key33), cfg.APIKeys[1].Hash)
	assert.Equal(t, NewAPIKey("ops", key33), cfg.APIKeys[1])
}

func TestAPIKeys_Invalid(t *testing.T) {
	tests := []struct {
		name, env, raw, want string
	}{
		{"no keys at all", EnvDevelopment, "", "API_KEYS is required"},
		{"only separators", EnvDevelopment, " , ,", "API_KEYS is required"},
		{"a pair without =", EnvDevelopment, key16, "must look like name=key"},
		{"an empty name", EnvDevelopment, "=" + key16, "API_KEYS name"},
		{"an upper-case name", EnvDevelopment, "Postman=" + key16, "API_KEYS name"},
		{"a name with a space", EnvDevelopment, "my key=" + key16, "API_KEYS name"},
		{"a name of 33 characters", EnvDevelopment, strings.Repeat("a", 33) + "=" + key16, "API_KEYS name"},
		{"a 15 character key in development", EnvDevelopment, "postman=" + key16[:15], "at least 16 characters"},
		{"a 16 character key in staging", EnvStaging, "ops=" + key16, "at least 32 characters in staging"},
		{"a 31 character key in production", EnvProduction, "ops=" + key32[:31], "at least 32 characters in production"},
		{"a duplicate name", EnvDevelopment, "ops=" + key16 + ",ops=" + key33, `name "ops" is used twice`},
		{"a duplicate key under two names", EnvDevelopment, "ops=" + key16 + ",postman=" + key16, "duplicates another key"},
		{"the demo key in production", EnvProduction, "demo=" + DemoAPIKey, "the published demo key"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadWith(t, tc.env, tc.raw)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestAPIKeys_PublishedKeysAreRefusedInProduction(t *testing.T) {
	assert.GreaterOrEqual(t, len(DemoAPIKey), minAPIKeyLenDeployed, "the demo key must be usable in staging")

	t.Run("the demo key works in staging and development", func(t *testing.T) {
		for _, env := range []string{EnvDevelopment, EnvStaging} {
			_, err := loadWith(t, env, "demo="+DemoAPIKey)
			require.NoError(t, err, env)
		}
	})
	t.Run("the demo key is refused in production, even next to a real one", func(t *testing.T) {
		_, err := loadWith(t, EnvProduction, "ops="+key32+",demo="+DemoAPIKey)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "the published demo key")
	})
}

func TestAPIKeys_ErrorsNeverEchoAKey(t *testing.T) {
	secret := "SuperSecretKeyValue0123456789"
	for _, raw := range []string{secret, "ops=" + secret[:10], "Bad Name=" + secret, "demo=" + DemoAPIKey} {
		_, err := loadWith(t, EnvProduction, raw)

		require.Error(t, err)
		assert.NotContains(t, err.Error(), secret)
		assert.NotContains(t, err.Error(), secret[:10])
		assert.NotContains(t, err.Error(), DemoAPIKey)
	}
}

func TestFingerprint(t *testing.T) {
	assert.Len(t, Fingerprint(key32), 8)
	assert.Equal(t, Fingerprint(key32), Fingerprint(key32))
	assert.NotEqual(t, Fingerprint(key32), Fingerprint(key33))
	assert.NotContains(t, key32, Fingerprint(key32))
}
