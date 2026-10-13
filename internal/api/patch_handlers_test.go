package api

import (
	"context"
	"errors"
	"fmt"
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
)

const conflictBody = `{"error":"unable to update the record due to an edit conflict, please try again"}`

// expectPreferencesLoad covers the reads that attach preferences to the response (rider has none).
func expectPreferencesLoad(store *mock.MockStore, id string) {
	store.EXPECT().GetPreferences(gomock.Any(), id).Return(sqlc.RiderPreference{}, domain.ErrNotFound)
	store.EXPECT().ListPreferredCategories(gomock.Any(), id).Return(nil, nil)
	store.EXPECT().ListPreferredAreas(gomock.Any(), id).Return(nil, nil)
}

func TestPatchLocation(t *testing.T) {
	const path = "/api/v2/riders/rider_003/location"

	t.Run("200 applies the ping and bumps the version", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetRider(gomock.Any(), "rider_003").Return(fakeRiderRow("rider_003"), nil) // version 3
		store.EXPECT().UpdateRiderLocation(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, p sqlc.UpdateRiderLocationParams) (sqlc.Rider, error) {
				assert.Equal(t, "rider_003", p.ID)
				assert.EqualValues(t, 3, p.Version, "the UPDATE is conditional on the version that was read")
				assert.Equal(t, -1.3, *p.LastLat)
				assert.Equal(t, 36.9, *p.LastLng)
				assert.Equal(t, 270.5, *p.LastBearingDeg)
				assert.Equal(t, 31.0, *p.LastSpeedKmh)
				row := fakeRiderRow("rider_003")
				row.LastLat, row.LastLng, row.LastBearingDeg, row.LastSpeedKmh = p.LastLat, p.LastLng, p.LastBearingDeg, p.LastSpeedKmh
				row.Version = 4
				return row, nil
			})
		expectPreferencesLoad(store, "rider_003")

		rec := send(t, h, http.MethodPatch, path, `{"latitude":-1.3,"longitude":36.9,"bearing_degrees":270.5,"speed_kmh":31}`)

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		rider := decode(t, rec)["rider"].(map[string]any)
		assert.EqualValues(t, 4, rider["version"])
		assert.InDelta(t, 270.5, rider["bearing_degrees"], 1e-9, "bearings come back without float noise")
		assert.InDelta(t, -1.3, rider["current_location"].(map[string]any)["lat"], 1e-9)
	})

	t.Run("omitted bearing and speed keep their values, explicit zeros are applied", func(t *testing.T) {
		tests := []struct {
			name        string
			body        string
			wantBearing float64
			wantSpeed   float64
		}{
			{"omitted", `{"latitude":-1.3,"longitude":36.9}`, 95, 22}, // fakeRiderRow: bearing 95, speed 22
			{"explicit zeros", `{"latitude":-1.3,"longitude":36.9,"bearing_degrees":0,"speed_kmh":0}`, 0, 0},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				h, store := newStoreHandler(t)
				store.EXPECT().GetRider(gomock.Any(), "rider_003").Return(fakeRiderRow("rider_003"), nil)
				store.EXPECT().UpdateRiderLocation(gomock.Any(), gomock.Any()).DoAndReturn(
					func(_ context.Context, p sqlc.UpdateRiderLocationParams) (sqlc.Rider, error) {
						assert.Equal(t, tc.wantBearing, *p.LastBearingDeg)
						assert.Equal(t, tc.wantSpeed, *p.LastSpeedKmh)
						return fakeRiderRow("rider_003"), nil
					})
				expectPreferencesLoad(store, "rider_003")

				rec := send(t, h, http.MethodPatch, path, tc.body)

				assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			})
		}
	})

	t.Run("200 when X-Expected-Version matches", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetRider(gomock.Any(), "rider_003").Return(fakeRiderRow("rider_003"), nil)
		store.EXPECT().UpdateRiderLocation(gomock.Any(), gomock.Any()).Return(fakeRiderRow("rider_003"), nil)
		expectPreferencesLoad(store, "rider_003")

		rec := send(t, h, http.MethodPatch, path, `{"latitude":-1.3,"longitude":36.9}`, "X-Expected-Version", "3")

		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})

	t.Run("400 for malformed input", func(t *testing.T) {
		tests := []struct {
			name, body string
			headers    []string
		}{
			{"empty body", ``, nil},
			{"bad json", `{"latitude":`, nil},
			{"unknown field", `{"latitude":-1.3,"longitude":36.9,"altitude":10}`, nil},
			{"wrong type", `{"latitude":"north","longitude":36.9}`, nil},
			{"non-numeric version header", `{"latitude":-1.3,"longitude":36.9}`, []string{"X-Expected-Version", "abc"}},
			{"zero version header", `{"latitude":-1.3,"longitude":36.9}`, []string{"X-Expected-Version", "0"}},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				h, _ := newStoreHandler(t)

				rec := send(t, h, http.MethodPatch, path, tc.body, tc.headers...)

				assert.Equal(t, http.StatusBadRequest, rec.Code)
			})
		}
	})

	t.Run("404 for unknown rider", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetRider(gomock.Any(), "rider_003").Return(sqlc.Rider{}, domain.ErrNotFound)

		rec := send(t, h, http.MethodPatch, path, `{"latitude":-1.3,"longitude":36.9}`)

		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("409 when X-Expected-Version is stale, without touching the row", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetRider(gomock.Any(), "rider_003").Return(fakeRiderRow("rider_003"), nil) // version 3

		rec := send(t, h, http.MethodPatch, path, `{"latitude":-1.3,"longitude":36.9}`, "X-Expected-Version", "2")

		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Equal(t, conflictBody, strings.TrimSpace(rec.Body.String()))
	})

	t.Run("409 when another request changed the rider in between", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetRider(gomock.Any(), "rider_003").Return(fakeRiderRow("rider_003"), nil)
		store.EXPECT().UpdateRiderLocation(gomock.Any(), gomock.Any()).Return(sqlc.Rider{}, domain.ErrNotFound) // 0 rows

		rec := send(t, h, http.MethodPatch, path, `{"latitude":-1.3,"longitude":36.9}`)

		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Equal(t, conflictBody, strings.TrimSpace(rec.Body.String()))
	})

	t.Run("422 with per-field messages", func(t *testing.T) {
		tests := []struct{ name, body, wantField string }{
			{"missing latitude", `{"longitude":36.9}`, "latitude"},
			{"missing longitude", `{"latitude":-1.3}`, "longitude"},
			{"latitude too high", `{"latitude":90.1,"longitude":36.9}`, "latitude"},
			{"longitude too low", `{"latitude":-1.3,"longitude":-180.5}`, "longitude"},
			{"bearing 360", `{"latitude":-1.3,"longitude":36.9,"bearing_degrees":360}`, "bearing_degrees"},
			{"negative bearing", `{"latitude":-1.3,"longitude":36.9,"bearing_degrees":-1}`, "bearing_degrees"},
			{"speed too high", `{"latitude":-1.3,"longitude":36.9,"speed_kmh":200.1}`, "speed_kmh"},
			{"negative speed", `{"latitude":-1.3,"longitude":36.9,"speed_kmh":-5}`, "speed_kmh"},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				h, _ := newStoreHandler(t)

				rec := send(t, h, http.MethodPatch, path, tc.body)

				require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
				assert.Contains(t, errFields(t, rec), tc.wantField)
			})
		}
	})

	t.Run("422 boundaries are accepted", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetRider(gomock.Any(), "rider_003").Return(fakeRiderRow("rider_003"), nil)
		store.EXPECT().UpdateRiderLocation(gomock.Any(), gomock.Any()).Return(fakeRiderRow("rider_003"), nil)
		expectPreferencesLoad(store, "rider_003")

		rec := send(t, h, http.MethodPatch, path, `{"latitude":-90,"longitude":180,"bearing_degrees":359.99,"speed_kmh":200}`)

		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})
}

