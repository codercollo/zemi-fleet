package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/codercollo/zemi-fleet/internal/clock"
	"github.com/codercollo/zemi-fleet/internal/mock"
	"github.com/codercollo/zemi-fleet/internal/repository"
)

type recordingSink struct{ events []LifecycleEvent }

func (s *recordingSink) Emit(event LifecycleEvent) { s.events = append(s.events, event) }

func TestOfflineService_SweepUsesClockAndEmitsReleaseEvents(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := mock.NewMockStore(ctrl)
	clk := clock.NewTest(time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC))
	sink := &recordingSink{}
	svc := NewOfflineService(store, clk, sink)
	store.EXPECT().SweepOfflineRiders(gomock.Any(), clk.Now(), 5*time.Minute).Return([]repository.OfflineSweepResult{
		{RiderID: "rider_1", ReleasedOrderIDs: []string{"ord_1", "ord_2"}},
		{RiderID: "rider_2"},
	}, nil)

	n, err := svc.Sweep(context.Background(), 5*time.Minute)

	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.Equal(t, []LifecycleEvent{
		{Type: "rider.offline", RiderID: "rider_1"},
		{Type: "order.released", OrderID: "ord_1"},
		{Type: "order.released", OrderID: "ord_2"},
		{Type: "rider.offline", RiderID: "rider_2"},
	}, sink.events)
}

func TestOfflineService_DisabledSweepDoesNothing(t *testing.T) {
	store := mock.NewMockStore(gomock.NewController(t))
	svc := NewOfflineService(store, clock.NewTest(testNow), &recordingSink{})

	n, err := svc.Sweep(context.Background(), 0)

	assert.NoError(t, err)
	assert.Zero(t, n)
}

func TestOfflineService_PropagatesSweepFailure(t *testing.T) {
	store := mock.NewMockStore(gomock.NewController(t))
	svc := NewOfflineService(store, clock.NewTest(testNow), NoopEventSink{})
	want := errors.New("database unavailable")
	store.EXPECT().SweepOfflineRiders(gomock.Any(), gomock.Any(), time.Minute).Return(nil, want)

	_, err := svc.Sweep(context.Background(), time.Minute)

	assert.ErrorIs(t, err, want)
}
