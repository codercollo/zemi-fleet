package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestIDFormats(t *testing.T) {
	tests := []struct {
		name   string
		f      idFormat
		value  string
		wantOK bool
	}{
		{"seed rider", riderID, "rider_001", true},
		{"generated rider (prefix + base32)", riderID, "rider_01M4T8XXEFJ6KSVXER72", true},
		{"40-character suffix is the limit", riderID, "rider_" + strings.Repeat("a", 40), true},
		{"41-character suffix is too long", riderID, "rider_" + strings.Repeat("a", 41), false},
		{"empty suffix", riderID, "rider_", false},
		{"wrong prefix", riderID, "ord_001", false},
		{"no prefix", riderID, "001", false},
		{"a space", riderID, "rider_ 001", false},
		{"a slash", riderID, "rider_1/../x", false},
		{"a quote and SQL", riderID, "rider_1'; DROP TABLE riders;--", false},
		{"a trailing newline", riderID, "rider_001\n", false},
		{"unicode letters", riderID, "rider_café", false},
		{"an underscore inside the suffix", riderID, "rider_a_b", false},
		{"uppercase prefix", riderID, "RIDER_001", false},
		{"order", orderID, "ord_01M4T8XXEFJ6KSVXER72", true},
		{"batch", batchID, "batch_01M4TH4HH81H4835C1ZK", true},
		{"vendor", vendorID, "ven_001", true},
		{"area", areaID, "area_008", true},
		{"category", categoryID, "electronics", true},
		{"category with an underscore", categoryID, "baby_goods", true},
		{"category with a capital", categoryID, "Electronics", false},
		{"category with a digit", categoryID, "food2", false},
		{"category too short", categoryID, "a", false},
		{"category 40 letters", categoryID, "a" + strings.Repeat("b", 39), true},
		{"category 41 letters", categoryID, "a" + strings.Repeat("b", 40), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantOK, tc.f.pattern.MatchString(tc.value))
		})
	}
}

func TestIdempotencyKeyFormat(t *testing.T) {
	for key, want := range map[string]bool{
		"a": true, "550e8400-e29b-41d4-a716-446655440000": true, "order:42/retry.1": true, strings.Repeat("k", 64): true,
		"": false, strings.Repeat("k", 65): false, "has space": false, "tab\there": false, "café": false, "new\nline": false,
	} {
		assert.Equal(t, want, idempotencyFormat.MatchString(key), "%q", key)
	}
}

// A malformed ID in a path cannot exist, so every ID route answers 404 without touching the database.
func TestPathIDsAreChecked(t *testing.T) {
	bad := []string{"ord_1%27%3B--", "..%2f..%2fetc", "rider_%20x", "x", strings.Repeat("a", 200)}
	routes := []struct{ method, pattern, body string }{
		{http.MethodGet, "/api/v2/orders/%s", ""},
		{http.MethodGet, "/api/v2/riders/%s", ""},
		{http.MethodPut, "/api/v2/riders/%s/preferences", `{}`},
		{http.MethodPatch, "/api/v2/riders/%s/location", `{"latitude":-1.3,"longitude":36.8}`},
		{http.MethodPatch, "/api/v2/riders/%s/availability", `{"is_available":true}`},
		{http.MethodPost, "/api/v2/batches/%s/accept", `{"rider_id":"rider_001"}`},
	}
	for _, r := range routes {
		for _, id := range bad {
			t.Run(r.method+" "+r.pattern+" "+id, func(t *testing.T) {
				h, _ := newStoreHandler(t) // no store call may happen

				rec := send(t, h, r.method, strings.Replace(r.pattern, "%s", id, 1), r.body)

				assert.Equal(t, http.StatusNotFound, rec.Code)
			})
		}
	}
}

func TestBodyIDsAreChecked(t *testing.T) {
	loc := `"rider_location":{"latitude":-1.26,"longitude":36.8}`
	tests := []struct{ name, path, body, wantField string }{
		{"match-orders rider_id", "/api/v2/riders/match-orders", `{"rider_id":"nobody","current_location":{"latitude":-1.2,"longitude":36.8},"current_orders_count":0}`, "rider_id"},
		{"batch rider_id", "/api/v2/orders/batch", `{"rider_id":"rider_1; DROP","` + loc[1:] + `,"max_orders":5,"radius_km":2}`, "rider_id"},
		{"accept rider_id", "/api/v2/batches/batch_1/accept", `{"rider_id":"ord_1"}`, "rider_id"},
		{"order vendor_id", "/api/v2/orders", `{"vendor_id":"vendor 1","category":"food","delivery_location":{"lat":-1.2,"lng":36.8},"value_kes":10,"delivery_fee_kes":1}`, "vendor_id"},
		{"order category", "/api/v2/orders", `{"vendor_id":"ven_001","category":"Food!","delivery_location":{"lat":-1.2,"lng":36.8},"value_kes":10,"delivery_fee_kes":1}`, "category"},
		{"match-riders order_id", "/api/v2/orders/match-riders", `{"order_id":"../../x"}`, "order_id"},
		{"match-riders category", "/api/v2/orders/match-riders", `{"order_details":{"category":"pizza pie","pickup_location":{"lat":-1.29,"lng":36.78},"delivery_location":{"lat":-1.30,"lng":36.8},"value_kes":100}}`, "order_details.category"},
		{"preferred category", "/api/v2/riders/rider_001/preferences", `{"preferred_categories":["food","DROP TABLE"]}`, "preferred_categories"},
		{"preferred area", "/api/v2/riders/rider_001/preferences", `{"preferred_areas":["kilimani"]}`, "preferred_areas"},
		{"too many preferred areas", "/api/v2/riders/rider_001/preferences", `{"preferred_areas":[` + manyAreas(51) + `]}`, "preferred_areas"},
		{"an over-long rider_id", "/api/v2/riders/match-orders", `{"rider_id":"rider_` + strings.Repeat("a", 200) + `","current_location":{"latitude":-1.2,"longitude":36.8},"current_orders_count":0}`, "rider_id"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newStoreHandler(t) // rejected before any store call
			method := http.MethodPost
			if strings.HasSuffix(tc.path, "/preferences") {
				method = http.MethodPut
			}

			rec := send(t, h, method, tc.path, tc.body)

			require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
			assert.Contains(t, errFields(t, rec), tc.wantField)
		})
	}
}

func manyAreas(n int) string {
	parts := make([]string, 0, n)
	for i := range n {
		parts = append(parts, fmt.Sprintf(`"area_x%03d"`, i))
	}
	return strings.Join(parts, ",")
}

func TestListBoundsAreChecked(t *testing.T) {
	t.Run("50 entries are allowed", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetRider(gomock.Any(), "rider_001").Return(fakeRiderRow("rider_001"), nil)
		store.EXPECT().GetAreasByIDs(gomock.Any(), gomock.Any()).Return(nil, nil)

		rec := send(t, h, http.MethodPut, "/api/v2/riders/rider_001/preferences", `{"preferred_areas":[`+manyAreas(50)+`]}`)

		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "well-formed but unknown areas: the service says so")
		assert.Contains(t, errFields(t, rec), "preferred_areas")
	})
}
