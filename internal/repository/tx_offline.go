package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/codercollo/zemi-fleet/internal/repository/sqlc"
)

type OfflineSweepResult struct {
	RiderID          string
	ReleasedOrderIDs []string
}

// SweepOfflineRiders flags stale riders and releases only orders not yet picked up.
// SKIP LOCKED makes concurrent sweepers safe: one sweep owns each rider row.
func (s *SQLStore) SweepOfflineRiders(ctx context.Context, now time.Time, after time.Duration) ([]OfflineSweepResult, error) {
	var results []OfflineSweepResult
	err := s.ExecTx(ctx, func(q sqlc.Querier) error {
		cutoff := now.Add(-after)
		riders, err := q.ListStaleRiders(ctx, &cutoff)
		if err != nil {
			return fmt.Errorf("list stale riders: %w", err)
		}
		for _, rider := range riders {
			if _, err := q.MarkRiderOffline(ctx, sqlc.MarkRiderOfflineParams{ID: rider.ID, OfflineFlaggedAt: &now}); err != nil {
				return fmt.Errorf("flag rider %s: %w", rider.ID, err)
			}
			orders, err := q.ListAssignedOrdersForRider(ctx, &rider.ID)
			if err != nil {
				return fmt.Errorf("list assigned orders for %s: %w", rider.ID, err)
			}
			result := OfflineSweepResult{RiderID: rider.ID}
			batches := make(map[string][]sqlc.ListAssignedOrdersForRiderRow)
			for _, order := range orders {
				if _, err := q.ReleaseAssignedOrder(ctx, order.ID); err != nil {
					return fmt.Errorf("release order %s: %w", order.ID, err)
				}
				result.ReleasedOrderIDs = append(result.ReleasedOrderIDs, order.ID)
				if order.BatchID != nil {
					batches[*order.BatchID] = append(batches[*order.BatchID], order)
				}
			}
			if len(result.ReleasedOrderIDs) > 0 {
				if err := q.DecrementRiderOrders(ctx, sqlc.DecrementRiderOrdersParams{ID: rider.ID, Column2: int16(len(result.ReleasedOrderIDs))}); err != nil {
					return fmt.Errorf("decrement rider %s: %w", rider.ID, err)
				}
				for batchID, released := range batches {
					// If picked-up orders remain, keep the batch alive but remove
					// released orders from its stops and financial totals. If none
					// remain, cancellation preserves the historical batch record.
					for _, order := range released {
						if err := q.RemoveReleasedOrderFromBatch(ctx, sqlc.RemoveReleasedOrderFromBatchParams{BatchID: batchID, OrderID: order.ID}); err != nil {
							return fmt.Errorf("remove released order %s from batch %s: %w", order.ID, batchID, err)
						}
						earnings := int64(0)
						if order.RiderEarningsCents != nil {
							earnings = *order.RiderEarningsCents
						}
						if err := q.DecrementBatchOrderTotals(ctx, sqlc.DecrementBatchOrderTotalsParams{
							ID: batchID, TotalDeliveryFeeCents: order.DeliveryFeeCents, EstimatedEarningsCents: earnings,
						}); err != nil {
							return fmt.Errorf("update batch %s totals: %w", batchID, err)
						}
					}
					if err := q.CancelBatchIfNoActiveOrders(ctx, batchID); err != nil {
						return fmt.Errorf("cancel empty batch %s: %w", batchID, err)
					}
				}
			}
			results = append(results, result)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("sweep offline riders: %w", err)
	}
	return results, nil
}
