package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

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
		lat, lng, bearing, speed, rate := -1.29, 36.79, float32(180), float32(18), 0.88

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

	_, err := NewRiderService(store).UpdatePreferences(context.Background(), "rider_001",
		domain.Preferences{Categories: []string{"pizza"}})

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

	_, err := NewRiderService(store).UpdatePreferences(context.Background(), "rider_001", domain.Preferences{})

	assert.ErrorIs(t, err, boom, "the first failing statement aborts the transaction body")
}

func TestUnknown(t *testing.T) {
	known := map[string]struct{}{"a": {}, "c": {}}

	assert.Equal(t, []string{"b", "d"}, unknown([]string{"a", "b", "c", "d"}, known))
	assert.Empty(t, unknown([]string{"a", "c"}, known))
	assert.Empty(t, unknown(nil, known))
}

func TestFloat32ToFloat64(t *testing.T) {
	assert.Equal(t, 38.7, float32ToFloat64(38.7))
	assert.Equal(t, 210.6, float32ToFloat64(210.6))
	assert.Equal(t, 0.0, float32ToFloat64(0))
}
