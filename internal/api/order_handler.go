package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/service"
	"github.com/codercollo/zemi-fleet/internal/validator"
)

// Coordinate and amount limits. The amount cap keeps cents safely inside int64 and
// rejects obvious typos; it is not a business rule.
const (
	maxLatitude  = 90.0
	maxLongitude = 180.0
	maxAmountKES = 100_000_000.0
	maxIDLength  = 64
)

type locationInput struct {
	Lat *float64 `json:"lat"`
	Lng *float64 `json:"lng"`
}

// checkLocation validates a required lat/lng pair and returns it.
func checkLocation(v *validator.Validator, field string, in *locationInput) domain.Location {
	if in == nil {
		v.AddError(field, "must be provided")
		return domain.Location{}
	}
	v.Check(in.Lat != nil, field+".lat", "must be provided")
	v.Check(in.Lng != nil, field+".lng", "must be provided")
	var loc domain.Location
	if in.Lat != nil {
		v.Check(validator.Between(*in.Lat, -maxLatitude, maxLatitude), field+".lat", "must be between -90 and 90")
		loc.Lat = *in.Lat
	}
	if in.Lng != nil {
		v.Check(validator.Between(*in.Lng, -maxLongitude, maxLongitude), field+".lng", "must be between -180 and 180")
		loc.Lng = *in.Lng
	}
	return loc
}

// createOrderRequest is the POST /orders body. The pickup point comes from the vendor.
type createOrderRequest struct {
	VendorID         string         `json:"vendor_id"`
	Category         string         `json:"category"`
	DeliveryLocation *locationInput `json:"delivery_location"`
	ValueKES         *float64       `json:"value_kes"`
	DeliveryFeeKES   *float64       `json:"delivery_fee_kes"`
}

// createOrderHandler handles POST /api/v2/orders.
func (s *Server) createOrderHandler(c *gin.Context) {
	var req createOrderRequest
	if err := readJSON(c, &req); err != nil {
		s.badRequestResponse(c, err)
		return
	}

	v := validator.New()
	req.VendorID = strings.TrimSpace(req.VendorID)
	req.Category = strings.TrimSpace(req.Category)
	v.Check(req.VendorID != "", "vendor_id", "must be provided")
	v.Check(len(req.VendorID) <= maxIDLength, "vendor_id", "must not be more than 64 characters long")
	v.Check(req.Category != "", "category", "must be provided")
	v.Check(len(req.Category) <= maxIDLength, "category", "must not be more than 64 characters long")
	delivery := checkLocation(v, "delivery_location", req.DeliveryLocation)

	valueCents := checkAmount(v, "value_kes", req.ValueKES, true)
	feeCents := checkAmount(v, "delivery_fee_kes", req.DeliveryFeeKES, false)
	if !v.Valid() {
		s.failedValidationResponse(c, v.Errors)
		return
	}

	order, err := s.orders.Create(c.Request.Context(), service.CreateOrderInput{
		VendorID:         req.VendorID,
		CategoryCode:     req.Category,
		Delivery:         delivery,
		ValueCents:       valueCents,
		DeliveryFeeCents: feeCents,
	})
	if err != nil {
		s.handleServiceError(c, err)
		return
	}

	headers := http.Header{"Location": []string{apiPrefix + "/orders/" + order.ID}}
	if err := writeJSON(c, http.StatusCreated, envelope{"order": newOrderDTO(order)}, headers); err != nil {
		s.serverErrorResponse(c, err)
	}
}

// checkAmount validates a required KES amount and converts it to cents.
// positive means zero is rejected (order value); otherwise zero is allowed (fee).
func checkAmount(v *validator.Validator, field string, kes *float64, positive bool) int64 {
	if kes == nil {
		v.AddError(field, "must be provided")
		return 0
	}
	cents, ok := kesToCents(*kes)
	switch {
	case !ok || *kes > maxAmountKES:
		v.AddError(field, "must be a valid amount with at most 2 decimal places")
	case positive && cents <= 0:
		v.AddError(field, "must be greater than zero")
	case cents < 0:
		v.AddError(field, "must not be negative")
	}
	return cents
}

// showOrderHandler handles GET /api/v2/orders/:id.
func (s *Server) showOrderHandler(c *gin.Context) {
	order, err := s.orders.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		s.handleServiceError(c, err)
		return
	}
	if err := writeJSON(c, http.StatusOK, envelope{"order": newOrderDTO(order)}, nil); err != nil {
		s.serverErrorResponse(c, err)
	}
}
