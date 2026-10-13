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

The cone is skipped (the matcher uses the **full circle**: distance filter only, and returns
`direction_filter_applied: false` and `angle_from_rider_degrees: null` for every match) in exactly two cases:

- the **bearing is null/omitted**: there is no direction to filter on;
- a **speed is reported and is below `CONE_MIN_SPEED_KMH`** (default 3).

A bearing with **no speed** uses the cone: nothing says the heading is noise, so it is trusted.

Why: a GPS heading is derived from movement. A stationary or very slow rider has a heading that is noise,
and cutting their results to a random 60 degree wedge would hide orders next to them.

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

---

## Algorithm: order batching

`POST /api/v2/orders/batch` offers a rider batches of up to 5 pending orders that are picked up close
together and delivered in similar directions. The pure algorithm is `internal/batching`
(`batcher.go`, `sequencer.go`, `scoring.go`: no DB, config or clock imports); the transaction that feeds
it is `internal/repository/tx_create_batches.go`.

### Steps

1. **Rider checks.** Unknown rider gives 404. A rider who is off shift or at capacity gets
   `{"batches": []}` and no transaction runs. Otherwise the effective batch size is
   `min(max_orders, max_capacity - current_orders_count)`, using the database's counts.
2. **Expire.** In one transaction, every open batch whose `expires_at <= now` (clock time) becomes
   `expired` and its orders go back to `pending` (`batch_id` and `batch_seq` cleared).
3. **Select.** Pending orders whose pickup is within `BATCH_SEARCH_RADIUS_KM` of the rider (bounding box
   in SQL, exact haversine in Go), oldest first, `FOR UPDATE SKIP LOCKED`. If two riders ask at the same
   moment, the second simply skips rows the first holds, so no order is ever batched twice.
4. **Form** (`batching.Form`): described below.
5. **Store.** Insert each batch (`status = 'open'`, no rider, pickup centroid, `pickup_spread_m`,
   `expires_at = now + BATCH_TTL`), its stops, and mark its orders `batched` with `batch_id` and
   `batch_seq` (their FIFO rank).
6. **Offer.** The rider is shown the new batches plus any still-open batches whose first pickup is
   within `BATCH_SEARCH_RADIUS_KM` and whose size fits their effective size. A `batch_offers` row is
   written per batch (once: asking again does not duplicate it or inflate `riders.offers_count`).
7. **Sort** by `distance_to_first_pickup_km` (rider to the route's first stop), then by the batch's
   oldest order (FIFO).

A batch stays `open`, assigned to nobody, until a rider accepts it (see "Exclusive batch acceptance"). Anyone
near the vendor may see it; the first to accept gets it exclusively.

### Listing open batches (`GET /api/v2/batches`)

`?lat=&lng=` (required), `radius_km` (default `BATCH_VISIBILITY_RADIUS_KM`, at most `BATCH_SEARCH_RADIUS_KM`),
`page` (default 1) and `page_size` (default 20, 1 to 50). It returns the open batches with `expires_at > now`
whose first pickup is within the radius, in the same shape as `POST /orders/batch` (so each carries
`distance_to_first_pickup_km` from the given point), sorted by that distance, then the oldest order's
`created_at`, then `batch_id`, with the book's `metadata` block (`current_page`, `page_size`, `first_page`,
`last_page`, `total_records`; `metadata` is `{}` when nothing matches, as in the book). It is **read-only**: it
records no offers and expires nothing, it only filters on `expires_at`.

### Forming batches (`Form`)

1. Sort orders by `created_at`, then `id` (**FIFO**).
2. The **seed** is the oldest order not yet in a batch.
3. Walk the remaining unused orders in FIFO order and add one if **both** hold:
   - its pickup is within the radius (`radius_km` from the request, at most `BATCH_PICKUP_RADIUS_KM`)
     of **every pickup already in the batch** (the pairwise rule), and
   - its trip bearing (pickup to delivery) is within `BATCH_DIRECTION_TOLERANCE_DEG` of the **seed's**
     trip bearing (`geo.AngleDiff`, so 350 and 20 degrees are 30 apart).

   Stop at the effective size (never more than `BATCH_MAX_ORDERS`).
4. An order with no partners becomes a batch of one, so it is still offered.
5. Repeat from step 2 until every order is in exactly one batch.

