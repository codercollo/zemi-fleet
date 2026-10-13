//go:build integration || e2e

// Package testdb gives integration and e2e tests a real PostgreSQL that is NOT the development database.
//
// Tests connect to TEST_DATABASE_URL only, and refuse to run unless the database name ends in "_test", so a
// mistyped URL can never truncate dev data. Every test starts from a known state: Reset empties the data tables
// (not schema_migrations) and the Load* helpers insert fixtures, so no test depends on run order or on what an
// earlier test left behind.
//
// The package imports nothing from the application beyond the standard driver, so any test package can use it.
package testdb

import (
	"context"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// EnvVar names the environment variable holding the test database URL.
const EnvVar = "TEST_DATABASE_URL"

// URL returns TEST_DATABASE_URL, failing the test when it is unset or does not point at a *_test database.
func URL(t testing.TB) string {
	t.Helper()
	url := os.Getenv(EnvVar)
	require.NotEmpty(t, url, "%s must point at a migrated test database (make dev/test/integration migrates it first)", EnvVar)

	cfg, err := pgconn.ParseConfig(url)
	require.NoError(t, err, "parse %s", EnvVar)
	require.True(t, strings.HasSuffix(cfg.Database, "_test"),
		"refusing to run: %s points at database %q, whose name does not end in _test", EnvVar, cfg.Database)
	return url
}

// Open returns a pool on the test database with UTC sessions (as the application's pool has) and closes it
// when the test ends. It double-checks the connected database's name as well.
func Open(t testing.TB) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(URL(t))
	require.NoError(t, err)
	cfg.MaxConns = 30
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"

	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	var name string
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT current_database()`).Scan(&name))
	require.True(t, strings.HasSuffix(name, "_test"), "connected to %q, which is not a test database", name)
	return pool
}

// dataTables are emptied by Reset: everything except schema_migrations.
const dataTables = `idempotency_keys, batch_offers, batch_stops, orders, batches, rider_preferred_areas, rider_preferred_categories,
	rider_preferences, riders, vendors, areas, categories`

// Reset empties every data table.
func Reset(t testing.TB, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), "TRUNCATE "+dataTables+" CASCADE")
	require.NoError(t, err)
}

// LoadReference inserts the categories, areas and vendors every scenario needs (db/testdata/reference.sql).
func LoadReference(t testing.TB, pool *pgxpool.Pool) {
	t.Helper()
	runFile(t, pool, "db", "testdata", "reference.sql")
}

// LoadSeed truncates and loads the development seed (db/seed/seed.sql) into the TEST database, for the tests
// that check the seed itself. Its timestamps are relative to the real now().
func LoadSeed(t testing.TB, pool *pgxpool.Pool) {
	t.Helper()
	runFile(t, pool, "db", "seed", "seed.sql")
}

// Fresh opens the test database, resets it and loads the reference data: the usual start of a test.
func Fresh(t testing.TB) *pgxpool.Pool {
	t.Helper()
	pool := Open(t)
	Reset(t, pool)
	LoadReference(t, pool)
	return pool
}

// runFile executes a (multi-statement) SQL file from the repository.
func runFile(t testing.TB, pool *pgxpool.Pool, elems ...string) {
	t.Helper()
	path := filepath.Join(append([]string{repoRoot(t)}, elems...)...)
	sql, err := os.ReadFile(path)
	require.NoError(t, err)

	conn, err := pool.Acquire(context.Background())
	require.NoError(t, err)
	defer conn.Release()
	// PgConn().Exec uses the simple protocol, which accepts many statements in one string.
	_, err = conn.Conn().PgConn().Exec(context.Background(), string(sql)).ReadAll()
	require.NoError(t, err, "run %s", path)
}

// repoRoot finds the directory holding go.mod, starting from this source file.
func repoRoot(t testing.TB) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "go.mod not found above %s", file)
		dir = parent
	}
}

// RiderFixture describes a rider to insert. Zero values give an available rider with capacity 5, no
// location, no history and no preferences.
type RiderFixture struct {
	ID       string
	Name     string // defaults to "Fixture Rider"
	Off      bool   // true: not available
	Capacity int    // defaults to 5
	// Lat and Lng are set together with SeenAt; leave nil for "never reported a location".
	Lat, Lng *float64
	SeenAt   *time.Time
	Offers   int
	Accepted int
	// Preferences: a minimum order value, preferred category codes and preferred area IDs (empty = no preference).
	MinOrderValueCents int64
	Categories         []string
	AreaIDs            []string
}

// AddRider inserts a rider (and their preferences) and returns the ID. It is a fixture loader: scenarios use it
// to set the stage, then drive everything else through the API.
func AddRider(t testing.TB, pool *pgxpool.Pool, f RiderFixture) string {
	t.Helper()
	ctx := context.Background()
	require.NotEmpty(t, f.ID)
	if f.Name == "" {
		f.Name = "Fixture Rider"
	}
	if f.Capacity == 0 {
		f.Capacity = 5
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(f.ID))
	phone := fmt.Sprintf("+254%09d", h.Sum32()%1_000_000_000)

	_, err := pool.Exec(ctx, `INSERT INTO riders (id, full_name, phone, is_available, max_capacity, last_lat, last_lng,
			location_updated_at, offers_count, accepted_count) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		f.ID, f.Name, phone, !f.Off, f.Capacity, f.Lat, f.Lng, f.SeenAt, f.Offers, f.Accepted)
	require.NoError(t, err, "insert rider %s", f.ID)

	if f.MinOrderValueCents > 0 || len(f.Categories) > 0 || len(f.AreaIDs) > 0 {
		_, err = pool.Exec(ctx, `INSERT INTO rider_preferences (rider_id, min_order_value_cents) VALUES ($1, $2)`, f.ID, f.MinOrderValueCents)
		require.NoError(t, err)
	}
	for _, c := range f.Categories {
		_, err = pool.Exec(ctx, `INSERT INTO rider_preferred_categories (rider_id, category_code) VALUES ($1, $2)`, f.ID, c)
		require.NoError(t, err)
	}
	for _, a := range f.AreaIDs {
		_, err = pool.Exec(ctx, `INSERT INTO rider_preferred_areas (rider_id, area_id) VALUES ($1, $2)`, f.ID, a)
		require.NoError(t, err)
	}
	return f.ID
}
