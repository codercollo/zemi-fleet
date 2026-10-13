package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codercollo/zemi-fleet/internal/config"
)

const (
	opsKey      = "ops-api-key-0123456789abcdef0123"
	wrongKey    = "definitely-the-wrong-key-0123456789"
	nearMissKey = testAPIKey + "-nearly-right"
	pingPath    = "/api/v2/ping"
	healthzPath = "/api/v2/healthz"
)

// twoClients configures two named keys.
func twoClients(c *config.Config) {
	c.APIKeys = []config.APIKey{config.NewAPIKey("postman", testAPIKey), config.NewAPIKey("ops", opsKey)}
}

// request sends a request with the given X-API-Key header value (no header at all when key is "").
func request(h http.Handler, method, path, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if key != "" {
		req.Header.Set(apiKeyHeader, key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRequireAPIKey_Rejections(t *testing.T) {
	tests := []struct{ name, key string }{
		{"a missing key", ""},
		{"a wrong key", wrongKey},
		{"a key that is a prefix of a real one", testAPIKey[:len(testAPIKey)-1]},
		{"a real key with something appended", testAPIKey + "x"},
		{"a real key in another case", "TEST-API-KEY-0123456789ABCDEF"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := newObserved(t)

			rec := request(o.handler(), http.MethodGet, pingPath, tc.key)

			assert.Equal(t, http.StatusUnauthorized, rec.Code)
			assert.JSONEq(t, `{"error":"invalid or missing API key"}`, rec.Body.String())
			assert.Equal(t, `ApiKey realm="zemi-fleet"`, rec.Header().Get("WWW-Authenticate"))
			assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
		})
	}
}

// A client must not be able to tell "no key" from "a key that does not exist".
func TestRequireAPIKey_MissingAndWrongAreIndistinguishable(t *testing.T) {
	o := newObserved(t)
	h := o.handler()

	missing := request(h, http.MethodGet, pingPath, "")
	wrong := request(h, http.MethodGet, pingPath, wrongKey)

	assert.Equal(t, missing.Code, wrong.Code)
	assert.Equal(t, missing.Body.String(), wrong.Body.String())
	assert.Equal(t, missing.Header().Get("WWW-Authenticate"), wrong.Header().Get("WWW-Authenticate"))
	assert.Equal(t, missing.Header().Get("Content-Type"), wrong.Header().Get("Content-Type"))
}

func TestRequireAPIKey_CorrectKeyReachesTheHandlerWithTheClientName(t *testing.T) {
	o := newObserved(t, twoClients)
	var seen []string
	h := o.handler(func(e *gin.Engine) {
		e.GET("/api/v2/whoami", func(c *gin.Context) {
			seen = append(seen, clientFromContext(c.Request.Context()))
			c.String(http.StatusOK, "ok")
		})
	})

	first := request(h, http.MethodGet, "/api/v2/whoami", testAPIKey)
	second := request(h, http.MethodGet, "/api/v2/whoami", opsKey)

	assert.Equal(t, http.StatusOK, first.Code)
	assert.Equal(t, http.StatusOK, second.Code)
	assert.Equal(t, []string{"postman", "ops"}, seen, "each key maps to its own client name")
	assert.Empty(t, first.Header().Get("WWW-Authenticate"))
}

func TestRequireAPIKey_NoConfiguredKeysFailsClosed(t *testing.T) {
	o := newObserved(t, func(c *config.Config) { c.APIKeys = nil })

	rec := request(o.handler(), http.MethodGet, pingPath, testAPIKey)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestRequireAPIKey_OnlyGetHealthzIsExempt(t *testing.T) {
	o := newObserved(t)
	h := o.handler()

	tests := []struct {
		name, method, path string
		want               int
	}{
		{"GET healthz needs no key", http.MethodGet, healthzPath, http.StatusOK},
		{"another method on healthz does", http.MethodDelete, healthzPath, http.StatusUnauthorized},
		{"an ordinary route does", http.MethodGet, pingPath, http.StatusUnauthorized},
		{"a real route does", http.MethodGet, "/api/v2/riders/rider_001", http.StatusUnauthorized},
		{"an unknown route does too, so routes cannot be probed", http.MethodGet, "/api/v2/nope", http.StatusUnauthorized},
		{"a lookalike of healthz does", http.MethodGet, healthzPath + "x", http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, request(h, tc.method, tc.path, "").Code)
		})
	}
}

// Every real route, with its real handler behind it, rejects a missing key before any service is called: the
// gomock Store would fail the test on an unexpected call.
func TestRequireAPIKey_ProtectsEveryRoute(t *testing.T) {
	h, _ := newStoreHandler(t) // no EXPECT(): any Store call fails the test
	routes := []struct{ method, path string }{
		{http.MethodPost, "/api/v2/orders"},
		{http.MethodPost, "/api/v2/orders/batch"},
		{http.MethodPost, "/api/v2/orders/match-riders"},
		{http.MethodGet, "/api/v2/orders/ord_1"},
		{http.MethodPost, "/api/v2/riders/match-orders"},
		{http.MethodGet, "/api/v2/riders/rider_1"},
		{http.MethodPut, "/api/v2/riders/rider_1/preferences"},
		{http.MethodPatch, "/api/v2/riders/rider_1/location"},
		{http.MethodPatch, "/api/v2/riders/rider_1/availability"},
		{http.MethodGet, "/api/v2/batches"},
		{http.MethodPost, "/api/v2/batches/batch_1/accept"},
	}
	for _, r := range routes {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			rec := request(h, r.method, r.path, "")

			assert.Equal(t, http.StatusUnauthorized, rec.Code)
		})
	}
}

