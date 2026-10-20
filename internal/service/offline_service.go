package service

import (
	"context"
	"time"

	"github.com/codercollo/zemi-fleet/internal/clock"
	"github.com/codercollo/zemi-fleet/internal/repository"
)

type OfflineService struct {
	store repository.Store
	clock clock.Clock
	sink  EventSink
}

func NewOfflineService(store repository.Store, clk clock.Clock, sink EventSink) *OfflineService {
	if sink == nil {
		sink = NoopEventSink{}
	}
	return &OfflineService{store: store, clock: clk, sink: sink}
}

func (s *OfflineService) Sweep(ctx context.Context, after time.Duration) (int, error) {
	if after <= 0 {
		return 0, nil
	}
	results, err := s.store.SweepOfflineRiders(ctx, s.clock.Now(), after)
	if err != nil {
		return 0, err
	}
	for _, result := range results {
		s.sink.Emit(LifecycleEvent{Type: "rider.offline", RiderID: result.RiderID})
		for _, orderID := range result.ReleasedOrderIDs {
			s.sink.Emit(LifecycleEvent{Type: "order.released", OrderID: orderID})
		}
	}
	return len(results), nil
}
