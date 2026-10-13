package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/codercollo/zemi-fleet/internal/clock"
	"github.com/codercollo/zemi-fleet/internal/config"
	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/mock"
	"github.com/codercollo/zemi-fleet/internal/repository/sqlc"
)

var defaultCone = config.ConeConfig{RadiusKM: 1, HalfAngleDeg: 30, MinSpeedKMH: 3, MaxResults: 20}

func ptrTo[T any](v T) *T { return &v }

type matchFixture struct {
	svc   *MatchingService
	store *mock.MockStore
	logs  *bytes.Buffer
}

func newMatchFixture(t *testing.T) matchFixture {
	t.Helper()
	store := mock.NewMockStore(gomock.NewController(t))
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return matchFixture{svc: NewMatchingService(store, clock.NewTest(testNow), logger, defaultCone), store: store, logs: logs}
}

func riderRow(current, capacity int16, available bool) sqlc.Rider {
	return sqlc.Rider{ID: "rider_001", IsAvailable: available, MaxCapacity: capacity, CurrentOrdersCount: current}
}

// pickupRow is a pending order at the given offset (degrees) from sarit.
func pickupRow(id string, dLat, dLng float64, age time.Duration) sqlc.ListPendingOrdersInBoxRow {
	return sqlc.ListPendingOrdersInBoxRow{
		ID: id, PickupLat: sarit.Lat + dLat, PickupLng: sarit.Lng + dLng,
		CreatedAt: testNow.Add(-age), RiderEarningsCents: ptrTo(int64(18000)),
	}
}

func TestMatchOrders_HappyPath(t *testing.T) {
	f := newMatchFixture(t)
	f.store.EXPECT().GetRider(gomock.Any(), "rider_001").Return(riderRow(2, 5, true), nil)
	f.store.EXPECT().UpdateRiderTelemetry(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, p sqlc.UpdateRiderTelemetryParams) error {
			assert.Equal(t, "rider_001", p.ID)
			assert.Equal(t, sarit.Lat, *p.LastLat)
			assert.Equal(t, sarit.Lng, *p.LastLng)
			assert.Equal(t, 0.0, *p.LastBearingDeg)
			assert.Equal(t, 25.0, *p.LastSpeedKmh)
			assert.True(t, p.LocationUpdatedAt.Equal(testNow), "stamped by the injected clock")
			return nil
		})
	f.store.EXPECT().ListPendingOrdersInBox(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, p sqlc.ListPendingOrdersInBoxParams) ([]sqlc.ListPendingOrdersInBoxRow, error) {
			assert.Less(t, p.MinLat, sarit.Lat)
			assert.Greater(t, p.MaxLat, sarit.Lat)
			assert.Less(t, p.MinLng, sarit.Lng)
			assert.Greater(t, p.MaxLng, sarit.Lng)
			return []sqlc.ListPendingOrdersInBoxRow{
				pickupRow("ord_north_old", 0.004, 0, 7*time.Minute+30*time.Second), // ~445 m ahead
				pickupRow("ord_north_near", 0.002, 0, 61*time.Minute),              // ~222 m ahead
				pickupRow("ord_south", -0.003, 0, time.Minute),                     // behind the rider: outside the cone
			}, nil
		})

	res, err := f.svc.MatchOrders(context.Background(), MatchOrdersInput{
		RiderID: "rider_001", Position: sarit, HeadingDeg: ptrTo(0.0), SpeedKMH: ptrTo(25.0), ReportedOrdersCount: 2,
	})

	require.NoError(t, err)
	assert.True(t, res.CanAcceptMore)
	assert.Equal(t, 3, res.MaxAdditional, "max_capacity 5 - current 2")
	assert.True(t, res.DirectionFilterApplied)
	require.Len(t, res.Matches, 2)
	assert.Equal(t, "ord_north_near", res.Matches[0].OrderID)
	assert.Equal(t, 61, res.Matches[0].AgeMinutes, "frozen clock makes the age exact")
	assert.Equal(t, "ord_north_old", res.Matches[1].OrderID)
	assert.Equal(t, 7, res.Matches[1].AgeMinutes, "7m30s floors to 7")
	assert.EqualValues(t, 18000, res.Matches[0].RiderEarningsCents)
	assert.NotContains(t, f.logs.String(), "different order count")
}

