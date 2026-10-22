package service

import (
	"context"
	"time"

	"github.com/codercollo/zemi-fleet/internal/capacity"
	"github.com/codercollo/zemi-fleet/internal/config"
	"github.com/codercollo/zemi-fleet/internal/repository"
	"github.com/codercollo/zemi-fleet/internal/repository/sqlc"
)

type capacityPolicy struct {
	params   capacity.Params
	timezone *time.Location
}

func newCapacityPolicy(cfg config.CapacityConfig) capacityPolicy {
	mode := capacity.Mode(cfg.Mode)
	if mode == "" {
		mode = capacity.ModeCount
	}
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		loc = time.UTC
	}
	return capacityPolicy{params: capacity.DefaultParams(mode), timezone: loc}
}

func (p capacityPolicy) effective(ctx context.Context, store repository.Store, rider sqlc.Rider, now time.Time) (int, error) {
	in := capacity.Inputs{MaxCapacity: int(rider.MaxCapacity), CurrentOrders: int(rider.CurrentOrdersCount)}
	if p.params.Mode == capacity.ModeDynamic {
		local := now.In(p.timezone)
		startLocal := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, p.timezone)
		endLocal := startLocal.AddDate(0, 0, 1)
		stats, err := store.GetRiderCapacityStats(ctx, sqlc.GetRiderCapacityStatsParams{
			RiderID: &rider.ID, StartOfDay: timePtr(startLocal.UTC()), EndOfDay: timePtr(endLocal.UTC()), RecentStart: now.Add(-30 * 24 * time.Hour),
		})
		if err != nil {
			return 0, err
		}
		in.ActiveWeightG = stats.ActiveWeightG
		in.ActiveVolumeCM3 = stats.ActiveVolumeCm3
		in.Hour = local.Hour()
		if stats.TotalBatches > 0 {
			in.CompletionRate = float64(stats.CompletedBatches) / float64(stats.TotalBatches)
		}
		in.DistanceTodayM = stats.DistanceTodayM
	}
	return capacity.EffectiveCapacity(in, p.params), nil
}

func timePtr(t time.Time) *time.Time { return &t }
