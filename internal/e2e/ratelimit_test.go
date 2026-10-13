//go:build e2e

package e2e

import (
	"io"
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario 8: the production middleware chain on the real stack: request ID, security headers, rate limit.
func TestE2E_RateLimitHeadersAndRequestID(t *testing.T) {
	// 5 requests may arrive at once, then 1 per second. The test clock does not move, so there is no refill.
	e := newEnv(t, "LIMITER_ENABLED", "true", "LIMITER_RPS", "1", "LIMITER_BURST", "5")

	do := func(path string, headers ...string) (*http.Response, string) {
		req, err := http.NewRequest(http.MethodGet, e.server.URL+path, nil)
		require.NoError(t, err)
		req.Header.Set("X-API-Key", testAPIKey)
		for i := 0; i+1 < len(headers); i += 2 {
			req.Header.Set(headers[i], headers[i+1])
		}
		res, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		e.mu.Lock()
		e.captured = append(e.captured, string(body))
		e.mu.Unlock()
		return res, string(body)
	}

	// A burst of 5 is served; the request ID is echoed and every response carries the security headers.
	for i := range 5 {
		res, _ := do("/api/v2/riders/rider_nobody", "X-Request-ID", "e2e-req-"+strconv.Itoa(i))
		assert.Equal(t, http.StatusNotFound, res.StatusCode, "an unknown rider, but served: not limited")
		assert.Equal(t, "e2e-req-"+strconv.Itoa(i), res.Header.Get("X-Request-ID"))
		assert.Equal(t, "nosniff", res.Header.Get("X-Content-Type-Options"))
		assert.Equal(t, "no-store", res.Header.Get("Cache-Control"))
		assert.Empty(t, res.Header.Values("Server"))
	}

	// The sixth is refused, whatever the client claims its address is.
	res, body := do("/api/v2/riders/rider_nobody", "X-Forwarded-For", "203.0.113.77", "X-Request-ID", "limited-1")
	assert.Equal(t, http.StatusTooManyRequests, res.StatusCode)
	assert.JSONEq(t, `{"error":"rate limit exceeded"}`, body)
	assert.Equal(t, "1", res.Header.Get("Retry-After"))
	assert.Equal(t, "limited-1", res.Header.Get("X-Request-ID"))
	assert.Equal(t, "nosniff", res.Header.Get("X-Content-Type-Options"), "even a 429 carries the security headers")

	// /healthz is exempt, and a malformed request ID is replaced.
	for range 20 {
		res, _ := do("/api/v2/healthz")
		assert.Equal(t, http.StatusOK, res.StatusCode)
	}
	res, _ = do("/api/v2/healthz", "X-Request-ID", "bad id with spaces")
	assert.NotEqual(t, "bad id with spaces", res.Header.Get("X-Request-ID"))
	assert.Regexp(t, `^[0-9a-f]{32}$`, res.Header.Get("X-Request-ID"))
}
