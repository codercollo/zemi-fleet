package repository

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/geo"
	"github.com/codercollo/zemi-fleet/internal/repository/sqlc"
)

// ListOpenBatchesParams are the inputs of GET /batches.
type ListOpenBatchesParams struct {
	Point   domain.Location
	RadiusM float64
	// Now is the clock's time; only batches with expires_at > Now are listed.
	Now              time.Time
	Page             int // 1-based
	PageSize         int
	SpatialQueryMode string
}

// ListOpenBatches returns one page of the open, unexpired batches whose first pickup is within RadiusM
// of the point, plus the total number of matches. Sorted by distance to the first pickup, then the
// oldest order's created_at, then batch id, so pages are stable.
//
// It is read-only: it records no offers and expires nothing (an expired batch is simply not listed).
// Bounding box in SQL, exact distance and the sort in Go, then only the requested page is loaded in full.
func (s *SQLStore) ListOpenBatches(ctx context.Context, arg ListOpenBatchesParams) ([]domain.Batch, int, error) {
	minLat, maxLat, minLng, maxLng := geo.BoundingBox(arg.Point, arg.RadiusM)
	if arg.SpatialQueryMode == "sql" {
		return s.listOpenBatchesSQL(ctx, arg, minLat, maxLat, minLng, maxLng)
	}
	rows, err := s.Queries.ListOpenBatchSummaries(ctx, sqlc.ListOpenBatchSummariesParams{
		Now: arg.Now, MinLat: minLat, MaxLat: maxLat, MinLng: minLng, MaxLng: maxLng,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("list open batches: %w", err)
	}

	type candidate struct {
		id       string
		distance float64
		oldest   time.Time
	}
	var found []candidate
	for _, r := range rows {
		d := geo.Haversine(arg.Point, domain.Location{Lat: r.FirstLat, Lng: r.FirstLng})
		if d <= arg.RadiusM {
			found = append(found, candidate{id: r.ID, distance: d, oldest: r.OldestOrderAt})
		}
	}
	slices.SortFunc(found, func(a, b candidate) int {
		if a.distance != b.distance {
			if a.distance < b.distance {
				return -1
			}
			return 1
		}
		if c := a.oldest.Compare(b.oldest); c != 0 {
			return c
		}
		return strings.Compare(a.id, b.id)
	})

	total := len(found)
	start := (arg.Page - 1) * arg.PageSize
	if start >= total {
		return []domain.Batch{}, total, nil
	}
	end := min(start+arg.PageSize, total)
	ids := make([]string, 0, end-start)
	for _, c := range found[start:end] {
		ids = append(ids, c.id)
	}

	batches, err := loadBatches(ctx, s.Queries, ids)
	if err != nil {
		return nil, 0, err
	}
	return batches, total, nil
}

func (s *SQLStore) listOpenBatchesSQL(ctx context.Context, arg ListOpenBatchesParams, minLat, maxLat, minLng, maxLng float64) ([]domain.Batch, int, error) {
	count, err := s.Queries.CountOpenBatchSummariesSQL(ctx, sqlc.CountOpenBatchSummariesSQLParams{
		Now: arg.Now, MinLat: minLat, MaxLat: maxLat, MinLng: minLng, MaxLng: maxLng, PointLat: arg.Point.Lat, PointLng: arg.Point.Lng, RadiusM: arg.RadiusM,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("count open batches: %w", err)
	}
	rows, err := s.Queries.ListOpenBatchSummariesSQL(ctx, sqlc.ListOpenBatchSummariesSQLParams{
		Now: arg.Now, MinLat: minLat, MaxLat: maxLat, MinLng: minLng, MaxLng: maxLng, PointLat: arg.Point.Lat, PointLng: arg.Point.Lng, RadiusM: arg.RadiusM,
		PageLimit: int32(arg.PageSize), PageOffset: int32((arg.Page - 1) * arg.PageSize),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("list open batches in SQL: %w", err)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	if len(ids) == 0 {
		return []domain.Batch{}, int(count), nil
	}
	batches, err := loadBatches(ctx, s.Queries, ids)
	if err != nil {
		return nil, 0, err
	}
	return batches, int(count), nil
}