**The pairwise rule and why.** The brief says pickups are "within 2 km *of each other*". Measuring only
from the seed would let A, B and C join when A-B is 1.5 km and A-C is 1.5 km but B-C is 3 km (B and C on
opposite sides of A), which breaks that promise. Checking every pair keeps the whole pickup cluster inside
the radius. Likewise A-B 1.5 km, B-C 1.5 km, A-C 3 km cannot all be one batch.

**Direction tolerance.** Direction is compared with the *seed's* bearing, not the previous member's, so a
batch cannot drift: trips at 0, 40 and 80 degrees are not one batch (80 is 80 from the seed) even though
each is within 45 of its neighbour. A trip shorter than 1 m has no direction and is compatible with any.

**FIFO.** Seeds are taken oldest-first and members are added oldest-first, so when a cluster has more
than 5 orders the 5 oldest share the first batch and nobody who has waited longer is passed over for a
newer order.

### Sequencing (`Sequence`)

The route starts at the seed's pickup. From the current position it goes to the **nearest feasible
stop**: the pickup of any order not yet collected, or the delivery of an order already collected and not
yet delivered. A delivery is never feasible before its own pickup, so every pickup precedes its delivery.
Ties go to a pickup before a delivery, then to the older order, so the result is deterministic.

### Scoring (`scoring.go`)

```
route_distance           = sum of the stop-to-stop legs (the first stop has leg 0)
direct(order)            = haversine(order pickup, order delivery)
along(order)             = distance travelled along the route from the order's PICKUP stop
                           to its DELIVERY stop
detour(order)            = min(1, direct(order) / along(order))
route_optimization_score = mean of detour(order) over the batch's orders        (a batch of one = 1.0)
distance_saved_km        = sum(direct(order)) - route_distance                  (negative if batching costs distance)
stop_time (seconds)      = cumulative_distance_to_stop / AVG_SPEED_KMH
                           + STOP_DWELL_MIN * 60 * (number of EARLIER stops)
estimated_time_min       = stop_time of that order's delivery, in minutes (the API rounds up)
estimated_earnings       = sum(rider_earnings_cents)   # 90% of the delivery fees
```

**Why a detour score.** The earlier "sum of trips / route" ratio hit its 1.0 cap for almost every
same-direction batch, so it could not tell batches apart. The detour score asks, per order, "how far does
this parcel actually travel between pickup and drop-off compared with the straight line?" An order delivered
straight after its pickup scores 1.0; one that rides along while the rider visits other stops scores less. The
mean over the batch then genuinely varies. A sequence that delivers immediately after each pickup scores
higher than "pick everything up, then deliver everything" (tested). `distance_saved_km` reports the other
side: how much shorter the batch route is than doing the trips one by one.

The score ignores the deadhead between one order's drop-off and the next pickup, and the distance from the rider
to the first pickup (that is `distance_to_first_pickup_km`).

### Worked example (seed data, fresh seed, `rider_001` at -1.2650, 36.8038)

The oldest pending order near Westlands is `ord_002` (Sarit Centre to Kasarani, bearing 65.9). It is the
seed. In FIFO order the next unused orders that fit are `ord_003` (Westlands Bites to Eastleigh, bearing
102.2: 36.3 degrees from the seed, pickup 325 m away), `ord_009` and `ord_029` (Sarit to Kasarani again)
and `ord_020` (Westlands Bites to Eastleigh again). That is five orders, so the batch is full:

```
batch   [ord_002, ord_003, ord_009, ord_020, ord_029]          (FIFO order)
route   1P:002 > 2P:009 > 3P:029 > 4P:003 > 5P:020 > 6D:003 > 7D:020 > 8D:002 > 9D:009 > 10D:029    (13.15 km)

order    direct   along the route (pickup stop -> delivery stop)   detour
ord_002  11.32 km   13.15 km  (stop 1 -> stop 8)                    0.861
ord_003   4.72 km    4.72 km  (stop 4 -> stop 6)                    1.000
ord_009  11.32 km   13.15 km  (stop 2 -> stop 9)                    0.861
ord_020   4.72 km    4.72 km  (stop 5 -> stop 7)                    1.000
ord_029  11.32 km   13.15 km  (stop 3 -> stop 10)                   0.861

score   (0.861 + 1.000 + 0.861 + 1.000 + 0.861) / 5 = 0.917
saved   43.41 km of separate trips - 13.15 km route = 30.26 km
time    ord_003 is delivered at stop 6: 16 min of driving + 5 earlier stops x 2 min = 26 min
earn    216 + 396 + 396 + 216 + 396 = 1,620 KES
```

