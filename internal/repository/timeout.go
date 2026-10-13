package repository

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/codercollo/zemi-fleet/internal/repository/sqlc"
)

// timeoutDB gives every statement its own deadline (DB_QUERY_TIMEOUT), so a slow
// query surfaces as context.DeadlineExceeded (a 500) instead of hanging the request.
// Wrapping DBTX covers every current and future sqlc query, inside or outside a
// transaction. Let's Go Further ch. 8.3.
type timeoutDB struct {
	sqlc.DBTX
	timeout time.Duration
}

func (d timeoutDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.DBTX.Exec(ctx, sql, args...)
}

// Query and QueryRow hand the result back lazily, so the deadline must outlive this call:
// the cancel runs when the rows are closed (or the row is scanned).
func (d timeoutDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	rows, err := d.DBTX.Query(ctx, sql, args...)
	if err != nil {
		cancel()
		return nil, err
	}
	return cancelRows{Rows: rows, cancel: cancel}, nil
}

func (d timeoutDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	return cancelRow{Row: d.DBTX.QueryRow(ctx, sql, args...), cancel: cancel}
}

type cancelRows struct {
	pgx.Rows
	cancel context.CancelFunc
}

func (r cancelRows) Close() {
	r.Rows.Close()
	r.cancel()
}

type cancelRow struct {
	pgx.Row
	cancel context.CancelFunc
}

func (r cancelRow) Scan(dest ...any) error {
	defer r.cancel()
	return r.Row.Scan(dest...)
}
