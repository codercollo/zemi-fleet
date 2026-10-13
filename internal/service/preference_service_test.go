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
	"github.com/codercollo/zemi-fleet/internal/repository/sqlc"
)

var matchCfg = config.MatchConfig{
	MaxDistanceKM: 10, LocationStaleAfter: 30 * time.Minute,
	WeightCategory: 0.25, WeightArea: 0.20, WeightProximity: 0.35, WeightAcceptance: 0.20, MaxResults: 10,
}

// yaya is a Kilimani pickup; hurlingham is the delivery, a little under a kilometre away.
var (
	yaya        = domain.Location{Lat: -1.2917, Lng: 36.7878}
	hurlingham  = domain.Location{Lat: -1.2900, Lng: 36.7920}
	errNotFound = domain.ErrNotFound
)

func newPrefService(t *testing.T) (*PreferenceService, *mock.MockStore) {
	t.Helper()
	store := mock.NewMockStore(gomock.NewController(t))
	return NewPreferenceService(store, clock.NewTest(testNow), matchCfg, 50), store
}

func details() *OrderDetails {
	return &OrderDetails{Category: "electronics", Pickup: yaya, Delivery: hurlingham, ValueCents: 500000}
}

func candidate(id string, current int16, minValue int64, rate float64, loc *domain.Location, seen *time.Time) sqlc.ListMatchCandidateRidersRow {
	row := sqlc.ListMatchCandidateRidersRow{
		ID: id, IsAvailable: true, CurrentOrdersCount: current, MaxCapacity: 5,
		MinOrderValueCents: minValue, AcceptanceRate: &rate, LocationUpdatedAt: seen, // counts 0: a newcomer, smoothed to 0.5
	}
	if loc != nil {
		row.LastLat, row.LastLng = &loc.Lat, &loc.Lng
	}
	return row
}

func TestMatchRiders_OrderDetailsAreUsedWithoutLoadingTheOrder(t *testing.T) {
	svc, store := newPrefService(t) // GetOrder is NOT expected
	fresh := testNow.Add(-5 * time.Minute)
	near := domain.Location{Lat: yaya.Lat + 0.009, Lng: yaya.Lng} // about 1 km
	store.EXPECT().GetCategory(gomock.Any(), "electronics").Return(sqlc.Category{Code: "electronics"}, nil)
	store.EXPECT().ListMatchCandidateRiders(gomock.Any()).Return([]sqlc.ListMatchCandidateRidersRow{
		candidate("rider_a", 1, 0, 0.5, &near, &fresh),
		candidate("rider_b", 0, 900000, 0.9, nil, nil), // minimum above the order value: excluded
	}, nil)
	store.EXPECT().ListPreferredCategoriesForRiders(gomock.Any(), []string{"rider_a", "rider_b"}).Return([]sqlc.RiderPreferredCategory{
		{RiderID: "rider_a", CategoryCode: "electronics"},
	}, nil)
	store.EXPECT().ListPreferredAreasForRiders(gomock.Any(), []string{"rider_a", "rider_b"}).Return([]sqlc.ListPreferredAreasForRidersRow{
		{RiderID: "rider_a", AreaID: "area_001", Name: "Kilimani", CenterLat: -1.2905, CenterLng: 36.7870, RadiusM: 2500},
	}, nil)

	got, err := svc.MatchRiders(context.Background(), MatchRidersInput{OrderID: "ord_ignored", Details: details()})

	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "rider_a", got[0].RiderID)
	assert.Equal(t, []string{"electronics", "Kilimani"}, got[0].MatchingPreferences)
	assert.Equal(t, 1, got[0].CurrentOrders)
	require.NotNil(t, got[0].DistanceM)
	assert.InDelta(t, 1000, *got[0].DistanceM, 5)
	// 0.25*1 + 0.20*1 + 0.35*(1-1.0007/10) + 0.20*0.5 = 0.8649
	assert.Equal(t, 0.86, got[0].Score)
}

func TestMatchRiders_StoredOrderIsLoadedByID(t *testing.T) {
	svc, store := newPrefService(t) // GetCategory is NOT expected: a stored order has a valid category by its foreign key
	store.EXPECT().GetOrder(gomock.Any(), "ord_001").Return(sqlc.Order{
		ID: "ord_001", CategoryCode: "food", PickupLat: yaya.Lat, PickupLng: yaya.Lng, ValueCents: 15000,
	}, nil)
	store.EXPECT().ListMatchCandidateRiders(gomock.Any()).Return([]sqlc.ListMatchCandidateRidersRow{
		candidate("rider_a", 0, 20000, 0.5, nil, nil), // wants at least 200 KES, the order is worth 150
		candidate("rider_b", 0, 10000, 0.5, nil, nil),
	}, nil)
	store.EXPECT().ListPreferredCategoriesForRiders(gomock.Any(), gomock.Any()).Return(nil, nil)
	store.EXPECT().ListPreferredAreasForRiders(gomock.Any(), gomock.Any()).Return(nil, nil)

	got, err := svc.MatchRiders(context.Background(), MatchRidersInput{OrderID: "ord_001"})

	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "rider_b", got[0].RiderID)
	assert.Empty(t, got[0].MatchingPreferences)
	assert.Nil(t, got[0].DistanceM, "no location reported: unknown")
}

