package api

import (
	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/validator"
)

func (s *Server) checkServiceArea(v *validator.Validator, field string, location domain.Location) {
	if !s.cfg.Geo.BoundsEnabled {
		return
	}
	bounds := s.cfg.Geo
	if location.Lat < bounds.MinLat || location.Lat > bounds.MaxLat || location.Lng < bounds.MinLng || location.Lng > bounds.MaxLng {
		v.AddError(field, "must be within the service area")
	}
}
