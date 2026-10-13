package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"

	"github.com/gin-gonic/gin"

	"github.com/codercollo/zemi-fleet/internal/logging"
)

const (
	requestIDHeader = "X-Request-ID"
	// requestIDBytes of randomness make a generated ID (32 hex characters).
	requestIDBytes = 16
	// unmatchedRoute is the route label in the access log for a request no route matched (404 and 405).
	unmatchedRoute = "unmatched"
)

// requestIDPattern is what an incoming X-Request-ID must look like to be believed: 1 to 64 characters from a safe
// set. Anything else (too long, spaces, quotes, newlines, Unicode) is replaced, never echoed.
var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// requestID gives every request an ID: the client's X-Request-ID if it is safe, otherwise a fresh random one.
// The ID is echoed in the response and stored in the request context, where logging picks it up.
func (s *Server) requestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(requestIDHeader)
		if !requestIDPattern.MatchString(id) {
			id = newRequestID()
		}
		c.Writer.Header().Set(requestIDHeader, id)
		c.Request = c.Request.WithContext(logging.WithRequestID(c.Request.Context(), id))
		c.Next()
	}
}

func newRequestID() string {
	b := make([]byte, requestIDBytes)
	if _, err := rand.Read(b); err != nil {
		// The OS entropy source failing is not worth failing a request over; the ID is for tracing, not security.
		return "unavailable"
	}
	return hex.EncodeToString(b)
}

// securityHeaders sets the headers every response carries, errors and 404s included.
//
//	X-Content-Type-Options: nosniff   browsers must not guess a content type
//	Cache-Control: no-store           responses hold per-rider data and are never cached
//	Server:                           removed; the stack is not advertised
func (s *Server) securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")
		h.Del("Server")
		c.Next()
	}
}

// accessLog writes one line per request, after it has been answered: method, route TEMPLATE (/riders/:id, never
// the raw path, which carries IDs and could carry anything), status, duration, bytes written, request ID and client IP.
// The message is fixed; every value is an attribute. The line is written from a deferred call so a request that
// panics (and is turned into a 500 by recoverPanic, which sits outside this middleware) is logged too, as a 500.
func (s *Server) accessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := s.clock.Now()
		completed := false
		defer func() {
			status := c.Writer.Status()
			if !completed {
				status = http.StatusInternalServerError
			}
			s.logger.LogAttrs(c.Request.Context(), slog.LevelInfo, "request",
				slog.String("method", c.Request.Method),
				slog.String("route", routeOf(c)),
				slog.Int("status", status),
				slog.Int64("duration_ms", s.clock.Now().Sub(start).Milliseconds()),
				slog.Int("bytes", max(c.Writer.Size(), 0)),
				slog.String("client_ip", c.ClientIP()),
			)
		}()
		c.Next()
		completed = true
	}
}

// recoverPanic turns a panic in any handler into a logged error and the generic 500, instead of a dropped connection
// and a stack trace on stderr. Connection: close tells the client not to reuse the connection, which may be in an
// unknown state. It is the only recovery middleware: the router is built with gin.New(), never gin.Default().
// Let's Go Further ch. 10.
func (s *Server) recoverPanic() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if v := recover(); v != nil {
				c.Writer.Header().Set("Connection", "close")
				s.logger.LogAttrs(c.Request.Context(), slog.LevelError, "panic recovered",
					slog.String("panic", logging.ScrubPhones(fmt.Sprint(v))),
					slog.String("method", c.Request.Method),
					slog.String("route", routeOf(c)),
					slog.String("stack", string(debug.Stack())),
				)
				c.Abort()
				s.errorResponse(c, http.StatusInternalServerError,
					"the server encountered a problem and could not process your request")
			}
		}()
		c.Next()
	}
}

func routeOf(c *gin.Context) string {
	if r := c.FullPath(); r != "" {
		return r
	}
	return unmatchedRoute
}
