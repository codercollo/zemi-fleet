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

	v2.POST("/orders", s.createOrderHandler)
	v2.GET("/orders/:id", s.showOrderHandler)

	v2.GET("/riders/:id", s.showRiderHandler)
	v2.PUT("/riders/:id/preferences", s.updatePreferencesHandler)
	v2.PATCH("/riders/:id/location", s.updateLocationHandler)
	v2.PATCH("/riders/:id/availability", s.updateAvailabilityHandler)

	return r
}
