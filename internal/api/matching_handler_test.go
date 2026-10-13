package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/repository/sqlc"
)

const matchPath = "/api/v2/riders/match-orders"

// A rider at Sarit Centre heading north at 25 km/h with 2 orders.
const validMatchBody = `{"rider_id":"rider_001","current_location":{"latitude":-1.2635,"longitude":36.8030},
	"bearing_degrees":0,"speed_kmh":25,"current_orders_count":2}`

func matchRiderRow(current, capacity int16, available bool) sqlc.Rider {
	return sqlc.Rider{ID: "rider_001", FullName: "Brian", Phone: "+254722000001", IsAvailable: available, MaxCapacity: capacity, CurrentOrdersCount: current}
}

func pendingRow(id string, dLat float64, age time.Duration, earnings int64) sqlc.ListPendingOrdersInBoxRow {
	return sqlc.ListPendingOrdersInBoxRow{
		ID: id, PickupLat: -1.2635 + dLat, PickupLng: 36.8030, CreatedAt: testNow.Add(-age), RiderEarningsCents: &earnings,
	}
}

func TestMatchOrders_200(t *testing.T) {
	t.Run("brief's response shape, rounded and in KES", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetRider(gomock.Any(), "rider_001").Return(matchRiderRow(2, 5, true), nil)
		store.EXPECT().UpdateRiderTelemetry(gomock.Any(), gomock.Any()).Return(nil)
		store.EXPECT().ListPendingOrdersInBox(gomock.Any(), gomock.Any()).Return([]sqlc.ListPendingOrdersInBoxRow{
			pendingRow("ord_a", 0.00225, 12*time.Minute+40*time.Second, 11700), // ~250.2 m due north
			pendingRow("ord_b", 0.0045, 3*time.Hour, 25050),                    // ~500.4 m due north
			pendingRow("ord_behind", -0.002, time.Minute, 100),                 // behind: outside the cone
		}, nil)

		rec := send(t, h, http.MethodPost, matchPath, validMatchBody)

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		body := decode(t, rec)
		assert.ElementsMatch(t, []string{"matching_orders", "can_accept_more_orders", "max_additional_orders", "direction_filter_applied"}, keys(body))
		assert.Equal(t, true, body["can_accept_more_orders"])
		assert.EqualValues(t, 3, body["max_additional_orders"])
		assert.Equal(t, true, body["direction_filter_applied"])

		orders := body["matching_orders"].([]any)
		require.Len(t, orders, 2)
		first := orders[0].(map[string]any)
		assert.ElementsMatch(t, []string{"order_id", "pickup_location", "distance_from_rider_km", "angle_from_rider_degrees", "order_age_minutes", "estimated_earnings_kes"}, keys(first))
		assert.Equal(t, "ord_a", first["order_id"])
		assert.Equal(t, 0.25, first["distance_from_rider_km"], "250.2 m rounds to 0.25 km")
		assert.Equal(t, 0.0, first["angle_from_rider_degrees"])
		assert.EqualValues(t, 12, first["order_age_minutes"], "12m40s floors to a whole 12")
		assert.Equal(t, 117.0, first["estimated_earnings_kes"], "rider_earnings_cents shown as KES")
		assert.Equal(t, map[string]any{"lat": -1.2635 + 0.00225, "lng": 36.8030}, first["pickup_location"])
		second := orders[1].(map[string]any)
		assert.Equal(t, "ord_b", second["order_id"])
		assert.Equal(t, 0.5, second["distance_from_rider_km"])
		assert.EqualValues(t, 180, second["order_age_minutes"])
		assert.Equal(t, 250.5, second["estimated_earnings_kes"])
	})

	t.Run("angle is rounded to one decimal and is absolute", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetRider(gomock.Any(), gomock.Any()).Return(matchRiderRow(0, 5, true), nil)
		store.EXPECT().UpdateRiderTelemetry(gomock.Any(), gomock.Any()).Return(nil)
		// ~12.5 degrees west of north (atan(0.001/0.0045)) (a negative signed angle): 0.0045 lat, -0.001 lng
		row := pendingRow("ord_w", 0.0045, time.Minute, 100)
		row.PickupLng = 36.8030 - 0.001
		store.EXPECT().ListPendingOrdersInBox(gomock.Any(), gomock.Any()).Return([]sqlc.ListPendingOrdersInBoxRow{row}, nil)

		rec := send(t, h, http.MethodPost, matchPath, validMatchBody)

		require.Equal(t, http.StatusOK, rec.Code)
		angle := decode(t, rec)["matching_orders"].([]any)[0].(map[string]any)["angle_from_rider_degrees"].(float64)
		assert.InDelta(t, 12.5, angle, 0.051)
		assert.Equal(t, angle, float64(int(angle*10+0.5))/10, "exactly one decimal")
	})

	t.Run("unknown bearing and speed: full circle, angle null", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetRider(gomock.Any(), gomock.Any()).Return(matchRiderRow(0, 5, true), nil)
		store.EXPECT().UpdateRiderTelemetry(gomock.Any(), gomock.Any()).Return(nil)
		store.EXPECT().ListPendingOrdersInBox(gomock.Any(), gomock.Any()).Return([]sqlc.ListPendingOrdersInBoxRow{
			pendingRow("ord_behind", -0.002, time.Minute, 100),
		}, nil)

		for _, body := range []string{
			`{"rider_id":"rider_001","current_location":{"latitude":-1.2635,"longitude":36.8030},"current_orders_count":0}`,
		} {
			rec := send(t, h, http.MethodPost, matchPath, body)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			out := decode(t, rec)
			assert.Equal(t, false, out["direction_filter_applied"])
			order := out["matching_orders"].([]any)[0].(map[string]any)
			assert.Contains(t, order, "angle_from_rider_degrees")
			assert.Nil(t, order["angle_from_rider_degrees"], "null, not 0, when the cone was not applied")
		}
	})

	t.Run("explicit nulls for bearing and speed mean unknown", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetRider(gomock.Any(), gomock.Any()).Return(matchRiderRow(0, 5, true), nil)
		store.EXPECT().UpdateRiderTelemetry(gomock.Any(), gomock.Any()).Return(nil)
		store.EXPECT().ListPendingOrdersInBox(gomock.Any(), gomock.Any()).Return(nil, nil)

		rec := send(t, h, http.MethodPost, matchPath,
			`{"rider_id":"rider_001","current_location":{"latitude":-1.2635,"longitude":36.8030},"bearing_degrees":null,"speed_kmh":null,"current_orders_count":0}`)

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, false, decode(t, rec)["direction_filter_applied"])
	})

	t.Run("no pending orders gives an empty array", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetRider(gomock.Any(), gomock.Any()).Return(matchRiderRow(2, 5, true), nil)
		store.EXPECT().UpdateRiderTelemetry(gomock.Any(), gomock.Any()).Return(nil)
		store.EXPECT().ListPendingOrdersInBox(gomock.Any(), gomock.Any()).Return(nil, nil)

		rec := send(t, h, http.MethodPost, matchPath, validMatchBody)

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `"matching_orders":[]`)
		assert.True(t, decode(t, rec)["can_accept_more_orders"].(bool))
	})

	t.Run("rider at 5/5 gets [] and can_accept_more_orders false, with no query", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetRider(gomock.Any(), gomock.Any()).Return(matchRiderRow(5, 5, true), nil)
		store.EXPECT().UpdateRiderTelemetry(gomock.Any(), gomock.Any()).Return(nil)

		rec := send(t, h, http.MethodPost, matchPath,
			`{"rider_id":"rider_003","current_location":{"latitude":-1.2635,"longitude":36.8030},"bearing_degrees":0,"speed_kmh":25,"current_orders_count":5}`)

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), `"matching_orders":[]`)
		body := decode(t, rec)
		assert.Equal(t, false, body["can_accept_more_orders"])
		assert.EqualValues(t, 0, body["max_additional_orders"])
	})

	t.Run("unavailable rider gets []", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetRider(gomock.Any(), gomock.Any()).Return(matchRiderRow(0, 5, false), nil)
		store.EXPECT().UpdateRiderTelemetry(gomock.Any(), gomock.Any()).Return(nil)

		rec := send(t, h, http.MethodPost, matchPath, validMatchBody)

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `"matching_orders":[]`)
		assert.Equal(t, false, decode(t, rec)["can_accept_more_orders"])
	})

	t.Run("a wrong count in the request is not an error", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetRider(gomock.Any(), gomock.Any()).Return(matchRiderRow(4, 5, true), nil)
		store.EXPECT().UpdateRiderTelemetry(gomock.Any(), gomock.Any()).Return(nil)
		store.EXPECT().ListPendingOrdersInBox(gomock.Any(), gomock.Any()).Return(nil, nil)

		rec := send(t, h, http.MethodPost, matchPath, validMatchBody) // says 2, server says 4

		require.Equal(t, http.StatusOK, rec.Code)
		assert.EqualValues(t, 1, decode(t, rec)["max_additional_orders"], "the server's count wins")
	})
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestMatchOrders_400(t *testing.T) {
	tests := []struct{ name, body string }{
		{"empty body", ``},
		{"bad json", `{"rider_id":`},
		{"unknown field", `{"rider_id":"rider_001","altitude":4}`},
		{"wrong type", `{"rider_id":7}`},
		{"string speed", `{"rider_id":"r","current_location":{"latitude":-1,"longitude":36},"speed_kmh":"fast","current_orders_count":0}`},
		{"two documents", validMatchBody + validMatchBody},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newStoreHandler(t)

			rec := send(t, h, http.MethodPost, matchPath, tc.body)

			assert.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}
}

