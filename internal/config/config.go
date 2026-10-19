// Package config loads and validates settings from environment variables, app.env and defaults.
package config

import (
	"errors"
	"fmt"
	"math"
	"net"
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

	// OrderMinTripM is the shortest allowed pickup-to-delivery distance in metres. A shorter trip has
	// no meaningful direction and is not worth a rider; creating one is a 422.
	OrderMinTripM int `mapstructure:"ORDER_MIN_TRIP_M"`

	// OrderMaxValueKES and OrderMaxDeliveryFeeKES are the largest amounts a request may carry (KES). They bound every
	// amount that is converted to int64 cents, so no sum of cents can come near overflowing.
	OrderMaxValueKES       float64 `mapstructure:"ORDER_MAX_VALUE_KES"`
	OrderMaxDeliveryFeeKES float64 `mapstructure:"ORDER_MAX_DELIVERY_FEE_KES"`

	// IdempotencyTTL is how long an Idempotency-Key is remembered; older keys are ignored and deleted on write.
	IdempotencyTTL time.Duration `mapstructure:"IDEMPOTENCY_TTL"`
	// IdempotencyProcessingTimeout is how long a claimed key may stay unfinished before a retry may take it over.
	IdempotencyProcessingTimeout time.Duration `mapstructure:"IDEMPOTENCY_PROCESSING_TIMEOUT"`

	// APIKeysRaw is API_KEYS ("name=key,name=key") exactly as configured. Load parses it into APIKeys and clears it,
	// so after loading only hashes are held in memory.
	APIKeysRaw string `mapstructure:"API_KEYS"`
	// APIKeys are the clients allowed to call the API (every route except /healthz), as name + SHA-256 of the key.
	APIKeys []APIKey `mapstructure:"-"`

	Limiter LimiterConfig `mapstructure:",squash"`
	CORS    CORSConfig    `mapstructure:",squash"`

	// MetricsEnabled turns on GET /debug/vars (Let's Go Further ch. 19). When METRICS_ENABLED is not set it is true in
	// development and staging and false in production, decided in Load. Disabled means the route is not registered (404).
	MetricsEnabled bool `mapstructure:"-"`

	// DocsEnabled turns on GET /docs (Swagger UI) and GET /openapi.yaml. When DOCS_ENABLED is not set it is true in
	// development and staging and false in production. Disabled means the routes are not registered (404).
	DocsEnabled bool `mapstructure:"-"`

	// ShutdownTimeout is how long SIGINT/SIGTERM waits for in-flight requests to finish before the server gives up.
	ShutdownTimeout time.Duration `mapstructure:"SHUTDOWN_TIMEOUT"`

	Cone  ConeConfig  `mapstructure:",squash"`
	Batch BatchConfig `mapstructure:",squash"`
	Match MatchConfig `mapstructure:",squash"`
	Geo   GeoConfig   `mapstructure:",squash"`
}

// GeoConfig controls optional service-area validation for geographic inputs.
type GeoConfig struct {
	BoundsEnabled bool    `mapstructure:"GEO_BOUNDS_ENABLED"`
	MinLat        float64 `mapstructure:"GEO_MIN_LAT"`
	MaxLat        float64 `mapstructure:"GEO_MAX_LAT"`
	MinLng        float64 `mapstructure:"GEO_MIN_LNG"`
	MaxLng        float64 `mapstructure:"GEO_MAX_LNG"`
}

