//go:build integration

package service

import (
	"context"
	"os"
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
)

func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	require.NotEmpty(t, dsn, "DATABASE_URL must be set (make exports it from app.env)")
	pool, err := repository.OpenPool(context.Background(), config.DBConfig{DSN: dsn, MaxConns: 5, MaxConnIdleTime: time.Minute, QueryTimeout: 3 * time.Second})
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// The seeded orders carry precomputed trip distances and bearings. Recomputing them with
// internal/geo (what OrderService now uses) must give the same stored values, so swapping
// the temporary helper for the geo package changed nothing observable.
func TestIntegration_SeededTripsMatchGeo(t *testing.T) {
	pool := integrationPool(t)
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
	if checked == 0 {
		t.Skip("no seeded orders (run make dev/db/seed)")
	}
	t.Logf("compared %d seeded orders", checked)
}

func roundHalfUp(v float64) int64 { return int64(v + 0.5) }

// created_at is the injected clock's time, not the database's now(), and reads back as UTC.
func TestIntegration_OrderCreatedAtComesFromClock(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	var vendorID, category string
	err := pool.QueryRow(ctx, `SELECT v.id, c.code FROM vendors v, categories c ORDER BY v.id, c.code LIMIT 1`).Scan(&vendorID, &category)
	if err != nil {
		t.Skip("no vendors/categories (run make dev/db/seed)")
	}

	fixed := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC) // years in the past: now() could never produce it
	svc := NewOrderService(repository.NewStore(pool, 3*time.Second), clock.NewTest(fixed), 9000)

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