func TestMatchOrders_404(t *testing.T) {
	h, store := newStoreHandler(t)
	store.EXPECT().GetRider(gomock.Any(), "rider_999").Return(sqlc.Rider{}, domain.ErrNotFound)

	rec := send(t, h, http.MethodPost, matchPath,
		`{"rider_id":"rider_999","current_location":{"latitude":-1.2635,"longitude":36.8030},"current_orders_count":0}`)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestMatchOrders_422(t *testing.T) {
	loc := `"current_location":{"latitude":-1.2635,"longitude":36.8030}`
	tests := []struct{ name, body, wantField string }{
		{"latitude too high", `{"rider_id":"r",` + strings.Replace(loc, "-1.2635", "91", 1) + `,"current_orders_count":0}`, "current_location.latitude"},
		{"latitude too low", `{"rider_id":"r",` + strings.Replace(loc, "-1.2635", "-90.5", 1) + `,"current_orders_count":0}`, "current_location.latitude"},
		{"longitude too high", `{"rider_id":"r",` + strings.Replace(loc, "36.8030", "181", 1) + `,"current_orders_count":0}`, "current_location.longitude"},
		{"missing latitude", `{"rider_id":"r","current_location":{"longitude":36.8},"current_orders_count":0}`, "current_location.latitude"},
		{"missing longitude", `{"rider_id":"r","current_location":{"latitude":-1.2},"current_orders_count":0}`, "current_location.longitude"},
		{"missing location", `{"rider_id":"r","current_orders_count":0}`, "current_location"},
		{"bearing 360", `{"rider_id":"r",` + loc + `,"bearing_degrees":360,"current_orders_count":0}`, "bearing_degrees"},
		{"negative bearing", `{"rider_id":"r",` + loc + `,"bearing_degrees":-0.1,"current_orders_count":0}`, "bearing_degrees"},
		{"negative speed", `{"rider_id":"r",` + loc + `,"speed_kmh":-1,"current_orders_count":0}`, "speed_kmh"},
		{"speed above 200", `{"rider_id":"r",` + loc + `,"speed_kmh":200.5,"current_orders_count":0}`, "speed_kmh"},
		{"missing rider_id", `{` + loc + `,"current_orders_count":0}`, "rider_id"},
		{"blank rider_id", `{"rider_id":"   ",` + loc + `,"current_orders_count":0}`, "rider_id"},
		{"count above 5", `{"rider_id":"r",` + loc + `,"current_orders_count":6}`, "current_orders_count"},
		{"negative count", `{"rider_id":"r",` + loc + `,"current_orders_count":-1}`, "current_orders_count"},
		{"missing count", `{"rider_id":"r",` + loc + `}`, "current_orders_count"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newStoreHandler(t) // no store call may happen

			rec := send(t, h, http.MethodPost, matchPath, tc.body)

			require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
			assert.Contains(t, errFields(t, rec), tc.wantField)
		})
	}

	t.Run("boundary values are accepted", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetRider(gomock.Any(), gomock.Any()).Return(matchRiderRow(5, 5, true), nil)
		store.EXPECT().UpdateRiderTelemetry(gomock.Any(), gomock.Any()).Return(nil)

		rec := send(t, h, http.MethodPost, matchPath,
			`{"rider_id":"r","current_location":{"latitude":-90,"longitude":180},"bearing_degrees":359.99,"speed_kmh":200,"current_orders_count":5}`)

		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})
}

