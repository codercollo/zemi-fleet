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
	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/mock"
	"github.com/codercollo/zemi-fleet/internal/repository/sqlc"
)

const (
	idemTTL     = 24 * time.Hour
	idemTimeout = 2 * time.Minute
)

func newIdemService(t *testing.T) (*IdempotencyService, *mock.MockStore) {
	t.Helper()
	store := mock.NewMockStore(gomock.NewController(t))
	return NewIdempotencyService(store, clock.NewTest(testNow), idemTTL, idemTimeout), store
}

func ptrOf[T any](v T) *T { return &v }

func TestHashBody(t *testing.T) {
	a := HashBody([]byte(`{"a":1,"b":[1,2]}`))

	assert.Equal(t, a, HashBody([]byte("{ \"a\": 1,\n  \"b\": [1, 2] }")), "whitespace does not change the body")
	assert.NotEqual(t, a, HashBody([]byte(`{"a":2,"b":[1,2]}`)), "a different value does")
	assert.NotEqual(t, a, HashBody([]byte(`{"b":[1,2],"a":1}`)), "key order is part of the body")
	assert.Len(t, a, 64, "sha256 as hex")
	assert.Equal(t, HashBody([]byte("not json")), HashBody([]byte("not json")), "non-JSON is hashed as it is")
	assert.NotEqual(t, HashBody(nil), HashBody([]byte("{}")))
}

func TestStorable(t *testing.T) {
	for status, want := range map[int]bool{200: true, 201: true, 204: true, 299: true, 301: false, 400: false, 404: false, 409: false, 422: false, 500: false, 503: false} {
		assert.Equal(t, want, Storable(status), "status %d", status)
	}
}

func TestBegin_FirstRequestOwnsTheKey(t *testing.T) {
	svc, store := newIdemService(t)
	gomock.InOrder(
		store.EXPECT().DeleteExpiredIdempotencyKeys(gomock.Any(), testNow.Add(-idemTTL)).Return(nil),
		store.EXPECT().ClaimIdempotencyKey(gomock.Any(), sqlc.ClaimIdempotencyKeyParams{
			Key: "k1", Endpoint: "/api/v2/orders", RequestHash: "h1", CreatedAt: testNow,
		}).Return(int64(1), nil),
	)

	decision, _, err := svc.Begin(context.Background(), "k1", "/api/v2/orders", "h1")

	require.NoError(t, err)
	assert.Equal(t, Proceed, decision)
}

func TestBegin_ExistingKey(t *testing.T) {
	headers := []byte(`{"Location":"/api/v2/orders/ord_1"}`)
	tests := []struct {
		name     string
		row      sqlc.IdempotencyKey
		takeOver int64
		want     Decision
		wantBody string
	}{
		{"same body, finished: replay", sqlc.IdempotencyKey{RequestHash: "h1", ResponseStatus: ptrOf(int32(201)), ResponseBody: ptrOf(`{"order":{}}`), ResponseHeaders: headers, CreatedAt: testNow.Add(-time.Hour)}, 0, Replay, `{"order":{}}`},
		{"different body: mismatch, even when finished", sqlc.IdempotencyKey{RequestHash: "other", ResponseStatus: ptrOf(int32(201)), ResponseBody: ptrOf(`{}`), CreatedAt: testNow}, 0, Mismatch, ""},
		{"different body while still running: mismatch", sqlc.IdempotencyKey{RequestHash: "other", CreatedAt: testNow}, 0, Mismatch, ""},
		{"same body, still running: in progress", sqlc.IdempotencyKey{RequestHash: "h1", CreatedAt: testNow.Add(-10 * time.Second)}, 0, InProgress, ""},
		{"same body, claim abandoned past the timeout: take it over", sqlc.IdempotencyKey{RequestHash: "h1", CreatedAt: testNow.Add(-idemTimeout - time.Second)}, 1, Proceed, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, store := newIdemService(t)
			store.EXPECT().DeleteExpiredIdempotencyKeys(gomock.Any(), gomock.Any()).Return(nil)
			store.EXPECT().ClaimIdempotencyKey(gomock.Any(), gomock.Any()).Return(int64(0), nil)
			store.EXPECT().GetIdempotencyKey(gomock.Any(), sqlc.GetIdempotencyKeyParams{Key: "k1", Endpoint: "/api/v2/orders"}).Return(tc.row, nil)
			if tc.want == Proceed || tc.want == InProgress {
				store.EXPECT().TakeOverIdempotencyKey(gomock.Any(), sqlc.TakeOverIdempotencyKeyParams{
					RequestHash: "h1", Now: testNow, Key: "k1", Endpoint: "/api/v2/orders", StaleBefore: testNow.Add(-idemTimeout),
				}).Return(tc.takeOver, nil)
			}

			decision, stored, err := svc.Begin(context.Background(), "k1", "/api/v2/orders", "h1")

			require.NoError(t, err)
			assert.Equal(t, tc.want, decision)
			if tc.want == Replay {
				assert.Equal(t, 201, stored.Status)
				assert.Equal(t, tc.wantBody, string(stored.Body))
				assert.Equal(t, map[string]string{"Location": "/api/v2/orders/ord_1"}, stored.Headers)
			}
		})
	}
}

