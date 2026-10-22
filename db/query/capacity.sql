-- Capacity inputs are aggregated in one query so every caller uses the same snapshot.
-- start_of_day/end_of_day are the business-day boundary in UTC.
-- name: GetRiderCapacityStats :one
WITH active AS (
    SELECT COALESCE(SUM(weight_g), 0)::bigint AS active_weight_g,
           COALESCE(SUM(volume_cm3), 0)::bigint AS active_volume_cm3
    FROM orders AS o
    WHERE o.rider_id = sqlc.arg(rider_id)
      AND o.status IN ('assigned', 'picked_up')
), history AS (
    SELECT COUNT(*) FILTER (WHERE status = 'completed')::bigint AS completed_batches,
           COUNT(*)::bigint AS total_batches,
           COALESCE(SUM(total_distance_m) FILTER (WHERE status = 'completed'
               AND completed_at >= sqlc.arg(start_of_day)
               AND completed_at < sqlc.arg(end_of_day)), 0)::bigint AS distance_today_m
    FROM batches AS b
    WHERE b.rider_id = sqlc.arg(rider_id)
      AND b.created_at >= sqlc.arg(recent_start)
)
SELECT active.active_weight_g, active.active_volume_cm3,
       history.completed_batches, history.total_batches, history.distance_today_m
FROM active, history;