func TestMatchOrders_500HidesInternals(t *testing.T) {
	h, store := newStoreHandler(t)
	store.EXPECT().GetRider(gomock.Any(), gomock.Any()).Return(matchRiderRow(0, 5, true), nil)
	store.EXPECT().UpdateRiderTelemetry(gomock.Any(), gomock.Any()).Return(errors.New("pq: relation riders is locked on 10.0.0.5"))

	rec := send(t, h, http.MethodPost, matchPath, validMatchBody)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), "10.0.0.5")
}

// A bearing with no speed is trusted: the cone applies (a null bearing is what disables it).
func TestMatchOrders_BearingWithoutSpeedUsesCone(t *testing.T) {
	h, store := newStoreHandler(t)
	store.EXPECT().GetRider(gomock.Any(), gomock.Any()).Return(matchRiderRow(0, 5, true), nil)
	store.EXPECT().UpdateRiderTelemetry(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().ListPendingOrdersInBox(gomock.Any(), gomock.Any()).Return([]sqlc.ListPendingOrdersInBoxRow{
		pendingRow("ord_ahead", 0.002, time.Minute, 100), pendingRow("ord_behind", -0.002, time.Minute, 100),
	}, nil)

	rec := send(t, h, http.MethodPost, matchPath,
		`{"rider_id":"rider_001","current_location":{"latitude":-1.2635,"longitude":36.8030},"bearing_degrees":0,"current_orders_count":0}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decode(t, rec)
	assert.Equal(t, true, body["direction_filter_applied"])
	orders := body["matching_orders"].([]any)
	require.Len(t, orders, 1)
	assert.Equal(t, "ord_ahead", orders[0].(map[string]any)["order_id"])
}