The other batches from the same request score 0.881 to 1.0, e.g. `ord_034` + `ord_035` = 0.881 (one
parcel waits while the rider picks up the second) and `ord_036` + `ord_040` = 1.0.

The next oldest unused order, `ord_004` (Yaya Centre to Madaraka, bearing 121), is both too far from Sarit
(about 3.6 km) and heading a different way (55 degrees off the seed), so it seeds a different batch (`ord_004`,
`ord_005`, `ord_010`, `ord_011`, `ord_013`), and so on. `ord_018` is left over from that cluster (6 Yaya
orders, capacity 5) and becomes a batch of one.

### Configuration

| Variable | Default | Meaning |
|---|---|---|
| `BATCH_MAX_ORDERS` | 5 | Largest batch (1 to 5; the database allows 5) and the largest `max_orders` a request may use |
| `BATCH_PICKUP_RADIUS_KM` | 2 | Largest `radius_km` a request may use (the pairwise pickup radius) |
| `BATCH_DIRECTION_TOLERANCE_DEG` | 45 | Largest bearing difference from the seed's trip |
| `BATCH_SEARCH_RADIUS_KM` | 5 | How far from the rider orders are batched and open batches are shown |
| `BATCH_VISIBILITY_RADIUS_KM` | 3 | Default `radius_km` of `GET /batches` (a request may use up to the search radius) |
| `ORDER_MIN_TRIP_M` | 50 | Shortest allowed pickup-to-delivery distance; shorter orders are rejected with 422 |
| `BATCH_TTL` | 10m | How long an open batch is offered before it expires |
| `AVG_SPEED_KMH` | 20 | Driving speed for the time estimates |
| `STOP_DWELL_MIN` | 2 | Minutes spent at each earlier stop |

### Assumptions and decisions

- Batching runs per request, around the asking rider (not as a background job), and only on orders within
  the search radius of that rider.
- A request's `radius_km` is the pairwise pickup radius; `BATCH_PICKUP_RADIUS_KM` is only the ceiling.
- Existing open batches are shown to any rider whose location is within `BATCH_SEARCH_RADIUS_KM` of the
  batch's first pickup and whose effective size fits the batch; the request's `radius_km` does not filter them.
- A batch's route starts at its first pickup; `distance_to_first_pickup_km` is the rider-specific leg
  to get there.
- `estimated_time_min` is rounded **up** to a whole minute so the estimate never under-promises.
- `POST /orders` rejects an order whose pickup (the vendor) and delivery are closer than `ORDER_MIN_TRIP_M`
  (default 50 m). A trip that short has no usable direction, and batching and matching compare bearings.
  The batching code still guards against a zero-length trip (no divide by zero), but cannot meet one.
- `distance_saved_km` can be negative: a batch whose route is longer than its separate trips is reported honestly.
- Batching does not record the rider's position (only `match-orders` does).

### Limitations (for Improvements)

- **Nearest neighbour is a heuristic, not an optimal tour.** A small TSP-with-precedence solver (or
  insertion heuristics plus 2-opt) would shorten some routes.
- **Straight-line (haversine) distances.** Real road distance and traffic (OSRM or similar) would change
  the grouping, the sequence and the time estimates. Out of scope per the brief.
- **Greedy FIFO grouping** is not an optimal partition: it never trades a slightly worse seed batch for a
  better overall result, and it does not look at delivery clustering, only direction.
- **Scoring** measures per-order detour only; it ignores the deadhead between a drop-off and the next
  pickup, the distance from the rider to the first pickup, and order value or urgency.
- **Concurrent requests** skip locked orders rather than wait, so a rider who asks at the same instant as
  another may not see the other's brand-new batches until their next request.
- **No re-batching of open batches.** Orders stay in their batch until it is accepted or expires.

---

## Exclusive batch acceptance

