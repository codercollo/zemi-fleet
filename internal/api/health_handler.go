package api

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// healthCheckTimeout keeps a hung database from hanging the health endpoint.
const healthCheckTimeout = 2 * time.Second

// healthzHandler reports status plus a live database check; 503 when the DB is down.
// Let's Go Further ch. 2.4.
func (s *Server) healthzHandler(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), healthCheckTimeout)
	defer cancel()

	status, dbState := http.StatusOK, "up"
	if err := s.db.Ping(ctx); err != nil {
		s.logError(c, err)
		status, dbState = http.StatusServiceUnavailable, "down"
	}

	data := envelope{
		"status": map[bool]string{true: "available", false: "unavailable"}[status == http.StatusOK],
		"system_info": envelope{
			"environment": s.cfg.Env,
			"version":     s.version,
			"database":    dbState,
		},
	}
	if err := writeJSON(c, status, data, nil); err != nil {
		s.serverErrorResponse(c, err)
	}
}
