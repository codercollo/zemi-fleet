package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/repository/sqlc"
)

// Store is everything the service layer may do to the database: the sqlc queries
// plus ExecTx for multi-statement units of work. Services depend on this
// interface, so tests can swap in a gomock Store (Simple Bank pattern).
type Store interface {
	sqlc.Querier

	// ExecTx runs fn inside one transaction and commits if it returns nil. The
	// Querier passed to fn is bound to that transaction.
	ExecTx(ctx context.Context, fn func(q sqlc.Querier) error) error
}

// SQLStore is the PostgreSQL implementation of Store.
type SQLStore struct {
	*sqlc.Queries
	pool    *pgxpool.Pool
	timeout time.Duration
}

var _ Store = (*SQLStore)(nil)

// NewStore builds a Store on top of a pgx pool. queryTimeout bounds every statement.
func NewStore(pool *pgxpool.Pool, queryTimeout time.Duration) *SQLStore {
	return &SQLStore{Queries: sqlc.New(wrap(pool, queryTimeout)), pool: pool, timeout: queryTimeout}
}

// wrap layers the per-statement deadline and the ErrNoRows mapping onto a DBTX.
func wrap(db sqlc.DBTX, timeout time.Duration) sqlc.DBTX {
	return notFoundDB{timeoutDB{DBTX: db, timeout: timeout}}
}

// ExecTx runs fn in a transaction, rolling back on any error.
func (s *SQLStore) ExecTx(ctx context.Context, fn func(q sqlc.Querier) error) error {
	beginCtx, cancel := context.WithTimeout(ctx, s.timeout)
	tx, err := s.pool.Begin(beginCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	if err := fn(sqlc.New(wrap(tx, s.timeout))); err != nil {
		// Roll back even if ctx is already cancelled, otherwise the connection stays checked out.
		rbCtx, rbCancel := context.WithTimeout(context.WithoutCancel(ctx), s.timeout)
		defer rbCancel()
		if rbErr := tx.Rollback(rbCtx); rbErr != nil {
			return fmt.Errorf("tx err: %w; rollback err: %w", err, rbErr)
		}
		return err
	}

	commitCtx, commitCancel := context.WithTimeout(ctx, s.timeout)
	defer commitCancel()
	if err := tx.Commit(commitCtx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// notFoundDB wraps the pool or a transaction so that every :one query reports a
// missing row as domain.ErrNotFound instead of pgx.ErrNoRows. Doing it at the
// DBTX level covers all current and future queries without per-method wrappers.
type notFoundDB struct{ sqlc.DBTX }

func (d notFoundDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return notFoundRow{d.DBTX.QueryRow(ctx, sql, args...)}
}

type notFoundRow struct{ pgx.Row }

func (r notFoundRow) Scan(dest ...any) error {
	err := r.Row.Scan(dest...)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	return err
}
