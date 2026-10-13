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
	"github.com/codercollo/zemi-fleet/internal/config"
	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/mock"
	"github.com/codercollo/zemi-fleet/internal/repository"
	"github.com/codercollo/zemi-fleet/internal/repository/sqlc"
)

var batchCfg = config.BatchConfig{
	MaxOrders: 5, PickupRadiusKM: 2, DirectionToleranceDeg: 45, SearchRadiusKM: 5, VisibilityRadiusKM: 3,
	TTL: 10 * time.Minute, AvgSpeedKMH: 20, StopDwellMin: 2,
}

type batchFixture struct {
	svc   *BatchService
	store *mock.MockStore
	clk   *clock.Test
}

func newBatchFixture(t *testing.T) batchFixture {
	t.Helper()
	store := mock.NewMockStore(gomock.NewController(t))
	clk := clock.NewTest(testNow)
	return batchFixture{svc: NewBatchService(store, clk, batchCfg), store: store, clk: clk}
}

func batchOf(id string, firstPickup domain.Location, oldest time.Time) domain.Batch {
	return domain.Batch{
		ID:     id,
		Status: domain.BatchOpen,
		Stops:  []domain.BatchStop{{Seq: 1, Type: domain.StopPickup, OrderID: "o_" + id, Location: firstPickup}},
		Orders: []domain.BatchOrder{{ID: "o_" + id, CreatedAt: oldest}},
	}
}

func TestCreateBatches_RiderCannotTakeOrders(t *testing.T) {
	tests := []struct {
		name string
		row  sqlc.Rider
	}{
		{"at capacity 5/5", riderRow(5, 5, true)},
		{"at a lower personal capacity 3/3", riderRow(3, 3, true)},
		{"off shift", riderRow(0, 5, false)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newBatchFixture(t) // CreateBatchesTx is NOT expected: gomock fails on an unexpected call
			f.store.EXPECT().GetRider(gomock.Any(), "rider_001").Return(tc.row, nil)

			got, err := f.svc.CreateBatches(context.Background(), CreateBatchesInput{
				RiderID: "rider_001", RiderLocation: sarit, MaxOrders: 5, RadiusKM: 2,
			})

			require.NoError(t, err)
			assert.NotNil(t, got, "an empty list, never nil")
			assert.Empty(t, got)
		})
	}
}

func TestCreateBatches_UnknownRider(t *testing.T) {
	f := newBatchFixture(t)
	f.store.EXPECT().GetRider(gomock.Any(), "rider_999").Return(sqlc.Rider{}, domain.ErrNotFound)

	_, err := f.svc.CreateBatches(context.Background(), CreateBatchesInput{RiderID: "rider_999", MaxOrders: 5, RadiusKM: 2})

	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestCreateBatches_EffectiveSizeIsMinOfRequestAndCapacity(t *testing.T) {
	tests := []struct {
		name     string
		current  int16
		capacity int16
		maxReq   int
		want     int
	}{
		{"empty rider gets what they asked for", 0, 5, 3, 3},
		{"asks for more than the capacity left", 3, 5, 5, 2},
		{"one slot left", 4, 5, 5, 1},
		{"asks for fewer than the capacity left", 1, 5, 2, 2},
		{"lower personal capacity", 1, 3, 5, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newBatchFixture(t)
			f.store.EXPECT().GetRider(gomock.Any(), "rider_001").Return(riderRow(tc.current, tc.capacity, true), nil)
			f.store.EXPECT().CreateBatchesTx(gomock.Any(), gomock.Any()).DoAndReturn(
				func(_ context.Context, p repository.CreateBatchesParams) ([]domain.Batch, error) {
					assert.Equal(t, tc.want, p.Batching.MaxOrders)
					assert.Equal(t, "rider_001", p.RiderID)
					assert.Equal(t, sarit, p.RiderLocation)
					assert.Equal(t, 1500.0, p.Batching.PickupRadiusM, "the request's radius_km is the pairwise radius, in metres")
					assert.Equal(t, 5000.0, p.SearchRadiusM)
					assert.Equal(t, 45.0, p.Batching.DirectionToleranceDeg)
					assert.Equal(t, 20.0, p.Batching.AvgSpeedKMH)
					assert.Equal(t, 2.0, p.Batching.StopDwellMin)
					assert.Equal(t, 10*time.Minute, p.TTL)
					assert.True(t, p.Now.Equal(testNow), "the clock's time")
					return nil, nil
				})

			got, err := f.svc.CreateBatches(context.Background(), CreateBatchesInput{
				RiderID: "rider_001", RiderLocation: sarit, MaxOrders: tc.maxReq, RadiusKM: 1.5,
			})

			require.NoError(t, err)
			assert.NotNil(t, got)
			assert.Empty(t, got)
		})
	}
}

