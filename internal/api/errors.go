package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/logging"
)

// logError logs the failure once, at the edge. The message is fixed text; the error, which may contain anything
// (a driver message, a value the client sent), goes into an attribute, where the handler encodes it (CWE-117). The
// request ID is added by the logger's context handler. Phone numbers are masked first.
func (s *Server) logError(c *gin.Context, err error) {
	s.logger.LogAttrs(c.Request.Context(), slog.LevelError, "request failed",
		slog.String("error", logging.ScrubPhones(err.Error())),
		slog.String("method", c.Request.Method),
		slog.String("route", routeOf(c)),
	)
}

// errorResponse is the single place error JSON is written.
// Let's Go Further ch. 4.6.
func (s *Server) errorResponse(c *gin.Context, status int, message any) {
	if err := writeJSON(c, status, envelope{"error": message}, nil); err != nil {
		s.logError(c, err)
		c.Writer.WriteHeader(http.StatusInternalServerError)
	}
}

// serverErrorResponse never leaks internals to the client.
func (s *Server) serverErrorResponse(c *gin.Context, err error) {
	s.logError(c, err)
	s.errorResponse(c, http.StatusInternalServerError,
		"the server encountered a problem and could not process your request")
}

func (s *Server) notFoundResponse(c *gin.Context) {
	s.errorResponse(c, http.StatusNotFound, "the requested resource could not be found")
}

func (s *Server) methodNotAllowedResponse(c *gin.Context) {
	s.errorResponse(c, http.StatusMethodNotAllowed,
		fmt.Sprintf("the %s method is not supported for this resource", c.Request.Method))
}

func (s *Server) badRequestResponse(c *gin.Context, err error) {
	s.errorResponse(c, http.StatusBadRequest, err.Error())
}

// failedValidationResponse returns 422 with per-field messages.
func (s *Server) failedValidationResponse(c *gin.Context, fields map[string]string) {
	s.errorResponse(c, http.StatusUnprocessableEntity, fields)
}

// handleServiceError is the one place domain errors become HTTP statuses:
// not found -> 404, edit conflict / batch taken -> 409,
// validation failure / rider at capacity / rider unavailable -> 422, anything else -> 500.
func (s *Server) handleServiceError(c *gin.Context, err error) {
	var validationErr *domain.ValidationError

	switch {
	case errors.Is(err, domain.ErrNotFound):
		s.notFoundResponse(c)
	case errors.Is(err, domain.ErrEditConflict):
		s.errorResponse(c, http.StatusConflict,
			"unable to update the record due to an edit conflict, please try again")
	case errors.Is(err, domain.ErrBatchTaken):
		s.errorResponse(c, http.StatusConflict, "batch is no longer available")
	case errors.Is(err, domain.ErrRiderUnavailable):
		s.errorResponse(c, http.StatusUnprocessableEntity, "rider is not available")
	case errors.Is(err, domain.ErrRiderAtCapacity):
		s.errorResponse(c, http.StatusUnprocessableEntity, "the rider does not have capacity for these orders")
	case errors.As(err, &validationErr):
		s.failedValidationResponse(c, validationErr.Fields)
	default:
		s.serverErrorResponse(c, err)
	}
}
