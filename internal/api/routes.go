package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// apiPrefix is the versioned base path for every route.
const apiPrefix = "/api/v2"

// Routes builds the router. Gin's router is a fork of httprouter, so the
// book's httprouter patterns carry over. Let's Go Further ch. 2.5.
func (s *Server) Routes() http.Handler { return s.router() }

// router builds the engine. extra lets a test register routes (one that panics, one that is slow) that sit behind
// the real middleware chain; production passes none.
//
// MIDDLEWARE ORDER (outermost first), and why:
//
//	recoverPanic     outermost, so a panic in ANY later middleware or handler becomes a logged 500, not a dropped connection
//	requestID        next, so everything after (logs, errors) can carry the ID
//	securityHeaders  before anything can answer, so even a 429 or a 404 carries nosniff and no-store
//	accessLog        wraps the rest, so the line records the final status (rate-limited and panicked requests too)
//	rateLimit        before any work is done for the client; /healthz is exempt
//	requireAPIKey    after the limiter, so guessing keys is rate-limited; only GET /healthz is exempt; before any handler
//	                 or idempotency, so an unauthenticated request never reaches a service
//	handlers         the idempotency middleware sits on its two POST routes only, after everything above
//
// The engine comes from gin.New(), never gin.Default(): Default adds Gin's own Logger and Recovery, and we have
// exactly one of each, ours.
func (s *Server) router(extra ...func(*gin.Engine)) *gin.Engine {
	r := gin.New()

	// Trust X-Forwarded-For only from configured proxies. Empty (the default) trusts none, so ClientIP() is the TCP
	// peer and cannot be forged: no spoofed address, no choosing your own rate-limit bucket.
	if err := r.SetTrustedProxies(append([]string{}, s.cfg.Limiter.TrustedProxies...)); err != nil {
		panic(err) // config validation already rejected unparsable entries
	}

	// Return JSON 405s instead of falling through to 404.
	r.HandleMethodNotAllowed = true
	r.NoRoute(s.notFoundResponse)
	r.NoMethod(s.methodNotAllowedResponse)

	r.Use(s.recoverPanic(), s.requestID(), s.securityHeaders(), s.accessLog(), s.rateLimit(), s.requireAPIKey())

	v2 := r.Group(apiPrefix)
	v2.GET("/healthz", s.healthzHandler)

	// POST /orders and POST /orders/batch honour an Idempotency-Key header (see idempotency.go).
	v2.POST("/orders", s.idempotent(), s.createOrderHandler)
	v2.POST("/orders/batch", s.idempotent(), s.createBatchesHandler)
	v2.POST("/orders/match-riders", s.matchRidersHandler)
	v2.GET("/orders/:id", s.showOrderHandler)

	// Static path next to the :id routes: Gin matches it first. It is a POST, which has no :id sibling.
	v2.POST("/riders/match-orders", s.matchOrdersHandler)
	v2.GET("/riders/:id", s.showRiderHandler)
	v2.PUT("/riders/:id/preferences", s.updatePreferencesHandler)
	v2.PATCH("/riders/:id/location", s.updateLocationHandler)
	v2.PATCH("/riders/:id/availability", s.updateAvailabilityHandler)

	v2.GET("/batches", s.listBatchesHandler)
	v2.POST("/batches/:id/accept", s.acceptBatchHandler)

	for _, register := range extra {
		register(r)
	}
	return r
}
