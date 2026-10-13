package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-faker/faker/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/codercollo/zemi-fleet/internal/clock"
	"github.com/codercollo/zemi-fleet/internal/config"
	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/mock"
	"github.com/codercollo/zemi-fleet/internal/repository/sqlc"
	"github.com/codercollo/zemi-fleet/internal/service"
)

const testPayoutBPS = 9000

// testNow is the fixed instant every handler test runs at.
var testNow = time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)

// testBatchCfg mirrors the default BATCH_* settings.
var testBatchCfg = config.BatchConfig{MaxOrders: 5, PickupRadiusKM: 2, DirectionToleranceDeg: 45, SearchRadiusKM: 5, TTL: 10 * time.Minute, AvgSpeedKMH: 20, StopDwellMin: 2}

// testCone mirrors the default CONE_* settings.
var testCone = config.ConeConfig{RadiusKM: 1, HalfAngleDeg: 30, MinSpeedKMH: 3, MaxResults: 20}

// newStoreHandler builds the real router and services on top of a gomock Store.
func newStoreHandler(t *testing.T) (http.Handler, *mock.MockStore) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	store := mock.NewMockStore(gomock.NewController(t))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := NewServer(config.Config{Env: config.EnvDevelopment, RiderPayoutBPS: testPayoutBPS, Batch: testBatchCfg}, logger,
		fakeDB{}, service.NewOrderService(store, clock.NewTest(testNow), testPayoutBPS), service.NewRiderService(store, clock.NewTest(testNow)),
		service.NewMatchingService(store, clock.NewTest(testNow), logger, testCone),
		service.NewBatchService(store, clock.NewTest(testNow), testBatchCfg), "test")
	return srv.Routes(), store
}

// send performs a request; headers are name, value pairs.
func send(t *testing.T, h http.Handler, method, path, body string, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	return out
}

// errFields returns the per-field map of a 422 response.
func errFields(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	fields, ok := decode(t, rec)["error"].(map[string]any)
	require.True(t, ok, "error should be an object: %s", rec.Body.String())
	return fields
}

func fakeVendor() sqlc.Vendor {
	return sqlc.Vendor{ID: "ven_" + faker.Word(), Name: faker.Name(), Phone: "+254711000001", Lat: -1.2635, Lng: 36.8030}
}

func orderRowFrom(p sqlc.CreateOrderParams) sqlc.Order {
	earnings := p.DeliveryFeeCents * int64(p.RiderPayoutBps) / 10000
	platform := p.DeliveryFeeCents - earnings
	return sqlc.Order{
		ID: p.ID, VendorID: p.VendorID, CategoryCode: p.CategoryCode, Status: sqlc.OrderStatusPending,
		PickupLat: p.PickupLat, PickupLng: p.PickupLng, DeliveryLat: p.DeliveryLat, DeliveryLng: p.DeliveryLng,
		TripDistanceM: p.TripDistanceM, TripBearingDeg: p.TripBearingDeg,
		ValueCents: p.ValueCents, DeliveryFeeCents: p.DeliveryFeeCents, RiderPayoutBps: p.RiderPayoutBps,
		RiderEarningsCents: &earnings, PlatformFeeCents: &platform,
		CreatedAt: time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC),
		Version: 1,
	}
}

