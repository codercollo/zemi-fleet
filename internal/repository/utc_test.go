package repository

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var eat = time.FixedZone("EAT", 3*60*60)

// timeRow scans a fixed local-zone time, the way pgx hands back timestamptz.
type timeRow struct{ t time.Time }

func (r timeRow) Scan(dest ...any) error {
	switch d := dest[0].(type) {
	case *time.Time:
		*d = r.t
	case **time.Time:
		v := r.t
		*d = &v
	}
	return nil
}

type timeDB struct {
	hangingDB
	t time.Time
}

func (d timeDB) QueryRow(context.Context, string, ...any) pgx.Row { return timeRow{d.t} }

func TestUTCNormalisation(t *testing.T) {
	local := time.Date(2026, 10, 13, 12, 0, 0, 0, eat)
	db := utcDB{timeDB{t: local}}

	t.Run("time.Time", func(t *testing.T) {
		var got time.Time
		require.NoError(t, db.QueryRow(context.Background(), "").Scan(&got))

		assert.Equal(t, time.UTC, got.Location())
		assert.True(t, got.Equal(local), "same instant")
	})

	t.Run("*time.Time", func(t *testing.T) {
		var got *time.Time
		require.NoError(t, db.QueryRow(context.Background(), "").Scan(&got))

		require.NotNil(t, got)
		assert.Equal(t, time.UTC, got.Location())
		assert.True(t, got.Equal(local))
	})

	t.Run("NULL stays nil and other types are untouched", func(t *testing.T) {
		var none *time.Time
		var text string
		normalizeUTC([]any{&none, &text})

		assert.Nil(t, none)
		assert.Empty(t, text)
	})

	t.Run("errors pass through without normalising", func(t *testing.T) {
		row := utcRow{errRow{pgx.ErrNoRows}}

		assert.ErrorIs(t, row.Scan(new(time.Time)), pgx.ErrNoRows)
	})
}