func TestMatchRiders_UnknownOrder(t *testing.T) {
	svc, store := newPrefService(t)
	store.EXPECT().GetOrder(gomock.Any(), "ord_nope").Return(sqlc.Order{}, errNotFound)

	_, err := svc.MatchRiders(context.Background(), MatchRidersInput{OrderID: "ord_nope"})

	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestMatchRiders_DetailsValidation(t *testing.T) {
	tests := []struct {
		name       string
		category   error
		delivery   domain.Location
		wantFields []string
	}{
		{"unknown category", errNotFound, hurlingham, []string{"order_details.category"}},
		{"trip too short", nil, domain.Location{Lat: yaya.Lat + 0.0003, Lng: yaya.Lng}, []string{"order_details.delivery_location"}},
		{"both", errNotFound, yaya, []string{"order_details.category", "order_details.delivery_location"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, store := newPrefService(t) // no candidate query may run
			store.EXPECT().GetCategory(gomock.Any(), "electronics").Return(sqlc.Category{}, tc.category)
			d := details()
			d.Delivery = tc.delivery

			_, err := svc.MatchRiders(context.Background(), MatchRidersInput{Details: d})

			var verr *domain.ValidationError
			require.ErrorAs(t, err, &verr)
			for _, f := range tc.wantFields {
				assert.Contains(t, verr.Fields, f)
			}
			assert.Len(t, verr.Fields, len(tc.wantFields))
		})
	}
}

func TestMatchRiders_NoCandidateRiders(t *testing.T) {
	svc, store := newPrefService(t) // the preference queries are NOT expected
	store.EXPECT().GetCategory(gomock.Any(), gomock.Any()).Return(sqlc.Category{}, nil)
	store.EXPECT().ListMatchCandidateRiders(gomock.Any()).Return(nil, nil)

	got, err := svc.MatchRiders(context.Background(), MatchRidersInput{Details: details()})

	require.NoError(t, err)
	assert.NotNil(t, got)
	assert.Empty(t, got)
}

// Freshness is judged against the injected clock: the same location is known at 29 minutes and unknown at 31.
func TestMatchRiders_LocationFreshnessUsesTheClock(t *testing.T) {
	loc := domain.Location{Lat: yaya.Lat + 0.009, Lng: yaya.Lng}
	for _, tc := range []struct {
		name      string
		age       time.Duration
		wantKnown bool
	}{
		{"29 minutes old", 29 * time.Minute, true},
		{"exactly 30 minutes old", 30 * time.Minute, true},
		{"31 minutes old", 31 * time.Minute, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, store := newPrefService(t)
			seen := testNow.Add(-tc.age)
			store.EXPECT().GetCategory(gomock.Any(), gomock.Any()).Return(sqlc.Category{}, nil)
			store.EXPECT().ListMatchCandidateRiders(gomock.Any()).Return([]sqlc.ListMatchCandidateRidersRow{candidate("r", 0, 0, 0.5, &loc, &seen)}, nil)
			store.EXPECT().ListPreferredCategoriesForRiders(gomock.Any(), gomock.Any()).Return(nil, nil)
			store.EXPECT().ListPreferredAreasForRiders(gomock.Any(), gomock.Any()).Return(nil, nil)

			got, err := svc.MatchRiders(context.Background(), MatchRidersInput{Details: details()})

			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, tc.wantKnown, got[0].DistanceM != nil)
		})
	}
}

func TestMatchRiders_StoreErrors(t *testing.T) {
	boom := errors.New("boom")
	one := []sqlc.ListMatchCandidateRidersRow{candidate("r", 0, 0, 0, nil, nil)}
	steps := []struct {
		name  string
		setup func(*mock.MockStore)
	}{
		{"category lookup", func(s *mock.MockStore) {
			s.EXPECT().GetCategory(gomock.Any(), gomock.Any()).Return(sqlc.Category{}, boom)
		}},
		{"candidate riders", func(s *mock.MockStore) {
			s.EXPECT().GetCategory(gomock.Any(), gomock.Any()).Return(sqlc.Category{}, nil)
			s.EXPECT().ListMatchCandidateRiders(gomock.Any()).Return(nil, boom)
		}},
		{"preferred categories", func(s *mock.MockStore) {
			s.EXPECT().GetCategory(gomock.Any(), gomock.Any()).Return(sqlc.Category{}, nil)
			s.EXPECT().ListMatchCandidateRiders(gomock.Any()).Return(one, nil)
			s.EXPECT().ListPreferredCategoriesForRiders(gomock.Any(), gomock.Any()).Return(nil, boom)
		}},
		{"preferred areas", func(s *mock.MockStore) {
			s.EXPECT().GetCategory(gomock.Any(), gomock.Any()).Return(sqlc.Category{}, nil)
			s.EXPECT().ListMatchCandidateRiders(gomock.Any()).Return(one, nil)
			s.EXPECT().ListPreferredCategoriesForRiders(gomock.Any(), gomock.Any()).Return(nil, nil)
			s.EXPECT().ListPreferredAreasForRiders(gomock.Any(), gomock.Any()).Return(nil, boom)
		}},
	}
	for _, tc := range steps {
		t.Run(tc.name, func(t *testing.T) {
			svc, store := newPrefService(t)
			tc.setup(store)

			_, err := svc.MatchRiders(context.Background(), MatchRidersInput{Details: details()})

			assert.ErrorIs(t, err, boom)
		})
	}
}

func TestNewPreferenceService_ConvertsConfig(t *testing.T) {
	svc := NewPreferenceService(nil, clock.NewTest(testNow), matchCfg, 50)

	assert.Equal(t, 10_000.0, svc.params.MaxDistanceM)
	assert.Equal(t, 30*time.Minute, svc.params.StaleAfter)
	assert.Equal(t, 10, svc.params.MaxResults)
	assert.Equal(t, 0.35, svc.params.Weights.Proximity)
	assert.Equal(t, 50.0, svc.minTripM)
}
