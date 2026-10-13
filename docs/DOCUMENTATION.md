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

---

## Algorithm: rider preference matching

`POST /api/v2/orders/match-riders` answers: *"which riders would want this order, best first?"* The pure
algorithm is `internal/matching/preference.go` (no DB, config or HTTP imports); the service
(`internal/service/preference_service.go`) loads the riders and their preferences and feeds it.

### Request

`{"order_id": "...", "order_details": {"category", "pickup_location": {"lat","lng"}, "delivery_location": {"lat","lng"}, "value_kes"}}`

- `order_details` lets the match run for an order that is **not saved yet**. When it is sent it is used, and the
  stored order (if `order_id` names one) is not read: **details win**.
- When `order_details` is omitted, the order is loaded by `order_id` (`404` if unknown). `order_id` is required
  in that case and optional otherwise.
- Validation (`422`): the category must exist; `lat`/`lng` in range; `value_kes` greater than 0 with at most 2
  decimals; pickup and delivery at least `ORDER_MIN_TRIP_M` apart (the same rule as `POST /orders`).

### Hard filters vs the score, and why

**Preferences are hard filters; the score only ranks the riders who passed them.** The brief says a rider who
prefers "electronics only" must not be offered a food order, and asks for "all riders whose preferences match".
If preferences were just score terms, a nearby rider with a great acceptance rate could outrank the rule and
still appear for an order they said they never take, and the `matching_riders` list would contain people who
will decline. Filtering first keeps the list honest; scoring then orders it. A preference left **empty** means
"accepts everything" (no filter).

Any one of these excludes a rider:

| Filter | Rule |
|---|---|
| Availability | `is_available` is false |
| Capacity | `current_orders_count >= max_capacity` |
| Minimum order value | order `value_kes` is below the rider's minimum (equal is **in**; 1 KES below is out) |
| Category | the rider has category preferences and the order's category is not one of them |
| Area | the rider has area preferences and the pickup is inside none of them (haversine to the area centre `<= radius_m`; **on the boundary is in**) |
| Distance | the rider has a **known, fresh** location farther than `MATCH_MAX_DISTANCE_KM` from the pickup |

### Location freshness

A rider's location is **fresh** if `location_updated_at` is within `RIDER_LOCATION_STALE_AFTER` (default 30 minutes)
of the clock. A **stale or missing** location counts as **unknown**: the rider is *not* excluded by distance (we
do not know they are far), but they score 0 for proximity and `distance_to_pickup_km` is `null`. A location with no
timestamp cannot be trusted and is treated as unknown. A timestamp slightly in the future (clock skew) counts as fresh.

### Score

```
category   = 1.0 if the rider explicitly prefers this category, 0.5 if they have no category preference
area       = 1.0 if the pickup is inside an explicitly preferred area, 0.5 if they have no area preference
proximity  = 1 - distance / MATCH_MAX_DISTANCE_KM       (0 if the location is unknown)
acceptance = (accepted + 1) / (offers + 2)               the SMOOTHED acceptance rate; 0.5 for a newcomer

match_score = round2( 0.25*category + 0.20*area + 0.35*proximity + 0.20*acceptance )
```

The weights are `MATCH_WEIGHT_CATEGORY` (0.25), `MATCH_WEIGHT_AREA` (0.20), `MATCH_WEIGHT_PROXIMITY` (0.35) and
`MATCH_WEIGHT_ACCEPTANCE` (0.20). **They must sum to 1.0**; the server refuses to start otherwise (a tolerance of
1e-9 absorbs float noise). The neutral 0.5 means "no opinion": a rider with no preferences sits between a rider who
explicitly wants the order (1.0) and one who would have been filtered out.

