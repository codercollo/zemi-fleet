package api

import (
	"net/http"
	"strings"

	"github.com/codercollo/zemi-fleet/internal/validator"
	"github.com/gin-gonic/gin"
)

type tokenRequest struct {
	RiderID  string   `json:"rider_id"`
	FullName string   `json:"full_name"`
	Phone    string   `json:"phone"`
	Scopes   []string `json:"scopes"`
}

func (s *Server) issueTokenHandler(c *gin.Context) {
	if s.tokens == nil || s.identity == nil {
		s.notFoundResponse(c)
		return
	}
	var req tokenRequest
	if err := readJSON(c, &req); err != nil {
		s.badRequestResponse(c, err)
		return
	}
	v := validator.New()
	req.RiderID = strings.TrimSpace(req.RiderID)
	req.FullName = strings.TrimSpace(req.FullName)
	req.Phone = strings.TrimSpace(req.Phone)
	checkID(v, "rider_id", req.RiderID, riderID)
	v.Check(req.FullName != "", "full_name", "must be provided")
	v.Check(req.Phone != "", "phone", "must be provided")
	// The development verifier can prove only a rider identity. Dispatcher
	// issuance must be supplied by a production IdentityVerifier.
	allowed := map[string]bool{"batches:accept": true, "riders:write": true, "orders:create": true}
	if len(req.Scopes) == 0 {
		req.Scopes = []string{"batches:accept", "riders:write"}
	}
	for _, scope := range req.Scopes {
		v.Check(allowed[scope], "scopes", "contains an unsupported scope")
	}
	if !v.Valid() {
		s.failedValidationResponse(c, v.Errors)
		return
	}
	subject, err := s.identity.Verify(c.Request.Context(), IdentityRequest{RiderID: req.RiderID, FullName: req.FullName, Phone: req.Phone})
	if err != nil {
		s.errorResponse(c, http.StatusUnauthorized, "identity verification failed")
		return
	}
	token, err := s.tokens.issue(subject, req.Scopes, s.clock.Now())
	if err != nil {
		s.serverErrorResponse(c, err)
		return
	}
	if err := writeJSON(c, http.StatusCreated, envelope{"token": token, "token_type": "Bearer", "expires_in": int(s.cfg.Auth.TokenTTL.Seconds())}, nil); err != nil {
		s.serverErrorResponse(c, err)
	}
}
