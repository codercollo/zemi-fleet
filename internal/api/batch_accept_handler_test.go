package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/repository"
)

func acceptedBatch(riderID string) domain.Batch {
	b := twoOrderBatch()
	b.Status = domain.BatchAccepted
	at := testNow.Add(-time.Minute)
	b.RiderID, b.AcceptedAt = &riderID, &at
	return b
}

func acceptPath(id string) string { return "/api/v2/batches/" + id + "/accept" }

func TestAcceptBatch_200(t *testing.T) {
	t.Run("returns the accepted batch with rider and time", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().AcceptBatchTx(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, p repository.AcceptBatchParams) (domain.Batch, error) {
				assert.Equal(t, "batch_X1", p.BatchID)
				assert.Equal(t, "rider_002", p.RiderID)
				assert.True(t, p.Now.Equal(testNow), "the clock's time")
				return acceptedBatch("rider_002"), nil
			})

		rec := send(t, h, http.MethodPost, acceptPath("batch_X1"), `{"rider_id":"rider_002"}`)

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		body := decode(t, rec)
		assert.Equal(t, []string{"batch"}, keys(body))
		b := body["batch"].(map[string]any)
		assert.Equal(t, "batch_X1", b["batch_id"])
		assert.Equal(t, "accepted", b["status"])
		assert.Equal(t, "rider_002", b["rider_id"])
		assert.Equal(t, "2026-10-13T08:59:00Z", b["accepted_at"])
		assert.Len(t, b["orders"], 2)
		assert.Len(t, b["sequence"], 4)
		assert.Equal(t, 0.84, b["distance_saved_km"])
		assertInOrder(t, rec.Body.String(), `"batch_id"`, `"status"`, `"rider_id"`, `"accepted_at"`, `"orders"`, `"sequence"`, `"total_distance_km"`)
	})

	t.Run("a retry by the same rider is the same 200 (idempotent)", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().AcceptBatchTx(gomock.Any(), gomock.Any()).Return(acceptedBatch("rider_002"), nil).Times(2)

		first := send(t, h, http.MethodPost, acceptPath("batch_X1"), `{"rider_id":"rider_002"}`)
		second := send(t, h, http.MethodPost, acceptPath("batch_X1"), `{"rider_id":"rider_002"}`)

		assert.Equal(t, http.StatusOK, first.Code)
		assert.Equal(t, http.StatusOK, second.Code)
		assert.JSONEq(t, first.Body.String(), second.Body.String())
	})
}

func TestAcceptBatch_Errors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{"unknown batch or rider", fmt.Errorf("accept batch: %w", domain.ErrNotFound), http.StatusNotFound, "could not be found"},
		{"taken or expired", fmt.Errorf("accept batch: %w", domain.ErrBatchTaken), http.StatusConflict, "batch is no longer available"},
		{"rider at capacity", fmt.Errorf("accept batch: %w", domain.ErrRiderAtCapacity), http.StatusUnprocessableEntity, "capacity"},
		{"rider unavailable", fmt.Errorf("accept batch: %w", domain.ErrRiderUnavailable), http.StatusUnprocessableEntity, "rider is not available"},
		{"anything else is a 500 that leaks nothing", errors.New("deadlock on 10.0.0.5"), http.StatusInternalServerError, "could not process"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, store := newStoreHandler(t)
			store.EXPECT().AcceptBatchTx(gomock.Any(), gomock.Any()).Return(domain.Batch{}, tc.err)

			rec := send(t, h, http.MethodPost, acceptPath("batch_X1"), `{"rider_id":"rider_002"}`)

			assert.Equal(t, tc.wantStatus, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), tc.wantBody)
			assert.NotContains(t, rec.Body.String(), "10.0.0.5")
		})
	}
}

func TestAcceptBatch_BadBody(t *testing.T) {
	t.Run("400", func(t *testing.T) {
		for _, body := range []string{``, `{"rider_id":`, `{"rider_id":"r","extra":1}`, `{"rider_id":5}`} {
			h, _ := newStoreHandler(t)

			rec := send(t, h, http.MethodPost, acceptPath("batch_X1"), body)

			assert.Equal(t, http.StatusBadRequest, rec.Code, body)
		}
	})
	t.Run("422", func(t *testing.T) {
		for _, body := range []string{`{}`, `{"rider_id":""}`, `{"rider_id":"   "}`} {
			h, _ := newStoreHandler(t) // no store call may happen

			rec := send(t, h, http.MethodPost, acceptPath("batch_X1"), body)

			require.Equal(t, http.StatusUnprocessableEntity, rec.Code, body)
			assert.Contains(t, errFields(t, rec), "rider_id")
		}
	})
}

const listPath = "/api/v2/batches"

