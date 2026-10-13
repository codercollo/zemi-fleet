package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codercollo/zemi-fleet/internal/config"
)

const (
	trustedOrigin   = "http://localhost:3000"
	untrustedOrigin = "http://evil.example"
)

// corsHandler builds the real router with the given trusted origins.
func corsHandler(origins ...string) http.Handler {
	cfg := testConfig()
	cfg.CORS = config.CORSConfig{TrustedOrigins: origins}
	return NewServer(cfg, discardLogger(), fakeDB{}, Services{}, "test").Routes()
}

// preflight sends an OPTIONS request the way a browser does: an Origin and the method it wants to use, and NO API key.
func preflight(h http.Handler, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodOptions, "/api/v2/orders", nil)
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "content-type,x-api-key")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCORS_TrustedOriginGetsHeaders(t *testing.T) {
	h := corsHandler(trustedOrigin)

	rec := send(t, h, http.MethodGet, "/api/v2/healthz", "", "Origin", trustedOrigin)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, trustedOrigin, rec.Header().Get("Access-Control-Allow-Origin"), "the origin itself, never *")
	assert.Equal(t, "Location, X-Request-ID, Retry-After, Idempotent-Replay", rec.Header().Get("Access-Control-Expose-Headers"))
	assert.Empty(t, rec.Header().Get("Access-Control-Allow-Credentials"), "API keys, not cookies")
	assert.Contains(t, rec.Header().Values("Vary"), "Origin")
}

func TestCORS_UntrustedOriginGetsNoHeaders(t *testing.T) {
	tests := []struct{ name, origin string }{
		{"another site", untrustedOrigin},
		{"same host, other port", "http://localhost:3001"},
		{"same host, other scheme", "https://localhost:3000"},
		{"a prefix of the trusted origin", "http://localhost:300"},
		{"the trusted origin with a suffix", "http://localhost:3000.evil.example"},
		{"different case", "HTTP://LOCALHOST:3000"},
		{"the literal null origin", "null"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := send(t, corsHandler(trustedOrigin), http.MethodGet, "/api/v2/healthz", "", "Origin", tc.origin)

			assert.Equal(t, http.StatusOK, rec.Code, "the request itself still works: CORS is the browser's rule")
			for _, name := range []string{"Access-Control-Allow-Origin", "Access-Control-Expose-Headers", "Access-Control-Allow-Methods"} {
				assert.Empty(t, rec.Header().Get(name), name)
			}
			assert.Contains(t, rec.Header().Values("Vary"), "Origin")
		})
	}
}

func TestCORS_PreflightFromTrustedOriginNeedsNoKey(t *testing.T) {
	rec := preflight(corsHandler(trustedOrigin), trustedOrigin)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, trustedOrigin, rec.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "GET, POST, PUT, PATCH", rec.Header().Get("Access-Control-Allow-Methods"))
	assert.Equal(t, "Content-Type, X-API-Key, Idempotency-Key, X-Request-ID, X-Expected-Version", rec.Header().Get("Access-Control-Allow-Headers"))
	assert.Equal(t, "600", rec.Header().Get("Access-Control-Max-Age"))
	assert.Contains(t, rec.Header().Values("Vary"), "Origin")
	assert.Contains(t, rec.Header().Values("Vary"), "Access-Control-Request-Method")
	assert.Empty(t, rec.Header().Get("WWW-Authenticate"))
	assert.Empty(t, rec.Body.String())
}

func TestCORS_PreflightFromUntrustedOriginFallsThroughTo401(t *testing.T) {
	rec := preflight(corsHandler(trustedOrigin), untrustedOrigin)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.NotEmpty(t, rec.Header().Get("WWW-Authenticate"))
	for _, name := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Methods", "Access-Control-Allow-Headers", "Access-Control-Max-Age", "Access-Control-Expose-Headers"} {
		assert.Empty(t, rec.Header().Get(name), name)
	}
	assert.Contains(t, rec.Header().Values("Vary"), "Origin")
	assert.Contains(t, rec.Header().Values("Vary"), "Access-Control-Request-Method")
}

func TestCORS_OPTIONSWithoutRequestMethodIsNotAPreflight(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "/api/v2/orders", nil)
	req.Header.Set("Origin", trustedOrigin)
	rec := httptest.NewRecorder()

	corsHandler(trustedOrigin).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code, "it takes the normal path, which needs a key")
	assert.Empty(t, rec.Header().Get("Access-Control-Allow-Methods"))
	assert.NotContains(t, rec.Header().Values("Vary"), "Access-Control-Request-Method")
}

func TestCORS_VaryOriginIsAlwaysPresent(t *testing.T) {
	tests := []struct {
		name    string
		origins []string
		origin  string
	}{
		{"no origins configured, no Origin header", nil, ""},
		{"no origins configured, with an Origin", nil, trustedOrigin},
		{"configured, no Origin header", []string{trustedOrigin}, ""},
		{"configured, trusted", []string{trustedOrigin}, trustedOrigin},
		{"configured, untrusted", []string{trustedOrigin}, untrustedOrigin},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var headers []string
			if tc.origin != "" {
				headers = []string{"Origin", tc.origin}
			}

			rec := send(t, corsHandler(tc.origins...), http.MethodGet, "/api/v2/healthz", "", headers...)

			assert.Contains(t, rec.Header().Values("Vary"), "Origin")
		})
	}
}

func TestCORS_EmptyConfigAddsNoCORSHeaders(t *testing.T) {
	h := corsHandler()

	for name, rec := range map[string]*httptest.ResponseRecorder{
		"a request from a browser": send(t, h, http.MethodGet, "/api/v2/healthz", "", "Origin", trustedOrigin),
		"a preflight":              preflight(h, trustedOrigin),
	} {
		t.Run(name, func(t *testing.T) {
			for key := range rec.Header() {
				assert.False(t, strings.HasPrefix(key, "Access-Control-"), "unexpected header %s", key)
			}
		})
	}
	assert.Equal(t, http.StatusUnauthorized, preflight(h, trustedOrigin).Code)
}

func TestCORS_SeveralTrustedOriginsEachEchoedBack(t *testing.T) {
	h := corsHandler(trustedOrigin, "https://app.zemi.africa")

	for _, origin := range []string{trustedOrigin, "https://app.zemi.africa"} {
		rec := send(t, h, http.MethodGet, "/api/v2/healthz", "", "Origin", origin)
		assert.Equal(t, origin, rec.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestCORS_HeadersAlsoOnErrorResponses(t *testing.T) {
	rec := send(t, corsHandler(trustedOrigin), http.MethodGet, "/api/v2/nowhere", "", "Origin", trustedOrigin)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, trustedOrigin, rec.Header().Get("Access-Control-Allow-Origin"), "so browser code can read the error")
}