// MatchConfig holds the rider preference matching tunables (POST /orders/match-riders).
type MatchConfig struct {
	// MaxDistanceKM: a rider with a fresh, known location farther than this from the pickup is excluded,
	// and it is the distance at which the proximity score reaches 0.
	MaxDistanceKM float64 `mapstructure:"MATCH_MAX_DISTANCE_KM"`
	// LocationStaleAfter: a location older than this is treated as unknown (not filtered, scores 0 for proximity).
	LocationStaleAfter time.Duration `mapstructure:"RIDER_LOCATION_STALE_AFTER"`
	// The four score weights; they must sum to 1.0.
	WeightCategory   float64 `mapstructure:"MATCH_WEIGHT_CATEGORY"`
	WeightArea       float64 `mapstructure:"MATCH_WEIGHT_AREA"`
	WeightProximity  float64 `mapstructure:"MATCH_WEIGHT_PROXIMITY"`
	WeightAcceptance float64 `mapstructure:"MATCH_WEIGHT_ACCEPTANCE"`
	// MaxResults caps matching_riders.
	MaxResults int `mapstructure:"MATCH_MAX_RESULTS"`
}

// Limits for validating the match settings.
const (
	maxMatchDistanceKM = 100.0
	maxMatchResults    = 100
	weightSumTolerance = 1e-9
)

// BatchConfig holds the order-batching tunables.
type BatchConfig struct {
	// SpatialQueryMode selects the legacy Go-filtered path or SQL-side exact filtering/paging.
	SpatialQueryMode string `mapstructure:"SPATIAL_QUERY_MODE"`
	// MaxOrders is the largest batch the algorithm will form (and the largest max_orders a request may ask for).
	MaxOrders int `mapstructure:"BATCH_MAX_ORDERS"`
	// PickupRadiusKM is the largest pairwise distance between pickups in one batch, and the largest radius_km a request may ask for.
	PickupRadiusKM float64 `mapstructure:"BATCH_PICKUP_RADIUS_KM"`
	// DirectionToleranceDeg is how far a trip's bearing may differ from the batch seed's.
	DirectionToleranceDeg float64 `mapstructure:"BATCH_DIRECTION_TOLERANCE_DEG"`
	// SearchRadiusKM is how far from the rider pending orders and open batches are looked up.
	SearchRadiusKM float64 `mapstructure:"BATCH_SEARCH_RADIUS_KM"`
	// VisibilityRadiusKM is the default radius_km of GET /batches; a request may use up to SearchRadiusKM.
	VisibilityRadiusKM float64 `mapstructure:"BATCH_VISIBILITY_RADIUS_KM"`
	// TTL is how long an open batch stays on offer before it expires and its orders return to pending.
	TTL time.Duration `mapstructure:"BATCH_TTL"`
	// AvgSpeedKMH and StopDwellMin drive the delivery time estimates.
	AvgSpeedKMH  float64 `mapstructure:"AVG_SPEED_KMH"`
	StopDwellMin float64 `mapstructure:"STOP_DWELL_MIN"`
}

// Limits for validating the batch settings. MaxBatchOrders mirrors the batches.orders_count CHECK.
const (
	MaxBatchOrders    = 5
	maxBatchRadiusKM  = 50.0
	maxBatchDwellMin  = 60.0
	maxBatchAvgSpeed  = 200.0
	maxBatchTolerance = 180.0
	maxOrderMinTripM  = 10_000
	// maxAmountKESLimit keeps any configured amount (KES) times 100 far inside int64, even when summed over a batch.
	maxAmountKESLimit = 1_000_000_000.0
)

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

// LimiterConfig holds the per-client-IP rate limiter settings (Let's Go Further ch. 11).
type LimiterConfig struct {
	// Enabled switches the limiter on. When false no request is ever limited.
	Enabled bool `mapstructure:"LIMITER_ENABLED"`
	// RPS is the sustained requests per second allowed per client IP.
	RPS float64 `mapstructure:"LIMITER_RPS"`
	// Burst is how many requests one IP may make at once before the sustained rate applies.
	Burst int `mapstructure:"LIMITER_BURST"`
	// TrustedProxies lists the IPs or CIDRs of reverse proxies whose X-Forwarded-For header is believed.
	// Empty (the default) trusts none, so the client IP is always the TCP peer and cannot be spoofed.
	TrustedProxies []string `mapstructure:"TRUSTED_PROXIES"`
}

