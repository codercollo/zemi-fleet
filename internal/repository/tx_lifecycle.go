package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/repository/sqlc"
)

type CompleteStopParams struct {
	BatchID string
	Seq     int16
	Now     time.Time
}

type LifecycleResult struct {
	Order sqlc.Order
	Batch sqlc.Batch
}

func (s *SQLStore) CompleteBatchStopTx(ctx context.Context, arg CompleteStopParams) (LifecycleResult, error) {
	var result LifecycleResult
	err := s.ExecTx(ctx, func(q sqlc.Querier) error {
		var err error
		result, err = completeBatchStop(ctx, q, arg)
		return err
	})
	if err != nil {
		return LifecycleResult{}, fmt.Errorf("complete batch stop: %w", err)
	}
	return result, nil
}

func completeBatchStop(ctx context.Context, q sqlc.Querier, arg CompleteStopParams) (LifecycleResult, error) {
	// The batch is read before locking so its rider can be locked first. The
	// second read takes the actual row lock after the rider lock is held.
	base, err := q.GetBatch(ctx, arg.BatchID)
	if err != nil {
		return LifecycleResult{}, fmt.Errorf("get batch: %w", err)
	}
	if base.RiderID == nil {
		return LifecycleResult{}, domain.ErrEditConflict
	}
	if _, err := q.GetRiderForUpdate(ctx, *base.RiderID); err != nil {
		return LifecycleResult{}, fmt.Errorf("lock rider: %w", err)
	}
	batch, err := q.GetBatchForUpdate(ctx, arg.BatchID)
	if err != nil {
		return LifecycleResult{}, fmt.Errorf("lock batch: %w", err)
	}
	if batch.RiderID == nil || *batch.RiderID != *base.RiderID || (batch.Status != sqlc.BatchStatusAccepted && batch.Status != sqlc.BatchStatusInProgress) {
		return LifecycleResult{}, domain.ErrEditConflict
	}
	stop, err := q.GetBatchStop(ctx, sqlc.GetBatchStopParams{BatchID: arg.BatchID, Seq: arg.Seq})
	if err != nil {
		return LifecycleResult{}, fmt.Errorf("get stop: %w", err)
	}
	if stop.CompletedAt != nil {
		return LifecycleResult{}, domain.ErrEditConflict
	}
	order, err := q.GetOrderForUpdate(ctx, stop.OrderID)
	if err != nil {
		return LifecycleResult{}, fmt.Errorf("lock order: %w", err)
	}
	if order.BatchID == nil || *order.BatchID != arg.BatchID || order.RiderID == nil || *order.RiderID != *batch.RiderID {
		return LifecycleResult{}, domain.ErrEditConflict
	}

	var updated sqlc.Order
	switch stop.Type {
	case sqlc.StopTypePickup:
		updated, err = q.MarkOrderPickedUp(ctx, order.ID)
		if err != nil {
			return LifecycleResult{}, lifecycleUpdateError(err)
		}
		if _, err := q.MarkBatchInProgress(ctx, arg.BatchID); err != nil {
			return LifecycleResult{}, fmt.Errorf("start batch: %w", err)
		}
	case sqlc.StopTypeDelivery:
		switch order.Status {
		case sqlc.OrderStatusPickedUp:
			updated, err = q.MarkOrderDelivered(ctx, sqlc.MarkOrderDeliveredParams{ID: order.ID, DeliveredAt: &arg.Now})
		case sqlc.OrderStatusReturning:
			updated, err = q.MarkOrderReturned(ctx, order.ID)
		default:
			return LifecycleResult{}, domain.ErrEditConflict
		}
		if err != nil {
			return LifecycleResult{}, lifecycleUpdateError(err)
		}
		if err := q.ReleaseRiderCapacity(ctx, *batch.RiderID); err != nil {
			return LifecycleResult{}, fmt.Errorf("release rider capacity: %w", err)
		}
	default:
		return LifecycleResult{}, fmt.Errorf("unknown stop type %q", stop.Type)
	}
	if n, err := q.CompleteBatchStop(ctx, sqlc.CompleteBatchStopParams{BatchID: arg.BatchID, Seq: arg.Seq, CompletedAt: &arg.Now}); err != nil {
		return LifecycleResult{}, fmt.Errorf("complete stop: %w", err)
	} else if n != 1 {
		return LifecycleResult{}, domain.ErrEditConflict
	}
	completed, err := q.CompleteBatchIfDone(ctx, sqlc.CompleteBatchIfDoneParams{ID: arg.BatchID, CompletedAt: &arg.Now})
	if err == nil {
		batch = completed
	}
	if err != nil && !isNotFound(err) {
		return LifecycleResult{}, fmt.Errorf("complete batch: %w", err)
	}
	if err == nil {
		eventType := ""
		if updated.Status == sqlc.OrderStatusDelivered {
			eventType = "order.delivered"
		}
		if updated.Status == sqlc.OrderStatusReturned {
			eventType = "order.returned"
		}
		if eventType != "" {
			if err := writeOutboxEvent(ctx, q, eventType, map[string]any{"order_id": updated.ID, "batch_id": arg.BatchID, "rider_id": *batch.RiderID}, arg.Now); err != nil {
				return LifecycleResult{}, err
			}
		}
		return LifecycleResult{Order: updated, Batch: batch}, nil
	}
	return LifecycleResult{Order: updated, Batch: batch}, nil
}