func TestCreateOrder(t *testing.T) {
	validBody := `{"vendor_id":"ven_001","category":"electronics","delivery_location":{"lat":-1.2905,"lng":36.7870},
		"value_kes":4500.50,"delivery_fee_kes":250}`

	t.Run("201 with Location header and KES amounts", func(t *testing.T) {
		h, store := newStoreHandler(t)
		vendor := fakeVendor()
		store.EXPECT().GetVendor(gomock.Any(), "ven_001").Return(vendor, nil)
		store.EXPECT().GetCategory(gomock.Any(), "electronics").Return(sqlc.Category{Code: "electronics"}, nil)
		store.EXPECT().CreateOrder(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, p sqlc.CreateOrderParams) (sqlc.Order, error) {
				assert.Equal(t, vendor.Lat, p.PickupLat, "pickup is snapshotted from the vendor")
				assert.EqualValues(t, 450050, p.ValueCents)
				assert.EqualValues(t, 25000, p.DeliveryFeeCents)
				assert.EqualValues(t, testPayoutBPS, p.RiderPayoutBps)
				assert.True(t, p.CreatedAt.Equal(testNow), "created_at comes from the injected clock, not SQL now()")
				assert.Equal(t, time.UTC, p.CreatedAt.Location())
				return orderRowFrom(p), nil
			})

		rec := send(t, h, http.MethodPost, "/api/v2/orders", validBody)

		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		order := decode(t, rec)["order"].(map[string]any)
		assert.Equal(t, "/api/v2/orders/"+order["id"].(string), rec.Header().Get("Location"))
		assert.Equal(t, "pending", order["status"])
		assert.Equal(t, "2026-10-13T09:00:00Z", order["created_at"], "RFC 3339 in UTC")
		assert.InDelta(t, 4500.50, order["value_kes"], 0.001)
		assert.InDelta(t, 225.0, order["estimated_earnings_kes"], 0.001)
		assert.InDelta(t, 25.0, order["platform_fee_kes"], 0.001)
		assert.InDelta(t, 90.0, order["rider_payout_percent"], 0.001)
	})

	t.Run("400 for malformed input", func(t *testing.T) {
		tests := []struct{ name, body string }{
			{"empty body", ``},
			{"bad json", `{"vendor_id":`},
			{"unknown field", `{"vendor_id":"ven_001","colour":"red"}`},
			{"wrong type", `{"vendor_id":42}`},
			{"two documents", validBody + validBody},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				h, _ := newStoreHandler(t)

				rec := send(t, h, http.MethodPost, "/api/v2/orders", tc.body)

				assert.Equal(t, http.StatusBadRequest, rec.Code)
				assert.IsType(t, "", decode(t, rec)["error"])
			})
		}
	})

	t.Run("422 with per-field messages", func(t *testing.T) {
		tests := []struct {
			name      string
			body      string
			wantField string
		}{
			{"missing vendor", `{"category":"food","delivery_location":{"lat":-1.2,"lng":36.8},"value_kes":10,"delivery_fee_kes":5}`, "vendor_id"},
			{"missing category", `{"vendor_id":"ven_001","delivery_location":{"lat":-1.2,"lng":36.8},"value_kes":10,"delivery_fee_kes":5}`, "category"},
			{"missing location", `{"vendor_id":"ven_001","category":"food","value_kes":10,"delivery_fee_kes":5}`, "delivery_location"},
			{"lat too high", `{"vendor_id":"ven_001","category":"food","delivery_location":{"lat":91,"lng":36.8},"value_kes":10,"delivery_fee_kes":5}`, "delivery_location.lat"},
			{"lng too low", `{"vendor_id":"ven_001","category":"food","delivery_location":{"lat":-1.2,"lng":-181},"value_kes":10,"delivery_fee_kes":5}`, "delivery_location.lng"},
			{"lng missing", `{"vendor_id":"ven_001","category":"food","delivery_location":{"lat":-1.2},"value_kes":10,"delivery_fee_kes":5}`, "delivery_location.lng"},
			{"zero value", `{"vendor_id":"ven_001","category":"food","delivery_location":{"lat":-1.2,"lng":36.8},"value_kes":0,"delivery_fee_kes":5}`, "value_kes"},
			{"negative value", `{"vendor_id":"ven_001","category":"food","delivery_location":{"lat":-1.2,"lng":36.8},"value_kes":-3,"delivery_fee_kes":5}`, "value_kes"},
			{"fraction of a cent", `{"vendor_id":"ven_001","category":"food","delivery_location":{"lat":-1.2,"lng":36.8},"value_kes":10.005,"delivery_fee_kes":5}`, "value_kes"},
			{"negative fee", `{"vendor_id":"ven_001","category":"food","delivery_location":{"lat":-1.2,"lng":36.8},"value_kes":10,"delivery_fee_kes":-1}`, "delivery_fee_kes"},
			{"missing fee", `{"vendor_id":"ven_001","category":"food","delivery_location":{"lat":-1.2,"lng":36.8},"value_kes":10}`, "delivery_fee_kes"},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				h, _ := newStoreHandler(t) // no store calls expected: validation fails first

				rec := send(t, h, http.MethodPost, "/api/v2/orders", tc.body)

				require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
				assert.Contains(t, errFields(t, rec), tc.wantField)
			})
		}
	})

	t.Run("422 when vendor and category do not exist", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetVendor(gomock.Any(), "ven_001").Return(sqlc.Vendor{}, domain.ErrNotFound)
		store.EXPECT().GetCategory(gomock.Any(), "electronics").Return(sqlc.Category{}, domain.ErrNotFound)

		rec := send(t, h, http.MethodPost, "/api/v2/orders", validBody)

		require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		fields := errFields(t, rec)
		assert.Contains(t, fields, "vendor_id")
		assert.Contains(t, fields, "category")
	})

	t.Run("500 hides internals", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetVendor(gomock.Any(), "ven_001").Return(sqlc.Vendor{}, errors.New("connection refused to 10.0.0.5"))

		rec := send(t, h, http.MethodPost, "/api/v2/orders", validBody)

		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.NotContains(t, rec.Body.String(), "10.0.0.5")
	})

	t.Run("405 for other methods", func(t *testing.T) {
		h, _ := newStoreHandler(t)

		rec := send(t, h, http.MethodDelete, "/api/v2/orders", "")

		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})
}

