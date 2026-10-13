package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/mock"
	"github.com/codercollo/zemi-fleet/internal/repository/sqlc"
	"github.com/codercollo/zemi-fleet/internal/service"
)

const orderPath = "/api/v2/orders"

const orderJSON = `{"vendor_id":"ven_001","category":"electronics","delivery_location":{"lat":-1.2905,"lng":36.7870},"value_kes":4500.50,"delivery_fee_kes":250}`

// expectFreshClaim sets up the key being free: expired keys are cleaned, then the claim succeeds.
func expectFreshClaim(store *mock.MockStore) {
	store.EXPECT().DeleteExpiredIdempotencyKeys(gomock.Any(), testNow.Add(-testIdempotencyTTL)).Return(nil)
	store.EXPECT().ClaimIdempotencyKey(gomock.Any(), gomock.Any()).Return(int64(1), nil)
}

// expectOrderCreated sets up a successful POST /orders behind the middleware.
func expectOrderCreated(t *testing.T, store *mock.MockStore) {
	t.Helper()
	store.EXPECT().GetVendor(gomock.Any(), "ven_001").Return(fakeVendor(), nil)
	store.EXPECT().GetCategory(gomock.Any(), "electronics").Return(sqlc.Category{Code: "electronics"}, nil)
	store.EXPECT().CreateOrder(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, p sqlc.CreateOrderParams) (sqlc.Order, error) { return orderRowFrom(p), nil })
}

func TestIdempotency_NoHeaderIsUntouched(t *testing.T) {
	h, store := newStoreHandler(t) // no idempotency calls are expected
	expectOrderCreated(t, store)

	rec := send(t, h, http.MethodPost, orderPath, orderJSON)

	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Empty(t, rec.Header().Get("Idempotent-Replay"))
}

func TestIdempotency_FirstRequestRunsAndIsStored(t *testing.T) {
	h, store := newStoreHandler(t)
	expectFreshClaim(store)
	expectOrderCreated(t, store)
	var stored sqlc.CompleteIdempotencyKeyParams
	store.EXPECT().CompleteIdempotencyKey(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, p sqlc.CompleteIdempotencyKeyParams) error { stored = p; return nil })

	rec := send(t, h, http.MethodPost, orderPath, orderJSON, "Idempotency-Key", "abc-123")

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Empty(t, rec.Header().Get("Idempotent-Replay"))
	assert.Equal(t, "abc-123", stored.Key)
	assert.Equal(t, orderPath, stored.Endpoint)
	assert.EqualValues(t, 201, *stored.ResponseStatus)
	assert.Equal(t, rec.Body.String(), *stored.ResponseBody, "what was sent is what is stored")
	assert.Contains(t, string(stored.ResponseHeaders), "/api/v2/orders/ord_", "the Location header is stored for replay")
}

func TestIdempotency_ReplayReturnsTheStoredResponse(t *testing.T) {
	h, store := newStoreHandler(t) // GetVendor and CreateOrder are NOT expected: the handler must not run
	body := `{"order":{"id":"ord_X1"}}` + "\n"
	store.EXPECT().DeleteExpiredIdempotencyKeys(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().ClaimIdempotencyKey(gomock.Any(), gomock.Any()).Return(int64(0), nil)
	store.EXPECT().GetIdempotencyKey(gomock.Any(), gomock.Any()).Return(sqlc.IdempotencyKey{
		RequestHash: service.HashBody([]byte(orderJSON)), ResponseStatus: ptrTo(int32(201)), ResponseBody: ptrTo(body),
		ResponseHeaders: []byte(`{"Location":"/api/v2/orders/ord_X1"}`), CreatedAt: testNow,
	}, nil)

	// Same JSON, different whitespace: still the same request.
	rec := send(t, h, http.MethodPost, orderPath, strings.ReplaceAll(orderJSON, ",", ",\n  "), "Idempotency-Key", "abc-123")

	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, body, rec.Body.String())
	assert.Equal(t, "/api/v2/orders/ord_X1", rec.Header().Get("Location"))
	assert.Equal(t, "true", rec.Header().Get("Idempotent-Replay"))
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
}

