package api

import (
	"math"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/service"
	"github.com/codercollo/zemi-fleet/internal/validator"
)

// Rounding applied to the matching response: kilometres to 2 decimals, angles to 1.
const (
	kmDecimals    = 100.0
	angleDecimals = 10.0
)

// matchOrdersRequest is the POST /riders/match-orders body. bearing_degrees and speed_kmh are
// optional: null or omitted means "unknown", which makes the matcher fall back to the full circle.
type matchOrdersRequest struct {
	RiderID         string `json:"rider_id"`
	CurrentLocation *struct {
		Latitude  *float64 `json:"latitude"`
		Longitude *float64 `json:"longitude"`
	} `json:"current_location"`
	BearingDegrees     *float64 `json:"bearing_degrees"`
	SpeedKMH           *float64 `json:"speed_kmh"`
	CurrentOrdersCount *int     `json:"current_orders_count"`
}

// matchOrdersHandler handles POST /api/v2/riders/match-orders.
func (s *Server) matchOrdersHandler(c *gin.Context) {
	var req matchOrdersRequest
	if err := readJSON(c, &req); err != nil {
		s.badRequestResponse(c, err)
		return
	}

	v := validator.New()
	req.RiderID = strings.TrimSpace(req.RiderID)
	v.Check(req.RiderID != "", "rider_id", "must be provided")
	v.Check(len(req.RiderID) <= maxIDLength, "rider_id", "must not be more than 64 characters long")

	var position domain.Location
	if req.CurrentLocation == nil {
		v.AddError("current_location", "must be provided")
	} else {
		v.Check(req.CurrentLocation.Latitude != nil, "current_location.latitude", "must be provided")
		v.Check(req.CurrentLocation.Longitude != nil, "current_location.longitude", "must be provided")
		if lat := req.CurrentLocation.Latitude; lat != nil {
			v.Check(validator.Between(*lat, -maxLatitude, maxLatitude), "current_location.latitude", "must be between -90 and 90")
			position.Lat = *lat
		}
		if lng := req.CurrentLocation.Longitude; lng != nil {
			v.Check(validator.Between(*lng, -maxLongitude, maxLongitude), "current_location.longitude", "must be between -180 and 180")
			position.Lng = *lng
		}
	}
	if b := req.BearingDegrees; b != nil {
		v.Check(*b >= 0 && *b < maxBearingDegrees, "bearing_degrees", "must be at least 0 and less than 360")
	}
	if sp := req.SpeedKMH; sp != nil {
		v.Check(validator.Between(*sp, 0, maxSpeedKMH), "speed_kmh", "must be between 0 and 200")
	}
	reported := 0
	if req.CurrentOrdersCount == nil {
		v.AddError("current_orders_count", "must be provided")
	} else {
		reported = *req.CurrentOrdersCount
		v.Check(validator.Between(reported, 0, domain.MaxRiderCapacity), "current_orders_count",
			"must be between 0 and 5")
	}
	if !v.Valid() {
		s.failedValidationResponse(c, v.Errors)
		return
	}

	result, err := s.matching.MatchOrders(c.Request.Context(), service.MatchOrdersInput{
		RiderID:             req.RiderID,
		Position:            position,
		HeadingDeg:          req.BearingDegrees,
		SpeedKMH:            req.SpeedKMH,
		ReportedOrdersCount: reported,
	})
	if err != nil {
		s.handleServiceError(c, err)
		return
	}

	if err := writeJSON(c, http.StatusOK, newMatchOrdersDTO(result), nil); err != nil {
		s.serverErrorResponse(c, err)
	}
}

type matchedOrderDTO struct {
	OrderID               string      `json:"order_id"`
	PickupLocation        locationDTO `json:"pickup_location"`
	DistanceFromRiderKM   float64     `json:"distance_from_rider_km"`
	AngleFromRiderDegrees *float64    `json:"angle_from_rider_degrees"`
	OrderAgeMinutes       int         `json:"order_age_minutes"`
	EstimatedEarningsKES  float64     `json:"estimated_earnings_kes"`
}

// newMatchOrdersDTO builds the response body. It is the brief's shape plus direction_filter_applied.
func newMatchOrdersDTO(r service.MatchOrdersResult) envelope {
	orders := make([]matchedOrderDTO, 0, len(r.Matches)) // non-nil: encodes as [] not null
	for _, m := range r.Matches {
		dto := matchedOrderDTO{
			OrderID:              m.OrderID,
			PickupLocation:       newLocationDTO(m.Pickup),
			DistanceFromRiderKM:  roundTo(m.DistanceM/metresPerKM, kmDecimals),
			OrderAgeMinutes:      m.AgeMinutes,
			EstimatedEarningsKES: centsToKES(m.RiderEarningsCents),
		}
		if m.AngleDeg != nil {
			a := roundTo(*m.AngleDeg, angleDecimals)
			dto.AngleFromRiderDegrees = &a
		}
		orders = append(orders, dto)
	}
	return envelope{
		"matching_orders":          orders,
		"can_accept_more_orders":   r.CanAcceptMore,
		"max_additional_orders":    r.MaxAdditional,
		"direction_filter_applied": r.DirectionFilterApplied,
	}
}

// roundTo rounds v to the precision given by scale (100 = 2 decimals, 10 = 1 decimal).
func roundTo(v, scale float64) float64 { return math.Round(v*scale) / scale }
