package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/repository/sqlc"
)

const matchRidersPath = "/api/v2/orders/match-riders"

const detailsBody = `{"order_id":"ord_new","order_details":{"category":"electronics",
	"pickup_location":{"lat":-1.2917,"lng":36.7878},"delivery_location":{"lat":-1.2900,"lng":36.7920},"value_kes":5000}}`

func candidateRow(id string, current int16, minValue int64, rate float64, lat, lng *float64, seen *time.Time) sqlc.ListMatchCandidateRidersRow {
	return sqlc.ListMatchCandidateRidersRow{
		ID: id, IsAvailable: true, CurrentOrdersCount: current, MaxCapacity: 5, MinOrderValueCents: minValue,
		AcceptanceRate: &rate, LastLat: lat, LastLng: lng, LocationUpdatedAt: seen,
	}
}

func f64(v float64) *float64 { return &v }

func TestMatchRiders_200(t *testing.T) {
	t.Run("the brief shape in the brief key order, from order_details", func(t *testing.T) {
		h, store := newStoreHandler(t) // GetOrder is NOT expected: details win over order_id
		seen := testNow.Add(-2 * time.Minute)
		store.EXPECT().GetCategory(gomock.Any(), "electronics").Return(sqlc.Category{Code: "electronics"}, nil)
		store.EXPECT().ListMatchCandidateRiders(gomock.Any()).Return([]sqlc.ListMatchCandidateRidersRow{
			candidateRow("rider_a", 2, 0, 0.8125, f64(-1.2917+0.009), f64(36.7878), &seen),
			candidateRow("rider_b", 0, 0, 0.5, nil, nil, nil),
		}, nil)
		store.EXPECT().ListPreferredCategoriesForRiders(gomock.Any(), gomock.Any()).Return([]sqlc.RiderPreferredCategory{
			{RiderID: "rider_a", CategoryCode: "electronics"},
		}, nil)
		store.EXPECT().ListPreferredAreasForRiders(gomock.Any(), gomock.Any()).Return([]sqlc.ListPreferredAreasForRidersRow{
			{RiderID: "rider_a", AreaID: "area_001", Name: "Kilimani", CenterLat: -1.2905, CenterLng: 36.787, RadiusM: 2500},
		}, nil)

		rec := send(t, h, http.MethodPost, matchRidersPath, detailsBody)

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		body := decode(t, rec)
		assert.Equal(t, []string{"matching_riders"}, keys(body))
		riders := body["matching_riders"].([]any)
		require.Len(t, riders, 2)
		first := riders[0].(map[string]any)
		assert.ElementsMatch(t, []string{"rider_id", "match_score", "matching_preferences", "current_orders_count", "distance_to_pickup_km", "acceptance_rate"}, keys(first))
		assert.Equal(t, "rider_a", first["rider_id"])
		assert.Equal(t, []any{"electronics", "Kilimani"}, first["matching_preferences"])
		assert.EqualValues(t, 2, first["current_orders_count"])
		assert.Equal(t, 1.0, first["distance_to_pickup_km"])
		assert.Equal(t, 0.813, first["acceptance_rate"], "3 decimals")
		assert.Equal(t, 0.93, first["match_score"], "2 decimals: 0.25 + 0.20 + 0.35*0.9 + 0.20*0.8125 = 0.9275")
		second := riders[1].(map[string]any)
		assert.Equal(t, "rider_b", second["rider_id"])
		assert.Contains(t, second, "distance_to_pickup_km")
		assert.Nil(t, second["distance_to_pickup_km"], "unknown location is null, not 0")
		assert.Equal(t, []any{}, second["matching_preferences"], "an empty array for a rider with no preferences")
		assertInOrder(t, rec.Body.String(), `"rider_id"`, `"match_score"`, `"matching_preferences"`, `"current_orders_count"`, `"distance_to_pickup_km"`, `"acceptance_rate"`)
	})

	t.Run("by order_id alone", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetOrder(gomock.Any(), "ord_001").Return(sqlc.Order{ID: "ord_001", CategoryCode: "groceries", PickupLat: -1.2975, PickupLng: 36.786, ValueCents: 139000}, nil)
		store.EXPECT().ListMatchCandidateRiders(gomock.Any()).Return([]sqlc.ListMatchCandidateRidersRow{candidateRow("rider_a", 0, 0, 0.5, nil, nil, nil)}, nil)
		store.EXPECT().ListPreferredCategoriesForRiders(gomock.Any(), gomock.Any()).Return(nil, nil)
		store.EXPECT().ListPreferredAreasForRiders(gomock.Any(), gomock.Any()).Return(nil, nil)

		rec := send(t, h, http.MethodPost, matchRidersPath, `{"order_id":"ord_001"}`)

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Len(t, decode(t, rec)["matching_riders"], 1)
	})

	t.Run("order_details without an order_id (an unsaved order)", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetCategory(gomock.Any(), gomock.Any()).Return(sqlc.Category{}, nil)
		store.EXPECT().ListMatchCandidateRiders(gomock.Any()).Return(nil, nil)

		rec := send(t, h, http.MethodPost, matchRidersPath,
			`{"order_details":{"category":"food","pickup_location":{"lat":-1.29,"lng":36.78},"delivery_location":{"lat":-1.30,"lng":36.80},"value_kes":300}}`)

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})

	t.Run("no riders is an empty array", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetCategory(gomock.Any(), gomock.Any()).Return(sqlc.Category{}, nil)
		store.EXPECT().ListMatchCandidateRiders(gomock.Any()).Return(nil, nil)

		rec := send(t, h, http.MethodPost, matchRidersPath, detailsBody)

		require.Equal(t, http.StatusOK, rec.Code)
		assert.JSONEq(t, `{"matching_riders":[]}`, rec.Body.String())
	})
}

