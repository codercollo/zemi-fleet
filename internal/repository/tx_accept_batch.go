package repository

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/geo"
	"github.com/codercollo/zemi-fleet/internal/repository/sqlc"
)

// AcceptBatchParams are the inputs of the accept-batch transaction.
type AcceptBatchParams struct {
	BatchID string
	RiderID string
	// Now is the clock's time: accepted_at, responded_at, and the expiry check all use it.
	Now time.Time
}

// AcceptBatchTx lets a rider claim an open batch. Exactly one rider can ever win a given batch.
//
// Everything runs in ONE transaction, with locks taken in a fixed order (rider first, then batch), so two
// transactions can never wait on each other in a cycle:
//
//  1. lock the rider row (FOR UPDATE): unknown rider is ErrNotFound, an off-shift rider is
//     ErrRiderUnavailable. Locking the rider also serialises two concurrent accepts by the SAME rider,
//     so their capacity checks see each other's result.
//  2. read the batch: unknown is ErrNotFound. If this rider already holds it, return it (a retry of
//     a request whose response was lost is idempotent).
//  3. capacity: current_orders_count + the batch's orders must fit max_capacity, else ErrRiderAtCapacity.
//  4. claim: UPDATE batches ... WHERE id = $1 AND status = 'open' AND expires_at > now. This conditional
//     UPDATE is the exclusivity guarantee: Postgres locks the row, a competing claim waits for the
//     winner's commit, re-evaluates the WHERE against the committed row, sees status = 'accepted' and
//     matches nothing. No row back means the batch was taken, expired, or already ours (see below).
//  5. on success: assign the orders to the rider, bump the rider's counters, and record the offer
//     (inserting one, and counting it in offers_count, if the rider was never shown this batch).
//
// If step 4 matches nothing, the batch is re-read: ours already is 200 (idempotent), gone is
// ErrNotFound, otherwise ErrBatchTaken (taken by someone else, or expired).
func (s *SQLStore) AcceptBatchTx(ctx context.Context, arg AcceptBatchParams) (domain.Batch, error) {
	var result domain.Batch
	err := s.ExecTx(ctx, func(q sqlc.Querier) error {
		var err error
		result, err = acceptBatch(ctx, q, arg)
		return err
	})
	if err != nil {
		return domain.Batch{}, fmt.Errorf("accept batch: %w", err)
	}
	return result, nil
}

func acceptBatch(ctx context.Context, q sqlc.Querier, arg AcceptBatchParams) (domain.Batch, error) {
	// 1. rider first (the fixed lock order)
	rider, err := q.GetRiderForUpdate(ctx, arg.RiderID)
	if err != nil {
		return domain.Batch{}, fmt.Errorf("lock rider: %w", err)
	}
	if !rider.IsAvailable {
		return domain.Batch{}, domain.ErrRiderUnavailable
	}

	// 2. the batch, plus the idempotent retry
	existing, err := q.GetBatch(ctx, arg.BatchID)
	if err != nil {
		return domain.Batch{}, fmt.Errorf("get batch: %w", err)
	}
	if heldBy(existing, arg.RiderID) {
		return loadOne(ctx, q, existing.ID)
	}

	// 3. capacity
	if int(rider.CurrentOrdersCount)+int(existing.OrdersCount) > int(rider.MaxCapacity) {
		return domain.Batch{}, domain.ErrRiderAtCapacity
	}

	// 4. the claim
	rid := arg.RiderID
	claimed, err := q.AcceptBatch(ctx, sqlc.AcceptBatchParams{RiderID: &rid, Now: &arg.Now, ID: arg.BatchID})
	if errors.Is(err, domain.ErrNotFound) {
		current, getErr := q.GetBatch(ctx, arg.BatchID)
		if getErr != nil {
			return domain.Batch{}, fmt.Errorf("re-read batch: %w", getErr)
		}
		if heldBy(current, arg.RiderID) {
			return loadOne(ctx, q, current.ID)
		}
		return domain.Batch{}, domain.ErrBatchTaken
	}
	if err != nil {
		return domain.Batch{}, fmt.Errorf("claim batch: %w", err)
	}

	// 5. consequences of winning
	batchID := claimed.ID
	n, err := q.AssignBatchOrdersToRider(ctx, sqlc.AssignBatchOrdersToRiderParams{RiderID: &rid, BatchID: &batchID})
	if err != nil {
		return domain.Batch{}, fmt.Errorf("assign orders: %w", err)
	}
	if int(n) != int(claimed.OrdersCount) {
		// We own the batch row and its orders are 'batched' by construction; this guards the invariant.
		return domain.Batch{}, fmt.Errorf("assign orders: expected %d, assigned %d", claimed.OrdersCount, n)
	}

	var distance int32 // rider to the batch's pickup centroid; unknown location counts as 0
	if rider.LastLat != nil && rider.LastLng != nil {
		distance = int32(math.Round(geo.Haversine(
			domain.Location{Lat: *rider.LastLat, Lng: *rider.LastLng},
			domain.Location{Lat: claimed.PickupCentroidLat, Lng: claimed.PickupCentroidLng})))
	}
	inserted, err := q.CreateBatchOffer(ctx, sqlc.CreateBatchOfferParams{
		BatchID: claimed.ID, RiderID: arg.RiderID, DistanceM: distance, OfferedAt: arg.Now,
	})
	if err != nil {
		return domain.Batch{}, fmt.Errorf("create offer: %w", err)
	}
	if err := q.MarkBatchOfferAccepted(ctx, sqlc.MarkBatchOfferAcceptedParams{Now: &arg.Now, BatchID: claimed.ID, RiderID: arg.RiderID}); err != nil {
		return domain.Batch{}, fmt.Errorf("mark offer accepted: %w", err)
	}
	if err := q.RecordRiderAccept(ctx, sqlc.RecordRiderAcceptParams{
		Orders: int32(n), NewOffers: int32(inserted), ID: arg.RiderID,
	}); err != nil {
		return domain.Batch{}, fmt.Errorf("update rider: %w", err)
	}

	return loadOne(ctx, q, claimed.ID)
}

// heldBy reports whether the batch has already been accepted by this rider.
func heldBy(b sqlc.Batch, riderID string) bool {
	return b.RiderID != nil && *b.RiderID == riderID
}

func loadOne(ctx context.Context, q sqlc.Querier, id string) (domain.Batch, error) {
	batches, err := loadBatches(ctx, q, []string{id})
	if err != nil {
		return domain.Batch{}, err
	}
	if len(batches) != 1 {
		return domain.Batch{}, fmt.Errorf("load batch %s: %w", id, domain.ErrNotFound)
	}
	return batches[0], nil
}
