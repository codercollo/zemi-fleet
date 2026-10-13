# Zemi Fleet V2: Documentation

This file grows phase by phase. Each algorithm gets its own section; setup, API reference,
assumptions and improvements are completed in Phase 12.

---

## Algorithm: proximity matching

`POST /api/v2/riders/match-orders` answers: *"which pending pickups are close and roughly ahead of this
moving rider?"* The pure algorithm lives in `internal/matching/cone.go` (no database, config or HTTP
imports); `internal/service/matching_service.go` feeds it from the database.

### The cone

```
                      heading (bearing_degrees)
                              ^
                    -30 deg   |   +30 deg
                        \     |     /
                         \    |    /       . order C   (outside: 41 deg off heading)
              order A     \   |   /
              (in, 12 deg) \  |  /   order B (in, exactly 30 deg: edge is inclusive)
                    .       \ | /      .
                             \|/
                          [ RIDER ]  ------ radius (CONE_RADIUS_KM, inclusive) ------>
                              |
                              |            . order D  (behind: outside the cone)
                              v
```

Defaults: radius 1 km, half angle 30 degrees (so a 60 degree cone). Angles are measured from the
rider's heading, clockwise positive, and wrap correctly across north: a heading of 350 degrees and a
pickup at bearing 10 degrees are 20 degrees apart, not 340.

### Steps

1. **Record the rider's position.** The call stores the position, bearing, speed and a clock timestamp
   on the rider (same rules as `PATCH /riders/:id/location`: an omitted bearing or speed is stored as
   `NULL`). It deliberately does **not** use the optimistic version check and does not bump the rider's
   version, so a location ping can never fail with `409` and never makes the rider's own versioned
   writes conflict.
2. **Capacity check.** A rider who is unavailable, or whose stored `current_orders_count` has reached
   `max_capacity`, gets `matching_orders: []`, `can_accept_more_orders: false` and
   `max_additional_orders: 0`. The candidate query is not run.
3. **Candidates from SQL.** Pending orders (`status = 'pending'` only, so anything already batched or
   assigned is excluded) whose pickup lies inside the bounding box of the radius around the rider
   (`geo.BoundingBox`). The literal `status = 'pending'` plus the lat/lng range lets Postgres use the
   partial index `idx_orders_pending_pickup`. The box is only a cheap prefilter; it is slightly larger
   than the circle.
4. **Distance filter.** Exact great-circle distance (`geo.Haversine`); keep pickups at distance
   **<= radius** (inclusive).
5. **Direction filter (the cone), when it applies:** keep pickups whose bearing from the rider is within
   `CONE_HALF_ANGLE_DEG` of the rider's heading, **inclusive** at exactly the half angle
   (`geo.WithinCone`). Each match reports its absolute angle (0 to 30) in `angle_from_rider_degrees`.
6. **Sort** by distance ascending, then `created_at` ascending (oldest first, FIFO), then `order_id`,
   so the same input always gives the same order.
7. **Cap** at `CONE_MAX_RESULTS`.

### The fallback rule (when the cone is *not* used)

The cone is applied only if the heading is known **and** the speed is known **and** the speed is at
least `CONE_MIN_SPEED_KMH` (default 3). Otherwise the matcher uses the **full circle** (distance filter
only) and returns `direction_filter_applied: false` and `angle_from_rider_degrees: null` for every match.

Why: a GPS heading is derived from movement. A stationary or very slow rider (or one whose phone
reports no heading) has a heading that is noise, and cutting their results to a random 60 degree wedge
would hide orders next to them. An unknown speed is treated like a slow one, because nothing vouches
for the heading.

### The authoritative count

`can_accept_more_orders` and `max_additional_orders` (`max_capacity - current_orders_count`) always come
from the **database**. The server is what assigns orders, so its count cannot be stale the way a phone's
can. The request's `current_orders_count` is validated (0 to 5) but is **never used for a decision**; if
it disagrees with the stored count, a warning is logged (`rider reported a different order count than
the server holds`) and the request carries on.

### Response

Example (illustrative values):

```json
{
  "matching_orders": [
    {
      "order_id": "ord_036",
      "pickup_location": { "lat": -1.266, "lng": 36.8045 },
      "distance_from_rider_km": 0.18,
      "angle_from_rider_degrees": 4.2,
      "order_age_minutes": 14,
      "estimated_earnings_kes": 234.0
    }
  ],
  "can_accept_more_orders": true,
  "max_additional_orders": 5,
  "direction_filter_applied": true
}
```

- `distance_from_rider_km` is rounded to 2 decimals, `angle_from_rider_degrees` to 1.
- `order_age_minutes` is whole minutes (floored) from the injected clock to the order's `created_at`;
  it is never negative.
- `estimated_earnings_kes` is the order's `rider_earnings_cents` (the rider's 90% share, snapshotted on
  the order) shown as KES.
- `matching_orders` is always an array, `[]` when there is nothing to show.

### Configuration

| Variable | Default | Meaning |
|---|---|---|
| `CONE_RADIUS_KM` | 1 | Maximum pickup distance from the rider (inclusive) |
| `CONE_HALF_ANGLE_DEG` | 30 | Half the cone's opening (30 = a 60 degree cone) |
| `CONE_MIN_SPEED_KMH` | 3 | Below this speed (or with an unknown heading/speed) the full circle is used |
| `CONE_MAX_RESULTS` | 20 | Cap on `matching_orders` |

### Edge cases and assumptions

- **Float tolerance.** A pickup exactly on the radius or exactly on the cone edge is *in*. Because
  trigonometry can land a few ULPs over, comparisons allow 1 micrometre / 1e-6 degrees of slack. Both
  are far below GPS precision.
- **Pickup at the rider's own position** (closer than 1 m) has no meaningful bearing, so it counts as
  inside the cone with angle 0.
- **Distance ties** are exact-value ties (typically two orders from the same vendor), broken by age.
  Pickups a few metres apart are ordered by distance, not age.
- **Heading 10 degrees, order at bearing 340 degrees** is exactly 30 degrees away, so it is *inside*
  (inclusive edge); 339 degrees is outside.
- **Rider at capacity or unavailable** still has their position recorded; only the matching is skipped.
  `direction_filter_applied` is `false` in that case because no filter ran.
- A rider's position is trusted as sent (validated only for range); no check that it is in Nairobi.