// Each call hands the transaction the clock's current time; the transaction expires batches whose
// expires_at is at or before it. Advancing the test clock past the TTL therefore changes what the next call does.
func TestCreateBatches_UsesTheClockForExpiry(t *testing.T) {
	f := newBatchFixture(t)
	var nows []time.Time
	f.store.EXPECT().GetRider(gomock.Any(), gomock.Any()).Return(riderRow(0, 5, true), nil).Times(2)
	f.store.EXPECT().CreateBatchesTx(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, p repository.CreateBatchesParams) ([]domain.Batch, error) {
			nows = append(nows, p.Now)
			return nil, nil
		}).Times(2)
	in := CreateBatchesInput{RiderID: "rider_001", RiderLocation: sarit, MaxOrders: 5, RadiusKM: 2}

	_, err := f.svc.CreateBatches(context.Background(), in)
	require.NoError(t, err)
	f.clk.Advance(batchCfg.TTL + time.Minute)
	_, err = f.svc.CreateBatches(context.Background(), in)
	require.NoError(t, err)

	require.Len(t, nows, 2)
	assert.True(t, nows[1].Sub(nows[0]) > batchCfg.TTL, "the second call is past the first batch's expiry")
}

func TestCreateBatches_SortsByDistanceThenOldestOrder(t *testing.T) {
	f := newBatchFixture(t)
	nearOld := batchOf("b_near_old", sarit, testNow.Add(-60*time.Minute))
	nearNew := batchOf("b_near_new", sarit, testNow.Add(-5*time.Minute))
	far := batchOf("b_far", domain.Location{Lat: sarit.Lat + 0.01, Lng: sarit.Lng}, testNow.Add(-120*time.Minute))
	mid := batchOf("b_mid", domain.Location{Lat: sarit.Lat + 0.004, Lng: sarit.Lng}, testNow.Add(-30*time.Minute))
	f.store.EXPECT().GetRider(gomock.Any(), gomock.Any()).Return(riderRow(0, 5, true), nil)
	f.store.EXPECT().CreateBatchesTx(gomock.Any(), gomock.Any()).Return([]domain.Batch{far, nearNew, mid, nearOld}, nil)

	got, err := f.svc.CreateBatches(context.Background(), CreateBatchesInput{RiderID: "rider_001", RiderLocation: sarit, MaxOrders: 5, RadiusKM: 2})

	require.NoError(t, err)
	var ids []string
	for _, o := range got {
		ids = append(ids, o.Batch.ID)
	}
	assert.Equal(t, []string{"b_near_old", "b_near_new", "b_mid", "b_far"}, ids,
		"nearest first; a distance tie goes to the batch with the older order")
	assert.InDelta(t, 0, got[0].DistanceToFirstPickupM, 1e-6)
	assert.InDelta(t, 445, got[2].DistanceToFirstPickupM, 2)
}

func TestCreateBatches_TransactionErrorIsReturned(t *testing.T) {
	f := newBatchFixture(t)
	boom := errors.New("boom")
	f.store.EXPECT().GetRider(gomock.Any(), gomock.Any()).Return(riderRow(0, 5, true), nil)
	f.store.EXPECT().CreateBatchesTx(gomock.Any(), gomock.Any()).Return(nil, boom)

	_, err := f.svc.CreateBatches(context.Background(), CreateBatchesInput{RiderID: "rider_001", MaxOrders: 5, RadiusKM: 2})

	assert.ErrorIs(t, err, boom)
}

