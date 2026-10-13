package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/codercollo/zemi-fleet/internal/repository/sqlc"
)

// ExpireBatchesTx expires every open batch whose expires_at is at or before now and puts its orders back to
// pending, returning how many batches expired. POST /orders/batch does this as its first step; match-orders
// calls it too, so an order whose batch timed out is offered again straight away instead of staying invisible
// until somebody next asks for batches.
func (s *SQLStore) ExpireBatchesTx(ctx context.Context, now time.Time) (int, error) {
	var n int
	err := s.ExecTx(ctx, func(q sqlc.Querier) error {
		var err error
		n, err = expireStale(ctx, q, now)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("expire batches: %w", err)
	}
	return n, nil
}

// expireStale is the shared body: mark stale open batches expired, then release their orders.
func expireStale(ctx context.Context, q sqlc.Querier, now time.Time) (int, error) {
	expired, err := q.ExpireStaleBatches(ctx, now)
	if err != nil {
		return 0, fmt.Errorf("expire stale batches: %w", err)
	}
	if len(expired) > 0 {
		if err := q.ReleaseOrdersFromBatches(ctx, expired); err != nil {
			return 0, fmt.Errorf("release orders of expired batches: %w", err)
		}
	}
	return len(expired), nil
}
