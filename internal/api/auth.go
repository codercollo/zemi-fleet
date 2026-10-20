package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/codercollo/zemi-fleet/internal/config"
)

const (
	apiKeyHeader = "X-API-Key"
	// authChallenge tells a client how to authenticate (RFC 9110 section 11.6.1). It is on every 401.
	authChallenge = `ApiKey realm="zemi-fleet"`
	// authFailedMessage is the same text for a missing key and a wrong one, so a client cannot tell them apart.
	authFailedMessage = "invalid or missing API key"
	// noKeyFingerprint is logged when the request carried no key at all.
	noKeyFingerprint = "none"
)

// publicRoutes are the GET routes that need no key: the load balancers' health check and the API documentation
// (the docs describe the API; they hold no data). Every other route, /debug/vars included, is protected.
var publicRoutes = map[string]bool{apiPrefix + "/healthz": true, docsPath: true, specPath: true}

type clientNameKey struct{}

// clientFromContext returns the name of the authenticated API client in ctx, or "" if the request is not authenticated.
func clientFromContext(ctx context.Context) string {
	name, _ := ctx.Value(clientNameKey{}).(string)
	return name
}

// requireAPIKey rejects every request that does not carry a valid X-API-Key, except the public routes (GET /healthz, which load
// balancers poll, and the documentation pages). It sits after rateLimit, so guessing keys is rate-limited, and before idempotency and the handlers, so
// an unauthenticated request never reaches a service. Let's Go Further ch. 16 (authentication middleware).
//
// The key is hashed with SHA-256 and the hash is compared with every stored hash using
// subtle.ConstantTimeCompare, without stopping at the first match: the time taken depends neither on which key
// matched nor on how many leading bytes were right.
func (s *Server) requireAPIKey() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodGet && publicRoutes[c.FullPath()] {
			c.Next()
			return
		}

		if (s.cfg.Auth.Mode == "token" || s.cfg.Auth.Mode == "both") && c.FullPath() != apiPrefix+"/auth/token" {
			if raw := strings.TrimSpace(c.GetHeader("Authorization")); strings.HasPrefix(raw, "Bearer ") && s.tokens != nil {
				if claims, err := s.tokens.verify(strings.TrimSpace(strings.TrimPrefix(raw, "Bearer ")), s.clock.Now()); err == nil {
					scopes := map[string]bool{}
					for _, scope := range claims.Scopes {
						scopes[scope] = true
					}
					ctx := context.WithValue(c.Request.Context(), tokenContextKey{}, claims.Subject)
					ctx = context.WithValue(ctx, scopeContextKey{}, scopes)
					c.Request = c.Request.WithContext(ctx)
					c.Next()
					return
				}
			}
			if s.cfg.Auth.Mode == "token" {
				c.Writer.Header().Set("WWW-Authenticate", `Bearer realm="zemi-fleet"`)
				c.Abort()
				s.errorResponse(c, http.StatusUnauthorized, "invalid or missing bearer token")
				return
			}
		}

		presented := c.GetHeader(apiKeyHeader)
		sum := config.HashAPIKey(presented)
		client, matched := "", 0
		for _, k := range s.cfg.APIKeys {
			if subtle.ConstantTimeCompare(sum[:], k.Hash[:]) == 1 {
				client, matched = k.Name, 1
			}
		}
		if matched == 0 {
			fingerprint := noKeyFingerprint
			if presented != "" {
				fingerprint = config.Fingerprint(presented)
			}
			// Never the key itself: its fingerprint (8 hex characters of its SHA-256) is enough to correlate attempts.
			s.logger.LogAttrs(c.Request.Context(), slog.LevelWarn, "authentication failed",
				slog.String("key_fingerprint", fingerprint),
				slog.String("client_ip", c.ClientIP()),
				slog.String("method", c.Request.Method),
				slog.String("route", routeOf(c)),
			)
			c.Writer.Header().Set("WWW-Authenticate", authChallenge)
			c.Abort()
			s.errorResponse(c, http.StatusUnauthorized, authFailedMessage)
			return
		}

		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), clientNameKey{}, client))
		c.Next()
	}
}

func (s *Server) requireScope(scope string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if s.cfg.Auth.Mode == "apikey" || tokenSubject(c.Request.Context()) == "" || tokenScopes(c.Request.Context())["dispatcher"] || tokenScopes(c.Request.Context())[scope] {
			c.Next()
			return
		}
		c.Abort()
		s.errorResponse(c, http.StatusForbidden, "insufficient token scope")
	}
}

func (s *Server) requireRiderScope(scope string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if s.cfg.Auth.Mode == "apikey" || tokenSubject(c.Request.Context()) == "" || tokenScopes(c.Request.Context())["dispatcher"] {
			c.Next()
			return
		}
		if !tokenScopes(c.Request.Context())[scope] {
			c.Abort()
			s.errorResponse(c, http.StatusForbidden, "insufficient token scope")
			return
		}
		want := c.Param("id")
		if want == "" {
			body, err := io.ReadAll(c.Request.Body)
			if err != nil {
				c.Abort()
				s.errorResponse(c, http.StatusBadRequest, "invalid request body")
				return
			}
			c.Request.Body = io.NopCloser(strings.NewReader(string(body)))
			var v struct {
				RiderID string `json:"rider_id"`
			}
			_ = json.Unmarshal(body, &v)
			want = v.RiderID
		}
		if want != tokenSubject(c.Request.Context()) {
			c.Abort()
			s.errorResponse(c, http.StatusForbidden, "rider does not match token subject")
			return
		}
		c.Next()
	}
}
