package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// apiPrefix is the versioned base path for every route.
const apiPrefix = "/api/v2"

// Routes builds the router. Gin's router is a fork of httprouter, so the
// book's httprouter patterns carry over. Let's Go Further ch. 2.5.
func (s *Server) Routes() http.Handler {
	r := gin.New()

	// Return JSON 405s instead of falling through to 404.
	r.HandleMethodNotAllowed = true
	r.NoRoute(s.notFoundResponse)
	r.NoMethod(s.methodNotAllowedResponse)

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

	return r
}
