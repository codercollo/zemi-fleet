package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/repository/sqlc"
)

// hangingDB is a DBTX that never answers: every call blocks until its context is done.
type hangingDB struct{}

func (hangingDB) Exec(ctx context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	<-ctx.Done()
	return pgconn.CommandTag{}, ctx.Err()
}

func (hangingDB) Query(ctx context.Context, _ string, _ ...any) (pgx.Rows, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (hangingDB) QueryRow(ctx context.Context, _ string, _ ...any) pgx.Row {
	return hangingRow{ctx}
}

type hangingRow struct{ ctx context.Context }

func (r hangingRow) Scan(...any) error {
	<-r.ctx.Done()
	return r.ctx.Err()
}

// noRowDB answers every QueryRow with "no rows".
type noRowDB struct{ hangingDB }

func (noRowDB) QueryRow(context.Context, string, ...any) pgx.Row { return errRow{pgx.ErrNoRows} }

type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

func TestQueryTimeout(t *testing.T) {
	const timeout = 40 * time.Millisecond
	q := sqlc.New(wrap(hangingDB{}, timeout))

	tests := []struct {
		name string
		call func(ctx context.Context) error
	}{
		{"QueryRow (:one)", func(ctx context.Context) error { _, err := q.GetRider(ctx, "rider_001"); return err }},
		{"Query (:many)", func(ctx context.Context) error { _, err := q.ListPreferredAreas(ctx, "rider_001"); return err }},
		{"Exec (:exec)", func(ctx context.Context) error { return q.DeletePreferredAreas(ctx, "rider_001") }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()

			err := tc.call(context.Background()) // the caller set no deadline: the store adds its own

			require.Error(t, err)
			assert.ErrorIs(t, err, context.DeadlineExceeded)
			assert.GreaterOrEqual(t, time.Since(start), timeout)
			assert.Less(t, time.Since(start), 2*time.Second, "must not hang")
		})
	}
}

func TestQueryTimeout_CallerDeadlineStillWins(t *testing.T) {
	q := sqlc.New(wrap(hangingDB{}, time.Minute))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := q.GetRider(ctx, "rider_001")

	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 2*time.Second)
}

func TestNoRowsBecomesErrNotFound(t *testing.T) {
	q := sqlc.New(wrap(noRowDB{}, time.Second))

	_, err := q.GetRider(context.Background(), "rider_999")

	assert.ErrorIs(t, err, domain.ErrNotFound)
	assert.False(t, errors.Is(err, pgx.ErrNoRows))
}
