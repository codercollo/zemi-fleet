package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/repository"
	"github.com/codercollo/zemi-fleet/internal/repository/sqlc"
)

// RiderService reads riders, manages their preferences and applies partial updates.
type RiderService struct {
	store repository.Store
}

// NewRiderService wires the service to its store.
func NewRiderService(store repository.Store) *RiderService {
	return &RiderService{store: store}
}

// Get returns a rider with their preferences, or domain.ErrNotFound.
func (s *RiderService) Get(ctx context.Context, id string) (domain.Rider, error) {
	row, err := s.store.GetRider(ctx, id)
	if err != nil {
		return domain.Rider{}, fmt.Errorf("get rider: %w", err)
	}
	return s.withPreferences(ctx, row)
}

// withPreferences converts a rider row and attaches the rider's preferences.
func (s *RiderService) withPreferences(ctx context.Context, row sqlc.Rider) (domain.Rider, error) {
	rider := riderFromRow(row)
	prefs, err := s.loadPreferences(ctx, s.store, row.ID)
	if err != nil {
		return domain.Rider{}, err
	}
	rider.Preferences = prefs
	return rider, nil
}

// loadPreferences reads from any Querier so it can run inside or outside a transaction.
// A rider with no rider_preferences row has no minimum order value.
func (s *RiderService) loadPreferences(ctx context.Context, q sqlc.Querier, riderID string) (domain.Preferences, error) {
	prefs := domain.Preferences{Categories: []string{}, AreaIDs: []string{}}

	row, err := q.GetPreferences(ctx, riderID)
	switch {
	case err == nil:
		prefs.MinOrderValueCents = row.MinOrderValueCents
	case !errors.Is(err, domain.ErrNotFound):
		return prefs, fmt.Errorf("get preferences: %w", err)
	}

	cats, err := q.ListPreferredCategories(ctx, riderID)
	if err != nil {
		return prefs, fmt.Errorf("list preferred categories: %w", err)
	}
	areas, err := q.ListPreferredAreas(ctx, riderID)
	if err != nil {
		return prefs, fmt.Errorf("list preferred areas: %w", err)
	}
	if len(cats) > 0 {
		prefs.Categories = cats
	}
	if len(areas) > 0 {
		prefs.AreaIDs = areas
	}
	return prefs, nil
}

// checkVersion enforces the optional X-Expected-Version: when the client sent one
// and it is not the rider's current version, the client is editing a stale copy.
func checkVersion(current int32, expected *int) error {
	if expected != nil && *expected != int(current) {
		return domain.ErrEditConflict
	}
	return nil
}

// LocationUpdate is a partial location ping. Latitude and longitude always change;
// a nil BearingDeg or SpeedKmh leaves the stored value untouched (PATCH semantics,
// Let's Go Further ch. 8.1).
type LocationUpdate struct {
	Lat        float64
	Lng        float64
	BearingDeg *float64
	SpeedKmh   *float64
}

// UpdateLocation applies a location ping with optimistic locking: the UPDATE only
// matches the version that was read (or that the client expects), so a concurrent
// change makes it match no row and the call returns domain.ErrEditConflict.
// Let's Go Further ch. 8.2.
func (s *RiderService) UpdateLocation(ctx context.Context, id string, in LocationUpdate, expectedVersion *int) (domain.Rider, error) {
	cur, err := s.store.GetRider(ctx, id)
	if err != nil {
		return domain.Rider{}, fmt.Errorf("get rider: %w", err)
	}
	if err := checkVersion(cur.Version, expectedVersion); err != nil {
		return domain.Rider{}, err
	}

	bearing, speed := cur.LastBearingDeg, cur.LastSpeedKmh
	if in.BearingDeg != nil {
		bearing = in.BearingDeg
	}
	if in.SpeedKmh != nil {
		speed = in.SpeedKmh
	}

	row, err := s.store.UpdateRiderLocation(ctx, sqlc.UpdateRiderLocationParams{
		LastLat:        &in.Lat,
		LastLng:        &in.Lng,
		LastBearingDeg: bearing,
		LastSpeedKmh:   speed,
		ID:             id,
		Version:        cur.Version,
	})
	if err != nil {
		return domain.Rider{}, fmt.Errorf("update rider location: %w", versionMiss(err))
	}
	return s.withPreferences(ctx, row)
}

// UpdateAvailability switches a rider on or off shift, with the same optimistic locking.
func (s *RiderService) UpdateAvailability(ctx context.Context, id string, available bool, expectedVersion *int) (domain.Rider, error) {
	cur, err := s.store.GetRider(ctx, id)
	if err != nil {
		return domain.Rider{}, fmt.Errorf("get rider: %w", err)
	}
	if err := checkVersion(cur.Version, expectedVersion); err != nil {
		return domain.Rider{}, err
	}

	row, err := s.store.UpdateRiderAvailability(ctx, sqlc.UpdateRiderAvailabilityParams{
		IsAvailable: available,
		ID:          id,
		Version:     cur.Version,
	})
	if err != nil {
		return domain.Rider{}, fmt.Errorf("update rider availability: %w", versionMiss(err))
	}
	return s.withPreferences(ctx, row)
}