func TestMatchOrders_NoCandidates(t *testing.T) {
	f := newMatchFixture(t)
	f.store.EXPECT().GetRider(gomock.Any(), gomock.Any()).Return(riderRow(0, 5, true), nil)
	f.store.EXPECT().UpdateRiderTelemetry(gomock.Any(), gomock.Any()).Return(nil)
	f.store.EXPECT().ListPendingOrdersInBox(gomock.Any(), gomock.Any()).Return(nil, nil)

	res, err := f.svc.MatchOrders(context.Background(), MatchOrdersInput{RiderID: "rider_001", Position: sarit})

	require.NoError(t, err)
	assert.NotNil(t, res.Matches, "an empty list, never nil")
	assert.Empty(t, res.Matches)
	assert.True(t, res.CanAcceptMore)
	assert.Equal(t, 5, res.MaxAdditional)
	assert.False(t, res.DirectionFilterApplied, "no heading or speed: full circle")
}

func TestMatchOrders_NoQueryWhenRiderCannotTakeOrders(t *testing.T) {
	tests := []struct {
		name string
		row  sqlc.Rider
	}{
		{"at capacity 5/5", riderRow(5, 5, true)},
		{"at a lower personal capacity 3/3", riderRow(3, 3, true)},
		{"unavailable", riderRow(0, 5, false)},
		{"unavailable and full", riderRow(5, 5, false)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newMatchFixture(t)
			f.store.EXPECT().GetRider(gomock.Any(), "rider_001").Return(tc.row, nil)
			// The position is still recorded; the candidate query is NOT expected (gomock fails on an unexpected call).
			f.store.EXPECT().UpdateRiderTelemetry(gomock.Any(), gomock.Any()).Return(nil)

			res, err := f.svc.MatchOrders(context.Background(), MatchOrdersInput{
				RiderID: "rider_001", Position: sarit, HeadingDeg: ptrTo(0.0), SpeedKMH: ptrTo(20.0), ReportedOrdersCount: int(tc.row.CurrentOrdersCount),
			})

			require.NoError(t, err)
			assert.NotNil(t, res.Matches)
			assert.Empty(t, res.Matches)
			assert.False(t, res.CanAcceptMore)
			assert.Equal(t, 0, res.MaxAdditional)
		})
	}
}

func TestMatchOrders_UnknownRider(t *testing.T) {
	f := newMatchFixture(t) // no telemetry or query expected
	f.store.EXPECT().GetRider(gomock.Any(), "rider_999").Return(sqlc.Rider{}, domain.ErrNotFound)

	_, err := f.svc.MatchOrders(context.Background(), MatchOrdersInput{RiderID: "rider_999", Position: sarit})

	assert.ErrorIs(t, err, domain.ErrNotFound)
}

// The server's count is the truth: the request's count is logged when it disagrees and otherwise ignored.
func TestMatchOrders_ServerCountIsAuthoritative(t *testing.T) {
	tests := []struct {
		name         string
		reported     int
		serverCount  int16
		wantMax      int
		wantCanAccep bool
		wantLogged   bool
	}{
		{"agree", 2, 2, 3, true, false},
		{"app thinks it is free, server says 5/5", 0, 5, 0, false, true},
		{"app thinks it is full, server says 1", 5, 1, 4, true, true},
		{"app is one behind", 2, 3, 2, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newMatchFixture(t)
			f.store.EXPECT().GetRider(gomock.Any(), gomock.Any()).Return(riderRow(tc.serverCount, 5, true), nil)
			f.store.EXPECT().UpdateRiderTelemetry(gomock.Any(), gomock.Any()).Return(nil)
			if tc.wantCanAccep {
				f.store.EXPECT().ListPendingOrdersInBox(gomock.Any(), gomock.Any()).Return(nil, nil)
			}

			res, err := f.svc.MatchOrders(context.Background(), MatchOrdersInput{
				RiderID: "rider_001", Position: sarit, ReportedOrdersCount: tc.reported,
			})

			require.NoError(t, err, "a count mismatch must never fail the request")
			assert.Equal(t, tc.wantCanAccep, res.CanAcceptMore)
			assert.Equal(t, tc.wantMax, res.MaxAdditional)
			if tc.wantLogged {
				assert.Contains(t, f.logs.String(), "level=WARN")
				assert.Contains(t, f.logs.String(), "different order count")
				assert.Contains(t, f.logs.String(), "rider_id=rider_001")
			} else {
				assert.Empty(t, f.logs.String())
			}
		})
	}
}

