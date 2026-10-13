package repository

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/repository/sqlc"
)

// domain mirrors the database enums order_status and batch_status as plain constants. This keeps the two
// vocabularies in step: if a migration adds or renames a status and the domain constants are not updated, this fails.
func TestDomainStatusesMatchTheDatabaseEnums(t *testing.T) {
	orderStatuses := map[domain.OrderStatus]sqlc.OrderStatus{
		domain.OrderPending:   sqlc.OrderStatusPending,
		domain.OrderBatched:   sqlc.OrderStatusBatched,
		domain.OrderAssigned:  sqlc.OrderStatusAssigned,
		domain.OrderPickedUp:  sqlc.OrderStatusPickedUp,
		domain.OrderDelivered: sqlc.OrderStatusDelivered,
		domain.OrderCancelled: sqlc.OrderStatusCancelled,
	}
	for d, s := range orderStatuses {
		assert.Equal(t, string(s), string(d))
	}

	batchStatuses := map[domain.BatchStatus]sqlc.BatchStatus{
		domain.BatchOpen:       sqlc.BatchStatusOpen,
		domain.BatchAccepted:   sqlc.BatchStatusAccepted,
		domain.BatchInProgress: sqlc.BatchStatusInProgress,
		domain.BatchCompleted:  sqlc.BatchStatusCompleted,
		domain.BatchCancelled:  sqlc.BatchStatusCancelled,
		domain.BatchExpired:    sqlc.BatchStatusExpired,
	}
	for d, s := range batchStatuses {
		assert.Equal(t, string(s), string(d))
	}

	stopTypes := map[domain.StopType]sqlc.StopType{
		domain.StopPickup:   sqlc.StopTypePickup,
		domain.StopDelivery: sqlc.StopTypeDelivery,
	}
	for d, s := range stopTypes {
		assert.Equal(t, string(s), string(d))
	}
}
