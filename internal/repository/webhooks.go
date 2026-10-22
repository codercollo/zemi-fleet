package repository

import (
	"context"
	"time"

	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/repository/sqlc"
	"github.com/codercollo/zemi-fleet/internal/webhooks"
)

func (s *SQLStore) ClaimDue(ctx context.Context, now time.Time) (*webhooks.Delivery, error) {
	r, err := s.Queries.ClaimDueWebhookDelivery(ctx, now)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &webhooks.Delivery{ID: r.ID, Attempts: int(r.Attempts), NextAttemptAt: r.NextAttemptAt,
		Event: webhooks.Event{ID: r.EventID, Type: r.EventType, Payload: r.Payload}, URL: r.Url, Secret: r.Secret}, nil
}

func statusPtr(status int) *int32 { v := int32(status); return &v }
func (s *SQLStore) MarkDelivered(ctx context.Context, id string, _ time.Time) error {
	return s.Queries.MarkWebhookDelivered(ctx, sqlc.MarkWebhookDeliveredParams{ID: id})
}
func (s *SQLStore) MarkRetry(ctx context.Context, id string, next time.Time, attempts, status int) error {
	return s.Queries.MarkWebhookRetry(ctx, sqlc.MarkWebhookRetryParams{ID: id, NextAttemptAt: next, Attempts: int32(attempts), LastStatus: statusPtr(status)})
}
func (s *SQLStore) MarkFailed(ctx context.Context, id string, _ time.Time, status int) error {
	return s.Queries.MarkWebhookFailed(ctx, sqlc.MarkWebhookFailedParams{ID: id, LastStatus: statusPtr(status)})
}
func (s *SQLStore) ListFailed(ctx context.Context, limit, offset int) ([]webhooks.FailedDelivery, error) {
	rows, err := s.Queries.ListFailedWebhookDeliveries(ctx, sqlc.ListFailedWebhookDeliveriesParams{Limit: int32(limit), Offset: int32(offset)})
	if err != nil {
		return nil, err
	}
	out := make([]webhooks.FailedDelivery, 0, len(rows))
	for _, r := range rows {
		out = append(out, webhooks.FailedDelivery{ID: r.ID, EventID: r.EventID, EventType: r.EventType, SubscriptionID: r.SubscriptionID, ClientName: r.ClientName, Attempts: int(r.Attempts), LastStatus: func() *int {
			if r.LastStatus == nil {
				return nil
			}
			v := int(*r.LastStatus)
			return &v
		}(), NextAttemptAt: r.NextAttemptAt})
	}
	return out, nil
}
func (s *SQLStore) Retry(ctx context.Context, id string, now time.Time) error {
	n, err := s.Queries.RetryWebhookDelivery(ctx, sqlc.RetryWebhookDeliveryParams{ID: id, NextAttemptAt: now})
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}
