package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/repository"
	"github.com/codercollo/zemi-fleet/internal/repository/sqlc"
)

const batchPath = "/api/v2/orders/batch"

const validBatchBody = `{"rider_id":"rider_001","rider_location":{"latitude":-1.2650,"longitude":36.8038},"max_orders":5,"radius_km":2}`

func twoOrderBatch() domain.Batch {
	west := domain.Location{Lat: -1.2635, Lng: 36.8030}
	return domain.Batch{
		ID: "batch_X1", Status: domain.BatchOpen,
		PickupCentroid: west, TotalDistanceM: 6543, EstimatedEarningsCents: 31500, RouteOptimizationScore: 0.874,
		ExpiresAt: testNow.Add(10 * time.Minute),
		Orders: []domain.BatchOrder{
			{ID: "ord_1", Pickup: west, Delivery: domain.Location{Lat: -1.29, Lng: 36.79}, TripDistanceM: 3282, CreatedAt: testNow.Add(-time.Hour), Seq: 1},
			{ID: "ord_2", Pickup: west, Delivery: domain.Location{Lat: -1.30, Lng: 36.80}, TripDistanceM: 4100, CreatedAt: testNow.Add(-time.Minute), Seq: 2},
		},
		Stops: []domain.BatchStop{
			{Seq: 1, Type: domain.StopPickup, OrderID: "ord_1", Location: west, LegDistanceM: 0, ETAOffsetS: 0},
			{Seq: 2, Type: domain.StopPickup, OrderID: "ord_2", Location: west, LegDistanceM: 0, ETAOffsetS: 120},
			{Seq: 3, Type: domain.StopDelivery, OrderID: "ord_1", Location: domain.Location{Lat: -1.29, Lng: 36.79}, LegDistanceM: 3282, ETAOffsetS: 1130}, // 18.83 min
			{Seq: 4, Type: domain.StopDelivery, OrderID: "ord_2", Location: domain.Location{Lat: -1.30, Lng: 36.80}, LegDistanceM: 3261, ETAOffsetS: 1500}, // 25 min
		},
	}
}

