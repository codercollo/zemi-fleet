package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func (s *Server) failedWebhookDeliveriesHandler(c *gin.Context) {
	if s.webhookStore == nil || !s.cfg.Webhooks.Enabled {
		c.JSON(http.StatusNotFound, gin.H{"error": "webhooks disabled"})
		return
	}
	rows, err := s.webhookStore.ListFailed(c.Request.Context(), 100, 0)
	if err != nil {
		s.serverErrorResponse(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"deliveries": rows})
}

func (s *Server) retryWebhookDeliveryHandler(c *gin.Context) {
	if s.webhookStore == nil || !s.cfg.Webhooks.Enabled {
		c.JSON(http.StatusNotFound, gin.H{"error": "webhooks disabled"})
		return
	}
	if err := s.webhookStore.Retry(c.Request.Context(), c.Param("id"), s.clock.Now()); err != nil {
		s.serverErrorResponse(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