// The limiter sits before the key check, so guessing keys is rate-limited.
func TestRequireAPIKey_GuessingIsRateLimited(t *testing.T) {
	o := newObserved(t, limited(1, 2))
	h := o.handler()

	codes := []int{
		request(h, http.MethodGet, pingPath, "guess-one-0123456789abcdef").Code,
		request(h, http.MethodGet, pingPath, "guess-two-0123456789abcdef").Code,
		request(h, http.MethodGet, pingPath, "guess-three-0123456789abcdef").Code,
	}

	assert.Equal(t, []int{http.StatusUnauthorized, http.StatusUnauthorized, http.StatusTooManyRequests}, codes)
}

func TestRequireAPIKey_AccessLogNamesTheClient(t *testing.T) {
	o := newObserved(t, twoClients)
	h := o.handler()

	request(h, http.MethodGet, pingPath, opsKey)
	request(h, http.MethodGet, pingPath, wrongKey)
	request(h, http.MethodGet, healthzPath, "")

	lines := o.byMsg(t, "request")
	require.Len(t, lines, 3)
	assert.Equal(t, "ops", lines[0]["client"])
	assert.NotContains(t, lines[1], "client", "a rejected request has no client")
	assert.NotContains(t, lines[2], "client", "healthz is anonymous")
}

func TestRequireAPIKey_FailedAttemptsLogAFingerprintAndNeverTheKey(t *testing.T) {
	o := newObserved(t)
	h := o.handler()

	req := httptest.NewRequest(http.MethodGet, pingPath, nil)
	req.RemoteAddr = "203.0.113.9:5555"
	req.Header.Set(apiKeyHeader, wrongKey)
	h.ServeHTTP(httptest.NewRecorder(), req)
	request(h, http.MethodGet, pingPath, nearMissKey) // a near miss of the real key
	request(h, http.MethodGet, pingPath, "")
	request(h, http.MethodGet, pingPath, testAPIKey) // a success: the real key is in play, and must not be logged either

	warns := o.byMsg(t, "authentication failed")
	require.Len(t, warns, 3, "one Warn per failed attempt, none for the success")
	for _, w := range warns {
		assert.Equal(t, "WARN", w["level"])
	}
	assert.Equal(t, config.Fingerprint(wrongKey), warns[0]["key_fingerprint"])
	assert.Len(t, warns[0]["key_fingerprint"], 8)
	assert.Equal(t, "203.0.113.9", warns[0]["client_ip"])
	assert.Equal(t, "none", warns[2]["key_fingerprint"], "no key presented")

	all := o.logs.String()
	for _, secret := range []string{wrongKey, testAPIKey, nearMissKey} {
		assert.NotContains(t, all, secret, "a raw key must never reach the logs")
	}
}

func TestRequireAPIKey_LogInjectionViaTheKeyHeader(t *testing.T) {
	o := newObserved(t)
	forged := `x","msg":"request","status":200,"client":"admin`

	request(o.handler(), http.MethodGet, pingPath, forged)

	for _, line := range o.lines(t) { // lines() fails the test unless every line is valid JSON
		assert.NotEqual(t, "admin", line["client"])
	}
	assert.NotContains(t, o.logs.String(), forged)
}