// Limits for validating the limiter settings.
const (
	maxLimiterRPS   = 100_000.0
	maxLimiterBurst = 1_000_000
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
	"LIMITER_ENABLED":       true,
	"LIMITER_RPS":           10,
	"LIMITER_BURST":         20,
	"TRUSTED_PROXIES":       "",
	"API_KEYS":              "",
	"CORS_TRUSTED_ORIGINS":  "",
	"SHUTDOWN_TIMEOUT":      "30s",
	"CONE_RADIUS_KM":        1,
	"CONE_HALF_ANGLE_DEG":   30,
	"CONE_MIN_SPEED_KMH":    3,
	"CONE_MAX_RESULTS":      20,

	"MATCH_MAX_DISTANCE_KM":      10,
	"RIDER_LOCATION_STALE_AFTER": "30m",
	"MATCH_WEIGHT_CATEGORY":      0.25,
	"MATCH_WEIGHT_AREA":          0.20,
	"MATCH_WEIGHT_PROXIMITY":     0.35,
	"MATCH_WEIGHT_ACCEPTANCE":    0.20,
	"MATCH_MAX_RESULTS":          10,

	"BATCH_MAX_ORDERS":               5,
	"SPATIAL_QUERY_MODE":             "legacy",
	"BATCH_PICKUP_RADIUS_KM":         2,
	"BATCH_DIRECTION_TOLERANCE_DEG":  45,
	"BATCH_SEARCH_RADIUS_KM":         5,
	"BATCH_VISIBILITY_RADIUS_KM":     3,
	"ORDER_MIN_TRIP_M":               50,
	"ORDER_MAX_VALUE_KES":            10_000_000,
	"ORDER_MAX_DELIVERY_FEE_KES":     10_000_000,
	"IDEMPOTENCY_TTL":                "24h",
	"IDEMPOTENCY_PROCESSING_TIMEOUT": "2m",
	"BATCH_TTL":                      "10m",
	"AVG_SPEED_KMH":                  20,
	"STOP_DWELL_MIN":                 2,
	"GEO_BOUNDS_ENABLED":             false,
	"GEO_MIN_LAT":                    -4.7,
	"GEO_MAX_LAT":                    4.7,
	"GEO_MIN_LNG":                    33.9,
	"GEO_MAX_LNG":                    41.9,
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
	cfg.MetricsEnabled = onUnlessProduction(v, "METRICS_ENABLED", cfg.Env)
	cfg.DocsEnabled = onUnlessProduction(v, "DOCS_ENABLED", cfg.Env)
	cfg.APIKeys, _ = parseAPIKeys(cfg.APIKeysRaw, cfg.Env) // validate() has already reported any problem
	cfg.APIKeysRaw = ""
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
	if c.OrderMinTripM < 0 || c.OrderMinTripM > maxOrderMinTripM {
		errs = append(errs, fmt.Errorf("ORDER_MIN_TRIP_M must be between 0 and %d (got %d)", maxOrderMinTripM, c.OrderMinTripM))
	}
	for name, v := range map[string]float64{"ORDER_MAX_VALUE_KES": c.OrderMaxValueKES, "ORDER_MAX_DELIVERY_FEE_KES": c.OrderMaxDeliveryFeeKES} {
		if v <= 0 || v > maxAmountKESLimit {
			errs = append(errs, fmt.Errorf("%s must be greater than 0 and at most %v (got %v)", name, maxAmountKESLimit, v))
		}
	}
	if c.IdempotencyTTL <= 0 {
		errs = append(errs, fmt.Errorf("IDEMPOTENCY_TTL must be greater than zero (got %s)", c.IdempotencyTTL))
	}
	if c.IdempotencyProcessingTimeout <= 0 || c.IdempotencyProcessingTimeout > c.IdempotencyTTL {
		errs = append(errs, fmt.Errorf("IDEMPOTENCY_PROCESSING_TIMEOUT must be greater than zero and at most IDEMPOTENCY_TTL (got %s)", c.IdempotencyProcessingTimeout))
	}
	if c.RiderPayoutBPS < 0 || c.RiderPayoutBPS > MaxPayoutBPS {
		errs = append(errs, fmt.Errorf("RIDER_PAYOUT_BPS must be 0-%d (got %d)", MaxPayoutBPS, c.RiderPayoutBPS))
	}
	if c.DB.QueryTimeout <= 0 {
		errs = append(errs, fmt.Errorf("DB_QUERY_TIMEOUT must be greater than zero (got %s)", c.DB.QueryTimeout))
	}
	_, keyErrs := parseAPIKeys(c.APIKeysRaw, c.Env)
	errs = append(errs, keyErrs...)
	errs = append(errs, c.Limiter.validate()...)
	errs = append(errs, c.CORS.validate()...)
	if c.ShutdownTimeout <= 0 {
		errs = append(errs, fmt.Errorf("SHUTDOWN_TIMEOUT must be greater than zero (got %s)", c.ShutdownTimeout))
	}
	errs = append(errs, c.Cone.validate()...)
	errs = append(errs, c.Batch.validate()...)
	errs = append(errs, c.Match.validate()...)
	errs = append(errs, c.Geo.validate()...)
	return errors.Join(errs...)
}