func TestListBatches_200(t *testing.T) {
	t.Run("pages with the book's metadata", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().ListOpenBatches(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, p repository.ListOpenBatchesParams) ([]domain.Batch, int, error) {
				assert.Equal(t, domain.Location{Lat: -1.2650, Lng: 36.8038}, p.Point)
				assert.Equal(t, 3000.0, p.RadiusM, "radius_km defaults to BATCH_VISIBILITY_RADIUS_KM (3)")
				assert.Equal(t, 2, p.Page)
				assert.Equal(t, 2, p.PageSize)
				assert.True(t, p.Now.Equal(testNow))
				return []domain.Batch{twoOrderBatch()}, 5, nil
			})

		rec := send(t, h, http.MethodGet, listPath+"?lat=-1.2650&lng=36.8038&page=2&page_size=2", "")

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		body := decode(t, rec)
		assert.Equal(t, map[string]any{"current_page": 2.0, "page_size": 2.0, "first_page": 1.0, "last_page": 3.0, "total_records": 5.0}, body["metadata"])
		batches := body["batches"].([]any)
		require.Len(t, batches, 1)
		b := batches[0].(map[string]any)
		assert.Equal(t, "batch_X1", b["batch_id"])
		assert.Contains(t, b, "distance_to_first_pickup_km")
		assert.Contains(t, b, "distance_saved_km")
		assertInOrder(t, rec.Body.String(), `"batches"`, `"metadata"`, `"current_page"`, `"page_size"`, `"first_page"`, `"last_page"`, `"total_records"`)
	})

	t.Run("defaults: page 1, page_size 20; explicit radius", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().ListOpenBatches(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, p repository.ListOpenBatchesParams) ([]domain.Batch, int, error) {
				assert.Equal(t, 1, p.Page)
				assert.Equal(t, 20, p.PageSize)
				assert.Equal(t, 1500.0, p.RadiusM)
				return nil, 0, nil
			})

		rec := send(t, h, http.MethodGet, listPath+"?lat=-1.2650&lng=36.8038&radius_km=1.5", "")

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `"batches":[]`)
		assert.Contains(t, rec.Body.String(), `"metadata":{}`, "no records: empty metadata, as in the book")
	})

	t.Run("a page beyond the last is an empty list with the totals", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().ListOpenBatches(gomock.Any(), gomock.Any()).Return([]domain.Batch{}, 3, nil)

		rec := send(t, h, http.MethodGet, listPath+"?lat=-1.2&lng=36.8&page=9&page_size=2", "")

		require.Equal(t, http.StatusOK, rec.Code)
		body := decode(t, rec)
		assert.Empty(t, body["batches"])
		assert.EqualValues(t, 2, body["metadata"].(map[string]any)["last_page"])
	})
}

func TestListBatches_422(t *testing.T) {
	tests := []struct{ name, query, wantField string }{
		{"lat missing", "lng=36.8", "lat"},
		{"lng missing", "lat=-1.2", "lng"},
		{"lat not a number", "lat=north&lng=36.8", "lat"},
		{"lat out of range", "lat=91&lng=36.8", "lat"},
		{"lng out of range", "lat=-1.2&lng=-181", "lng"},
		{"radius zero", "lat=-1.2&lng=36.8&radius_km=0", "radius_km"},
		{"radius negative", "lat=-1.2&lng=36.8&radius_km=-1", "radius_km"},
		{"radius above the search radius", "lat=-1.2&lng=36.8&radius_km=5.1", "radius_km"},
		{"radius not a number", "lat=-1.2&lng=36.8&radius_km=far", "radius_km"},
		{"page zero", "lat=-1.2&lng=36.8&page=0", "page"},
		{"page negative", "lat=-1.2&lng=36.8&page=-3", "page"},
		{"page absurd", "lat=-1.2&lng=36.8&page=99999999999", "page"},
		{"page not a number", "lat=-1.2&lng=36.8&page=two", "page"},
		{"page_size zero", "lat=-1.2&lng=36.8&page_size=0", "page_size"},
		{"page_size 51", "lat=-1.2&lng=36.8&page_size=51", "page_size"},
		{"page_size not a number", "lat=-1.2&lng=36.8&page_size=lots", "page_size"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newStoreHandler(t) // no store call may happen

			rec := send(t, h, http.MethodGet, listPath+"?"+tc.query, "")

			require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
			assert.Contains(t, errFields(t, rec), tc.wantField)
		})
	}

	t.Run("boundaries are accepted", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().ListOpenBatches(gomock.Any(), gomock.Any()).Return(nil, 0, nil).Times(2)

		for _, q := range []string{"lat=-90&lng=180&radius_km=5&page=1&page_size=50", "lat=90&lng=-180&radius_km=0.01&page=10000000&page_size=1"} {
			rec := send(t, h, http.MethodGet, listPath+"?"+q, "")
			assert.Equal(t, http.StatusOK, rec.Code, q)
		}
	})
}

func TestListBatches_500(t *testing.T) {
	h, store := newStoreHandler(t)
	store.EXPECT().ListOpenBatches(gomock.Any(), gomock.Any()).Return(nil, 0, errors.New("boom"))

	rec := send(t, h, http.MethodGet, listPath+"?lat=-1.2&lng=36.8", "")

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestNewMetadata(t *testing.T) {
	assert.Equal(t, metadata{}, newMetadata(0, 1, 20))
	assert.Equal(t, metadata{CurrentPage: 1, PageSize: 20, FirstPage: 1, LastPage: 1, TotalRecords: 20}, newMetadata(20, 1, 20))
	assert.Equal(t, metadata{CurrentPage: 2, PageSize: 20, FirstPage: 1, LastPage: 2, TotalRecords: 21}, newMetadata(21, 2, 20))
}
