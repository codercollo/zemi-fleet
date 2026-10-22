package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/codercollo/zemi-fleet/internal/repository/sqlc"
	"github.com/codercollo/zemi-fleet/internal/util"
)

func writeOutboxEvent(ctx context.Context, q sqlc.Querier, eventType string, payload any, now time.Time) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal %s event: %w", eventType, err)
	}
	w, ok := q.(interface {
		CreateOutboxEvent(context.Context, sqlc.CreateOutboxEventParams) error
	})
	if !ok {
		return fmt.Errorf("transaction query does not support outbox events")
	}
	if err := w.CreateOutboxEvent(ctx, sqlc.CreateOutboxEventParams{
		ID: util.NewOutboxID(now), EventType: eventType, Payload: body, CreatedAt: now,
	}); err != nil {
		return fmt.Errorf("write %s event: %w", eventType, err)
	}
	return nil
}