func TestShowOrder(t *testing.T) {
	t.Run("200", func(t *testing.T) {
		h, store := newStoreHandler(t)
		row := orderRowFrom(sqlc.CreateOrderParams{
			ID: "ord_001", VendorID: "ven_001", CategoryCode: "food",
			TripDistanceM: 3500, TripBearingDeg: 123.4, ValueCents: 150000, DeliveryFeeCents: 20000, RiderPayoutBps: testPayoutBPS,
		})
		store.EXPECT().GetOrder(gomock.Any(), "ord_001").Return(row, nil)

		rec := send(t, h, http.MethodGet, "/api/v2/orders/ord_001", "")

		require.Equal(t, http.StatusOK, rec.Code)
		order := decode(t, rec)["order"].(map[string]any)
		assert.Equal(t, "ord_001", order["id"])
		assert.InDelta(t, 3.5, order["trip_distance_km"], 0.0001)
		assert.InDelta(t, 123.4, order["trip_bearing_degrees"], 0.01)
		assert.InDelta(t, 1500.0, order["value_kes"], 0.001)
		assert.InDelta(t, 180.0, order["estimated_earnings_kes"], 0.001)
		assert.Nil(t, order["batch_id"])
	})

	t.Run("404", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetOrder(gomock.Any(), "ord_nope").Return(sqlc.Order{}, domain.ErrNotFound)

		rec := send(t, h, http.MethodGet, "/api/v2/orders/ord_nope", "")

		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.Contains(t, rec.Body.String(), `"error"`)
	})

	t.Run("500", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetOrder(gomock.Any(), "ord_001").Return(sqlc.Order{}, errors.New("boom"))

		rec := send(t, h, http.MethodGet, "/api/v2/orders/ord_001", "")

		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})
}

func fakeRiderRow(id string) sqlc.Rider {
	lat, lng, bearing, speed, rate := -1.266, 36.806, 95.0, 22.0, 0.775
	return sqlc.Rider{
		ID: id, FullName: faker.Name(), Phone: "+254722000001", IsAvailable: true,
		MaxCapacity: 5, CurrentOrdersCount: 2,
		LastLat: &lat, LastLng: &lng, LastBearingDeg: &bearing, LastSpeedKmh: &speed,
		AcceptanceRate: &rate, Version: 3,
	}
}

