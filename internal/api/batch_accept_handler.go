package api

import (
	"errors"
	"log/slog"
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

// Pagination limits (Let's Go Further ch. 9.1). The page cap stops page * page_size overflowing.
const (
	defaultPageSize = 20
	maxPageSize     = 50
	maxPage         = 10_000_000
)

// metadata is the book's pagination block. Its fields are omitted when there are no records (ch. 9.6).
type metadata struct {
	CurrentPage  int `json:"current_page,omitempty"`
	PageSize     int `json:"page_size,omitempty"`
	FirstPage    int `json:"first_page,omitempty"`
	LastPage     int `json:"last_page,omitempty"`
	TotalRecords int `json:"total_records,omitempty"`
}

func newMetadata(total, page, pageSize int) metadata {
	if total == 0 {
		return metadata{}
	}
	return metadata{
		CurrentPage:  page,
		PageSize:     pageSize,
		FirstPage:    1,
		LastPage:     (total + pageSize - 1) / pageSize,
		TotalRecords: total,
	}
}

// listBatchesResponse is a struct so batches come before metadata.
type listBatchesResponse struct {
	Batches  []batchDTO `json:"batches"`
	Metadata metadata   `json:"metadata"`
}

// listBatchesHandler handles GET /api/v2/batches?lat=&lng=&radius_km=&page=&page_size=.
func (s *Server) listBatchesHandler(c *gin.Context) {
	v := validator.New()
	cfg := s.cfg.Batch

	lat, latOK := queryFloat(v, c, "lat")
	lng, lngOK := queryFloat(v, c, "lng")
	if latOK {
		v.Check(validator.Between(lat, -maxLatitude, maxLatitude), "lat", "must be between -90 and 90")
	}
	if lngOK {
		v.Check(validator.Between(lng, -maxLongitude, maxLongitude), "lng", "must be between -180 and 180")
	}
	if latOK && lngOK {
		s.checkServiceArea(v, "location", domain.Location{Lat: lat, Lng: lng})
	}

	radiusKM := cfg.VisibilityRadiusKM // default
	if raw := c.Query("radius_km"); raw != "" {
		r, err := strconv.ParseFloat(raw, 64)
		switch {
		case err != nil || math.IsNaN(r) || math.IsInf(r, 0):
			v.AddError("radius_km", "must be a number")
		case r <= 0 || r > cfg.SearchRadiusKM:
			v.AddError("radius_km", "must be greater than 0 and at most "+strconv.FormatFloat(cfg.SearchRadiusKM, 'f', -1, 64)+" km")
		default:
			radiusKM = r
		}
	}

	page := queryInt(v, c, "page", 1)
	pageSize := queryInt(v, c, "page_size", defaultPageSize)
	v.Check(page >= 1 && page <= maxPage, "page", "must be between 1 and "+strconv.Itoa(maxPage))
	v.Check(pageSize >= 1 && pageSize <= maxPageSize, "page_size", "must be between 1 and "+strconv.Itoa(maxPageSize))

	if !v.Valid() {
		s.failedValidationResponse(c, v.Errors)
		return
	}

	result, err := s.batches.ListBatches(c.Request.Context(), service.ListBatchesInput{
		Point:    domain.Location{Lat: lat, Lng: lng},
		RadiusKM: radiusKM,
		Page:     page,
		PageSize: pageSize,
	})
	if err != nil {
		s.handleServiceError(c, err)
		return
	}

	resp := listBatchesResponse{Batches: newBatchDTOs(result.Offers), Metadata: newMetadata(result.Total, page, pageSize)}
	if err := writeJSON(c, http.StatusOK, resp, nil); err != nil {
		s.serverErrorResponse(c, err)
	}
}

// queryFloat reads a required float query parameter, recording an error when it is missing or not a number.
func queryFloat(v *validator.Validator, c *gin.Context, name string) (float64, bool) {
	raw := c.Query(name)
	if raw == "" {
		v.AddError(name, "must be provided")
		return 0, false
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		v.AddError(name, "must be a number")
		return 0, false
	}
	return f, true
}

// queryInt reads an optional integer query parameter with a default. A malformed value is a validation
// error, and the returned value is then the default so range checks do not add a second message.
func queryInt(v *validator.Validator, c *gin.Context, name string, def int) int {
	raw := c.Query(name)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		v.AddError(name, "must be an integer")
		return def
	}
	return n
}