func TestCreateBatches_200(t *testing.T) {
	t.Run("brief's shape plus sequence, distance_to_first_pickup_km and expires_at", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetRider(gomock.Any(), "rider_001").Return(matchRiderRow(1, 5, true), nil)
		store.EXPECT().CreateBatchesTx(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, p repository.CreateBatchesParams) ([]domain.Batch, error) {
				assert.Equal(t, 4, p.Batching.MaxOrders, "min(5 asked, 5-1 spare)")
				assert.Equal(t, 2000.0, p.Batching.PickupRadiusM)
				return []domain.Batch{twoOrderBatch()}, nil
			})

		rec := send(t, h, http.MethodPost, batchPath, validBatchBody)

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		body := decode(t, rec)
		assert.Equal(t, []string{"batches"}, keys(body))
		batches := body["batches"].([]any)
		require.Len(t, batches, 1)
		b := batches[0].(map[string]any)
		assert.ElementsMatch(t, []string{"batch_id", "orders", "sequence", "total_distance_km", "distance_saved_km", "estimated_earnings_kes",
			"route_optimization_score", "distance_to_first_pickup_km", "expires_at"}, keys(b))
		assert.Equal(t, "batch_X1", b["batch_id"])
		assert.Equal(t, 6.54, b["total_distance_km"])
		assert.Equal(t, 0.84, b["distance_saved_km"], "solo trips 3.282 + 4.100 km minus the 6.543 km route")
		assert.Equal(t, 315.0, b["estimated_earnings_kes"])
		assert.Equal(t, 0.874, b["route_optimization_score"])
		assert.Equal(t, 0.19, b["distance_to_first_pickup_km"], "rider at -1.2650,36.8038 to the first pickup")
		assert.Equal(t, "2026-10-13T09:10:00Z", b["expires_at"])

		orders := b["orders"].([]any)
		require.Len(t, orders, 2)
		o1 := orders[0].(map[string]any)
		assert.ElementsMatch(t, []string{"order_id", "pickup", "delivery", "distance_km", "estimated_time_min"}, keys(o1))
		assert.Equal(t, "ord_1", o1["order_id"])
		assert.Equal(t, 3.28, o1["distance_km"])
		assert.EqualValues(t, 19, o1["estimated_time_min"], "1130 s = 18.83 min, rounded up")
		assert.EqualValues(t, 25, orders[1].(map[string]any)["estimated_time_min"])
		assert.Equal(t, map[string]any{"lat": -1.29, "lng": 36.79}, o1["delivery"])

		seq := b["sequence"].([]any)
		require.Len(t, seq, 4)
		first := seq[0].(map[string]any)
		assert.ElementsMatch(t, []string{"seq", "type", "order_id", "location"}, keys(first))
		assert.EqualValues(t, 1, first["seq"])
		assert.Equal(t, "pickup", first["type"])
		assert.Equal(t, "delivery", seq[2].(map[string]any)["type"])
	})

	t.Run("keys appear in the brief's order", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetRider(gomock.Any(), gomock.Any()).Return(matchRiderRow(0, 5, true), nil)
		store.EXPECT().CreateBatchesTx(gomock.Any(), gomock.Any()).Return([]domain.Batch{twoOrderBatch()}, nil)

		rec := send(t, h, http.MethodPost, batchPath, validBatchBody)

		assertInOrder(t, rec.Body.String(), `"batch_id"`, `"orders"`, `"sequence"`, `"total_distance_km"`,
			`"distance_saved_km"`, `"estimated_earnings_kes"`, `"route_optimization_score"`, `"distance_to_first_pickup_km"`, `"expires_at"`)
		assertInOrder(t, rec.Body.String(), `"order_id"`, `"pickup"`, `"delivery"`, `"distance_km"`, `"estimated_time_min"`)
	})

	t.Run("no batches is an empty array", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetRider(gomock.Any(), gomock.Any()).Return(matchRiderRow(0, 5, true), nil)
		store.EXPECT().CreateBatchesTx(gomock.Any(), gomock.Any()).Return(nil, nil)

		rec := send(t, h, http.MethodPost, batchPath, validBatchBody)

		require.Equal(t, http.StatusOK, rec.Code)
		assert.JSONEq(t, `{"batches":[]}`, rec.Body.String())
	})

	t.Run("rider at capacity or off shift: [] and no transaction", func(t *testing.T) {
		for name, row := range map[string]sqlc.Rider{"5/5": matchRiderRow(5, 5, true), "off shift": matchRiderRow(0, 5, false)} {
			t.Run(name, func(t *testing.T) {
				h, store := newStoreHandler(t)
				store.EXPECT().GetRider(gomock.Any(), gomock.Any()).Return(row, nil)

				rec := send(t, h, http.MethodPost, batchPath, validBatchBody)

				require.Equal(t, http.StatusOK, rec.Code)
				assert.JSONEq(t, `{"batches":[]}`, rec.Body.String())
			})
		}
	})
}

func assertInOrder(t *testing.T, body string, needles ...string) {
	t.Helper()
	prev := -1
	for _, n := range needles {
		i := strings.Index(body, n)
		require.GreaterOrEqual(t, i, 0, "%s missing from %s", n, body)
		require.Greater(t, i, prev, "%s is out of order in %s", n, body)
		prev = i
	}
}

func TestCreateBatches_400(t *testing.T) {
	for _, body := range []string{``, `{"rider_id":`, `{"rider_id":"r","colour":1}`, `{"rider_id":7}`, `{"max_orders":"five"}`, validBatchBody + validBatchBody} {
		h, _ := newStoreHandler(t)

		rec := send(t, h, http.MethodPost, batchPath, body)

		assert.Equal(t, http.StatusBadRequest, rec.Code, body)
	}
}