func TestShowRider(t *testing.T) {
	t.Run("200 with preferences", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetRider(gomock.Any(), "rider_001").Return(fakeRiderRow("rider_001"), nil)
		store.EXPECT().GetPreferences(gomock.Any(), "rider_001").Return(sqlc.RiderPreference{RiderID: "rider_001", MinOrderValueCents: 50000}, nil)
		store.EXPECT().ListPreferredCategories(gomock.Any(), "rider_001").Return([]string{"clothing", "fragile"}, nil)
		store.EXPECT().ListPreferredAreas(gomock.Any(), "rider_001").Return([]string{"area_001"}, nil)

		rec := send(t, h, http.MethodGet, "/api/v2/riders/rider_001", "")

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		rider := decode(t, rec)["rider"].(map[string]any)
		assert.Equal(t, "rider_001", rider["id"])
		assert.EqualValues(t, 3, rider["max_additional_orders"])
		assert.InDelta(t, 95.0, rider["bearing_degrees"], 0.001)
		assert.InDelta(t, -1.266, rider["current_location"].(map[string]any)["lat"], 1e-9)
		prefs := rider["preferences"].(map[string]any)
		assert.InDelta(t, 500.0, prefs["min_order_value_kes"], 0.001)
		assert.Equal(t, []any{"clothing", "fragile"}, prefs["preferred_categories"])
		assert.Equal(t, []any{"area_001"}, prefs["preferred_areas"])
	})

	t.Run("200 with no preference row and no location", func(t *testing.T) {
		h, store := newStoreHandler(t)
		row := fakeRiderRow("rider_008")
		row.LastLat, row.LastLng, row.LastBearingDeg, row.LastSpeedKmh = nil, nil, nil, nil
		store.EXPECT().GetRider(gomock.Any(), "rider_008").Return(row, nil)
		store.EXPECT().GetPreferences(gomock.Any(), "rider_008").Return(sqlc.RiderPreference{}, domain.ErrNotFound)
		store.EXPECT().ListPreferredCategories(gomock.Any(), "rider_008").Return(nil, nil)
		store.EXPECT().ListPreferredAreas(gomock.Any(), "rider_008").Return(nil, nil)

		rec := send(t, h, http.MethodGet, "/api/v2/riders/rider_008", "")

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		rider := decode(t, rec)["rider"].(map[string]any)
		assert.Nil(t, rider["current_location"])
		assert.Nil(t, rider["bearing_degrees"])
		prefs := rider["preferences"].(map[string]any)
		assert.Equal(t, []any{}, prefs["preferred_categories"], "empty list, not null")
		assert.Equal(t, []any{}, prefs["preferred_areas"])
		assert.EqualValues(t, 0, prefs["min_order_value_kes"])
	})

	t.Run("404", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetRider(gomock.Any(), "rider_999").Return(sqlc.Rider{}, domain.ErrNotFound)

		rec := send(t, h, http.MethodGet, "/api/v2/riders/rider_999", "")

		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("500", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetRider(gomock.Any(), "rider_001").Return(sqlc.Rider{}, errors.New("boom"))

		rec := send(t, h, http.MethodGet, "/api/v2/riders/rider_001", "")

		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})
}

// expectTx makes the mock Store run the transaction body against itself, standing in for a real tx.
func expectTx(store *mock.MockStore) {
	store.EXPECT().ExecTx(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, fn func(sqlc.Querier) error) error { return fn(store) })
}

