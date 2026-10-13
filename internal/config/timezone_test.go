package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTimezone(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		want    string
		wantErr bool
	}{
		{"default is Nairobi", "", "Africa/Nairobi", false},
		{"custom IANA zone", "APP_TIMEZONE=Europe/London\n", "Europe/London", false},
		{"UTC is fine", "APP_TIMEZONE=UTC\n", "UTC", false},
		{"unknown zone is rejected", "APP_TIMEZONE=Mars/Olympus_Mons\n", "", true},
		{"abbreviation is rejected", "APP_TIMEZONE=EAT\n", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			path := writeEnvFile(t, "DATABASE_URL=postgres://u:p@localhost:5432/db\n"+tc.env)

			cfg, err := Load(path)

			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "APP_TIMEZONE")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, cfg.Timezone)
			assert.Equal(t, tc.want, cfg.Location().String())
		})
	}
}

func TestLocationFallsBackToUTC(t *testing.T) {
	assert.Equal(t, time.UTC, Config{Timezone: "not/a_zone"}.Location())
}
