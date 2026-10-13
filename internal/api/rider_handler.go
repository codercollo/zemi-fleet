package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/validator"
)

// showRiderHandler handles GET /api/v2/riders/:id (preferences included).
func (s *Server) showRiderHandler(c *gin.Context) {
	rider, err := s.riders.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		s.handleServiceError(c, err)
		return
	}
	if err := writeJSON(c, http.StatusOK, envelope{"rider": newRiderDTO(rider)}, nil); err != nil {
		s.serverErrorResponse(c, err)
	}
}

// updatePreferencesRequest is the PUT body. PUT replaces everything, so an omitted
// list means "no preference" (matches all) and an omitted minimum means 0.
type updatePreferencesRequest struct {
	MinOrderValueKES    *float64 `json:"min_order_value_kes"`
	PreferredCategories []string `json:"preferred_categories"`
	PreferredAreas      []string `json:"preferred_areas"`
}

// updatePreferencesHandler handles PUT /api/v2/riders/:id/preferences.
func (s *Server) updatePreferencesHandler(c *gin.Context) {
	var req updatePreferencesRequest
	if err := readJSON(c, &req); err != nil {
		s.badRequestResponse(c, err)
		return
	}

	v := validator.New()
	var minCents int64
	if req.MinOrderValueKES != nil {
		cents, ok := kesToCents(*req.MinOrderValueKES)
		switch {
		case !ok || *req.MinOrderValueKES > maxAmountKES:
			v.AddError("min_order_value_kes", "must be a valid amount with at most 2 decimal places")
		case cents < 0:
			v.AddError("min_order_value_kes", "must not be negative")
		}
		minCents = cents
	}
	checkIDList(v, "preferred_categories", req.PreferredCategories)
	checkIDList(v, "preferred_areas", req.PreferredAreas)
	if !v.Valid() {
		s.failedValidationResponse(c, v.Errors)
		return
	}

	rider, err := s.riders.UpdatePreferences(c.Request.Context(), c.Param("id"), domain.Preferences{
		MinOrderValueCents: minCents,
		Categories:         req.PreferredCategories,
		AreaIDs:            req.PreferredAreas,
	})
	if err != nil {
		s.handleServiceError(c, err)
		return
	}
	if err := writeJSON(c, http.StatusOK, envelope{"preferences": newPreferencesDTO(rider.Preferences)}, nil); err != nil {
		s.serverErrorResponse(c, err)
	}
}

// checkIDList rejects blank, over-long and duplicate entries; existence is checked by the service.
func checkIDList(v *validator.Validator, field string, values []string) {
	for _, val := range values {
		if val == "" || len(val) > maxIDLength {
			v.AddError(field, "must only contain non-empty values of at most 64 characters")
			return
		}
	}
	v.Check(validator.Unique(values), field, "must not contain duplicate values")
}
