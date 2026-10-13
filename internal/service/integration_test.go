//go:build integration

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codercollo/zemi-fleet/internal/clock"
	"github.com/codercollo/zemi-fleet/internal/config"
	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/geo"
	"github.com/codercollo/zemi-fleet/internal/repository"
	"github.com/codercollo/zemi-fleet/internal/testdb"
)

func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := repository.OpenPool(context.Background(), config.DBConfig{DSN: testdb.URL(t), MaxConns: 5, MaxConnIdleTime: time.Minute, QueryTimeout: 3 * time.Second})
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	testdb.Reset(t, pool)
	testdb.LoadReference(t, pool) // categories, areas, vendors; each test adds the riders and orders it needs
	return pool
}

// The seeded orders carry precomputed trip distances and bearings. Recomputing them with
// internal/geo (what OrderService now uses) must give the same stored values, so swapping
// the temporary helper for the geo package changed nothing observable.
func TestIntegration_SeededTripsMatchGeo(t *testing.T) {
	pool := integrationPool(t)
	testdb.LoadSeed(t, pool) // the development seed, into the TEST database
	rows, err := pool.Query(context.Background(),
		`SELECT id, pickup_lat, pickup_lng, delivery_lat, delivery_lng, trip_distance_m, trip_bearing_deg
		   FROM orders WHERE id ~ '^ord_[0-9]{3}$' ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()

	checked := 0
	for rows.Next() {
		var id string
		var pLat, pLng, dLat, dLng, storedBearing float64
		var storedDistance int32
		require.NoError(t, rows.Scan(&id, &pLat, &pLng, &dLat, &dLng, &storedDistance, &storedBearing))

		pickup, delivery := domain.Location{Lat: pLat, Lng: pLng}, domain.Location{Lat: dLat, Lng: dLng}
		assert.EqualValues(t, storedDistance, int32(roundHalfUp(geo.Haversine(pickup, delivery))), id)
		assert.InDelta(t, storedBearing, roundBearing(geo.Bearing(pickup, delivery)), 1e-9, id)
		checked++
	}
	require.NoError(t, rows.Err())
	require.Equal(t, 40, checked, "the seed has 40 orders")
	t.Logf("compared %d seeded orders", checked)
}

func roundHalfUp(v float64) int64 { return int64(v + 0.5) }

// created_at is the injected clock's time, not the database's now(), and reads back as UTC.
func TestIntegration_OrderCreatedAtComesFromClock(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	var vendorID, category string
	err := pool.QueryRow(ctx, `SELECT v.id, c.code FROM vendors v, categories c ORDER BY v.id, c.code LIMIT 1`).Scan(&vendorID, &category)
	require.NoError(t, err, "reference data (vendors, categories) should be loaded")

	fixed := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC) // years in the past: now() could never produce it
	svc := NewOrderService(repository.NewStore(pool, 3*time.Second), clock.NewTest(fixed), 9000, 50)

	order, err := svc.Create(ctx, CreateOrderInput{
		VendorID: vendorID, CategoryCode: category,
		Delivery: domain.Location{Lat: -1.30, Lng: 36.80}, ValueCents: 10000, DeliveryFeeCents: 20000,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM orders WHERE id = $1`, order.ID) })

	assert.True(t, order.CreatedAt.Equal(fixed), "got %s", order.CreatedAt)
	assert.Equal(t, time.UTC, order.CreatedAt.Location())
	assert.Contains(t, order.ID, "ord_")

	reread, err := svc.Get(ctx, order.ID)
	require.NoError(t, err)
	assert.True(t, reread.CreatedAt.Equal(fixed))
	assert.Equal(t, time.UTC, reread.CreatedAt.Location())
}

// db/testdata/reference.sql is a copy of the reference tables in db/seed/seed.sql. If the two ever differ,
// tests built on the reference data would be testing something other than the seed users see.
func TestIntegration_ReferenceDataMatchesSeed(t *testing.T) {
	pool := integrationPool(t) // reset + the reference data
	ctx := context.Background()

	// The seeded columns only: created_at and friends differ between any two loads.
	queries := map[string]string{
		"categories": `SELECT code, label FROM categories ORDER BY code`,
		"areas":      `SELECT id, name, center_lat, center_lng, radius_m FROM areas ORDER BY id`,
		"vendors":    `SELECT id, name, phone, lat, lng FROM vendors ORDER BY id`,
	}
	dump := func() map[string]string {
		out := map[string]string{}
		for table, q := range queries {
			rows, err := pool.Query(ctx, q)
			require.NoError(t, err)
			var lines []string
			for rows.Next() {
				vals, err := rows.Values()
				require.NoError(t, err)
				lines = append(lines, fmt.Sprint(vals...))
			}
			require.NoError(t, rows.Err())
			rows.Close()
			out[table] = fmt.Sprint(lines)
		}
		return out
	}

	fromReference := dump()
	testdb.LoadSeed(t, pool)
	fromSeed := dump()

	for table := range queries {
		assert.NotEmpty(t, fromReference[table], table)
		assert.Equal(t, fromSeed[table], fromReference[table], "%s differ between db/testdata/reference.sql and db/seed/seed.sql", table)
	}
}
