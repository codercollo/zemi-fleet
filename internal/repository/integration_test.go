//go:build integration

package repository

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codercollo/zemi-fleet/internal/config"
)

// A genuinely slow statement (pg_sleep) is cancelled at DB_QUERY_TIMEOUT: the caller gets a
// deadline error after about the timeout, not after the full two seconds.
func TestIntegration_SlowQueryIsCancelled(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	require.NotEmpty(t, dsn, "DATABASE_URL must be set (make exports it from app.env)")
	pool, err := OpenPool(context.Background(), config.DBConfig{DSN: dsn, MaxConns: 5, MaxConnIdleTime: time.Minute})
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	const timeout = 150 * time.Millisecond
	db := wrap(pool, timeout)

	start := time.Now()
	var one int
	err = db.QueryRow(context.Background(), "SELECT pg_sleep(2)::text::int").Scan(&one)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.GreaterOrEqual(t, elapsed, timeout)
	assert.Less(t, elapsed, time.Second, "cancelled long before pg_sleep finished")

	// The pool stays usable afterwards (the cancelled connection is replaced, which can take
	// longer than the tiny timeout above, so use a realistic one).
	require.NoError(t, wrap(pool, 5*time.Second).QueryRow(context.Background(), "SELECT 1").Scan(&one))
}
