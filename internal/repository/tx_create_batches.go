package repository

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/codercollo/zemi-fleet/internal/batching"
	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/geo"
	"github.com/codercollo/zemi-fleet/internal/repository/sqlc"
	"github.com/codercollo/zemi-fleet/internal/util"
)

// CreateBatchesParams are the inputs of the create-batches transaction.
type CreateBatchesParams struct {
	RiderID       string
	RiderLocation domain.Location
	// Now is the clock's time; every timestamp written (created_at, expires_at, offered_at) derives from it.
	Now time.Time
	// TTL is how long a new batch stays open.
	TTL time.Duration
	// SearchRadiusM bounds both the pending orders that get batched and the open batches that are shown.
	SearchRadiusM float64
	// Batching holds the grouping rules. MaxOrders is the rider's effective batch size and
	// PickupRadiusM is the request's pairwise pickup radius.
	Batching batching.Params
}

// CreateBatchesTx forms batches from the pending orders near a rider and returns every open batch
// on offer to them (the new ones plus any still-open existing ones that fit), all in ONE transaction:
//
//  1. expire open batches past their TTL and put their orders back to pending;
//  2. select pending orders near the rider FOR UPDATE SKIP LOCKED, so two riders asking at the same
//     moment never batch the same order (the second request just skips the locked rows);
//  3. batching.Form groups them; insert the batches, their stops, and mark the orders batched;
//  4. record a batch_offers row for the rider for every batch shown (a repeat request does not
//     duplicate it) and add the new offers to riders.offers_count;
//  5. load the batches in full.
//
// Batches stay 'open' with no rider: per the client, a batch is assigned only when a rider accepts it.
func (s *SQLStore) CreateBatchesTx(ctx context.Context, arg CreateBatchesParams) ([]domain.Batch, error) {
	var result []domain.Batch
	err := s.ExecTx(ctx, func(q sqlc.Querier) error {
		var err error
		result, err = createBatches(ctx, q, arg)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("create batches: %w", err)
	}
	return result, nil
}

// createBatches is the body of the transaction, split out so it runs against any Querier.
func createBatches(ctx context.Context, q sqlc.Querier, arg CreateBatchesParams) ([]domain.Batch, error) {
	// 1. expiry
	expired, err := q.ExpireStaleBatches(ctx, arg.Now)
	if err != nil {
		return nil, fmt.Errorf("expire stale batches: %w", err)
	}
	if len(expired) > 0 {
		if err := q.ReleaseOrdersFromBatches(ctx, expired); err != nil {
			return nil, fmt.Errorf("release orders of expired batches: %w", err)
		}
	}

	// 2. candidates: bounding box in SQL (row-locked), exact distance in Go
	minLat, maxLat, minLng, maxLng := geo.BoundingBox(arg.RiderLocation, arg.SearchRadiusM)
	rows, err := q.ListPendingOrdersForBatching(ctx, sqlc.ListPendingOrdersForBatchingParams{
		MinLat: minLat, MaxLat: maxLat, MinLng: minLng, MaxLng: maxLng,
	})
	if err != nil {
		return nil, fmt.Errorf("list pending orders: %w", err)
	}
	candidates := make([]batching.Order, 0, len(rows))
	for _, r := range rows {
		pickup := domain.Location{Lat: r.PickupLat, Lng: r.PickupLng}
		if geo.Haversine(arg.RiderLocation, pickup) > arg.SearchRadiusM {
			continue
		}
		var earnings int64
		if r.RiderEarningsCents != nil {
			earnings = *r.RiderEarningsCents
		}
		candidates = append(candidates, batching.Order{
			ID:                 r.ID,
			Pickup:             pickup,
			Delivery:           domain.Location{Lat: r.DeliveryLat, Lng: r.DeliveryLng},
			CreatedAt:          r.CreatedAt,
			DeliveryFeeCents:   r.DeliveryFeeCents,
			RiderEarningsCents: earnings,
		})
	}

	// 3. form and store
	expiresAt := arg.Now.Add(arg.TTL)
	for _, b := range batching.Form(candidates, arg.Batching) {
		if err := insertBatch(ctx, q, b, arg.Now, expiresAt); err != nil {
			return nil, err
		}
	}

	// 4. which open batches does this rider get to see? New ones plus existing ones that fit.
	open, err := q.ListOpenBatchesNearPoint(ctx, sqlc.ListOpenBatchesNearPointParams{
		Now: arg.Now, MaxOrders: int32(arg.Batching.MaxOrders),
		MinLat: minLat, MaxLat: maxLat, MinLng: minLng, MaxLng: maxLng,
	})
	if err != nil {
		return nil, fmt.Errorf("list open batches: %w", err)
	}
	ids := make([]string, 0, len(open))
	for _, o := range open {
		if geo.Haversine(arg.RiderLocation, domain.Location{Lat: o.FirstLat, Lng: o.FirstLng}) <= arg.SearchRadiusM {
			ids = append(ids, o.ID)
		}
	}
	if len(ids) == 0 {
		return []domain.Batch{}, nil
	}

	batches, err := loadBatches(ctx, q, ids)
	if err != nil {
		return nil, err
	}

	// 5. offers
	var newOffers int32
	for _, b := range batches {
		n, err := q.CreateBatchOffer(ctx, sqlc.CreateBatchOfferParams{
			BatchID:   b.ID,
			RiderID:   arg.RiderID,
			DistanceM: int32(math.Round(geo.Haversine(arg.RiderLocation, b.PickupCentroid))),
			OfferedAt: arg.Now,
		})
		if err != nil {
			return nil, fmt.Errorf("create batch offer: %w", err)
		}
		newOffers += int32(n)
	}
	if newOffers > 0 {
		if err := q.AddRiderOffers(ctx, sqlc.AddRiderOffersParams{ID: arg.RiderID, Offers: newOffers}); err != nil {
			return nil, fmt.Errorf("add rider offers: %w", err)
		}
	}
	return batches, nil
}

// insertBatch writes one formed batch, its stops, and marks its orders batched.
func insertBatch(ctx context.Context, q sqlc.Querier, b batching.Batch, now, expiresAt time.Time) error {
	id := util.NewBatchID(now)
	if _, err := q.CreateBatch(ctx, sqlc.CreateBatchParams{
		ID:                     id,
		PickupCentroidLat:      b.Centroid.Lat,
		PickupCentroidLng:      b.Centroid.Lng,
		PickupSpreadM:          int32(math.Round(b.PickupSpreadM)),
		OrdersCount:            int16(len(b.Orders)),
		TotalDistanceM:         int32(math.Round(b.RouteDistanceM)),
		EstimatedDurationS:     int32(math.Round(b.DurationS)),
		TotalDeliveryFeeCents:  b.FeeCents,
		EstimatedEarningsCents: b.EarningsCents,
		RouteOptimizationScore: b.Score,
		ExpiresAt:              expiresAt,
		CreatedAt:              now,
	}); err != nil {
		return fmt.Errorf("create batch: %w", err)
	}

	for _, s := range b.Stops {
		if err := q.CreateBatchStop(ctx, sqlc.CreateBatchStopParams{
			BatchID:      id,
			Seq:          int16(s.Seq),
			OrderID:      s.OrderID,
			Type:         sqlc.StopType(s.Type),
			Lat:          s.Location.Lat,
			Lng:          s.Location.Lng,
			LegDistanceM: int32(math.Round(s.LegDistanceM)),
			EtaOffsetS:   int32(math.Round(s.ETASeconds)),
		}); err != nil {
			return fmt.Errorf("create batch stop: %w", err)
		}
	}

	for i, o := range b.Orders {
		batchID, seq := id, int16(i+1)
		n, err := q.AssignOrderToBatch(ctx, sqlc.AssignOrderToBatchParams{BatchID: &batchID, BatchSeq: &seq, ID: o.ID})
		if err != nil {
			return fmt.Errorf("assign order %s: %w", o.ID, err)
		}
		if n != 1 {
			// We hold the row lock, so a pending order cannot change under us; this guards the invariant.
			return fmt.Errorf("assign order %s: expected 1 row, got %d", o.ID, n)
		}
	}
	return nil
}

// loadBatches reads batches with their orders and stops, in the order of ids.
func loadBatches(ctx context.Context, q sqlc.Querier, ids []string) ([]domain.Batch, error) {
	rows, err := q.GetBatchesByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("get batches: %w", err)
	}
	stops, err := q.ListBatchStops(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list batch stops: %w", err)
	}
	orders, err := q.ListBatchOrders(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list batch orders: %w", err)
	}

	byID := make(map[string]*domain.Batch, len(rows))
	for _, r := range rows {
		byID[r.ID] = &domain.Batch{
			ID:                     r.ID,
			Status:                 domain.BatchStatus(r.Status),
			PickupCentroid:         domain.Location{Lat: r.PickupCentroidLat, Lng: r.PickupCentroidLng},
			PickupSpreadM:          int(r.PickupSpreadM),
			OrdersCount:            int(r.OrdersCount),
			TotalDistanceM:         int(r.TotalDistanceM),
			EstimatedDurationS:     int(r.EstimatedDurationS),
			TotalDeliveryFeeCents:  r.TotalDeliveryFeeCents,
			EstimatedEarningsCents: r.EstimatedEarningsCents,
			RouteOptimizationScore: r.RouteOptimizationScore,
			ExpiresAt:              r.ExpiresAt,
			CreatedAt:              r.CreatedAt,
			RiderID:                r.RiderID,
			AcceptedAt:             r.AcceptedAt,
			Version:                int(r.Version),
		}
	}
	for _, s := range stops {
		if b := byID[s.BatchID]; b != nil {
			b.Stops = append(b.Stops, domain.BatchStop{
				Seq: int(s.Seq), Type: domain.StopType(s.Type), OrderID: s.OrderID,
				Location:     domain.Location{Lat: s.Lat, Lng: s.Lng},
				LegDistanceM: int(s.LegDistanceM), ETAOffsetS: int(s.EtaOffsetS),
			})
		}
	}
	for _, o := range orders {
		if o.BatchID == nil {
			continue
		}
		if b := byID[*o.BatchID]; b != nil {
			bo := domain.BatchOrder{
				ID:            o.ID,
				Pickup:        domain.Location{Lat: o.PickupLat, Lng: o.PickupLng},
				Delivery:      domain.Location{Lat: o.DeliveryLat, Lng: o.DeliveryLng},
				TripDistanceM: int(o.TripDistanceM),
				CreatedAt:     o.CreatedAt,
			}
			if o.RiderEarningsCents != nil {
				bo.RiderEarningsCents = *o.RiderEarningsCents
			}
			if o.BatchSeq != nil {
				bo.Seq = int(*o.BatchSeq)
			}
			b.Orders = append(b.Orders, bo)
		}
	}

	out := make([]domain.Batch, 0, len(ids))
	for _, id := range ids {
		if b := byID[id]; b != nil {
			out = append(out, *b)
		}
	}
	return out, nil
}