type AttemptParams struct {
	OrderID, ReasonCode string
	Now                 time.Time
}

func (s *SQLStore) RecordDeliveryAttemptTx(ctx context.Context, arg AttemptParams) (sqlc.Order, error) {
	var result sqlc.Order
	err := s.ExecTx(ctx, func(q sqlc.Querier) error {
		base, err := q.GetOrder(ctx, arg.OrderID)
		if err != nil {
			return err
		}
		if base.RiderID == nil || base.BatchID == nil {
			return domain.ErrEditConflict
		}
		if _, err := q.GetRiderForUpdate(ctx, *base.RiderID); err != nil {
			return err
		}
		if _, err := q.GetBatchForUpdate(ctx, *base.BatchID); err != nil {
			return err
		}
		locked, err := q.GetOrderForUpdate(ctx, arg.OrderID)
		if err != nil {
			return err
		}
		if locked.RiderID == nil || locked.BatchID == nil {
			return domain.ErrEditConflict
		}
		result, err = q.RecordDeliveryAttempt(ctx, locked.ID)
		if err != nil {
			return lifecycleUpdateError(err)
		}
		return nil
	})
	if err != nil {
		return sqlc.Order{}, fmt.Errorf("record delivery attempt: %w", err)
	}
	return result, nil
}

type CancelParams struct {
	OrderID, ReasonCode string
	Now                 time.Time
	CompensationBPS     int
}

func (s *SQLStore) CancelOrderTx(ctx context.Context, arg CancelParams) (sqlc.Order, error) {
	var result sqlc.Order
	err := s.ExecTx(ctx, func(q sqlc.Querier) error {
		base, err := q.GetOrder(ctx, arg.OrderID)
		if err != nil {
			return err
		}
		if base.RiderID == nil || base.BatchID == nil {
			return domain.ErrEditConflict
		}
		if _, err := q.GetRiderForUpdate(ctx, *base.RiderID); err != nil {
			return err
		}
		if _, err := q.GetBatchForUpdate(ctx, *base.BatchID); err != nil {
			return err
		}
		locked, err := q.GetOrderForUpdate(ctx, arg.OrderID)
		if err != nil {
			return err
		}
		if locked.RiderID == nil || locked.BatchID == nil {
			return domain.ErrEditConflict
		}
		compensation := int64(0)
		if locked.Status == sqlc.OrderStatusPickedUp {
			compensation = locked.DeliveryFeeCents * int64(arg.CompensationBPS) / 10000
		}
		result, err = q.CancelOrder(ctx, sqlc.CancelOrderParams{ID: arg.OrderID, CancelledAt: &arg.Now, CancelReason: &arg.ReasonCode, RiderCompensationCents: compensation})
		if err != nil {
			return lifecycleUpdateError(err)
		}
		if err := q.CompleteOrderStops(ctx, sqlc.CompleteOrderStopsParams{BatchID: *locked.BatchID, OrderID: locked.ID, CompletedAt: &arg.Now}); err != nil {
			return err
		}
		if err := q.ReleaseRiderCapacity(ctx, *locked.RiderID); err != nil {
			return err
		}
		if _, err := q.CompleteBatchIfDone(ctx, sqlc.CompleteBatchIfDoneParams{ID: *locked.BatchID, CompletedAt: &arg.Now}); err != nil && !isNotFound(err) {
			return err
		}
		if err := writeOutboxEvent(ctx, q, "order.cancelled", map[string]any{"order_id": result.ID, "batch_id": *locked.BatchID, "rider_id": *locked.RiderID}, arg.Now); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return sqlc.Order{}, fmt.Errorf("cancel order: %w", err)
	}
	return result, nil
}

func lifecycleUpdateError(err error) error {
	if isNotFound(err) {
		return domain.ErrEditConflict
	}
	return err
}

func isNotFound(err error) bool { return errors.Is(err, domain.ErrNotFound) }
