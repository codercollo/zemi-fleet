package api

import (
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/codercollo/zemi-fleet/internal/config"
)

func newGeoBoundsHandler() http.Handler {
	gin.SetMode(gin.TestMode)
	cfg := testConfig()
	cfg.Geo = config.GeoConfig{BoundsEnabled: true, MinLat: -4.7, MaxLat: 4.7, MinLng: 33.9, MaxLng: 41.9}
	return NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), fakeDB{}, Services{}, "test").Routes()
}

func TestServiceAreaValidationAcrossEndpoints(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"create order delivery", http.MethodPost, "/api/v2/orders", `{"vendor_id":"ven_001","category":"food","delivery_location":{"lat":4.700001,"lng":36.8},"value_kes":100,"delivery_fee_kes":10}`},
		{"match orders location", http.MethodPost, "/api/v2/riders/match-orders", `{"rider_id":"rider_001","current_location":{"latitude":-5,"longitude":36.8},"current_orders_count":0}`},
		{"create batch location", http.MethodPost, "/api/v2/orders/batch", `{"rider_id":"rider_001","rider_location":{"latitude":-1.2,"longitude":42},"max_orders":1,"radius_km":1}`},
		{"list batches location", http.MethodGet, "/api/v2/batches?lat=-1.2&lng=33.899999", ""},
		{"match riders pickup", http.MethodPost, "/api/v2/orders/match-riders", `{"order_details":{"category":"food","pickup_location":{"lat":4.700001,"lng":36.8},"delivery_location":{"lat":-1.3,"lng":36.8},"value_kes":100}}`},
		{"match riders delivery", http.MethodPost, "/api/v2/orders/match-riders", `{"order_details":{"category":"food","pickup_location":{"lat":-1.3,"lng":36.8},"delivery_location":{"lat":-1.3,"lng":42},"value_kes":100}}`},
		{"rider location", http.MethodPatch, "/api/v2/riders/rider_001/location", `{"latitude":-4.700001,"longitude":36.8}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := send(t, newGeoBoundsHandler(), tc.method, tc.path, tc.body)
			require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), "must be within the service area")
		})
	}
}

func TestServiceAreaBoundaryIsInclusive(t *testing.T) {
	v := newGeoBoundsHandler()
	rec := send(t, v, http.MethodPost, "/api/v2/riders/match-orders", `{"rider_id":"rider_001","current_location":{"latitude":4.7,"longitude":41.9},"current_orders_count":0}`)
	// The request reaches the service after validation; nil services then produce a server error.
	require.NotEqual(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
}