// acceptBatchRequest is the POST /batches/:id/accept body.
type acceptBatchRequest struct {
	RiderID string `json:"rider_id"`
}

// acceptedBatchDTO is a batch after a rider has taken it.
type acceptedBatchDTO struct {
	BatchID                string          `json:"batch_id"`
	Status                 string          `json:"status"`
	RiderID                string          `json:"rider_id"`
	AcceptedAt             time.Time       `json:"accepted_at"`
	Orders                 []batchOrderDTO `json:"orders"`
	Sequence               []stopDTO       `json:"sequence"`
	TotalDistanceKM        float64         `json:"total_distance_km"`
	DistanceSavedKM        float64         `json:"distance_saved_km"`
	EstimatedEarningsKES   float64         `json:"estimated_earnings_kes"`
	RouteOptimizationScore float64         `json:"route_optimization_score"`
	ExpiresAt              time.Time       `json:"expires_at"`
}

// auditAcceptFailure records the two accept outcomes worth an audit line: losing the race (409) and being refused
// for capacity (422). Other failures are logged by the normal error path.
func (s *Server) auditAcceptFailure(c *gin.Context, batchID, riderID string, err error) {
	switch {
	case errors.Is(err, domain.ErrBatchTaken):
		s.logger.InfoContext(c.Request.Context(), "batch accept conflict",
			slog.String("batch_id", batchID), slog.String("rider_id", riderID))
	case errors.Is(err, domain.ErrRiderAtCapacity):
		s.logger.InfoContext(c.Request.Context(), "batch accept rejected, rider at capacity",
			slog.String("batch_id", batchID), slog.String("rider_id", riderID))
	}
}

// acceptBatchHandler handles POST /api/v2/batches/:id/accept.
func (s *Server) acceptBatchHandler(c *gin.Context) {
	id, ok := s.pathID(c, batchID)
	if !ok {
		return
	}
	var req acceptBatchRequest
	if err := readJSON(c, &req); err != nil {
		s.badRequestResponse(c, err)
		return
	}

	v := validator.New()
	req.RiderID = strings.TrimSpace(req.RiderID)
	checkID(v, "rider_id", req.RiderID, riderID)
	if !v.Valid() {
		s.failedValidationResponse(c, v.Errors)
		return
	}

	batch, err := s.batches.Accept(c.Request.Context(), id, req.RiderID)
	if err != nil {
		s.auditAcceptFailure(c, id, req.RiderID, err)
		s.handleServiceError(c, err)
		return
	}
	// Audit trail: who took which batch. Fixed message, values as attributes.
	s.logger.InfoContext(c.Request.Context(), "batch accepted",
		slog.String("batch_id", batch.ID), slog.String("rider_id", req.RiderID), slog.Int("orders", len(batch.Orders)))

	// The same shape as the offer, minus the rider-specific distance, plus who holds it and since when.
	base := newBatchDTO(service.BatchOffer{Batch: batch})
	dto := acceptedBatchDTO{
		BatchID:                base.BatchID,
		Status:                 string(batch.Status),
		Orders:                 base.Orders,
		Sequence:               base.Sequence,
		TotalDistanceKM:        base.TotalDistanceKM,
		DistanceSavedKM:        base.DistanceSavedKM,
		EstimatedEarningsKES:   base.EstimatedEarningsKES,
		RouteOptimizationScore: base.RouteOptimizationScore,
		ExpiresAt:              base.ExpiresAt,
	}
	if batch.RiderID != nil {
		dto.RiderID = *batch.RiderID
	}
	if batch.AcceptedAt != nil {
		dto.AcceptedAt = batch.AcceptedAt.UTC()
	}
	if err := writeJSON(c, http.StatusOK, envelope{"batch": dto}, nil); err != nil {
		s.serverErrorResponse(c, err)
	}
}
