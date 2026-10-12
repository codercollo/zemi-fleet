package api

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

// logError logs the failure once, at the edge, with enough request context to trace it.
func (s *Server) logError(c *gin.Context, err error) {
	s.logger.Error(err.Error(), "method", c.Request.Method, "uri", c.Request.URL.RequestURI())
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
func (s *Server) failedValidationResponse(c *gin.Context, errors map[string]string) {
	s.errorResponse(c, http.StatusUnprocessableEntity, errors)
}
