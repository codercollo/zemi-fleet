//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codercollo/zemi-fleet/internal/config"
	"github.com/codercollo/zemi-fleet/internal/testdb"
)

func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := testdb.URL(t)
	pool, err := OpenPool(context.Background(), config.DBConfig{DSN: dsn, MaxConns: 5, MaxConnIdleTime: time.Minute, QueryTimeout: 3 * time.Second})
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// OpenPool pins every session to UTC, whatever the server's or role's default zone is.
func TestIntegration_SessionTimezoneIsUTC(t *testing.T) {
	pool := integrationPool(t)

	var tz string
	require.NoError(t, pool.QueryRow(context.Background(), "SHOW timezone").Scan(&tz))

	assert.Equal(t, "UTC", tz)
}

// A timestamptz read through the store comes back with Location() == UTC, and it is the
// same instant the database holds. Reading it with the bare pool shows why the wrapper is needed.
func TestIntegration_TimestamptzReadsBackInUTC(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	const stored = "2026-10-13 12:30:15+03" // 09:30:15 UTC
	want := time.Date(2026, 10, 13, 9, 30, 15, 0, time.UTC)

	var viaStore time.Time
	require.NoError(t, wrap(pool, 3*time.Second).QueryRow(ctx, "SELECT $1::timestamptz", stored).Scan(&viaStore))
	var viaStorePtr *time.Time
	require.NoError(t, wrap(pool, 3*time.Second).QueryRow(ctx, "SELECT $1::timestamptz", stored).Scan(&viaStorePtr))

	assert.Equal(t, time.UTC, viaStore.Location())
	assert.True(t, viaStore.Equal(want), "got %s", viaStore)
	require.NotNil(t, viaStorePtr)
	assert.Equal(t, time.UTC, viaStorePtr.Location())

	var raw time.Time
	require.NoError(t, pool.QueryRow(ctx, "SELECT $1::timestamptz", stored).Scan(&raw))
	assert.True(t, raw.Equal(want), "same instant even before normalising")
}