func TestCreateBatches_404(t *testing.T) {
	h, store := newStoreHandler(t)
	store.EXPECT().GetRider(gomock.Any(), "rider_999").Return(sqlc.Rider{}, domain.ErrNotFound)

	rec := send(t, h, http.MethodPost, batchPath,
		`{"rider_id":"rider_999","rider_location":{"latitude":-1.26,"longitude":36.8},"max_orders":5,"radius_km":2}`)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestCreateBatches_422(t *testing.T) {
	loc := `"rider_location":{"latitude":-1.26,"longitude":36.8}`
	tests := []struct{ name, body, wantField string }{
		{"max_orders 6", `{"rider_id":"r",` + loc + `,"max_orders":6,"radius_km":2}`, "max_orders"},
		{"max_orders 0", `{"rider_id":"r",` + loc + `,"max_orders":0,"radius_km":2}`, "max_orders"},
		{"max_orders negative", `{"rider_id":"r",` + loc + `,"max_orders":-1,"radius_km":2}`, "max_orders"},
		{"max_orders missing", `{"rider_id":"r",` + loc + `,"radius_km":2}`, "max_orders"},
		{"radius 0", `{"rider_id":"r",` + loc + `,"max_orders":5,"radius_km":0}`, "radius_km"},
		{"radius negative", `{"rider_id":"r",` + loc + `,"max_orders":5,"radius_km":-1}`, "radius_km"},
		{"radius above the configured 2 km", `{"rider_id":"r",` + loc + `,"max_orders":5,"radius_km":2.5}`, "radius_km"},
		{"radius missing", `{"rider_id":"r",` + loc + `,"max_orders":5}`, "radius_km"},
		{"rider_id missing", `{` + loc + `,"max_orders":5,"radius_km":2}`, "rider_id"},
		{"location missing", `{"rider_id":"r","max_orders":5,"radius_km":2}`, "rider_location"},
		{"latitude out of range", `{"rider_id":"r","rider_location":{"latitude":91,"longitude":36.8},"max_orders":5,"radius_km":2}`, "rider_location.latitude"},
		{"longitude out of range", `{"rider_id":"r","rider_location":{"latitude":-1.2,"longitude":-181},"max_orders":5,"radius_km":2}`, "rider_location.longitude"},
		{"longitude missing", `{"rider_id":"r","rider_location":{"latitude":-1.2},"max_orders":5,"radius_km":2}`, "rider_location.longitude"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newStoreHandler(t) // no store call may happen

			rec := send(t, h, http.MethodPost, batchPath, tc.body)

			require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
			assert.Contains(t, errFields(t, rec), tc.wantField)
		})
	}

	t.Run("boundaries 1 and the configured maximum are accepted", func(t *testing.T) {
		for _, body := range []string{
			`{"rider_id":"r",` + loc + `,"max_orders":1,"radius_km":0.01}`,
			`{"rider_id":"r",` + loc + `,"max_orders":5,"radius_km":2}`,
		} {
			h, store := newStoreHandler(t)
			store.EXPECT().GetRider(gomock.Any(), gomock.Any()).Return(matchRiderRow(5, 5, true), nil)

			rec := send(t, h, http.MethodPost, batchPath, body)

			assert.Equal(t, http.StatusOK, rec.Code, body)
		}
	})
}

func TestCreateBatches_500HidesInternals(t *testing.T) {
	h, store := newStoreHandler(t)
	store.EXPECT().GetRider(gomock.Any(), gomock.Any()).Return(matchRiderRow(0, 5, true), nil)
	store.EXPECT().CreateBatchesTx(gomock.Any(), gomock.Any()).Return(nil, errors.New("deadlock detected on 10.0.0.5"))

	rec := send(t, h, http.MethodPost, batchPath, validBatchBody)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), "10.0.0.5")
}

// match-orders writes its keys in the brief's order (a map would sort them alphabetically).
func TestMatchOrders_KeyOrder(t *testing.T) {
	h, store := newStoreHandler(t)
	store.EXPECT().GetRider(gomock.Any(), gomock.Any()).Return(matchRiderRow(0, 5, true), nil)
	store.EXPECT().UpdateRiderTelemetry(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().ListPendingOrdersInBox(gomock.Any(), gomock.Any()).Return(
		[]sqlc.ListPendingOrdersInBoxRow{pendingRow("ord_a", 0.002, time.Minute, 100)}, nil)

	rec := send(t, h, http.MethodPost, matchPath, validMatchBody)

	require.Equal(t, http.StatusOK, rec.Code)
	assertInOrder(t, rec.Body.String(), `"matching_orders"`, `"can_accept_more_orders"`, `"max_additional_orders"`, `"direction_filter_applied"`)
	assertInOrder(t, rec.Body.String(), `"order_id"`, `"pickup_location"`, `"distance_from_rider_km"`,
		`"angle_from_rider_degrees"`, `"order_age_minutes"`, `"estimated_earnings_kes"`)
}