func TestUpdatePreferences(t *testing.T) {
	body := `{"min_order_value_kes":750,"preferred_categories":["food","groceries"],"preferred_areas":["area_001","area_002"]}`

	t.Run("200 replaces categories and areas in one transaction", func(t *testing.T) {
		h, store := newStoreHandler(t)
		ctx := gomock.Any()
		bumped := fakeRiderRow("rider_001")
		bumped.Version = 4
		store.EXPECT().GetRider(ctx, "rider_001").Return(fakeRiderRow("rider_001"), nil)
		store.EXPECT().GetCategoriesByCodes(ctx, []string{"food", "groceries"}).
			Return([]sqlc.Category{{Code: "food"}, {Code: "groceries"}}, nil)
		store.EXPECT().GetAreasByIDs(ctx, []string{"area_001", "area_002"}).
			Return([]sqlc.Area{{ID: "area_001"}, {ID: "area_002"}}, nil)
		expectTx(store)
		gomock.InOrder(
			store.EXPECT().GetRiderForUpdate(ctx, "rider_001").Return(fakeRiderRow("rider_001"), nil),
			store.EXPECT().UpsertPreferences(ctx, sqlc.UpsertPreferencesParams{RiderID: "rider_001", MinOrderValueCents: 75000}).
				Return(sqlc.RiderPreference{}, nil),
			store.EXPECT().DeletePreferredCategories(ctx, "rider_001").Return(nil),
			store.EXPECT().DeletePreferredAreas(ctx, "rider_001").Return(nil),
			store.EXPECT().InsertPreferredCategories(ctx, sqlc.InsertPreferredCategoriesParams{
				RiderID: "rider_001", CategoryCodes: []string{"food", "groceries"}}).Return(nil),
			store.EXPECT().InsertPreferredAreas(ctx, sqlc.InsertPreferredAreasParams{
				RiderID: "rider_001", AreaIds: []string{"area_001", "area_002"}}).Return(nil),
			store.EXPECT().BumpRiderVersion(ctx, sqlc.BumpRiderVersionParams{ID: "rider_001", Version: 3}).Return(bumped, nil),
		)
		// The response re-reads the stored preferences.
		store.EXPECT().GetPreferences(ctx, "rider_001").Return(sqlc.RiderPreference{MinOrderValueCents: 75000}, nil)
		store.EXPECT().ListPreferredCategories(ctx, "rider_001").Return([]string{"food", "groceries"}, nil)
		store.EXPECT().ListPreferredAreas(ctx, "rider_001").Return([]string{"area_001", "area_002"}, nil)

		rec := send(t, h, http.MethodPut, "/api/v2/riders/rider_001/preferences", body)

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		rider := decode(t, rec)["rider"].(map[string]any)
		assert.EqualValues(t, 4, rider["version"], "preferences changes bump the rider version")
		prefs := rider["preferences"].(map[string]any)
		assert.InDelta(t, 750.0, prefs["min_order_value_kes"], 0.001)
		assert.Equal(t, []any{"food", "groceries"}, prefs["preferred_categories"])
	})

	t.Run("200 with empty lists clears preferences without inserting", func(t *testing.T) {
		h, store := newStoreHandler(t)
		ctx := gomock.Any()
		store.EXPECT().GetRider(ctx, "rider_001").Return(fakeRiderRow("rider_001"), nil)
		expectTx(store)
		store.EXPECT().GetRiderForUpdate(ctx, "rider_001").Return(fakeRiderRow("rider_001"), nil)
		store.EXPECT().UpsertPreferences(ctx, sqlc.UpsertPreferencesParams{RiderID: "rider_001"}).Return(sqlc.RiderPreference{}, nil)
		store.EXPECT().DeletePreferredCategories(ctx, "rider_001").Return(nil)
		store.EXPECT().DeletePreferredAreas(ctx, "rider_001").Return(nil)
		store.EXPECT().BumpRiderVersion(ctx, gomock.Any()).Return(fakeRiderRow("rider_001"), nil)
		store.EXPECT().GetPreferences(ctx, "rider_001").Return(sqlc.RiderPreference{}, nil)
		store.EXPECT().ListPreferredCategories(ctx, "rider_001").Return(nil, nil)
		store.EXPECT().ListPreferredAreas(ctx, "rider_001").Return(nil, nil)

		rec := send(t, h, http.MethodPut, "/api/v2/riders/rider_001/preferences", `{}`)

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, []any{}, decode(t, rec)["rider"].(map[string]any)["preferences"].(map[string]any)["preferred_areas"])
	})

	t.Run("400 for malformed input", func(t *testing.T) {
		for _, b := range []string{``, `[`, `{"preferred_categories":"food"}`, `{"nope":1}`} {
			h, _ := newStoreHandler(t)

			rec := send(t, h, http.MethodPut, "/api/v2/riders/rider_001/preferences", b)

			assert.Equal(t, http.StatusBadRequest, rec.Code, b)
		}
	})

	t.Run("422 for invalid shape", func(t *testing.T) {
		tests := []struct{ name, body, wantField string }{
			{"negative minimum", `{"min_order_value_kes":-1}`, "min_order_value_kes"},
			{"fraction of a cent", `{"min_order_value_kes":1.234}`, "min_order_value_kes"},
			{"duplicate category", `{"preferred_categories":["food","food"]}`, "preferred_categories"},
			{"blank area", `{"preferred_areas":[""]}`, "preferred_areas"},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				h, _ := newStoreHandler(t)

				rec := send(t, h, http.MethodPut, "/api/v2/riders/rider_001/preferences", tc.body)

				require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
				assert.Contains(t, errFields(t, rec), tc.wantField)
			})
		}
	})

	t.Run("422 for unknown category and area IDs", func(t *testing.T) {
		h, store := newStoreHandler(t)
		ctx := gomock.Any()
		store.EXPECT().GetRider(ctx, "rider_001").Return(fakeRiderRow("rider_001"), nil)
		store.EXPECT().GetCategoriesByCodes(ctx, []string{"food", "pizza"}).Return([]sqlc.Category{{Code: "food"}}, nil)
		store.EXPECT().GetAreasByIDs(ctx, []string{"area_zzz"}).Return(nil, nil)

		rec := send(t, h, http.MethodPut, "/api/v2/riders/rider_001/preferences",
			`{"preferred_categories":["food","pizza"],"preferred_areas":["area_zzz"]}`)

		require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		fields := errFields(t, rec)
		assert.Equal(t, "unknown categories: pizza", fields["preferred_categories"])
		assert.Equal(t, "unknown areas: area_zzz", fields["preferred_areas"])
	})

	t.Run("404 for unknown rider", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetRider(gomock.Any(), "rider_999").Return(sqlc.Rider{}, domain.ErrNotFound)

		rec := send(t, h, http.MethodPut, "/api/v2/riders/rider_999/preferences", body)

		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("500 when the transaction fails", func(t *testing.T) {
		h, store := newStoreHandler(t)
		ctx := gomock.Any()
		store.EXPECT().GetRider(ctx, "rider_001").Return(fakeRiderRow("rider_001"), nil)
		store.EXPECT().ExecTx(ctx, gomock.Any()).Return(fmt.Errorf("commit tx: %w", errors.New("deadlock")))

		rec := send(t, h, http.MethodPut, "/api/v2/riders/rider_001/preferences", `{}`)

		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.NotContains(t, rec.Body.String(), "deadlock")
	})
}

