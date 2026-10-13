//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apispec "github.com/codercollo/zemi-fleet/api"
	"github.com/codercollo/zemi-fleet/internal/testdb"
)

// contract validates real responses from the running stack against api/openapi.yaml: the status must be documented
// for the operation and the body and headers must match the schema, so the spec cannot drift from the code.
type contract struct {
	e      *env
	router routers.Router
}

func newContract(t *testing.T, e *env) *contract {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromData(apispec.Spec)
	require.NoError(t, err)
	require.NoError(t, doc.Validate(context.Background()))
	router, err := gorillamux.NewRouter(doc)
	require.NoError(t, err)
	return &contract{e: e, router: router}
}

// call sends the request (with the API key unless a header value of "" removes it), checks the status, validates the
// response against the spec and returns the parsed body.
func (c *contract) call(method, path string, body any, wantStatus int, headers ...string) map[string]any {
	c.e.t.Helper()
	t := c.e.t
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		require.NoError(t, err)
	}
	req, err := http.NewRequest(method, c.e.server.URL+path, bytes.NewReader(payload))
	require.NoError(t, err)
	req.Header.Set("X-API-Key", testAPIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i+1] == "" {
			req.Header.Del(headers[i])
			continue
		}
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	require.Equal(t, wantStatus, res.StatusCode, "%s %s: %s", method, path, raw)

	c.e.mu.Lock()
	c.e.captured = append(c.e.captured, string(raw))
	c.e.mu.Unlock()

	route, pathParams, err := c.router.FindRoute(req)
	require.NoError(t, err, "%s %s is not in api/openapi.yaml", method, path)
	require.NoError(t, openapi3filter.ValidateResponse(context.Background(), &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{
			Request: req, PathParams: pathParams, Route: route,
			Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
		},
		Status:  res.StatusCode,
		Header:  res.Header,
		Body:    io.NopCloser(bytes.NewReader(raw)),
		Options: &openapi3filter.Options{IncludeResponseStatus: true},
	}), "%s %s answered %d with a body or headers the spec does not describe: %s", method, path, res.StatusCode, raw)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(raw, &parsed))
	return parsed
}