**Why smoothed.** Scoring the raw `accepted / offered` rate gives every newcomer 0 and buries them under riders
with any history at all, and gives a rider with one lucky offer a perfect 1.0. `(accepted + 1) / (offers + 2)` (Laplace
smoothing) starts a newcomer at a neutral 0.5 and converges on the real rate as offers accumulate: 1 accepted of 1
offered is 0.67, 0 of 1 is 0.33, 45 of 50 is 0.88, 1 of 20 is 0.09. The response still shows the **raw stored**
`acceptance_rate` (what the rider's record actually is); only the score uses the smoothed value.

**Sorting:** `match_score` descending, then distance ascending with unknown distances last, then `rider_id`. The
sort uses the *rounded* score, the number the client sees, so two riders who both show 0.80 are ordered by distance
rather than by an invisible third decimal. Capped at `MATCH_MAX_RESULTS` (10). `matching_riders` is always an array.

**`matching_preferences`** lists only the *explicit* matches: the category code if the rider prefers it, then the
names of every preferred area that contains the pickup (areas can overlap), e.g. `["electronics","Kilimani"]`. It
is `[]` for a rider with no preferences.

### Worked example (fresh seed)

An **electronics** order worth **KES 5,000**, picked up at Yaya Centre in Kilimani (-1.2917, 36.7878):

```
filtered out (hard filters)
  rider_001 Brian   prefers electronics, but only the Westlands and Parklands areas: pickup is in neither
  rider_003 Kevin   at capacity (5/5)
  rider_005 Samuel  off shift
  rider_006 Grace   prefers food and documents only
  rider_007 Peter   prefers Upper Hill and South B only

ranked   (acceptance below is the SMOOTHED rate used in the score)
  rider_002 Mercy   0.25*1.0 (prefers electronics)  + 0.20*1.0 (pickup is in Kilimani, Kileleshwa and Lavington, all preferred)
                    + 0.35*(1 - 0.39/10) = 0.336    + 0.20*(23/27 = 0.852) = 0.170   = 0.957  -> 0.96
  rider_010 Aisha   0.25*0.5 (no category pref)     + 0.20*0.5 (no area pref)
                    + 0.35*(1 - 0.25/10) = 0.341    + 0.20*(31/37 = 0.838) = 0.168   = 0.734  -> 0.73
  rider_009 David   0.25*0.5                        + 0.20*0.5
                    + 0.35*(1 - 4.48/10) = 0.193    + 0.20*(49/62 = 0.790) = 0.158   = 0.576  -> 0.58
  rider_004 Faith   0.125 + 0.10 + 0.35*(1 - 5.24/10) = 0.167 + 0.20*(12/14 = 0.857) = 0.171 = 0.563 -> 0.56
  rider_008 Lucy    0.125 + 0.10 + 0 (no location yet) + 0.20*0.5 (no offers yet: neutral) = 0.325 -> 0.33   (distance_to_pickup_km: null)
```

The raw rates shown in the response are 0.88, 0.857, 0.8, 0.917 and 0 (Lucy has no offers), exactly as stored; smoothing
changes the ranking terms, not the numbers reported. Faith's raw 0.917 is the best record of the five, but her 5.24 km
costs her more than it earns her.

At KES 300 Mercy drops out (her minimum order value is KES 500), and a **food** order drops Mercy too and brings
in Grace (prefers food; the pickup is inside her Kileleshwa and Lavington areas).

### Configuration

| Variable | Default | Meaning |
|---|---|---|
| `MATCH_MAX_DISTANCE_KM` | 10 | A rider with a fresh location farther than this is excluded; also where the proximity score reaches 0 |
| `RIDER_LOCATION_STALE_AFTER` | 30m | Older locations are unknown (not filtered, proximity 0, distance null) |
| `MATCH_WEIGHT_CATEGORY` / `_AREA` / `_PROXIMITY` / `_ACCEPTANCE` | 0.25 / 0.20 / 0.35 / 0.20 | Score weights; must sum to 1.0 |
| `MATCH_MAX_RESULTS` | 10 | Cap on `matching_riders` |

### Assumptions and decisions

- The pool of candidates is every rider who is on shift with spare capacity; the SQL only pre-filters those two
  conditions and the pure function applies all six (so each is testable in isolation).
- A new rider with no offers yet scores a neutral 0.5 on acceptance (smoothing), so a newcomer is neither buried nor
  favoured; their response shows `acceptance_rate: 0` because that is the stored value.
- `acceptance_rate` in the response is the raw stored rate with 3 decimals; `match_score` has 2 decimals.
- Order creation's minimum trip rule also applies to `order_details`, so an unsaved order is judged as strictly
  as a saved one. A stored order found by `order_id` is not re-validated.
- The seeded rider locations are timestamped when the seed runs, so after `RIDER_LOCATION_STALE_AFTER` they
  become unknown until the riders ping again (see "Try it" in the README for the ping to send first).

### Limitations (for Improvements)

- Straight-line distance from the rider's *last known* position, which may be minutes old, not a road route or an ETA.
- The proximity score is linear in distance; a rider 5 km away scores half of one at the pickup regardless of traffic.
- All eligible riders are loaded and scored in memory per call; fine for a city fleet, a geo-indexed prefilter
  would be needed for tens of thousands of riders.
- Preferences are all-or-nothing filters. There is no "prefers X but will accept Y at a premium".
- It ranks riders but does not reserve or notify anyone; the offer flow is the batch accept in Phase 7.

---

## Idempotency (`Idempotency-Key`)

`POST /orders` and `POST /orders/batch` accept an `Idempotency-Key` header (1 to 64 visible ASCII characters), so a
client that times out and retries cannot create a second order or form a second set of batches. Without the header
nothing changes.

| Situation | Result |
|---|---|
| New key | The request runs normally. The key is **claimed first** (an atomic `INSERT ... ON CONFLICT DO NOTHING`), so two simultaneous requests with one key cannot both run |
| Same key, same body, after the first finished | The stored response is **replayed**: same status, same body, same `Location`, plus `Idempotent-Replay: true`. Nothing is executed |
| Same key, **different** body | `422`, field `idempotency_key` |
| Same key, same body, first still running | `409`, retry shortly |
| First request failed (any non-2xx) | The key is **released**: the failure is not stored, so the corrected retry runs |
| A claim whose request never finished (a crash) | After `IDEMPOTENCY_PROCESSING_TIMEOUT` (2m) another request with the same body takes it over |
| Key older than `IDEMPOTENCY_TTL` (24h) | Ignored; expired keys are deleted whenever a new key is written |
| Malformed key (a space, a control or non-ASCII character, or more than 64 characters) | `400` |

A key is scoped to its endpoint (`/api/v2/orders` and `/api/v2/orders/batch` are independent). "Same body" means the
same JSON after whitespace is removed (a retry that only re-indents still matches; reordering keys does not). Storage:
table `idempotency_keys` (`key`, `endpoint`, `request_hash`, `response_status`, `response_body`, `response_headers`,
`created_at`), migration `000002_idempotency`. A replayed `POST /orders/batch` returns the batches as they were when
first computed, which may have since been accepted or expired; send a new key for a fresh result.

---

## Authentication

Phase 10 (Let's Go Further ch. 16, adapted: API keys, not tokens issued to users). Every route except `GET /api/v2/healthz`
requires an `X-API-Key` header.

### Key format and configuration

`API_KEYS` is a comma-separated list of `name=key` pairs, e.g. `postman=<key>,ops=<key>`. It is validated at startup (the process
refuses to start otherwise, and the message never echoes a key):

| Rule | Detail |
|---|---|
| At least one key | an empty or missing `API_KEYS` is an error |
| Name | 1-32 characters of `[a-z0-9_-]`; it identifies the client in logs |
| Key length | at least **16** characters in `development`, at least **32** in `staging` and `production` |
| Unique | no duplicate names and no duplicate keys |
| Demo key | in `production`, a key equal to the published demo key (`zemi-demo-key-not-for-production-0001`) is refused |

A key may contain any character except `,`; only the first `=` of a pair separates the name. Generate one with
`openssl rand -hex 32` (64 hex characters, 256 bits).

### Hashing and constant-time comparison

After loading, **only SHA-256 hashes** are kept (`config.APIKey{Name, Hash}`); the raw `API_KEYS` string is cleared. On each
request the middleware hashes the `X-API-Key` value and compares that hash with **every** stored hash using
`subtle.ConstantTimeCompare`, without stopping at the first match. Two properties follow: the comparison time does not depend on
how many leading bytes were right (no byte-by-byte timing oracle), and it does not depend on *which* key matched or how many
keys exist. Hashing first also gives both sides a fixed 32-byte length, so the key's length cannot leak through the comparison
either. (Keys are random and long, so a plain unsalted SHA-256 is the right tool; a slow password hash such as bcrypt would only
cost every request CPU.)

### Middleware order and what it does

```
recoverPanic -> requestID -> securityHeaders -> accessLog -> rateLimit -> requireAPIKey -> [idempotent] -> handler
```

- After `rateLimit`: guessing keys is rate-limited per IP, and an unauthenticated flood is limited before any hashing.
- Before idempotency and the handlers: an unauthenticated request cannot reserve an `Idempotency-Key` or reach a service or the
  database. The e2e test `TestE2E_UnauthenticatedRequestsChangeNothing` fires every write route without a key and checks that
  row counts, idempotency records and rider versions are unchanged.
- After `accessLog`, so a 401 is access-logged too. The same goes for unknown paths: an unauthenticated request to a route
  that does not exist is a `401`, not a `404`, so routes cannot be probed.
- Exempt: `GET /api/v2/healthz` only (load balancers and the Docker healthcheck call it without a key). Any other method on that
  path needs a key.
- On success the client's name is put in the request context, and the access log line gets `client=<name>`.

### Failures: one message, and a log line with a fingerprint

A missing key and a wrong key get the **identical** response: `401 {"error":"invalid or missing API key"}` with
`WWW-Authenticate: ApiKey realm="zemi-fleet"`. If the messages differed ("no key" vs "unknown key"), a probing client would learn
whether a header it sent was even well-formed, and which of its guesses to refine. One message tells it nothing; an honest client
sees `WWW-Authenticate` and knows what to send.

Each failed attempt logs one `WARN` line, `authentication failed`, with `key_fingerprint` (the first 8 hex characters of the
presented key's SHA-256, or `none` when no key was sent), `client_ip`, `method` and `route`. The fingerprint lets an operator see
that the same wrong key keeps being retried, or recognise a stale key in a client's configuration, and 32 bits of a hash reveal
nothing usable about the key. **The raw key is never logged**: `TestRequireAPIKey_FailedAttemptsLogAFingerprintAndNeverTheKey`
captures the logs and asserts that no presented key appears in them. A hostile header value cannot forge a log line either
(it is an attribute, never the message: CWE-117).

### Decisions beyond the brief

- **Fail closed:** a server built with no keys at all (possible only in a hand-built test config, since `Load` refuses it)
  answers 401 on every route except healthz.
- **`STAGING_API_KEYS`, not `API_KEYS`, in `docker-compose.yml`.** `make` exports `app.env`, so `API_KEYS` from a developer's
  `app.env` (a short development key) would override the compose fallback, and the staging stack, which demands 32 characters,
  would refuse to start. The compose file maps `STAGING_API_KEYS` (default: the demo key) onto the container's `API_KEYS`.
- **Key rotation:** add the new `name=key`, restart, move the clients over, remove the old pair, restart. Several keys can be
  valid at once for exactly this reason.
- **No key expiry, scopes or revocation list.** A key is valid until it is removed from the configuration.
- `X-API-Key` is read from the header only, never from the query string (URLs end up in logs and browser history).

## Security

How the code maps to the usual weakness lists. "Proof" is the test or check that fails if the protection is removed.

| Weakness | What we did | Proof |
|---|---|---|
| **CWE-362** race condition (OWASP A04 insecure design) | **Exclusive accept:** one conditional `UPDATE batches ... WHERE status = 'open' AND expires_at > now` decides the winner; the rider row is locked first, then the batch, in that fixed order (no deadlock). **Capacity:** checked under the rider row lock, with a `CHECK (current_orders_count <= max_capacity)` as the last line. **Batch creation:** `FOR UPDATE SKIP LOCKED`. **Idempotency claim:** one atomic insert. **Rider edits:** optimistic locking on `version` | integration: 20 riders accept one batch (1 winner); one rider accepts two batches (never over capacity); concurrent riders never share an order; 20 concurrent claims of one idempotency key (1 runs); e2e concurrent load with SQL invariants |
| **CWE-89** SQL injection (OWASP A03) | Every query is a **sqlc**-generated, parameterised statement; no SQL is built from strings. The IDs that reach queries are also format-checked first | `grep` finds no string-built SQL outside tests; `internal/api/ids_test.go` sends `'; DROP TABLE` in every ID position |
| **CWE-190** integer overflow / wraparound | Money is **int64 cents**, never float. KES is converted with `math.Round` (19.99 becomes 1999, not 1998), values with more than 2 decimals are rejected, and `ORDER_MAX_VALUE_KES` / `ORDER_MAX_DELIVERY_FEE_KES` (default 10,000,000, validated at most 1e9) bound every amount. Batch totals use **saturating** addition. Page numbers and list lengths are bounded | `money_test.go` (0.1 + 0.2, 19.99, the maximum, every cent from 0.00 to 20,000.00 round-trips), `batching/money_test.go` (saturation) |
| **CWE-20** improper input validation (OWASP A03) | JSON is decoded strictly (unknown fields, trailing data and bodies over 1 MiB are `400`). Every ID in a path or body must match its format (`^rider_[0-9A-Za-z]{1,40}$`, `^ord_...`, `^batch_...`, `^ven_...`, `^area_...`, category `^[a-z][a-z_]{1,39}$`); malformed path IDs are `404` without a query. Coordinates, bearings, speeds, amounts, `page`, `page_size`, `radius_km` and `max_orders` are range-checked; list inputs are capped at 50 entries; `Idempotency-Key` and `X-Expected-Version` are validated. There are **no free-text fields**: every string a client sends is an ID, a code or a header value, each length-capped | handler tests for each rule (`ids_test.go`, `*_422` tables) |
| **CWE-209** information exposure through error messages (OWASP A05) | Every unexpected error is logged once, server side, and the client gets a fixed message: *"the server encountered a problem and could not process your request"*. Domain errors map to fixed 404/409/422 messages. A malformed or unknown ID is the same `404` | handler tests assert a 500 never contains the internal error text (hosts, SQL, driver messages) |
| **A06** vulnerable and outdated components | **`govulncheck`** (pinned, `v1.9.0`) runs in `make dev/audit` and in CI, and covers the dependencies we actually call *and* the Go standard library of the toolchain in use. Its first run found real issues in `golang.org/x/text`, `golang.org/x/net` and `github.com/quic-go/quic-go` (all pulled in through Gin); they were upgraded | `make dev/audit`; CI `audit` job (which installs the newest patch of the Go line) |
| **A07** identification and authentication failures | Every route except `GET /healthz` needs an API key. Keys come from configuration, are held only as **SHA-256 hashes**, and are compared with `subtle.ConstantTimeCompare` against all of them, so timing leaks nothing. A missing and a wrong key get the same `401` (with `WWW-Authenticate`); guessing is rate-limited (the limiter runs first) and logged with a fingerprint, never the key | `internal/api/auth_test.go` (rejections, indistinguishable 401s, healthz-only exemption, every route protected, logs never contain a key), e2e `TestE2E_UnauthenticatedRequestsChangeNothing` |
| **CWE-798** hard-coded credentials | No key is in the code: they come from `API_KEYS` (environment or `app.env`, which is git-ignored). The one published key, the demo key in the README, compose file and Postman environment, is **refused when `APP_ENV=production`**, and staging and production demand 32+ characters. `.env.example` holds a development-only placeholder | `internal/config/apikeys_test.go` (each rule, the demo key in production, errors never echo a key) |
| **CWE-400** resource exhaustion (OWASP A04) | 1 MiB body cap; per-statement query timeout (`DB_QUERY_TIMEOUT`); `page_size` at most 50; bounded list inputs and batch sizes; HTTP server read/write timeouts. Per-IP rate limiting (Phase 9), which also limits key guessing | `timeout_test.go`, `TestIntegration_SlowQueryIsCancelled` |

**Production binaries are built in CI and Docker with the latest Go 1.27 patch; govulncheck is enforced there.** (`go.mod` states only the minimum, 1.26.0; a developer machine on an older patch gets a warning from `make dev/vulncheck`, CI does not.)

### Known limitation: a shared API key lets a client act as any rider

Phase 10 authenticates the **client application** with an API key (one named key per client, so two clients can be told apart and
one revoked without the other). It does **not** identify a rider. The `rider_id` in a request body or path is trusted as sent, so a
holder of a key can call `POST /batches/:id/accept` as any rider, read any rider's record, or move any rider's location. That is
CWE-639 (authorization bypass through a user-controlled key), OWASP **A01 Broken Access Control** (an insecure direct object
reference). It is acceptable for a trusted rider app talking to its own backend and is not acceptable for a public API; a key
shipped inside a mobile app can also be extracted from it.

**The fix is per-rider tokens** (PASETO; an Improvement, not built): `POST /auth/token` would issue a short-lived token whose subject
is a `rider_id`, and every handler would take `rider_id` from the token instead of the request (or require the two to be equal).
Let's Go Further ch. 17-style **permission scopes** would limit what a token may do, for example `batches:accept`, `riders:write`,
`orders:create`, and a dispatcher scope for tokens that may act for anyone. Until then, treat each API key as a secret that grants
the full rider-fleet surface, give every client its own, and rotate it if it leaks.

### Other things worth knowing

- Idempotency keys are not secrets and are not scoped to a caller; two clients that choose the same key for the same body
  would share a response. Use random keys (a UUID per logical operation).
- Replayed responses are stored verbatim, including order IDs and earnings. The table inherits the database's access controls.
- `govulncheck` needs network access and the vulnerability database; a machine whose Go toolchain is older than the newest
  patch release of its line will (correctly) fail the audit until Go is updated.

---

## Logging, rate limiting and shutdown

### Middleware order

```
request -> recoverPanic -> requestID -> securityHeaders -> accessLog -> rateLimit -> requireAPIKey -> handler
                                                                                                 (idempotency: POST /orders, /orders/batch only)
```

`recoverPanic` is outermost so a panic anywhere below becomes a logged 500. `requestID` comes next so every later log line
can carry the ID. `securityHeaders` runs before anything can answer, so even a 429 or a 404 has the headers. `accessLog` wraps
the limiter and the handlers, so it records the final status of every request, rate-limited and panicked ones included.
`rateLimit` runs before any work is done for the client. `requireAPIKey` comes right after it (see "Authentication"). The router is built with `gin.New()`, never `gin.Default()`, so there
is exactly one logger and one recovery.

### Structured logging

- **Request ID.** An incoming `X-Request-ID` is kept only if it is 1 to 64 characters from `A-Z a-z 0-9 . _ -`; anything else is
  replaced by a random 32-character hex ID (the bad value is never echoed). The ID is returned in the response header and carried in
  the request context; a context-aware slog handler adds `request_id` to every record logged with that context.
- **Access log**, one `INFO` line per request, message `request`: `method`, `route` (the template such as `/api/v2/riders/:id`,
  or `unmatched`; never the raw path, which carries IDs), `status`, `duration_ms` (from the injected clock), `bytes`, `client_ip`,
  `request_id` and, for an authenticated request, `client` (the API key's name). Example (text handler in development):
  `level=INFO msg=request method=GET route=/api/v2/riders/:id status=200 duration_ms=2 bytes=412 client_ip=127.0.0.1 request_id=...`
- **Errors** are logged once, at the edge (`request failed`, with the error, method, route and request ID).
- **Log injection (CWE-117).** A log *message* is always a fixed string in the code. Everything a client controls (headers, IDs,
  paths, error text) goes into an attribute, and the JSON/text handler escapes quotes, newlines and control characters inside it, so a
  value cannot start a second log line. `TestHostileValuesCannotForgeALogLine` and `TestLogging_UserInputIsNeverInTheMessage` check this.
- **Phone numbers** are masked in logs: `+254712345678` becomes `+2547****5678` (`logging.MaskPhone`), and error text is scrubbed
  of any phone number before it is logged (`logging.ScrubPhones`), because a database error can quote one.
- **Audit-level `INFO` lines** (fixed message, IDs as attributes): `batch accepted` (`batch_id`, `rider_id`, `orders`),
  `batch accept conflict` (the 409), `batch accept rejected, rider at capacity` (the 422).

### Panic recovery

A panic in any handler or middleware is recovered by `recoverPanic`: it sets `Connection: close` (the connection may be in a bad
state), logs `panic recovered` with the panic value, the stack and the request ID, and answers the generic 500 JSON. The client never
sees the panic value. Background goroutines are started with `Server.background`, which recovers and logs panics too.

### Rate limiting

A per-client-IP token bucket (`golang.org/x/time/rate`): each IP's bucket holds `LIMITER_BURST` tokens and refills at `LIMITER_RPS`
per second. A request over the limit gets `429` `{"error":"rate limit exceeded"}` with `Retry-After` (whole seconds, rounded up, at
least 1); a rejected request does not use a token. `/healthz` is exempt. Idle clients (not seen for 3 minutes) are removed by a
background task, using the injected clock; it stops at shutdown.

**Client IP and spoofing.** The limiter uses `c.ClientIP()`. With `TRUSTED_PROXIES` empty (the default) that is always the TCP peer, so a
client that sends `X-Forwarded-For` or `X-Real-IP` cannot choose its own address, and cannot escape or share a bucket. Behind a reverse
proxy, list the proxy's IP or CIDR in `TRUSTED_PROXIES`; then the header is believed *only* when the TCP peer is one of them.
Forgetting to do so makes all traffic look like one client (the proxy), which is limited as one; setting it too wide lets clients lie.

| Variable | Default | Meaning |
|---|---|---|
| `LIMITER_ENABLED` | true | Switch the limiter off entirely |
| `LIMITER_RPS` | 10 | Sustained requests per second per IP |
| `LIMITER_BURST` | 20 | Requests one IP may make at once |
| `TRUSTED_PROXIES` | empty | Comma-separated IPs/CIDRs whose `X-Forwarded-For` is believed; empty trusts none |
| `SHUTDOWN_TIMEOUT` | 30s | How long shutdown waits for in-flight requests |

### Security headers

Every response, including errors, 404/405 and 429, carries `X-Content-Type-Options: nosniff` and `Cache-Control: no-store`, and no
`Server` header.

### Graceful shutdown

On `SIGINT` or `SIGTERM` the server logs `shutting down server` (with the signal), then:
1. stops accepting new connections and waits up to `SHUTDOWN_TIMEOUT` for requests already in flight to finish;
2. stops the background tasks (the limiter cleanup) and waits for them;
3. closes the database pool;
4. logs `stopped server`.
If in-flight requests outlast the timeout the server gives up and exits with an error. Windows has only the interrupt (Ctrl+C), not
`SIGTERM`; Docker and systemd send `SIGTERM` to the process.

### Decisions beyond the brief

- The access line is `INFO` for every status; `request failed` and `panic recovered` are the `ERROR` lines. A 429 or 404 is not an error.
- A request that panics is access-logged as status 500 (the line is written from a deferred call because the recovery sits outside it).
- Rate limiter buckets are in memory, per process: with several API instances each has its own limits (a shared store, such as Redis,
  would be needed to limit across instances).
- Limiter state is keyed by IP only. Clients behind one NAT share a bucket.
- The idle-client timeout (3 minutes) and cleanup interval (1 minute) are constants; the rest is configuration.
- Because requests are limited per IP and before authentication, an unauthenticated flood, and a client guessing keys, is limited too (`requireAPIKey` sits after the limiter).
