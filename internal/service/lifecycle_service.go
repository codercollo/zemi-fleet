package service

import (
	"context"

	"github.com/codercollo/zemi-fleet/internal/clock"
	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/repository"
)

type LifecycleService struct {
	store                 repository.Store
	clock                 clock.Clock
	cancelCompensationBPS int
	sink                  EventSink
}

func NewLifecycleService(store repository.Store, clk clock.Clock, cancelCompensationBPS int, sink EventSink) *LifecycleService {
	if sink == nil {
		sink = NoopEventSink{}
	}
	return &LifecycleService{store: store, clock: clk, cancelCompensationBPS: cancelCompensationBPS, sink: sink}
}

func (s *LifecycleService) CompleteStop(ctx context.Context, batchID string, seq int16) (domain.Order, error) {
	result, err := s.store.CompleteBatchStopTx(ctx, repository.CompleteStopParams{BatchID: batchID, Seq: seq, Now: s.clock.Now()})
	if err != nil {
		return domain.Order{}, err
	}
	order := orderFromRow(result.Order)
	eventType := "order.delivered"
	if order.Status == domain.OrderReturned {
		eventType = "order.returned"
	}
	if order.Status == domain.OrderPickedUp {
		eventType = "order.picked_up"
	}
	s.sink.Emit(LifecycleEvent{Type: eventType, OrderID: order.ID, BatchID: batchID})
	return order, nil
}

func (s *LifecycleService) RecordAttempt(ctx context.Context, orderID, reasonCode string) (domain.Order, error) {
	row, err := s.store.RecordDeliveryAttemptTx(ctx, repository.AttemptParams{OrderID: orderID, ReasonCode: reasonCode, Now: s.clock.Now()})
	if err != nil {
		return domain.Order{}, err
	}
	order := orderFromRow(row)
	eventType := "order.attempt_failed"
	if order.Status == domain.OrderReturning {
		eventType = "order.returning"
	}
	s.sink.Emit(LifecycleEvent{Type: eventType, OrderID: order.ID, BatchID: stringPtrValue(order.BatchID)})
	return order, nil
}

func (s *LifecycleService) Cancel(ctx context.Context, orderID, reasonCode string) (domain.Order, error) {
	row, err := s.store.CancelOrderTx(ctx, repository.CancelParams{OrderID: orderID, ReasonCode: reasonCode, Now: s.clock.Now(), CompensationBPS: s.cancelCompensationBPS})
	if err != nil {
		return domain.Order{}, err
	}
	order := orderFromRow(row)
	s.sink.Emit(LifecycleEvent{Type: "order.cancelled", OrderID: order.ID, BatchID: stringPtrValue(order.BatchID)})
	return order, nil
}

func stringPtrValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
