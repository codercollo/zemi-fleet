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

// testNow is the instant the injected clock reports in every service test.
var testNow = time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)

var sarit = domain.Location{Lat: -1.2635, Lng: 36.8030}

func TestOrderService_Create_TripAndPayout(t *testing.T) {
	// 0.01 degrees of latitude is 1111.95 m anywhere on Earth; 0.01 of longitude shrinks by cos(lat).
	tests := []struct {
		name         string
		delivery     domain.Location
		payoutBPS    int
		wantDistance int32
		wantBearing  float64
	}{
		{"due north", domain.Location{Lat: sarit.Lat + 0.01, Lng: sarit.Lng}, 9000, 1112, 0},
		{"due east", domain.Location{Lat: sarit.Lat, Lng: sarit.Lng + 0.01}, 9000, 1112, 90},
		{"due south", domain.Location{Lat: sarit.Lat - 0.01, Lng: sarit.Lng}, 8500, 1112, 180},
		{"due west", domain.Location{Lat: sarit.Lat, Lng: sarit.Lng - 0.01}, 10000, 1112, 270},
		{"same point has no direction", sarit, 9000, 0, 0},
		{"Westlands to Karen", domain.Location{Lat: -1.3190, Lng: 36.7070}, 9000, 12_030, 0}, // distance and bearing checked loosely below
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := mock.NewMockStore(gomock.NewController(t))
			store.EXPECT().GetVendor(gomock.Any(), "ven_001").Return(sqlc.Vendor{ID: "ven_001", Lat: sarit.Lat, Lng: sarit.Lng}, nil)
			store.EXPECT().GetCategory(gomock.Any(), "food").Return(sqlc.Category{Code: "food"}, nil)
			var got sqlc.CreateOrderParams
			store.EXPECT().CreateOrder(gomock.Any(), gomock.Any()).DoAndReturn(
				func(_ context.Context, p sqlc.CreateOrderParams) (sqlc.Order, error) {
					got = p
					return sqlc.Order{ID: p.ID, RiderPayoutBps: p.RiderPayoutBps, TripDistanceM: p.TripDistanceM}, nil
				})
			svc := NewOrderService(store, clock.NewTest(testNow), tc.payoutBPS)

			order, err := svc.Create(context.Background(), CreateOrderInput{
				VendorID: "ven_001", CategoryCode: "food", Delivery: tc.delivery, ValueCents: 100000, DeliveryFeeCents: 20000,
			})

			require.NoError(t, err)
			assert.Equal(t, tc.payoutBPS, order.RiderPayoutBPS, "payout is snapshotted from the service config")
			assert.EqualValues(t, tc.payoutBPS, got.RiderPayoutBps)
			assert.Equal(t, sarit.Lat, got.PickupLat, "pickup is the vendor's location")
			assert.Regexp(t, `^ord_[0-9A-Z]{20}$`, got.ID)
			assert.True(t, got.CreatedAt.Equal(testNow), "created_at is the injected clock's time")
			assert.Equal(t, time.UTC, got.CreatedAt.Location())
			assert.GreaterOrEqual(t, got.TripBearingDeg, 0.0)
			assert.Less(t, got.TripBearingDeg, 360.0, "bearing must satisfy the column CHECK")
			if tc.name == "Westlands to Karen" {
				assert.InDelta(t, tc.wantDistance, got.TripDistanceM, 400)
				assert.InDelta(t, 240, got.TripBearingDeg, 1) // west-south-west
				return
			}
			assert.InDelta(t, tc.wantDistance, got.TripDistanceM, 1)
			assert.InDelta(t, tc.wantBearing, got.TripBearingDeg, 0.1)
		})
	}
}

func TestOrderService_Create_Validation(t *testing.T) {
	tests := []struct {
		name       string
		vendorErr  error
		catErr     error
		wantFields []string
	}{
		{"unknown vendor", domain.ErrNotFound, nil, []string{"vendor_id"}},
		{"unknown category", nil, domain.ErrNotFound, []string{"category"}},
		{"both unknown", domain.ErrNotFound, domain.ErrNotFound, []string{"vendor_id", "category"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := mock.NewMockStore(gomock.NewController(t))
			store.EXPECT().GetVendor(gomock.Any(), gomock.Any()).Return(sqlc.Vendor{}, tc.vendorErr)
			store.EXPECT().GetCategory(gomock.Any(), gomock.Any()).Return(sqlc.Category{}, tc.catErr)
			svc := NewOrderService(store, clock.NewTest(testNow), 9000)

			_, err := svc.Create(context.Background(), CreateOrderInput{VendorID: "v", CategoryCode: "c"})

			var verr *domain.ValidationError
			require.ErrorAs(t, err, &verr)
			for _, f := range tc.wantFields {
				assert.Contains(t, verr.Fields, f)
			}
			assert.Len(t, verr.Fields, len(tc.wantFields))
		})
	}
}

func TestOrderService_Create_StoreFailure(t *testing.T) {
	store := mock.NewMockStore(gomock.NewController(t))
	boom := errors.New("boom")
	store.EXPECT().GetVendor(gomock.Any(), gomock.Any()).Return(sqlc.Vendor{}, boom) // a real failure stops before the category lookup

	_, err := NewOrderService(store, clock.NewTest(testNow), 9000).Create(context.Background(), CreateOrderInput{})

	require.ErrorIs(t, err, boom)
	var verr *domain.ValidationError
	assert.False(t, errors.As(err, &verr))
}

func TestOrderService_Get(t *testing.T) {
	t.Run("maps generated columns and nullable fields", func(t *testing.T) {
		store := mock.NewMockStore(gomock.NewController(t))
		earn, fee := int64(18000), int64(2000)
		batch := "batch_1"
		store.EXPECT().GetOrder(gomock.Any(), "ord_1").Return(sqlc.Order{
			ID: "ord_1", Status: sqlc.OrderStatusBatched, RiderEarningsCents: &earn, PlatformFeeCents: &fee, BatchID: &batch,
			TripDistanceM: 2500, TripBearingDeg: 45.5,
		}, nil)

		order, err := NewOrderService(store, clock.NewTest(testNow), 9000).Get(context.Background(), "ord_1")

		require.NoError(t, err)
		assert.Equal(t, domain.OrderBatched, order.Status)
		assert.EqualValues(t, 18000, order.RiderEarningsCents)
		assert.EqualValues(t, 2000, order.PlatformFeeCents)
		assert.Equal(t, 2500, order.TripDistanceM)
		assert.Equal(t, &batch, order.BatchID)
		assert.Nil(t, order.RiderID)
	})

	t.Run("not found is preserved through wrapping", func(t *testing.T) {
		store := mock.NewMockStore(gomock.NewController(t))
		store.EXPECT().GetOrder(gomock.Any(), "x").Return(sqlc.Order{}, domain.ErrNotFound)

		_, err := NewOrderService(store, clock.NewTest(testNow), 9000).Get(context.Background(), "x")

		assert.ErrorIs(t, err, domain.ErrNotFound)
	})
}

func TestRoundBearing(t *testing.T) {
	assert.Equal(t, 0.0, roundBearing(359.97), "rounds up to 360 and wraps to 0")
	assert.Equal(t, 123.5, roundBearing(123.46))
}