// Scenario 12: the responses the API really sends match the OpenAPI contract.
func TestE2E_ResponsesMatchTheOpenAPIContract(t *testing.T) {
	e := newEnv(t)
	c := newContract(t, e)
	// A has a fresh location near the vendor; B is the loser of the accept race.
	a := testdb.AddRider(t, e.pool, testdb.RiderFixture{ID: "rider_a", Lat: f64(nearYayaLat), Lng: f64(nearYayaL), SeenAt: e.seenNow(),
		Offers: 10, Accepted: 9, Categories: []string{"electronics"}, AreaIDs: []string{"area_001"}})
	b := testdb.AddRider(t, e.pool, testdb.RiderFixture{ID: "rider_b", Lat: f64(-1.2780), Lng: f64(36.8000), SeenAt: e.seenNow(), Offers: 10, Accepted: 5})

	// Health, create, replay and read an order.
	c.call(http.MethodGet, "/api/v2/healthz", nil, http.StatusOK, "X-API-Key", "")
	order := map[string]any{"vendor_id": "ven_004", "category": "electronics",
		"delivery_location": map[string]float64{"lat": northeast[0][0], "lng": northeast[0][1]}, "value_kes": 5000, "delivery_fee_kes": 250}
	created := c.call(http.MethodPost, "/api/v2/orders", order, http.StatusCreated, "Idempotency-Key", "contract-1")
	replayed := c.call(http.MethodPost, "/api/v2/orders", order, http.StatusCreated, "Idempotency-Key", "contract-1")
	assert.Equal(t, created, replayed)
	orderID := created["order"].(map[string]any)["id"].(string)
	c.call(http.MethodGet, "/api/v2/orders/"+orderID, nil, http.StatusOK)
	e.clk.Advance(time.Minute)
	for _, extra := range []int{1, 2} {
		order["delivery_location"] = map[string]float64{"lat": northeast[extra][0], "lng": northeast[extra][1]}
		c.call(http.MethodPost, "/api/v2/orders", order, http.StatusCreated)
		e.clk.Advance(time.Minute)
	}

	// The three features.
	c.call(http.MethodPost, "/api/v2/riders/match-orders", map[string]any{
		"rider_id": a, "current_location": loc(nearYayaLat, nearYayaL), "bearing_degrees": 38, "speed_kmh": 20, "current_orders_count": 0}, http.StatusOK)
	c.call(http.MethodPost, "/api/v2/riders/match-orders", map[string]any{ // no bearing: full circle, null angles
		"rider_id": a, "current_location": loc(nearYayaLat, nearYayaL), "current_orders_count": 0}, http.StatusOK)
	c.call(http.MethodPost, "/api/v2/orders/match-riders", map[string]any{"order_id": orderID}, http.StatusOK)
	c.call(http.MethodPost, "/api/v2/orders/match-riders", map[string]any{"order_details": map[string]any{
		"category": "electronics", "pickup_location": map[string]float64{"lat": yayaLat, "lng": yayaLng},
		"delivery_location": map[string]float64{"lat": northeast[0][0], "lng": northeast[0][1]}, "value_kes": 4000}}, http.StatusOK)
	batches := c.call(http.MethodPost, "/api/v2/orders/batch", map[string]any{
		"rider_id": a, "rider_location": loc(nearYayaLat, nearYayaL), "max_orders": 5, "radius_km": 2}, http.StatusOK)
	batchID := batches["batches"].([]any)[0].(map[string]any)["batch_id"].(string)
	c.call(http.MethodGet, "/api/v2/batches?lat=-1.2917&lng=36.7878", nil, http.StatusOK)

	// The accept race: one winner, one 409.
	c.call(http.MethodPost, "/api/v2/batches/"+batchID+"/accept", map[string]string{"rider_id": a}, http.StatusOK)
	c.call(http.MethodPost, "/api/v2/batches/"+batchID+"/accept", map[string]string{"rider_id": b}, http.StatusConflict)

	// Riders.
	c.call(http.MethodGet, "/api/v2/riders/"+a, nil, http.StatusOK)
	c.call(http.MethodPut, "/api/v2/riders/"+a+"/preferences", map[string]any{
		"min_order_value_kes": 100, "preferred_categories": []string{"electronics"}, "preferred_areas": []string{"area_001"}}, http.StatusOK)
	c.call(http.MethodPatch, "/api/v2/riders/"+a+"/location", map[string]any{"latitude": yayaLat, "longitude": yayaLng}, http.StatusOK)
	c.call(http.MethodPatch, "/api/v2/riders/"+a+"/availability", map[string]any{"is_available": true}, http.StatusOK)
	c.call(http.MethodPatch, "/api/v2/riders/"+a+"/availability", map[string]any{"is_available": true}, http.StatusConflict,
		"X-Expected-Version", "1") // stale: the rider is long past version 1

	// Errors: a 422 with the field-map form, a 404 and a 401, plus metrics and a validation error on a query.
	invalid := c.call(http.MethodPost, "/api/v2/orders", map[string]any{"vendor_id": "ven_004"}, http.StatusUnprocessableEntity)
	assert.IsType(t, map[string]any{}, invalid["error"], "validation errors are an object of field to message")
	c.call(http.MethodGet, "/api/v2/orders/ord_nope", nil, http.StatusNotFound)
	unauth := c.call(http.MethodGet, "/api/v2/riders/"+a, nil, http.StatusUnauthorized, "X-API-Key", "")
	assert.IsType(t, "", unauth["error"], "other errors are a string")
	c.call(http.MethodGet, "/debug/vars", nil, http.StatusOK)
	c.call(http.MethodGet, "/api/v2/batches?lat=abc", nil, http.StatusUnprocessableEntity)
}
