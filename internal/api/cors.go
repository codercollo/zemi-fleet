package api

import (
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	originHeader         = "Origin"
	requestMethodHeader  = "Access-Control-Request-Method"
	allowOriginHeader    = "Access-Control-Allow-Origin"
	exposeHeadersHeader  = "Access-Control-Expose-Headers"
	allowMethodsHeader   = "Access-Control-Allow-Methods"
	allowHeadersHeader   = "Access-Control-Allow-Headers"
	maxAgeHeader         = "Access-Control-Max-Age"
	corsAllowedMethods   = "GET, POST, PUT, PATCH"
	corsExposedHeaders   = "Location, X-Request-ID, Retry-After, Idempotent-Replay"
	corsAllowedHeaders   = "Content-Type, X-API-Key, Idempotency-Key, X-Request-ID, X-Expected-Version"
	corsPreflightMaxAge  = 10 * time.Minute
	corsPreflightSuccess = http.StatusOK
)

// enableCORS lets browser code on a trusted origin call the API. Let's Go Further ch. 18.
//
//   - Vary: Origin is ALWAYS set, so a cache never serves one origin's answer (with its Allow-Origin) to another.
//     On a preflight Vary: Access-Control-Request-Method is added too.
//   - The origin is echoed back only when it EXACTLY matches CORS_TRUSTED_ORIGINS. Never "*", and no
//     Access-Control-Allow-Credentials: the API authenticates with an X-API-Key header, not cookies, so the browser
//     never needs to send credentials.
//   - A preflight (OPTIONS with Origin and Access-Control-Request-Method) from a trusted origin is answered here with 200
//     and the allowed methods and headers, BEFORE rate limiting and authentication: a browser never attaches the API
//     key to a preflight, so requiring it would make every cross-origin call fail.
//   - A preflight from any other origin gets no CORS headers and falls through to the normal chain (a 401 without a key),
//     so the browser blocks the real request.
//
// With CORS_TRUSTED_ORIGINS empty the only effect is the Vary header.
func (s *Server) enableCORS() gin.HandlerFunc {
	maxAge := strconv.Itoa(int(corsPreflightMaxAge.Seconds()))
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Add("Vary", originHeader)

		origin := c.GetHeader(originHeader)
		preflight := c.Request.Method == http.MethodOptions && c.GetHeader(requestMethodHeader) != ""
		if preflight {
			h.Add("Vary", requestMethodHeader)
		}

		if origin == "" || !slices.Contains(s.cfg.CORS.TrustedOrigins, origin) {
			c.Next()
			return
		}

		h.Set(allowOriginHeader, origin)
		h.Set(exposeHeadersHeader, corsExposedHeaders)
		if preflight {
			h.Set(allowMethodsHeader, corsAllowedMethods)
			h.Set(allowHeadersHeader, corsAllowedHeaders)
			h.Set(maxAgeHeader, maxAge)
			c.AbortWithStatus(corsPreflightSuccess)
			return
		}
		c.Next()
	}
}