func TestAccept_PassesTheClockAndTheResultThrough(t *testing.T) {
	f := newBatchFixture(t)
	rider := "rider_002"
	want := domain.Batch{ID: "batch_1", Status: domain.BatchAccepted, RiderID: &rider}
	f.store.EXPECT().AcceptBatchTx(gomock.Any(), repository.AcceptBatchParams{BatchID: "batch_1", RiderID: "rider_002", Now: testNow}).Return(want, nil)

	got, err := f.svc.Accept(context.Background(), "batch_1", "rider_002")

	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestAccept_DomainErrorsSurviveUnchanged(t *testing.T) {
	for _, sentinel := range []error{domain.ErrNotFound, domain.ErrBatchTaken, domain.ErrRiderAtCapacity, domain.ErrRiderUnavailable} {
		t.Run(sentinel.Error(), func(t *testing.T) {
			f := newBatchFixture(t)
			f.store.EXPECT().AcceptBatchTx(gomock.Any(), gomock.Any()).Return(domain.Batch{}, sentinel)

			_, err := f.svc.Accept(context.Background(), "batch_1", "rider_002")

			assert.ErrorIs(t, err, sentinel)
		})
	}
}

func TestAccept_UsesTheClockAtCallTime(t *testing.T) {
	f := newBatchFixture(t)
	var seen []time.Time
	f.store.EXPECT().AcceptBatchTx(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, p repository.AcceptBatchParams) (domain.Batch, error) {
			seen = append(seen, p.Now)
			return domain.Batch{}, domain.ErrBatchTaken
		}).Times(2)

	_, _ = f.svc.Accept(context.Background(), "b", "r")
	f.clk.Advance(batchCfg.TTL + time.Second) // past a batch formed at testNow: the transaction will see it as expired
	_, _ = f.svc.Accept(context.Background(), "b", "r")

	require.Len(t, seen, 2)
	assert.True(t, seen[0].Equal(testNow))
	assert.True(t, seen[1].Equal(testNow.Add(batchCfg.TTL+time.Second)))
}

func TestListBatches(t *testing.T) {
	t.Run("passes the page and radius, adds the distance to the first pickup", func(t *testing.T) {
		f := newBatchFixture(t)
		far := batchOf("b_far", domain.Location{Lat: sarit.Lat + 0.004, Lng: sarit.Lng}, testNow)
		f.store.EXPECT().ListOpenBatches(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, p repository.ListOpenBatchesParams) ([]domain.Batch, int, error) {
				assert.Equal(t, sarit, p.Point)
				assert.Equal(t, 2500.0, p.RadiusM)
				assert.Equal(t, 3, p.Page)
				assert.Equal(t, 10, p.PageSize)
				assert.True(t, p.Now.Equal(testNow))
				return []domain.Batch{far}, 23, nil
			})

		page, err := f.svc.ListBatches(context.Background(), ListBatchesInput{Point: sarit, RadiusKM: 2.5, Page: 3, PageSize: 10})

		require.NoError(t, err)
		assert.Equal(t, 23, page.Total)
		require.Len(t, page.Offers, 1)
		assert.InDelta(t, 445, page.Offers[0].DistanceToFirstPickupM, 2)
	})

	t.Run("an empty page is an empty non-nil slice", func(t *testing.T) {
		f := newBatchFixture(t)
		f.store.EXPECT().ListOpenBatches(gomock.Any(), gomock.Any()).Return(nil, 0, nil)

		page, err := f.svc.ListBatches(context.Background(), ListBatchesInput{Point: sarit, RadiusKM: 3, Page: 1, PageSize: 20})

		require.NoError(t, err)
		assert.NotNil(t, page.Offers)
		assert.Empty(t, page.Offers)
	})

	t.Run("store error", func(t *testing.T) {
		f := newBatchFixture(t)
		boom := errors.New("boom")
		f.store.EXPECT().ListOpenBatches(gomock.Any(), gomock.Any()).Return(nil, 0, boom)

		_, err := f.svc.ListBatches(context.Background(), ListBatchesInput{Point: sarit, RadiusKM: 3, Page: 1, PageSize: 20})

		assert.ErrorIs(t, err, boom)
	})
}