func TestPatchAvailability(t *testing.T) {
	const path = "/api/v2/riders/rider_003/availability"

	t.Run("200 switches availability off", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetRider(gomock.Any(), "rider_003").Return(fakeRiderRow("rider_003"), nil)
		store.EXPECT().UpdateRiderAvailability(gomock.Any(), sqlc.UpdateRiderAvailabilityParams{
			IsAvailable: false, ID: "rider_003", Version: 3,
		}).DoAndReturn(func(_ context.Context, p sqlc.UpdateRiderAvailabilityParams) (sqlc.Rider, error) {
			row := fakeRiderRow("rider_003")
			row.IsAvailable, row.Version = p.IsAvailable, 4
			return row, nil
		})
		expectPreferencesLoad(store, "rider_003")

		rec := send(t, h, http.MethodPatch, path, `{"is_available":false}`)

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		rider := decode(t, rec)["rider"].(map[string]any)
		assert.Equal(t, false, rider["is_available"], "false must not be mistaken for omitted")
		assert.EqualValues(t, 4, rider["version"])
	})

	t.Run("400 for malformed input", func(t *testing.T) {
		tests := []struct {
			name, body string
			headers    []string
		}{
			{"empty body", ``, nil},
			{"wrong type", `{"is_available":"yes"}`, nil},
			{"unknown field", `{"is_available":true,"on_break":true}`, nil},
			{"bad version header", `{"is_available":true}`, []string{"X-Expected-Version", "-4"}},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				h, _ := newStoreHandler(t)

				rec := send(t, h, http.MethodPatch, path, tc.body, tc.headers...)

				assert.Equal(t, http.StatusBadRequest, rec.Code)
			})
		}
	})

	t.Run("404 for unknown rider", func(t *testing.T) {
		h, store := newStoreHandler(t)
		store.EXPECT().GetRider(gomock.Any(), "rider_003").Return(sqlc.Rider{}, domain.ErrNotFound)

		rec := send(t, h, http.MethodPatch, path, `{"is_available":true}`)

		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("409 on a stale header and on a lost race", func(t *testing.T) {
		t.Run("stale header", func(t *testing.T) {
			h, store := newStoreHandler(t)
			store.EXPECT().GetRider(gomock.Any(), "rider_003").Return(fakeRiderRow("rider_003"), nil)

			rec := send(t, h, http.MethodPatch, path, `{"is_available":true}`, "X-Expected-Version", "1")

			assert.Equal(t, http.StatusConflict, rec.Code)
			assert.Equal(t, conflictBody, strings.TrimSpace(rec.Body.String()))
		})
		t.Run("lost race", func(t *testing.T) {
			h, store := newStoreHandler(t)
			store.EXPECT().GetRider(gomock.Any(), "rider_003").Return(fakeRiderRow("rider_003"), nil)
			store.EXPECT().UpdateRiderAvailability(gomock.Any(), gomock.Any()).Return(sqlc.Rider{}, domain.ErrNotFound)

			rec := send(t, h, http.MethodPatch, path, `{"is_available":true}`)

			assert.Equal(t, http.StatusConflict, rec.Code)
		})
	})

	t.Run("422 when is_available is missing", func(t *testing.T) {
		h, _ := newStoreHandler(t)

		rec := send(t, h, http.MethodPatch, path, `{}`)

		require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		assert.Contains(t, errFields(t, rec), "is_available")
	})
}

// A request whose context has a deadline and a store call that only returns when
// that deadline fires: the handler must answer 500 promptly instead of hanging, and
// must not leak the context error to the client.
func TestSlowQueryBecomes500(t *testing.T) {
	h, store := newStoreHandler(t)
	store.EXPECT().GetRider(gomock.Any(), "rider_003").DoAndReturn(
		func(ctx context.Context, _ string) (sqlc.Rider, error) {
			<-ctx.Done() // blocks past the deadline, like a hung query
			return sqlc.Rider{}, fmt.Errorf("query: %w", ctx.Err())
		})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v2/riders/rider_003", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	start := time.Now()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Less(t, time.Since(start), 2*time.Second)
	assert.NotContains(t, rec.Body.String(), "deadline")
	assert.True(t, errors.Is(ctx.Err(), context.DeadlineExceeded))
}
