package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/service"
	"github.com/codercollo/zemi-fleet/internal/validator"
)

// expectedVersionHeader lets a client say "only apply this if the rider is still at version N".
const expectedVersionHeader = "X-Expected-Version"

// Telemetry limits, matching the CHECK constraints on riders.
const (
	maxBearingDegrees = 360.0 // exclusive: bearings are in [0, 360)
	maxSpeedKMH       = 200.0
)

// expectedVersion reads the optional X-Expected-Version header. nil means the client did not send one.
func expectedVersion(c *gin.Context) (*int, error) {
	raw := c.GetHeader(expectedVersionHeader)
	if raw == "" {
		return nil, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return nil, errors.New(expectedVersionHeader + " header must be a positive integer")
	}
	return &n, nil
}

func (s *Server) writeRider(c *gin.Context, rider domain.Rider) {
	if err := writeJSON(c, http.StatusOK, envelope{"rider": newRiderDTO(rider)}, nil); err != nil {
		s.serverErrorResponse(c, err)
	}
}

// showRiderHandler handles GET /api/v2/riders/:id (preferences included).
func (s *Server) showRiderHandler(c *gin.Context) {
	id, ok := s.pathID(c, riderID)
	if !ok {
		return
	}
	rider, err := s.riders.Get(c.Request.Context(), id)
	if err != nil {
		s.handleServiceError(c, err)
		return
	}
	s.writeRider(c, rider)
}

// updatePreferencesRequest is the PUT body. PUT replaces everything, so an omitted
// list means "no preference" (matches all) and an omitted minimum means 0.
type updatePreferencesRequest struct {
	MinValueKES         *float64 `json:"min_order_value_kes"`
	PreferredCategories []string `json:"preferred_categories"`
	PreferredAreas      []string `json:"preferred_areas"`
}

// updatePreferencesHandler handles PUT /api/v2/riders/:id/preferences.
func (s *Server) updatePreferencesHandler(c *gin.Context) {
	id, ok := s.pathID(c, riderID)
	if !ok {
		return
	}
	var req updatePreferencesRequest
	if err := readJSON(c, &req); err != nil {
		s.badRequestResponse(c, err)
		return
	}
	expected, err := expectedVersion(c)
	if err != nil {
		s.badRequestResponse(c, err)
		return
	}

	v := validator.New()
	var minCents int64
	if req.MinValueKES != nil {
		cents, ok := kesToCents(*req.MinValueKES)
		switch {
		case !ok:
			v.AddError("min_order_value_kes", "must be a valid amount with at most 2 decimal places")
		case *req.MinValueKES > s.cfg.OrderMaxValueKES:
			v.AddError("min_order_value_kes", "must not be more than "+strconv.FormatFloat(s.cfg.OrderMaxValueKES, 'f', -1, 64)+" KES")
		case cents < 0:
			v.AddError("min_order_value_kes", "must not be negative")
		}
		minCents = cents
	}
	checkIDList(v, "preferred_categories", req.PreferredCategories, categoryID)
	checkIDList(v, "preferred_areas", req.PreferredAreas, areaID)
	if !v.Valid() {
		s.failedValidationResponse(c, v.Errors)
		return
	}

	rider, err := s.riders.UpdatePreferences(c.Request.Context(), id, domain.Preferences{
		MinOrderValueCents: minCents,
		Categories:         req.PreferredCategories,
		AreaIDs:            req.PreferredAreas,
	}, expected)
	if err != nil {
		s.handleServiceError(c, err)
		return
	}
	s.writeRider(c, rider)
}

// updateLocationRequest is the PATCH body. Pointers tell "omitted" apart from zero,
// which matters: bearing 0 is north and speed 0 is stationary. Let's Go Further ch. 8.1.
type updateLocationRequest struct {
	Latitude       *float64 `json:"latitude"`
	Longitude      *float64 `json:"longitude"`
	BearingDegrees *float64 `json:"bearing_degrees"`
	SpeedKMH       *float64 `json:"speed_kmh"`
}

// updateLocationHandler handles PATCH /api/v2/riders/:id/location.
func (s *Server) updateLocationHandler(c *gin.Context) {
	id, ok := s.pathID(c, riderID)
	if !ok {
		return
	}
	var req updateLocationRequest
	if err := readJSON(c, &req); err != nil {
		s.badRequestResponse(c, err)
		return
	}
	expected, err := expectedVersion(c)
	if err != nil {
		s.badRequestResponse(c, err)
		return
	}

	v := validator.New()
	v.Check(req.Latitude != nil, "latitude", "must be provided")
	v.Check(req.Longitude != nil, "longitude", "must be provided")
	if req.Latitude != nil {
		v.Check(validator.Between(*req.Latitude, -maxLatitude, maxLatitude), "latitude", "must be between -90 and 90")
	}
	if req.Longitude != nil {
		v.Check(validator.Between(*req.Longitude, -maxLongitude, maxLongitude), "longitude", "must be between -180 and 180")
	}
	if req.BearingDegrees != nil {
		v.Check(*req.BearingDegrees >= 0 && *req.BearingDegrees < maxBearingDegrees,
			"bearing_degrees", "must be at least 0 and less than 360")
	}
	if req.SpeedKMH != nil {
		v.Check(validator.Between(*req.SpeedKMH, 0, maxSpeedKMH), "speed_kmh", "must be between 0 and 200")
	}
	if !v.Valid() {
		s.failedValidationResponse(c, v.Errors)
		return
	}

	rider, err := s.riders.UpdateLocation(c.Request.Context(), id, service.LocationUpdate{
		Lat:        *req.Latitude,
		Lng:        *req.Longitude,
		BearingDeg: req.BearingDegrees,
		SpeedKmh:   req.SpeedKMH,
	}, expected)
	if err != nil {
		s.handleServiceError(c, err)
		return
	}
	s.writeRider(c, rider)
}

// updateAvailabilityRequest is the PATCH body for going on or off shift.
type updateAvailabilityRequest struct {
	IsAvailable *bool `json:"is_available"`
}

// updateAvailabilityHandler handles PATCH /api/v2/riders/:id/availability.
func (s *Server) updateAvailabilityHandler(c *gin.Context) {
	id, ok := s.pathID(c, riderID)
	if !ok {
		return
	}
	var req updateAvailabilityRequest
	if err := readJSON(c, &req); err != nil {
		s.badRequestResponse(c, err)
		return
	}
	expected, err := expectedVersion(c)
	if err != nil {
		s.badRequestResponse(c, err)
		return
	}

	v := validator.New()
	v.Check(req.IsAvailable != nil, "is_available", "must be provided")
	if !v.Valid() {
		s.failedValidationResponse(c, v.Errors)
		return
	}

	rider, err := s.riders.UpdateAvailability(c.Request.Context(), id, *req.IsAvailable, expected)
	if err != nil {
		s.handleServiceError(c, err)
		return
	}
	s.writeRider(c, rider)
}

// checkIDList validates a list of IDs or codes: a bounded count, each in the right format, no duplicates.
// Existence is checked by the service.
func checkIDList(v *validator.Validator, field string, values []string, f idFormat) {
	if len(values) > maxListItems {
		v.AddError(field, "must not contain more than "+strconv.Itoa(maxListItems)+" values")
		return
	}
	for _, val := range values {
		if val == "" || len(val) > maxIDLength || !f.pattern.MatchString(val) {
			v.AddError(field, "every value must be a valid code, expected something like "+f.example)
			return
		}
	}
	v.Check(validator.Unique(values), field, "must not contain duplicate values")
}