func (g GeoConfig) validate() []error {
	var errs []error
	if g.MinLat >= g.MaxLat {
		errs = append(errs, fmt.Errorf("GEO_MIN_LAT must be less than GEO_MAX_LAT (got %v and %v)", g.MinLat, g.MaxLat))
	}
	if g.MinLng >= g.MaxLng {
		errs = append(errs, fmt.Errorf("GEO_MIN_LNG must be less than GEO_MAX_LNG (got %v and %v)", g.MinLng, g.MaxLng))
	}
	if g.MinLat < -90 || g.MaxLat > 90 {
		errs = append(errs, fmt.Errorf("GEO latitude bounds must be between -90 and 90 (got %v and %v)", g.MinLat, g.MaxLat))
	}
	if g.MinLng < -180 || g.MaxLng > 180 {
		errs = append(errs, fmt.Errorf("GEO longitude bounds must be between -180 and 180 (got %v and %v)", g.MinLng, g.MaxLng))
	}
	return errs
}

// onUnlessProduction reads a boolean switch whose default depends on the environment: on in development and staging,
// off in production, unless the variable is set. It is not in defaults because a default cannot depend on APP_ENV.
func onUnlessProduction(v *viper.Viper, key, env string) bool {
	if v.IsSet(key) {
		return v.GetBool(key)
	}
	return env != EnvProduction
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

func (b BatchConfig) validate() []error {
	var errs []error
	if b.SpatialQueryMode != "legacy" && b.SpatialQueryMode != "sql" {
		errs = append(errs, fmt.Errorf("SPATIAL_QUERY_MODE must be legacy or sql (got %q)", b.SpatialQueryMode))
	}
	if b.MaxOrders < 1 || b.MaxOrders > MaxBatchOrders {
		errs = append(errs, fmt.Errorf("BATCH_MAX_ORDERS must be between 1 and %d (got %d)", MaxBatchOrders, b.MaxOrders))
	}
	if b.PickupRadiusKM <= 0 || b.PickupRadiusKM > maxBatchRadiusKM {
		errs = append(errs, fmt.Errorf("BATCH_PICKUP_RADIUS_KM must be greater than 0 and at most %v (got %v)", maxBatchRadiusKM, b.PickupRadiusKM))
	}
	if b.DirectionToleranceDeg <= 0 || b.DirectionToleranceDeg > maxBatchTolerance {
		errs = append(errs, fmt.Errorf("BATCH_DIRECTION_TOLERANCE_DEG must be greater than 0 and at most %v (got %v)", maxBatchTolerance, b.DirectionToleranceDeg))
	}
	if b.SearchRadiusKM <= 0 || b.SearchRadiusKM > maxBatchRadiusKM {
		errs = append(errs, fmt.Errorf("BATCH_SEARCH_RADIUS_KM must be greater than 0 and at most %v (got %v)", maxBatchRadiusKM, b.SearchRadiusKM))
	}
	if b.VisibilityRadiusKM <= 0 || b.VisibilityRadiusKM > b.SearchRadiusKM {
		errs = append(errs, fmt.Errorf("BATCH_VISIBILITY_RADIUS_KM must be greater than 0 and at most BATCH_SEARCH_RADIUS_KM (got %v, search radius %v)", b.VisibilityRadiusKM, b.SearchRadiusKM))
	}
	if b.TTL <= 0 {
		errs = append(errs, fmt.Errorf("BATCH_TTL must be greater than zero (got %s)", b.TTL))
	}
	if b.AvgSpeedKMH <= 0 || b.AvgSpeedKMH > maxBatchAvgSpeed {
		errs = append(errs, fmt.Errorf("AVG_SPEED_KMH must be greater than 0 and at most %v (got %v)", maxBatchAvgSpeed, b.AvgSpeedKMH))
	}
	if b.StopDwellMin < 0 || b.StopDwellMin > maxBatchDwellMin {
		errs = append(errs, fmt.Errorf("STOP_DWELL_MIN must be between 0 and %v (got %v)", maxBatchDwellMin, b.StopDwellMin))
	}
	return errs
}

func (m MatchConfig) validate() []error {
	var errs []error
	if m.MaxDistanceKM <= 0 || m.MaxDistanceKM > maxMatchDistanceKM {
		errs = append(errs, fmt.Errorf("MATCH_MAX_DISTANCE_KM must be greater than 0 and at most %v (got %v)", maxMatchDistanceKM, m.MaxDistanceKM))
	}
	if m.LocationStaleAfter <= 0 {
		errs = append(errs, fmt.Errorf("RIDER_LOCATION_STALE_AFTER must be greater than zero (got %s)", m.LocationStaleAfter))
	}
	if m.MaxResults < 1 || m.MaxResults > maxMatchResults {
		errs = append(errs, fmt.Errorf("MATCH_MAX_RESULTS must be between 1 and %d (got %d)", maxMatchResults, m.MaxResults))
	}
	weights := map[string]float64{
		"MATCH_WEIGHT_CATEGORY": m.WeightCategory, "MATCH_WEIGHT_AREA": m.WeightArea,
		"MATCH_WEIGHT_PROXIMITY": m.WeightProximity, "MATCH_WEIGHT_ACCEPTANCE": m.WeightAcceptance,
	}
	negative := false
	for name, w := range weights {
		if w < 0 || w > 1 {
			negative = true
			errs = append(errs, fmt.Errorf("%s must be between 0 and 1 (got %v)", name, w))
		}
	}
	if sum := m.WeightCategory + m.WeightArea + m.WeightProximity + m.WeightAcceptance; !negative && math.Abs(sum-1) > weightSumTolerance {
		errs = append(errs, fmt.Errorf("the MATCH_WEIGHT_* values must sum to 1.0 (got %v)", sum))
	}
	return errs
}

func (l LimiterConfig) validate() []error {
	var errs []error
	if l.RPS <= 0 || l.RPS > maxLimiterRPS {
		errs = append(errs, fmt.Errorf("LIMITER_RPS must be greater than 0 and at most %v (got %v)", maxLimiterRPS, l.RPS))
	}
	if l.Burst < 1 || l.Burst > maxLimiterBurst {
		errs = append(errs, fmt.Errorf("LIMITER_BURST must be between 1 and %d (got %d)", maxLimiterBurst, l.Burst))
	}
	for _, p := range l.TrustedProxies {
		if _, _, err := net.ParseCIDR(p); err == nil {
			continue
		}
		if net.ParseIP(p) == nil {
			errs = append(errs, fmt.Errorf("TRUSTED_PROXIES entry %q is not an IP address or a CIDR range", p))
		}
	}
	return errs
}
