package api

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"net/http"
	"regexp"

	"github.com/gin-gonic/gin"

	apispec "github.com/codercollo/zemi-fleet/api"
)

const (
	docsPath = "/docs"
	specPath = "/openapi.yaml"
	// cdnOrigin is the only third-party origin the docs page may load anything from: swagger-ui-dist, pinned to an
	// exact version and checked with Subresource Integrity in docs.html.
	cdnOrigin = "https://cdn.jsdelivr.net"
)

//go:embed docs.html
var docsPage []byte

var inlineScript = regexp.MustCompile(`(?s)<script>(.*?)</script>`)

// docsCSP allows the page to run only the pinned CDN's scripts and its own initialisation script (by hash, so no
// 'unsafe-inline' for scripts), to load styles from the CDN (Swagger UI also sets inline style attributes), images from
// itself or data: URIs, and to call only its own origin ("Try it out" and loading the spec). Nothing may frame it.
var docsCSP = buildDocsCSP(docsPage)

func buildDocsCSP(page []byte) string {
	script := inlineScript.FindSubmatch(page)
	sum := sha256.Sum256(script[1]) // the init script is the one <script> without a src attribute
	hash := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	return "default-src 'none'; script-src " + cdnOrigin + " " + hash + "; style-src " + cdnOrigin + " 'unsafe-inline'; " +
		"img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
}

// docsHandler serves the Swagger UI page. The documentation is public (no API key; "Try it out" asks for the key in
// the Authorize dialog) but it still goes through the rate limiter.
func (s *Server) docsHandler(c *gin.Context) {
	c.Writer.Header().Set("Content-Security-Policy", docsCSP)
	c.Data(http.StatusOK, "text/html; charset=utf-8", docsPage)
}

// specHandler serves the embedded OpenAPI document.
func (s *Server) specHandler(c *gin.Context) {
	c.Data(http.StatusOK, "application/yaml; charset=utf-8", apispec.Spec)
}
