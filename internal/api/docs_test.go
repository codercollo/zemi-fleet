package api

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apispec "github.com/codercollo/zemi-fleet/api"
	"github.com/codercollo/zemi-fleet/internal/config"
)

func docsHandlerWith(docs bool, mutate ...func(*config.Config)) http.Handler {
	cfg := testConfig()
	cfg.DocsEnabled = docs
	for _, m := range mutate {
		m(&cfg)
	}
	return NewServer(cfg, discardLogger(), fakeDB{}, Services{}, "test").Routes()
}

// get sends a GET WITHOUT an API key.
func getNoKey(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestDocs_EnabledServesBothWithoutAKey(t *testing.T) {
	h := docsHandlerWith(true)

	page := getNoKey(h, "/docs")
	spec := getNoKey(h, "/openapi.yaml")

	require.Equal(t, http.StatusOK, page.Code)
	assert.Contains(t, page.Header().Get("Content-Type"), "text/html")
	assert.Contains(t, page.Body.String(), "SwaggerUIBundle")
	assert.Contains(t, page.Body.String(), `"/openapi.yaml"`)
	require.Equal(t, http.StatusOK, spec.Code)
	assert.Contains(t, spec.Header().Get("Content-Type"), "yaml")
	assert.Equal(t, string(apispec.Spec), spec.Body.String())
}

func TestDocs_DisabledIs404(t *testing.T) {
	h := docsHandlerWith(false)

	for _, path := range []string{"/docs", "/openapi.yaml"} {
		rec := send(t, h, http.MethodGet, path, "") // with a valid key
		assert.Equal(t, http.StatusNotFound, rec.Code, path)
		assert.NotContains(t, rec.Body.String(), "swagger")
	}
}

func TestDocs_StillRateLimited(t *testing.T) {
	h := docsHandlerWith(true, func(c *config.Config) {
		c.Limiter = config.LimiterConfig{Enabled: true, RPS: 0.001, Burst: 1}
	})

	first := getNoKey(h, "/docs")
	second := getNoKey(h, "/openapi.yaml")

	assert.Equal(t, http.StatusOK, first.Code)
	assert.Equal(t, http.StatusTooManyRequests, second.Code)
}

func TestDocs_OtherRoutesStillNeedTheKey(t *testing.T) {
	h := docsHandlerWith(true)

	for _, path := range []string{"/api/v2/riders/rider_001", "/debug/vars", "/docs/anything", "/openapi.yaml/x"} {
		assert.Equal(t, http.StatusUnauthorized, getNoKey(h, path).Code, path)
	}
}

func TestDocs_ContentSecurityPolicyAllowsOnlyThePinnedCDN(t *testing.T) {
	rec := getNoKey(docsHandlerWith(true), "/docs")
	csp := rec.Header().Get("Content-Security-Policy")

	assert.Contains(t, csp, "default-src 'none'")
	assert.Contains(t, csp, "script-src https://cdn.jsdelivr.net 'sha256-")
	assert.NotContains(t, csp, "script-src 'unsafe", "no unsafe-inline or eval for scripts")
	assert.Contains(t, csp, "frame-ancestors 'none'")
	// Every origin the policy names is the CDN; nothing else may be allowed.
	for _, origin := range regexp.MustCompile(`https?://[^\s;]+`).FindAllString(csp, -1) {
		assert.Equal(t, "https://cdn.jsdelivr.net", origin)
	}
	assert.Equal(t, docsCSP, csp)
}

func TestDocs_PageLoadsOnlyPinnedIntegrityCheckedAssets(t *testing.T) {
	page := string(docsPage)

	urls := regexp.MustCompile(`(?:src|href)="(https?://[^"]+)"`).FindAllStringSubmatch(page, -1)
	require.NotEmpty(t, urls)
	for _, u := range urls {
		assert.Regexp(t, `^https://cdn\.jsdelivr\.net/npm/swagger-ui-dist@\d+\.\d+\.\d+/[a-z.-]+$`, u[1], "pinned to an exact version")
	}
	assert.Equal(t, len(urls), strings.Count(page, `integrity="sha384-`), "every external asset has Subresource Integrity")
}