func TestMatchOrders_UnknownHeadingAndSpeedStoredAsNull(t *testing.T) {
	f := newMatchFixture(t)
	f.store.EXPECT().GetRider(gomock.Any(), gomock.Any()).Return(riderRow(0, 5, true), nil)
	f.store.EXPECT().UpdateRiderTelemetry(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, p sqlc.UpdateRiderTelemetryParams) error {
			assert.Nil(t, p.LastBearingDeg, "omitted bearing is NULL, as in PATCH location")
			assert.Nil(t, p.LastSpeedKmh)
			return nil
		})
	f.store.EXPECT().ListPendingOrdersInBox(gomock.Any(), gomock.Any()).Return(nil, nil)

	_, err := f.svc.MatchOrders(context.Background(), MatchOrdersInput{RiderID: "rider_001", Position: sarit})

	require.NoError(t, err)
}

func TestMatchOrders_FullCircleFallbackKeepsBehindOrders(t *testing.T) {
	f := newMatchFixture(t)
	f.store.EXPECT().GetRider(gomock.Any(), gomock.Any()).Return(riderRow(0, 5, true), nil)
	f.store.EXPECT().UpdateRiderTelemetry(gomock.Any(), gomock.Any()).Return(nil)
	f.store.EXPECT().ListPendingOrdersInBox(gomock.Any(), gomock.Any()).Return([]sqlc.ListPendingOrdersInBoxRow{
		pickupRow("ord_ahead", 0.002, 0, time.Minute), pickupRow("ord_behind", -0.002, 0, time.Minute),
	}, nil)

	res, err := f.svc.MatchOrders(context.Background(), MatchOrdersInput{
		RiderID: "rider_001", Position: sarit, HeadingDeg: ptrTo(0.0), SpeedKMH: ptrTo(0.0),
	})

	require.NoError(t, err)
	assert.False(t, res.DirectionFilterApplied, "speed 0: the heading is noise")
	assert.Len(t, res.Matches, 2)
}

func TestMatchOrders_StoreErrors(t *testing.T) {
	boom := errors.New("boom")

	t.Run("rider lookup", func(t *testing.T) {
		f := newMatchFixture(t)
		f.store.EXPECT().GetRider(gomock.Any(), gomock.Any()).Return(sqlc.Rider{}, boom)

		_, err := f.svc.MatchOrders(context.Background(), MatchOrdersInput{RiderID: "rider_001"})

		assert.ErrorIs(t, err, boom)
	})
	t.Run("telemetry write", func(t *testing.T) {
		f := newMatchFixture(t)
		f.store.EXPECT().GetRider(gomock.Any(), gomock.Any()).Return(riderRow(0, 5, true), nil)
		f.store.EXPECT().UpdateRiderTelemetry(gomock.Any(), gomock.Any()).Return(boom)

		_, err := f.svc.MatchOrders(context.Background(), MatchOrdersInput{RiderID: "rider_001"})

		assert.ErrorIs(t, err, boom)
	})
	t.Run("candidate query", func(t *testing.T) {
		f := newMatchFixture(t)
		f.store.EXPECT().GetRider(gomock.Any(), gomock.Any()).Return(riderRow(0, 5, true), nil)
		f.store.EXPECT().UpdateRiderTelemetry(gomock.Any(), gomock.Any()).Return(nil)
		f.store.EXPECT().ListPendingOrdersInBox(gomock.Any(), gomock.Any()).Return(nil, boom)

		_, err := f.svc.MatchOrders(context.Background(), MatchOrdersInput{RiderID: "rider_001"})

		assert.ErrorIs(t, err, boom)
	})
}

func TestNewMatchingService_ConvertsKMToMetres(t *testing.T) {
	svc := NewMatchingService(nil, clock.NewTest(testNow), slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		config.ConeConfig{RadiusKM: 1.5, HalfAngleDeg: 45, MinSpeedKMH: 5, MaxResults: 7})

	assert.Equal(t, 1500.0, svc.params.RadiusM)
	assert.Equal(t, 45.0, svc.params.HalfAngleDeg)
	assert.Equal(t, 5.0, svc.params.MinSpeedKMH)
	assert.Equal(t, 7, svc.params.MaxResults)
}
