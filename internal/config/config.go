// Package config loads and validates settings from environment variables, app.env and defaults.
package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/viper"
)

// Environment names
const (
	EnvDevelopment = "development"
	EnvStaging     = "staging"
	EnvProduction  = "production"
)

// Config holds every application setting.
type Config struct {
	Env  string `mapstructure:"APP_ENV"`
	Port int    `mapstructure:"PORT"`
	// Timezone is the business time zone (IANA name) used for presentation and day boundaries.
	// Storage, the API and the database session are always UTC.
	Timezone string   `mapstructure:"APP_TIMEZONE"`
	DB       DBConfig `mapstructure:",squash"`

	// RiderPayoutBPS is the rider's share of each delivery fee in basis points
	// (9000 = 90%). It is snapshotted on every order, so changing it never
	// rewrites history.
	RiderPayoutBPS int `mapstructure:"RIDER_PAYOUT_BPS"`

	Cone ConeConfig `mapstructure:",squash"`
}

// ConeConfig holds the proximity-matching (60 degree cone) tunables.
type ConeConfig struct {
	// RadiusKM is how far from the rider a pickup may be (inclusive).
	RadiusKM float64 `mapstructure:"CONE_RADIUS_KM"`
	// HalfAngleDeg is half the cone's opening: 30 gives the brief's 60 degree cone.
	HalfAngleDeg float64 `mapstructure:"CONE_HALF_ANGLE_DEG"`
	// MinSpeedKMH is the speed below which a GPS heading is noise, so the cone is skipped.
	MinSpeedKMH float64 `mapstructure:"CONE_MIN_SPEED_KMH"`
	// MaxResults caps the matching_orders list.
	MaxResults int `mapstructure:"CONE_MAX_RESULTS"`
}

// Limits for validating the cone settings.
const (
	maxConeRadiusKM   = 50.0
	maxConeHalfAngle  = 180.0
	maxConeMinSpeed   = 200.0
	maxConeMaxResults = 1000
)

// MaxPayoutBPS is 100% in basis points.
const MaxPayoutBPS = 10000

// DBConfig holds the PostgreSQL connection pool settings.
type DBConfig struct {
	DSN             string        `mapstructure:"DATABASE_URL"`
	MaxConns        int32         `mapstructure:"DB_MAX_CONNS"`
	MinConns        int32         `mapstructure:"DB_MIN_CONNS"`
	MaxConnIdleTime time.Duration `mapstructure:"DB_MAX_CONN_IDLE_TIME"`
	// QueryTimeout bounds every statement, so a slow query fails with a 500 instead of hanging.
	// Let's Go Further ch. 8.3.
	QueryTimeout time.Duration `mapstructure:"DB_QUERY_TIMEOUT"`
}

// Default settings .
var defaults = map[string]any{
	"APP_ENV":               EnvDevelopment,
	"PORT":                  4000,
	"APP_TIMEZONE":          "Africa/Nairobi",
	"DATABASE_URL":          "",
	"DB_MAX_CONNS":          25,
	"DB_MIN_CONNS":          2,
	"DB_MAX_CONN_IDLE_TIME": "15m",
	"DB_QUERY_TIMEOUT":      "3s",
	"RIDER_PAYOUT_BPS":      9000,
	"CONE_RADIUS_KM":        1,
	"CONE_HALF_ANGLE_DEG":   30,
	"CONE_MIN_SPEED_KMH":    3,
	"CONE_MAX_RESULTS":      20,
}

// Load configuration
func Load(path string) (Config, error) {
	v := viper.New()
	for k, val := range defaults {
		v.SetDefault(k, val)
	}
	v.AutomaticEnv()

	if path != "" {
		if _, err := os.Stat(path); err == nil {
			v.SetConfigFile(path)
			v.SetConfigType("env")
			if err := v.ReadInConfig(); err != nil {
				return Config{}, fmt.Errorf("read config file %q: %w", path, err)
			}
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	var errs []error
	switch c.Env {
	case EnvDevelopment, EnvStaging, EnvProduction:
	default:
		errs = append(errs, fmt.Errorf("APP_ENV must be development, staging or production (got %q)", c.Env))
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil || c.Timezone == "" {
		errs = append(errs, fmt.Errorf("APP_TIMEZONE must be an IANA time zone such as Africa/Nairobi (got %q)", c.Timezone))
	}
	if c.Port < 1 || c.Port > 65535 {
		errs = append(errs, fmt.Errorf("PORT must be 1-65535 (got %d)", c.Port))
	}
	if c.DB.DSN == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	}
	if c.DB.MaxConns < 1 || c.DB.MinConns < 0 || c.DB.MinConns > c.DB.MaxConns {
		errs = append(errs, errors.New("DB_MIN_CONNS must be between 0 and DB_MAX_CONNS, and DB_MAX_CONNS >= 1"))
	}
	if c.RiderPayoutBPS < 0 || c.RiderPayoutBPS > MaxPayoutBPS {
		errs = append(errs, fmt.Errorf("RIDER_PAYOUT_BPS must be 0-%d (got %d)", MaxPayoutBPS, c.RiderPayoutBPS))
	}
	if c.DB.QueryTimeout <= 0 {
		errs = append(errs, fmt.Errorf("DB_QUERY_TIMEOUT must be greater than zero (got %s)", c.DB.QueryTimeout))
	}
	errs = append(errs, c.Cone.validate()...)
	return errors.Join(errs...)
}

// Location returns the business time zone. Load has already validated it, so the UTC
// fallback only applies to a hand-built Config.
func (c Config) Location() *time.Location {
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

func (c ConeConfig) validate() []error {
	var errs []error
	if c.RadiusKM <= 0 || c.RadiusKM > maxConeRadiusKM {
		errs = append(errs, fmt.Errorf("CONE_RADIUS_KM must be greater than 0 and at most %v (got %v)", maxConeRadiusKM, c.RadiusKM))
	}
	if c.HalfAngleDeg <= 0 || c.HalfAngleDeg > maxConeHalfAngle {
		errs = append(errs, fmt.Errorf("CONE_HALF_ANGLE_DEG must be greater than 0 and at most %v (got %v)", maxConeHalfAngle, c.HalfAngleDeg))
	}
	if c.MinSpeedKMH < 0 || c.MinSpeedKMH > maxConeMinSpeed {
		errs = append(errs, fmt.Errorf("CONE_MIN_SPEED_KMH must be between 0 and %v (got %v)", maxConeMinSpeed, c.MinSpeedKMH))
	}
	if c.MaxResults < 1 || c.MaxResults > maxConeMaxResults {
		errs = append(errs, fmt.Errorf("CONE_MAX_RESULTS must be between 1 and %d (got %d)", maxConeMaxResults, c.MaxResults))
	}
	return errs
}
