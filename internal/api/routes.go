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
//	metrics          (only when METRICS_ENABLED) right after the recovery, so every response is counted: 401s, 429s, panics
//	requestID        next, so everything after (logs, errors) can carry the ID
//	securityHeaders  before anything can answer, so even a 429 or a 404 carries nosniff and no-store
//	enableCORS       before auth and the limiter: a browser sends no API key on a preflight, so a trusted origin's
//	                 preflight is answered here; Vary: Origin is on every response
//	accessLog        wraps the rest, so the line records the final status (rate-limited and panicked requests too)
//	rateLimit        before any work is done for the client; /healthz is exempt
//	requireAPIKey    after the limiter, so guessing keys is rate-limited; only GET /healthz and the docs pages are exempt; before any handler
//	                 or idempotency, so an unauthenticated request never reaches a service
//	idempotency      on its two POST routes only, after everything above
//	handlers
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

	r.Use(s.recoverPanic())
	if s.cfg.MetricsEnabled {
		r.Use(s.metricsMiddleware())
	}
	r.Use(s.requestID(), s.securityHeaders(), s.enableCORS(), s.accessLog(), s.rateLimit(), s.requireAPIKey())

	// Documentation: public (no API key) but rate limited. Disabled means no routes (404).
	if s.cfg.DocsEnabled {
		r.GET(docsPath, s.docsHandler)
		r.GET(specPath, s.specHandler)
	}

	// Behind the same chain as everything else: it needs the API key and is rate limited. Disabled means no route (404).
	if s.cfg.MetricsEnabled {
		r.GET(metricsPath, s.metricsHandler)
	}

	v2 := r.Group(apiPrefix)
	v2.GET("/healthz", s.healthzHandler)
	v2.POST("/auth/token", s.issueTokenHandler)

	// POST /orders and POST /orders/batch honour an Idempotency-Key header (see idempotency.go).
	v2.POST("/orders", s.requireScope("orders:create"), s.idempotent(), s.createOrderHandler)
	v2.POST("/orders/batch", s.requireRiderScope("batches:accept"), s.idempotent(), s.createBatchesHandler)
	v2.POST("/orders/match-riders", s.requireScope("orders:create"), s.matchRidersHandler)
	v2.GET("/orders/:id", s.requireScope("orders:create"), s.showOrderHandler)
	v2.POST("/orders/:id/attempts", s.requireScope("orders:create"), s.idempotent(), s.recordAttemptHandler)
	v2.POST("/orders/:id/cancel", s.requireScope("orders:create"), s.idempotent(), s.cancelOrderHandler)

	// Static path next to the :id routes: Gin matches it first. It is a POST, which has no :id sibling.
	v2.POST("/riders/match-orders", s.requireRiderScope("batches:accept"), s.matchOrdersHandler)
	v2.GET("/riders/:id", s.requireRiderScope("riders:write"), s.showRiderHandler)
	v2.PUT("/riders/:id/preferences", s.requireRiderScope("riders:write"), s.updatePreferencesHandler)
	v2.PATCH("/riders/:id/location", s.requireRiderScope("riders:write"), s.updateLocationHandler)
	v2.PATCH("/riders/:id/availability", s.requireRiderScope("riders:write"), s.updateAvailabilityHandler)

	v2.GET("/batches", s.requireScope("batches:accept"), s.listBatchesHandler)
	v2.POST("/batches/:id/accept", s.requireRiderScope("batches:accept"), s.acceptBatchHandler)
	v2.POST("/batches/:id/stops/:seq/complete", s.requireScope("batches:accept"), s.idempotent(), s.completeBatchStopHandler)
	v2.GET("/webhooks/failed", s.requireScope("dispatcher"), s.failedWebhookDeliveriesHandler)
	v2.POST("/webhooks/:id/retry", s.requireScope("dispatcher"), s.retryWebhookDeliveryHandler)

	for _, register := range extra {
		register(r)
	}
	return r
}