`POST /api/v2/batches/:id/accept` with `{"rider_id": "..."}`. Per the client, a batch is an open offer: any
eligible rider near the vendor can see it, and **the first rider to accept gets it exclusively**; everyone
after that gets `409`. The whole decision is one transaction (`internal/repository/tx_accept_batch.go`).

### Steps

1. **Lock the rider** (`SELECT ... FROM riders WHERE id = $1 FOR UPDATE`). Unknown rider: `404`. Off shift:
   `422 "rider is not available"`.
2. **Read the batch.** Unknown batch: `404`. If this rider already holds it, return it with `200`
   (**idempotent retry**: a client whose response was lost can safely ask again).
3. **Capacity.** `current_orders_count + batch.orders_count > max_capacity` is `422` (`ErrRiderAtCapacity`).
4. **Claim**:

   ```sql
   UPDATE batches
   SET status = 'accepted', rider_id = $rider, accepted_at = $now, version = version + 1
   WHERE id = $batch AND status = 'open' AND expires_at > $now
   RETURNING *;
   ```
5. **No row back?** Re-read the batch: gone is `404`; held by this rider is `200` (idempotent); otherwise
   `409 "batch is no longer available"` (taken by someone else, or expired).
6. **Row back (we won):** set the batch's orders to `assigned` with `rider_id`; `riders.current_orders_count += n`,
   `accepted_count += 1`, `version += 1`; upsert the `batch_offers` row (`accepted = true`, `responded_at = now`),
   inserting it, and counting it in `offers_count`, if the rider was never shown this batch.

All times come from the injected clock.

### Why the conditional UPDATE guarantees one winner

The exclusivity lives in the `WHERE status = 'open'` of one statement, not in application code. When two
transactions run that UPDATE on the same batch, Postgres lets the first take the row lock and makes the second
**wait**. When the first commits, the second re-evaluates its `WHERE` against the committed row (this is how
`READ COMMITTED` treats a blocked UPDATE), finds `status = 'accepted'`, and updates **zero** rows. There is no
window between "check it is open" and "mark it taken" because they are the same statement. Zero rows is how a
loser learns it lost. Expiry is part of the same predicate (`expires_at > $now`), so an expired batch cannot be
claimed even before anything has marked it `expired`.

### Lock order

Every accept takes locks in the same order: **rider row first, then batch row**. A transaction never holds a
batch lock while waiting for a rider lock, so two accepts cannot wait on each other in a cycle: no deadlock.
Locking the rider first also serialises two concurrent accepts by the *same* rider, so the second one's
capacity check sees the first one's result and can never push the rider past `max_capacity` (the table's CHECK
constraint is the last line of defence). Different riders racing for one batch only contend on the batch row.

### Two riders racing

```
 Rider A (tx A)                              Rider B (tx B)                           batches row
 --------------                              --------------                           -----------
 BEGIN                                       BEGIN                                    status=open
 lock rider A FOR UPDATE                     lock rider B FOR UPDATE                  (different rows: no wait)
 read batch; capacity ok                     read batch; capacity ok
 UPDATE batches ... WHERE status='open'
   -> takes the row lock, sets accepted ---> UPDATE batches ... WHERE status='open'   LOCKED by A
 assign orders, bump rider A, offer          ...blocked, waiting for A...
 COMMIT -----------------------------------> lock released; B re-checks its WHERE     status=accepted, rider=A
                                             status is 'accepted': 0 rows updated
                                             re-read batch: held by A, not B
                                             ROLLBACK, return 409
```

Rider B leaves no trace: nothing is assigned, no counter moves, no offer is recorded.

### Decisions beyond the brief

- A successful accept bumps the rider's `version` (their order count really changed), so a client holding a
  stale `X-Expected-Version` will get a `409` on its next versioned write. Telemetry and offer counting do not bump it.
- A rider who accepts a batch they were never shown gets an offer recorded for it, so `acceptance_rate =
  accepted_count / offers_count` stays at most 1.
- Capacity is checked before the claim, as specified, so a rider already at capacity who tries a batch that is
  also taken gets `422`, not `409`.
- Other riders' unanswered offers for an accepted batch stay as they are (`accepted IS NULL`).
- Orders in an accepted batch are no longer offered by `match-orders`, `GET /batches` or `POST /orders/batch`.
