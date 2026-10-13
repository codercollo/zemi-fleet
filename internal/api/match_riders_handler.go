package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/codercollo/zemi-fleet/internal/service"
	"github.com/codercollo/zemi-fleet/internal/validator"
)

const acceptanceDecimals = 1000.0 // acceptance_rate is stored with 3 decimals

// matchRidersRequest is the POST /orders/match-riders body. order_details lets the match run for an
// order that is not saved yet; when it is sent it wins over a stored order with the same order_id.
type matchRidersRequest struct {
	OrderID      string `json:"order_id"`
	OrderDetails *struct {
		Category         string         `json:"category"`
		PickupLocation   *locationInput `json:"pickup_location"`
		DeliveryLocation *locationInput `json:"delivery_location"`
		ValueKES         *float64       `json:"value_kes"`
	} `json:"order_details"`
}

// matchRidersHandler handles POST /api/v2/orders/match-riders.
func (s *Server) matchRidersHandler(c *gin.Context) {
	var req matchRidersRequest
	if err := readJSON(c, &req); err != nil {
		s.badRequestResponse(c, err)
		return
	}

	v := validator.New()
	req.OrderID = strings.TrimSpace(req.OrderID)
	v.Check(len(req.OrderID) <= maxIDLength, "order_id", "must not be more than 64 characters long")

	in := service.MatchRidersInput{OrderID: req.OrderID}
	if d := req.OrderDetails; d == nil {
		// No details: the order must be a stored one, so an ID is required.
		v.Check(req.OrderID != "", "order_id", "must be provided when order_details is not")
	} else {
		category := strings.TrimSpace(d.Category)
		v.Check(category != "", "order_details.category", "must be provided")
		v.Check(len(category) <= maxIDLength, "order_details.category", "must not be more than 64 characters long")
		pickup := checkLocation(v, "order_details.pickup_location", d.PickupLocation)
		delivery := checkLocation(v, "order_details.delivery_location", d.DeliveryLocation)
		valueCents := checkAmount(v, "order_details.value_kes", d.ValueKES, true)
		in.Details = &service.OrderDetails{Category: category, Pickup: pickup, Delivery: delivery, ValueCents: valueCents}
	}
	if !v.Valid() {
		s.failedValidationResponse(c, v.Errors)
		return
	}

	matches, err := s.preferences.MatchRiders(c.Request.Context(), in)
	if err != nil {
		s.handleServiceError(c, err)
		return
	}

	riders := make([]matchedRiderDTO, 0, len(matches)) // non-nil: encodes as [] not null
	for _, m := range matches {
		dto := matchedRiderDTO{
			RiderID:             m.RiderID,
			MatchScore:          m.Score,
			MatchingPreferences: nonNil(m.MatchingPreferences),
			CurrentOrdersCount:  m.CurrentOrders,
			AcceptanceRate:      roundTo(m.AcceptanceRate, acceptanceDecimals),
		}
		if m.DistanceM != nil {
			km := roundTo(*m.DistanceM/metresPerKM, kmDecimals)
			dto.DistanceToPickupKM = &km
		}
		riders = append(riders, dto)
	}
	if err := writeJSON(c, http.StatusOK, envelope{"matching_riders": riders}, nil); err != nil {
		s.serverErrorResponse(c, err)
	}
}

// matchedRiderDTO has its fields in the brief's order. distance_to_pickup_km is null when the rider's
// location is unknown (never reported, or older than RIDER_LOCATION_STALE_AFTER).
type matchedRiderDTO struct {
	RiderID             string   `json:"rider_id"`
	MatchScore          float64  `json:"match_score"`
	MatchingPreferences []string `json:"matching_preferences"`
	CurrentOrdersCount  int      `json:"current_orders_count"`
	DistanceToPickupKM  *float64 `json:"distance_to_pickup_km"`
	AcceptanceRate      float64  `json:"acceptance_rate"`
}
