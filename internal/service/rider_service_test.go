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
	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/mock"
	"github.com/codercollo/zemi-fleet/internal/repository/sqlc"
)

func TestRiderFromRow(t *testing.T) {
	t.Run("no telemetry yet", func(t *testing.T) {
		rider := riderFromRow(sqlc.Rider{ID: "rider_008", MaxCapacity: 5})

		assert.Nil(t, rider.Location)
		assert.Nil(t, rider.BearingDeg)
		assert.Nil(t, rider.SpeedKmh)
		assert.Zero(t, rider.AcceptanceRate)
	})

	t.Run("with telemetry", func(t *testing.T) {
		lat, lng, bearing, speed, rate := -1.29, 36.79, 180.0, 18.0, 0.88

		rider := riderFromRow(sqlc.Rider{
			LastLat: &lat, LastLng: &lng, LastBearingDeg: &bearing, LastSpeedKmh: &speed, AcceptanceRate: &rate,
			CurrentOrdersCount: 2, MaxCapacity: 5,
		})

		require.NotNil(t, rider.Location)
		assert.Equal(t, domain.Location{Lat: lat, Lng: lng}, *rider.Location)
		assert.Equal(t, 180.0, *rider.BearingDeg)
		assert.Equal(t, 0.88, rider.AcceptanceRate)
		assert.Equal(t, 2, rider.CurrentOrders)
	})
}

func TestRiderService_UpdatePreferences_ValidationSkipsTransaction(t *testing.T) {
	store := mock.NewMockStore(gomock.NewController(t)) // no ExecTx expectation: a call would fail the test
	store.EXPECT().GetRider(gomock.Any(), "rider_001").Return(sqlc.Rider{ID: "rider_001"}, nil)
	store.EXPECT().GetCategoriesByCodes(gomock.Any(), []string{"pizza"}).Return(nil, nil)

	_, err := NewRiderService(store, clock.NewTest(testNow)).UpdatePreferences(context.Background(), "rider_001",
		domain.Preferences{Categories: []string{"pizza"}}, nil)

	var verr *domain.ValidationError
	require.ErrorAs(t, err, &verr)
	assert.Equal(t, "unknown categories: pizza", verr.Fields["preferred_categories"])
}

func TestRiderService_UpdatePreferences_RollsBackOnFailure(t *testing.T) {
	store := mock.NewMockStore(gomock.NewController(t))
	boom := errors.New("insert failed")
	store.EXPECT().GetRider(gomock.Any(), "rider_001").Return(sqlc.Rider{ID: "rider_001"}, nil)
	store.EXPECT().ExecTx(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, fn func(sqlc.Querier) error) error { return fn(store) })
	store.EXPECT().GetRiderForUpdate(gomock.Any(), "rider_001").Return(sqlc.Rider{}, nil)
	store.EXPECT().UpsertPreferences(gomock.Any(), gomock.Any()).Return(sqlc.RiderPreference{}, boom)

	_, err := NewRiderService(store, clock.NewTest(testNow)).UpdatePreferences(context.Background(), "rider_001", domain.Preferences{}, nil)

	assert.ErrorIs(t, err, boom, "the first failing statement aborts the transaction body")
}

func TestUnknown(t *testing.T) {
	known := map[string]struct{}{"a": {}, "c": {}}

	assert.Equal(t, []string{"b", "d"}, unknown([]string{"a", "b", "c", "d"}, known))
	assert.Empty(t, unknown([]string{"a", "c"}, known))
	assert.Empty(t, unknown(nil, known))
}

func TestMappersNormaliseToUTC(t *testing.T) {
	eat := time.FixedZone("EAT", 3*60*60)
	local := time.Date(2026, 10, 13, 12, 0, 0, 0, eat)

	rider := riderFromRow(sqlc.Rider{CreatedAt: local, UpdatedAt: local, LocationUpdatedAt: &local})
	order := orderFromRow(sqlc.Order{CreatedAt: local, UpdatedAt: local})

	for name, got := range map[string]time.Time{
		"rider created":  rider.CreatedAt,
		"rider updated":  rider.UpdatedAt,
		"rider location": *rider.LocationUpdatedAt,
		"order created":  order.CreatedAt,
		"order updated":  order.UpdatedAt,
	} {
		assert.Equal(t, time.UTC, got.Location(), name)
		assert.True(t, got.Equal(local), name)
	}
}

func TestUpdateLocation_StampsClockTime(t *testing.T) {
	clk := clock.NewTest(testNow)
	store := mock.NewMockStore(gomock.NewController(t))
	store.EXPECT().GetRider(gomock.Any(), "rider_001").Return(sqlc.Rider{ID: "rider_001", Version: 1}, nil).Times(2)
	var stamps []time.Time
	store.EXPECT().UpdateRiderLocation(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, p sqlc.UpdateRiderLocationParams) (sqlc.Rider, error) {
			stamps = append(stamps, *p.LocationUpdatedAt)
			return sqlc.Rider{ID: "rider_001"}, nil
		}).Times(2)
	store.EXPECT().GetPreferences(gomock.Any(), gomock.Any()).Return(sqlc.RiderPreference{}, domain.ErrNotFound).Times(2)
	store.EXPECT().ListPreferredCategories(gomock.Any(), gomock.Any()).Return(nil, nil).Times(2)
	store.EXPECT().ListPreferredAreas(gomock.Any(), gomock.Any()).Return(nil, nil).Times(2)
	svc := NewRiderService(store, clk)

	_, err := svc.UpdateLocation(context.Background(), "rider_001", LocationUpdate{Lat: -1.3, Lng: 36.8}, nil)
	require.NoError(t, err)
	clk.Advance(90 * time.Second)
	_, err = svc.UpdateLocation(context.Background(), "rider_001", LocationUpdate{Lat: -1.3, Lng: 36.8}, nil)
	require.NoError(t, err)

	require.Len(t, stamps, 2)
	assert.True(t, stamps[0].Equal(testNow))
	assert.True(t, stamps[1].Equal(testNow.Add(90*time.Second)))
}