func TestMatchRiders_400(t *testing.T) {
	for _, body := range []string{``, `{"order_id":`, `{"order_id":"o","extra":1}`, `{"order_id":5}`, `{"order_id":"o","order_details":{"value_kes":"lots"}}`, `{"order_id":"o"}{"order_id":"o"}`} {
		h, _ := newStoreHandler(t)

		rec := send(t, h, http.MethodPost, matchRidersPath, body)

		assert.Equal(t, http.StatusBadRequest, rec.Code, body)
	}
}

func TestMatchRiders_404(t *testing.T) {
	h, store := newStoreHandler(t)
	store.EXPECT().GetOrder(gomock.Any(), "ord_nope").Return(sqlc.Order{}, domain.ErrNotFound)

	rec := send(t, h, http.MethodPost, matchRidersPath, `{"order_id":"ord_nope"}`)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestMatchRiders_422(t *testing.T) {
	tests := []struct{ name, body, wantField string }{
		{"no order_id and no details", `{}`, "order_id"},
		{"blank order_id and no details", `{"order_id":"  "}`, "order_id"},
		{"details without category", `{"order_details":{"pickup_location":{"lat":-1.29,"lng":36.78},"delivery_location":{"lat":-1.30,"lng":36.8},"value_kes":100}}`, "order_details.category"},
		{"details without pickup", `{"order_details":{"category":"food","delivery_location":{"lat":-1.30,"lng":36.8},"value_kes":100}}`, "order_details.pickup_location"},
		{"details without delivery", `{"order_details":{"category":"food","pickup_location":{"lat":-1.29,"lng":36.78},"value_kes":100}}`, "order_details.delivery_location"},
		{"pickup latitude out of range", `{"order_details":{"category":"food","pickup_location":{"lat":91,"lng":36.78},"delivery_location":{"lat":-1.30,"lng":36.8},"value_kes":100}}`, "order_details.pickup_location.lat"},
		{"delivery longitude out of range", `{"order_details":{"category":"food","pickup_location":{"lat":-1.29,"lng":36.78},"delivery_location":{"lat":-1.30,"lng":181},"value_kes":100}}`, "order_details.delivery_location.lng"},
		{"value zero", `{"order_details":{"category":"food","pickup_location":{"lat":-1.29,"lng":36.78},"delivery_location":{"lat":-1.30,"lng":36.8},"value_kes":0}}`, "order_details.value_kes"},
		{"value negative", `{"order_details":{"category":"food","pickup_location":{"lat":-1.29,"lng":36.78},"delivery_location":{"lat":-1.30,"lng":36.8},"value_kes":-5}}`, "order_details.value_kes"},
		{"value missing", `{"order_details":{"category":"food","pickup_location":{"lat":-1.29,"lng":36.78},"delivery_location":{"lat":-1.30,"lng":36.8}}}`, "order_details.value_kes"},
		{"fraction of a cent", `{"order_details":{"category":"food","pickup_location":{"lat":-1.29,"lng":36.78},"delivery_location":{"lat":-1.30,"lng":36.8},"value_kes":10.005}}`, "order_details.value_kes"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newStoreHandler(t) // no store call may happen

			rec := send(t, h, http.MethodPost, matchRidersPath, tc.body)

			require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
			assert.Contains(t, errFields(t, rec), tc.wantField)
		})
	}

	t.Run("unknown category", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetCategory(gomock.Any(), "electronics").Return(sqlc.Category{}, domain.ErrNotFound)

		rec := send(t, h, http.MethodPost, matchRidersPath, detailsBody)

		require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		assert.Equal(t, "category does not exist", errFields(t, rec)["order_details.category"])
	})

	t.Run("pickup and delivery closer than ORDER_MIN_TRIP_M", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetCategory(gomock.Any(), gomock.Any()).Return(sqlc.Category{}, nil)

		rec := send(t, h, http.MethodPost, matchRidersPath,
			`{"order_details":{"category":"food","pickup_location":{"lat":-1.29,"lng":36.78},"delivery_location":{"lat":-1.2901,"lng":36.78},"value_kes":100}}`)

		require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		assert.Contains(t, errFields(t, rec)["order_details.delivery_location"], "at least 50 m")
	})
}