func TestHandleServiceError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"not found", fmt.Errorf("wrapped: %w", domain.ErrNotFound), http.StatusNotFound},
		{"edit conflict", domain.ErrEditConflict, http.StatusConflict},
		{"batch taken", domain.ErrBatchTaken, http.StatusConflict},
		{"rider at capacity", domain.ErrRiderAtCapacity, http.StatusUnprocessableEntity},
		{"validation", &domain.ValidationError{Fields: map[string]string{"x": "bad"}}, http.StatusUnprocessableEntity},
		{"unknown", errors.New("boom"), http.StatusInternalServerError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(fakeDB{})
			c, rec := newGinContext()

			srv.handleServiceError(c, tc.err)

			assert.Equal(t, tc.want, rec.Code)
		})
	}
}

func newGinContext() (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	return c, rec
}

func TestUpdatePreferences_VersionConflict(t *testing.T) {
	t.Run("409 when X-Expected-Version is stale", func(t *testing.T) {
		h, store := newStoreHandler(t)
		ctx := gomock.Any()
		store.EXPECT().GetRider(ctx, "rider_001").Return(fakeRiderRow("rider_001"), nil) // version 3
		expectTx(store)
		store.EXPECT().GetRiderForUpdate(ctx, "rider_001").Return(fakeRiderRow("rider_001"), nil)

		rec := send(t, h, http.MethodPut, "/api/v2/riders/rider_001/preferences", `{}`, "X-Expected-Version", "2")

		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Equal(t, `{"error":"unable to update the record due to an edit conflict, please try again"}`, strings.TrimSpace(rec.Body.String()))
	})
}
