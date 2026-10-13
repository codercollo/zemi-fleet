package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codercollo/zemi-fleet/internal/config"
)

type fakeDB struct{ err error }

func (f fakeDB) Ping(context.Context) error { return f.err }

func newTestServer(db Pinger) *Server {
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewServer(config.Config{Env: config.EnvDevelopment}, logger, db, nil, nil, nil, nil, nil, "test")
}

func do(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestHealthz(t *testing.T) {
	tests := []struct {
		name       string
		dbErr      error
		wantStatus int
		wantDB     string
	}{
		{"database up", nil, http.StatusOK, `"database":"up"`},
		{"database down", errors.New("boom"), http.StatusServiceUnavailable, `"database":"down"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestServer(fakeDB{err: tc.dbErr}).Routes()

			rec := do(t, h, http.MethodGet, "/api/v2/healthz")

			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
			assert.Contains(t, rec.Body.String(), tc.wantDB)
			assert.Contains(t, rec.Body.String(), `"version":"test"`)
		})
	}
}

func TestJSONNotFoundAndMethodNotAllowed(t *testing.T) {
	h := newTestServer(fakeDB{}).Routes()

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{"unknown route", http.MethodGet, "/api/v2/nope", http.StatusNotFound},
		{"wrong method", http.MethodPost, "/api/v2/healthz", http.StatusMethodNotAllowed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, h, tc.method, tc.path)

			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
			assert.Contains(t, rec.Body.String(), `"error"`)
		})
	}
}

func TestReadJSON(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"valid", `{"name":"a"}`, ""},
		{"empty body", ``, "body must not be empty"},
		{"malformed", `{"name":`, "badly-formed JSON"},
		{"wrong type", `{"name":1}`, `incorrect JSON type for field "name"`},
		{"unknown field", `{"nope":1}`, "unknown key"},
		{"two values", `{"name":"a"}{"name":"b"}`, "single JSON value"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))

			var p payload
			err := readJSON(c, &p)

			if tc.wantErr == "" {
				require.NoError(t, err)
				assert.Equal(t, "a", p.Name)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestErrorResponses(t *testing.T) {
	s := newTestServer(fakeDB{})

	tests := []struct {
		name       string
		respond    func(c *gin.Context)
		wantStatus int
		wantBody   string
	}{
		{
			"bad request",
			func(c *gin.Context) { s.badRequestResponse(c, errors.New("nope")) },
			http.StatusBadRequest, `{"error":"nope"}`,
		},
		{
			"failed validation",
			func(c *gin.Context) {
				s.failedValidationResponse(c, map[string]string{"lat": "must be between -90 and 90"})
			},
			http.StatusUnprocessableEntity, `{"error":{"lat":"must be between -90 and 90"}}`,
		},
		{
			"server error hides internals",
			func(c *gin.Context) { s.serverErrorResponse(c, errors.New("secret detail")) },
			http.StatusInternalServerError, "the server encountered a problem",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

			tc.respond(c)

			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.Contains(t, rec.Body.String(), tc.wantBody)
			assert.NotContains(t, rec.Body.String(), "secret detail")
		})
	}
}
