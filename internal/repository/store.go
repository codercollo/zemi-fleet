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

	// CreateBatchesTx forms batches near a rider and returns the open batches on offer to them
	// (tx_create_batches.go), plus how many of them this call newly formed (the rest were already open).
	CreateBatchesTx(ctx context.Context, arg CreateBatchesParams) (batches []domain.Batch, formed int, err error)

	// AcceptBatchTx lets a rider claim an open batch exclusively (tx_accept_batch.go). alreadyHeld is true when the
	// rider had accepted this batch before: an idempotent retry that changed nothing.
	AcceptBatchTx(ctx context.Context, arg AcceptBatchParams) (batch domain.Batch, alreadyHeld bool, err error)

	// ExpireBatchesTx expires stale open batches and releases their orders (tx_expire_batches.go).
	ExpireBatchesTx(ctx context.Context, now time.Time) (int, error)

	// ListOpenBatches pages through open, unexpired batches near a point, read-only (batch_listing.go).
	ListOpenBatches(ctx context.Context, arg ListOpenBatchesParams) ([]domain.Batch, int, error)

	CompleteBatchStopTx(ctx context.Context, arg CompleteStopParams) (LifecycleResult, error)
	RecordDeliveryAttemptTx(ctx context.Context, arg AttemptParams) (sqlc.Order, error)
	CancelOrderTx(ctx context.Context, arg CancelParams) (sqlc.Order, error)
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

// wrap layers the per-statement deadline, UTC normalisation and the ErrNoRows mapping onto a DBTX.
func wrap(db sqlc.DBTX, timeout time.Duration) sqlc.DBTX {
	return notFoundDB{utcDB{timeoutDB{DBTX: db, timeout: timeout}}}
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