// versionMiss turns "no row" from a versioned UPDATE into an edit conflict. The rider
// was just read, so a missing row means its version moved on, not that it never existed.
func versionMiss(err error) error {
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ErrEditConflict
	}
	return err
}

// UpdatePreferences replaces the rider's whole preference set. Unknown riders give
// domain.ErrNotFound; unknown category codes or area IDs give a *domain.ValidationError.
// The replacement runs in one transaction under a row lock on the rider, so two
// concurrent PUTs cannot interleave their delete-then-insert steps, and it bumps the
// rider's version so a stale X-Expected-Version is rejected with domain.ErrEditConflict.
func (s *RiderService) UpdatePreferences(ctx context.Context, riderID string, in domain.Preferences, expectedVersion *int) (domain.Rider, error) {
	if _, err := s.store.GetRider(ctx, riderID); err != nil {
		return domain.Rider{}, fmt.Errorf("get rider: %w", err)
	}

	if err := s.validatePreferences(ctx, in); err != nil {
		return domain.Rider{}, err
	}

	var updated sqlc.Rider
	err := s.store.ExecTx(ctx, func(q sqlc.Querier) error {
		locked, err := q.GetRiderForUpdate(ctx, riderID)
		if err != nil {
			return fmt.Errorf("lock rider: %w", err)
		}
		if err := checkVersion(locked.Version, expectedVersion); err != nil {
			return err
		}
		if _, err := q.UpsertPreferences(ctx, sqlc.UpsertPreferencesParams{
			RiderID:            riderID,
			MinOrderValueCents: in.MinOrderValueCents,
		}); err != nil {
			return fmt.Errorf("upsert preferences: %w", err)
		}
		if err := q.DeletePreferredCategories(ctx, riderID); err != nil {
			return fmt.Errorf("delete preferred categories: %w", err)
		}
		if err := q.DeletePreferredAreas(ctx, riderID); err != nil {
			return fmt.Errorf("delete preferred areas: %w", err)
		}
		if len(in.Categories) > 0 {
			if err := q.InsertPreferredCategories(ctx, sqlc.InsertPreferredCategoriesParams{
				RiderID: riderID, CategoryCodes: in.Categories,
			}); err != nil {
				return fmt.Errorf("insert preferred categories: %w", err)
			}
		}
		if len(in.AreaIDs) > 0 {
			if err := q.InsertPreferredAreas(ctx, sqlc.InsertPreferredAreasParams{
				RiderID: riderID, AreaIds: in.AreaIDs,
			}); err != nil {
				return fmt.Errorf("insert preferred areas: %w", err)
			}
		}
		updated, err = q.BumpRiderVersion(ctx, sqlc.BumpRiderVersionParams{ID: riderID, Version: locked.Version})
		if err != nil {
			return fmt.Errorf("bump rider version: %w", versionMiss(err))
		}
		return nil
	})
	if err != nil {
		return domain.Rider{}, fmt.Errorf("update preferences: %w", err)
	}
	return s.withPreferences(ctx, updated)
}

// validatePreferences checks that every category code and area ID exists.
func (s *RiderService) validatePreferences(ctx context.Context, in domain.Preferences) error {
	fields := map[string]string{}
	if len(in.Categories) > 0 {
		found, err := s.store.GetCategoriesByCodes(ctx, in.Categories)
		if err != nil {
			return fmt.Errorf("get categories: %w", err)
		}
		known := make(map[string]struct{}, len(found))
		for _, c := range found {
			known[c.Code] = struct{}{}
		}
		if missing := unknown(in.Categories, known); len(missing) > 0 {
			fields["preferred_categories"] = "unknown categories: " + strings.Join(missing, ", ")
		}
	}
	if len(in.AreaIDs) > 0 {
		found, err := s.store.GetAreasByIDs(ctx, in.AreaIDs)
		if err != nil {
			return fmt.Errorf("get areas: %w", err)
		}
		known := make(map[string]struct{}, len(found))
		for _, a := range found {
			known[a.ID] = struct{}{}
		}
		if missing := unknown(in.AreaIDs, known); len(missing) > 0 {
			fields["preferred_areas"] = "unknown areas: " + strings.Join(missing, ", ")
		}
	}
	if len(fields) > 0 {
		return &domain.ValidationError{Fields: fields}
	}
	return nil
}

// unknown returns the wanted values that are not in known, in their original order.
func unknown(wanted []string, known map[string]struct{}) []string {
	var missing []string
	for _, w := range wanted {
		if _, ok := known[w]; !ok {
			missing = append(missing, w)
		}
	}
	return missing
}