func TestIdempotency_SameKeyDifferentBodyIs422(t *testing.T) {
	h, store := newStoreHandler(t)
	store.EXPECT().DeleteExpiredIdempotencyKeys(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().ClaimIdempotencyKey(gomock.Any(), gomock.Any()).Return(int64(0), nil)
	store.EXPECT().GetIdempotencyKey(gomock.Any(), gomock.Any()).Return(sqlc.IdempotencyKey{
		RequestHash: service.HashBody([]byte(`{"something":"else"}`)), ResponseStatus: ptrTo(int32(201)), ResponseBody: ptrTo("{}"),
	}, nil)

	rec := send(t, h, http.MethodPost, orderPath, orderJSON, "Idempotency-Key", "abc-123")

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, errFields(t, rec), "idempotency_key")
}

func TestIdempotency_StillRunningIs409(t *testing.T) {
	h, store := newStoreHandler(t)
	store.EXPECT().DeleteExpiredIdempotencyKeys(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().ClaimIdempotencyKey(gomock.Any(), gomock.Any()).Return(int64(0), nil)
	store.EXPECT().GetIdempotencyKey(gomock.Any(), gomock.Any()).Return(sqlc.IdempotencyKey{
		RequestHash: service.HashBody([]byte(orderJSON)), CreatedAt: testNow,
	}, nil)
	store.EXPECT().TakeOverIdempotencyKey(gomock.Any(), gomock.Any()).Return(int64(0), nil)

	rec := send(t, h, http.MethodPost, orderPath, orderJSON, "Idempotency-Key", "abc-123")

	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "still being processed")
}