func TestBegin_KeyDeletedBetweenClaimAndRead(t *testing.T) {
	t.Run("claims again and wins", func(t *testing.T) {
		svc, store := newIdemService(t)
		store.EXPECT().DeleteExpiredIdempotencyKeys(gomock.Any(), gomock.Any()).Return(nil)
		gomock.InOrder(
			store.EXPECT().ClaimIdempotencyKey(gomock.Any(), gomock.Any()).Return(int64(0), nil),
			store.EXPECT().GetIdempotencyKey(gomock.Any(), gomock.Any()).Return(sqlc.IdempotencyKey{}, domain.ErrNotFound),
			store.EXPECT().ClaimIdempotencyKey(gomock.Any(), gomock.Any()).Return(int64(1), nil),
		)

		decision, _, err := svc.Begin(context.Background(), "k1", "/e", "h1")

		require.NoError(t, err)
		assert.Equal(t, Proceed, decision)
	})
	t.Run("claims again and loses: someone else got there first", func(t *testing.T) {
		svc, store := newIdemService(t)
		store.EXPECT().DeleteExpiredIdempotencyKeys(gomock.Any(), gomock.Any()).Return(nil)
		gomock.InOrder(
			store.EXPECT().ClaimIdempotencyKey(gomock.Any(), gomock.Any()).Return(int64(0), nil),
			store.EXPECT().GetIdempotencyKey(gomock.Any(), gomock.Any()).Return(sqlc.IdempotencyKey{}, domain.ErrNotFound),
			store.EXPECT().ClaimIdempotencyKey(gomock.Any(), gomock.Any()).Return(int64(0), nil),
		)

		decision, _, err := svc.Begin(context.Background(), "k1", "/e", "h1")

		require.NoError(t, err)
		assert.Equal(t, InProgress, decision)
	})
}

func TestBegin_StoreErrors(t *testing.T) {
	boom := errors.New("boom")
	steps := []struct {
		name  string
		setup func(*mock.MockStore)
	}{
		{"cleanup", func(s *mock.MockStore) {
			s.EXPECT().DeleteExpiredIdempotencyKeys(gomock.Any(), gomock.Any()).Return(boom)
		}},
		{"claim", func(s *mock.MockStore) {
			s.EXPECT().DeleteExpiredIdempotencyKeys(gomock.Any(), gomock.Any()).Return(nil)
			s.EXPECT().ClaimIdempotencyKey(gomock.Any(), gomock.Any()).Return(int64(0), boom)
		}},
		{"read", func(s *mock.MockStore) {
			s.EXPECT().DeleteExpiredIdempotencyKeys(gomock.Any(), gomock.Any()).Return(nil)
			s.EXPECT().ClaimIdempotencyKey(gomock.Any(), gomock.Any()).Return(int64(0), nil)
			s.EXPECT().GetIdempotencyKey(gomock.Any(), gomock.Any()).Return(sqlc.IdempotencyKey{}, boom)
		}},
		{"take over", func(s *mock.MockStore) {
			s.EXPECT().DeleteExpiredIdempotencyKeys(gomock.Any(), gomock.Any()).Return(nil)
			s.EXPECT().ClaimIdempotencyKey(gomock.Any(), gomock.Any()).Return(int64(0), nil)
			s.EXPECT().GetIdempotencyKey(gomock.Any(), gomock.Any()).Return(sqlc.IdempotencyKey{RequestHash: "h1"}, nil)
			s.EXPECT().TakeOverIdempotencyKey(gomock.Any(), gomock.Any()).Return(int64(0), boom)
		}},
		{"second claim", func(s *mock.MockStore) {
			s.EXPECT().DeleteExpiredIdempotencyKeys(gomock.Any(), gomock.Any()).Return(nil)
			s.EXPECT().ClaimIdempotencyKey(gomock.Any(), gomock.Any()).Return(int64(0), nil)
			s.EXPECT().GetIdempotencyKey(gomock.Any(), gomock.Any()).Return(sqlc.IdempotencyKey{}, domain.ErrNotFound)
			s.EXPECT().ClaimIdempotencyKey(gomock.Any(), gomock.Any()).Return(int64(0), boom)
		}},
	}
	for _, tc := range steps {
		t.Run(tc.name, func(t *testing.T) {
			svc, store := newIdemService(t)
			tc.setup(store)

			_, _, err := svc.Begin(context.Background(), "k1", "/e", "h1")

			assert.ErrorIs(t, err, boom)
		})
	}
}

func TestCompleteAndRelease(t *testing.T) {
	t.Run("complete stores status, body and headers", func(t *testing.T) {
		svc, store := newIdemService(t)
		store.EXPECT().CompleteIdempotencyKey(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, p sqlc.CompleteIdempotencyKeyParams) error {
				assert.Equal(t, "k1", p.Key)
				assert.Equal(t, "/e", p.Endpoint)
				assert.EqualValues(t, 201, *p.ResponseStatus)
				assert.Equal(t, `{"x":1}`, *p.ResponseBody)
				assert.JSONEq(t, `{"Location":"/x"}`, string(p.ResponseHeaders))
				return nil
			})

		err := svc.Complete(context.Background(), "k1", "/e", Stored{Status: 201, Body: []byte(`{"x":1}`), Headers: map[string]string{"Location": "/x"}})

		assert.NoError(t, err)
	})
	t.Run("errors are wrapped", func(t *testing.T) {
		svc, store := newIdemService(t)
		boom := errors.New("boom")
		store.EXPECT().CompleteIdempotencyKey(gomock.Any(), gomock.Any()).Return(boom)
		store.EXPECT().ReleaseIdempotencyKey(gomock.Any(), sqlc.ReleaseIdempotencyKeyParams{Key: "k1", Endpoint: "/e"}).Return(boom)

		assert.ErrorIs(t, svc.Complete(context.Background(), "k1", "/e", Stored{Status: 200}), boom)
		assert.ErrorIs(t, svc.Release(context.Background(), "k1", "/e"), boom)
	})
}
