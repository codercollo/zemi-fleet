package api

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/service"
	"github.com/codercollo/zemi-fleet/internal/validator"
)

const secondsPerMinute = 60.0

// createBatchesRequest is the POST /orders/batch body (the brief's shape).
type createBatchesRequest struct {
	RiderID       string `json:"rider_id"`
	RiderLocation *struct {
		Latitude  *float64 `json:"latitude"`
		Longitude *float64 `json:"longitude"`
	} `json:"rider_location"`
	MaxOrders *int     `json:"max_orders"`
	RadiusKM  *float64 `json:"radius_km"`
}

// createBatchesHandler handles POST /api/v2/orders/batch.
func (s *Server) createBatchesHandler(c *gin.Context) {
	var req createBatchesRequest
	if err := readJSON(c, &req); err != nil {
		s.badRequestResponse(c, err)
		return
	}

	v := validator.New()
	req.RiderID = strings.TrimSpace(req.RiderID)
	checkID(v, "rider_id", req.RiderID, riderID)

	var position domain.Location
	if req.RiderLocation == nil {
		v.AddError("rider_location", "must be provided")
	} else {
		v.Check(req.RiderLocation.Latitude != nil, "rider_location.latitude", "must be provided")
		v.Check(req.RiderLocation.Longitude != nil, "rider_location.longitude", "must be provided")
		if lat := req.RiderLocation.Latitude; lat != nil {
			v.Check(validator.Between(*lat, -maxLatitude, maxLatitude), "rider_location.latitude", "must be between -90 and 90")
			position.Lat = *lat
		}
		if lng := req.RiderLocation.Longitude; lng != nil {
			v.Check(validator.Between(*lng, -maxLongitude, maxLongitude), "rider_location.longitude", "must be between -180 and 180")
			position.Lng = *lng
		}
		if req.RiderLocation.Latitude != nil && req.RiderLocation.Longitude != nil {
			s.checkServiceArea(v, "rider_location", position)
		}
	}

	batch := s.cfg.Batch
	maxOrders, radiusKM := 0, 0.0
	if req.MaxOrders == nil {
		v.AddError("max_orders", "must be provided")
	} else {
		maxOrders = *req.MaxOrders
		v.Check(validator.Between(maxOrders, 1, batch.MaxOrders), "max_orders", "must be between 1 and "+strconv.Itoa(batch.MaxOrders))
	}
	if req.RadiusKM == nil {
		v.AddError("radius_km", "must be provided")
	} else {
		radiusKM = *req.RadiusKM
		v.Check(radiusKM > 0 && radiusKM <= batch.PickupRadiusKM, "radius_km",
			"must be greater than 0 and at most "+strconv.FormatFloat(batch.PickupRadiusKM, 'f', -1, 64)+" km")
	}
	if !v.Valid() {
		s.failedValidationResponse(c, v.Errors)
		return
	}

	offers, err := s.batches.CreateBatches(c.Request.Context(), service.CreateBatchesInput{
		RiderID:       req.RiderID,
		RiderLocation: position,
		MaxOrders:     maxOrders,
		RadiusKM:      radiusKM,
	})
	if err != nil {
		s.handleServiceError(c, err)
		return
	}

	if err := writeJSON(c, http.StatusOK, envelope{"batches": newBatchDTOs(offers)}, nil); err != nil {
		s.serverErrorResponse(c, err)
	}
}

type batchOrderDTO struct {
	OrderID          string      `json:"order_id"`
	Pickup           locationDTO `json:"pickup"`
	Delivery         locationDTO `json:"delivery"`
	DistanceKM       float64     `json:"distance_km"`
	EstimatedTimeMin int         `json:"estimated_time_min"`
}

type stopDTO struct {
	Seq      int         `json:"seq"`
	Type     string      `json:"type"`
	OrderID  string      `json:"order_id"`
	Location locationDTO `json:"location"`
}

// batchDTO is the brief's batch plus sequence (the suggested route), distance_saved_km (solo trips minus
// the batch's route; negative when batching costs distance), distance_to_first_pickup_km (specific to
// the asking rider or the query point) and expires_at.
type batchDTO struct {
	BatchID                 string          `json:"batch_id"`
	Orders                  []batchOrderDTO `json:"orders"`
	Sequence                []stopDTO       `json:"sequence"`
	TotalDistanceKM         float64         `json:"total_distance_km"`
	DistanceSavedKM         float64         `json:"distance_saved_km"`
	EstimatedEarningsKES    float64         `json:"estimated_earnings_kes"`
	RouteOptimizationScore  float64         `json:"route_optimization_score"`
	DistanceToFirstPickupKM float64         `json:"distance_to_first_pickup_km"`
	ExpiresAt               time.Time       `json:"expires_at"`
}

func newBatchDTOs(offers []service.BatchOffer) []batchDTO {
	out := make([]batchDTO, 0, len(offers)) // non-nil: encodes as [] not null
	for _, o := range offers {
		out = append(out, newBatchDTO(o))
	}
	return out
}

func newBatchDTO(o service.BatchOffer) batchDTO {
	b := o.Batch

	// An order's estimated time is when its delivery stop is reached; stop offsets are in seconds.
	deliveryAt := make(map[string]int, len(b.Orders))
	stops := make([]stopDTO, 0, len(b.Stops))
	for _, s := range b.Stops {
		if s.Type == domain.StopDelivery {
			deliveryAt[s.OrderID] = s.ETAOffsetS
		}
		stops = append(stops, stopDTO{Seq: s.Seq, Type: string(s.Type), OrderID: s.OrderID, Location: newLocationDTO(s.Location)})
	}

	orders := make([]batchOrderDTO, 0, len(b.Orders))
	for _, bo := range b.Orders {
		orders = append(orders, batchOrderDTO{
			OrderID:          bo.ID,
			Pickup:           newLocationDTO(bo.Pickup),
			Delivery:         newLocationDTO(bo.Delivery),
			DistanceKM:       roundTo(float64(bo.TripDistanceM)/metresPerKM, kmDecimals),
			EstimatedTimeMin: int(math.Ceil(float64(deliveryAt[bo.ID]) / secondsPerMinute)), // round up: never promise less time than needed
		})
	}

	return batchDTO{
		BatchID:                 b.ID,
		Orders:                  orders,
		Sequence:                stops,
		TotalDistanceKM:         roundTo(float64(b.TotalDistanceM)/metresPerKM, kmDecimals),
		DistanceSavedKM:         roundTo(float64(b.DistanceSavedM())/metresPerKM, kmDecimals),
		EstimatedEarningsKES:    centsToKES(b.EstimatedEarningsCents),
		RouteOptimizationScore:  b.RouteOptimizationScore,
		DistanceToFirstPickupKM: roundTo(o.DistanceToFirstPickupM/metresPerKM, kmDecimals),
		ExpiresAt:               b.ExpiresAt.UTC(),
	}
}