func TestIdempotency_AnAbandonedClaimIsTakenOver(t *testing.T) {
	h, store := newStoreHandler(t)
	store.EXPECT().DeleteExpiredIdempotencyKeys(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().ClaimIdempotencyKey(gomock.Any(), gomock.Any()).Return(int64(0), nil)
	store.EXPECT().GetIdempotencyKey(gomock.Any(), gomock.Any()).Return(sqlc.IdempotencyKey{
		RequestHash: service.HashBody([]byte(orderJSON)), CreatedAt: testNow.Add(-time.Hour),
	}, nil)
	store.EXPECT().TakeOverIdempotencyKey(gomock.Any(), gomock.Any()).Return(int64(1), nil)
	expectOrderCreated(t, store)
	store.EXPECT().CompleteIdempotencyKey(gomock.Any(), gomock.Any()).Return(nil)

	rec := send(t, h, http.MethodPost, orderPath, orderJSON, "Idempotency-Key", "abc-123")

	assert.Equal(t, http.StatusCreated, rec.Code)
}

func TestIdempotency_FailuresAreNotStoredAndReleaseTheKey(t *testing.T) {
	t.Run("a 422 from the handler", func(t *testing.T) {
		h, store := newStoreHandler(t)
		expectFreshClaim(store)
		store.EXPECT().GetVendor(gomock.Any(), "ven_001").Return(sqlc.Vendor{}, domain.ErrNotFound)
		store.EXPECT().GetCategory(gomock.Any(), "electronics").Return(sqlc.Category{Code: "electronics"}, nil)
		store.EXPECT().ReleaseIdempotencyKey(gomock.Any(), sqlc.ReleaseIdempotencyKeyParams{Key: "k-fail", Endpoint: orderPath}).Return(nil)
		// CompleteIdempotencyKey is NOT expected.

		rec := send(t, h, http.MethodPost, orderPath, orderJSON, "Idempotency-Key", "k-fail")

		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	})
	t.Run("a 500", func(t *testing.T) {
		h, store := newStoreHandler(t)
		expectFreshClaim(store)
		store.EXPECT().GetVendor(gomock.Any(), "ven_001").Return(sqlc.Vendor{}, errors.New("db down"))
		store.EXPECT().ReleaseIdempotencyKey(gomock.Any(), gomock.Any()).Return(nil)

		rec := send(t, h, http.MethodPost, orderPath, orderJSON, "Idempotency-Key", "k-500")

		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})
	t.Run("bad JSON never reaches the store beyond the claim", func(t *testing.T) {
		h, store := newStoreHandler(t)
		expectFreshClaim(store)
		store.EXPECT().ReleaseIdempotencyKey(gomock.Any(), gomock.Any()).Return(nil)

		rec := send(t, h, http.MethodPost, orderPath, `{"vendor_id":`, "Idempotency-Key", "k-bad")

		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

func TestIdempotency_BadKeysAreRejectedBeforeAnyWork(t *testing.T) {
	for name, key := range map[string]string{
		"a space":        "has space",
		"too long":       strings.Repeat("k", 65),
		"non-ASCII":      "café",
		"a tab":          "a	b",
		"only a newline": "\n",
	} {
		t.Run(name, func(t *testing.T) {
			h, _ := newStoreHandler(t) // no store call may happen
			req := httptest.NewRequest(http.MethodPost, orderPath, strings.NewReader(orderJSON))
			req.Header.Set("Idempotency-Key", key)
			rec := httptest.NewRecorder()

			h.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, rec.Body.String(), "Idempotency-Key")
		})
	}
	t.Run("64 characters is fine", func(t *testing.T) {
		h, store := newStoreHandler(t)
		expectFreshClaim(store)
		expectOrderCreated(t, store)
		store.EXPECT().CompleteIdempotencyKey(gomock.Any(), gomock.Any()).Return(nil)

		rec := send(t, h, http.MethodPost, orderPath, orderJSON, "Idempotency-Key", strings.Repeat("k", 64))

		assert.Equal(t, http.StatusCreated, rec.Code)
	})
}

func TestIdempotency_StoreFailureIs500AndRunsNothing(t *testing.T) {
	h, store := newStoreHandler(t)
	store.EXPECT().DeleteExpiredIdempotencyKeys(gomock.Any(), gomock.Any()).Return(errors.New("connection refused on 10.0.0.5"))

	rec := send(t, h, http.MethodPost, orderPath, orderJSON, "Idempotency-Key", "abc")

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), "10.0.0.5")
}

func TestIdempotency_AlsoCoversBatchRequests(t *testing.T) {
	h, store := newStoreHandler(t)
	store.EXPECT().DeleteExpiredIdempotencyKeys(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().ClaimIdempotencyKey(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, p sqlc.ClaimIdempotencyKeyParams) (int64, error) {
			assert.Equal(t, "/api/v2/orders/batch", p.Endpoint, "a key is scoped to its endpoint")
			return 0, nil
		})
	store.EXPECT().GetIdempotencyKey(gomock.Any(), gomock.Any()).Return(sqlc.IdempotencyKey{
		RequestHash: service.HashBody([]byte(validBatchBody)), ResponseStatus: ptrTo(int32(200)), ResponseBody: ptrTo(`{"batches":[]}` + "\n"),
	}, nil)

	rec := send(t, h, http.MethodPost, batchPath, validBatchBody, "Idempotency-Key", "batch-1")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"batches":[]}`, rec.Body.String())
	assert.Equal(t, "true", rec.Header().Get("Idempotent-Replay"))
}

func TestIdempotency_OtherRoutesIgnoreTheHeader(t *testing.T) {
	h, store := newStoreHandler(t) // no idempotency calls: GET /orders/:id has no middleware
	store.EXPECT().GetOrder(gomock.Any(), "ord_001").Return(sqlc.Order{ID: "ord_001"}, nil)

	rec := send(t, h, http.MethodGet, "/api/v2/orders/ord_001", "", "Idempotency-Key", "whatever")

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestIdempotency_MisconfiguredServerIs500(t *testing.T) {
	srv := newTestServer(fakeDB{}) // no services at all
	rec := send(t, srv.Routes(), http.MethodPost, orderPath, orderJSON, "Idempotency-Key", "abc")

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}
