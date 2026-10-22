package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/codercollo/zemi-fleet/internal/validator"
)

type lifecycleReasonRequest struct {
	ReasonCode string `json:"reason_code"`
}

func (s *Server) completeBatchStopHandler(c *gin.Context) {
	batchID, ok := s.pathID(c, batchID)
	if !ok {
		return
	}
	seq, err := strconv.Atoi(c.Param("seq"))
	if err != nil || seq < 1 || seq > 10 {
		s.notFoundResponse(c)
		return
	}
	order, err := s.lifecycle.CompleteStop(c.Request.Context(), batchID, int16(seq))
	if err != nil {
		s.handleServiceError(c, err)
		return
	}
	if err := writeJSON(c, http.StatusOK, envelope{"order": newOrderDTO(order)}, nil); err != nil {
		s.serverErrorResponse(c, err)
	}
}

func (s *Server) recordAttemptHandler(c *gin.Context) {
	orderID, ok := s.pathID(c, orderID)
	if !ok {
		return
	}
	var req lifecycleReasonRequest
	if err := readJSON(c, &req); err != nil {
		s.badRequestResponse(c, err)
		return
	}
	v := validator.New()
	req.ReasonCode = strings.TrimSpace(req.ReasonCode)
	checkReasonCode(v, "reason_code", req.ReasonCode)
	if !v.Valid() {
		s.failedValidationResponse(c, v.Errors)
		return
	}
	order, err := s.lifecycle.RecordAttempt(c.Request.Context(), orderID, req.ReasonCode)
	if err != nil {
		s.handleServiceError(c, err)
		return
	}
	if err := writeJSON(c, http.StatusOK, envelope{"order": newOrderDTO(order)}, nil); err != nil {
		s.serverErrorResponse(c, err)
	}
}

func (s *Server) cancelOrderHandler(c *gin.Context) {
	orderID, ok := s.pathID(c, orderID)
	if !ok {
		return
	}
	var req lifecycleReasonRequest
	if err := readJSON(c, &req); err != nil {
		s.badRequestResponse(c, err)
		return
	}
	v := validator.New()
	req.ReasonCode = strings.TrimSpace(req.ReasonCode)
	checkReasonCode(v, "reason_code", req.ReasonCode)
	if !v.Valid() {
		s.failedValidationResponse(c, v.Errors)
		return
	}
	order, err := s.lifecycle.Cancel(c.Request.Context(), orderID, req.ReasonCode)
	if err != nil {
		s.handleServiceError(c, err)
		return
	}
	if err := writeJSON(c, http.StatusOK, envelope{"order": newOrderDTO(order)}, nil); err != nil {
		s.serverErrorResponse(c, err)
	}
}

func checkReasonCode(v *validator.Validator, field, value string) {
	if value == "" {
		v.AddError(field, "must be provided")
		return
	}
	if !reasonCodeFormat.MatchString(value) {
		v.AddError(field, "must be a lowercase reason code")
	}
}
