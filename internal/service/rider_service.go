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

// RiderService reads riders and manages their preferences.
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
	rider := riderFromRow(row)

	prefs, err := s.loadPreferences(ctx, s.store, id)
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

// UpdatePreferences replaces the rider's whole preference set. Unknown riders give
// domain.ErrNotFound; unknown category codes or area IDs give a *domain.ValidationError.
// The replacement runs in one transaction under a row lock on the rider, so two
// concurrent PUTs cannot interleave their delete-then-insert steps.
func (s *RiderService) UpdatePreferences(ctx context.Context, riderID string, in domain.Preferences) (domain.Rider, error) {
	if _, err := s.store.GetRider(ctx, riderID); err != nil {
		return domain.Rider{}, fmt.Errorf("get rider: %w", err)
	}

	fields := map[string]string{}
	if len(in.Categories) > 0 {
		found, err := s.store.GetCategoriesByCodes(ctx, in.Categories)
		if err != nil {
			return domain.Rider{}, fmt.Errorf("get categories: %w", err)
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
			return domain.Rider{}, fmt.Errorf("get areas: %w", err)
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
		return domain.Rider{}, &domain.ValidationError{Fields: fields}
	}

	err := s.store.ExecTx(ctx, func(q sqlc.Querier) error {
		if _, err := q.GetRiderForUpdate(ctx, riderID); err != nil {
			return fmt.Errorf("lock rider: %w", err)
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
		return nil
	})
	if err != nil {
		return domain.Rider{}, fmt.Errorf("update preferences: %w", err)
	}
	return s.Get(ctx, riderID)
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
