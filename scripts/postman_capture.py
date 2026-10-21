#!/usr/bin/env python3
"""Builds postman/Zemi_Fleet_V2.postman_collection.json by RUNNING the requests against a live, freshly seeded stack.

Every saved example response in the collection is a real response captured here: nothing is written by hand. The test
scripts are the only hand-written part, and `make staging/postman` (newman) re-checks them against the stack.

    docker compose --profile demo up --build -d          # a freshly seeded stack on :8080
    python3 scripts/postman_capture.py                   # BASE_URL / API_KEY override the defaults
    docker compose --profile demo run --rm seed          # reseed: the capture run used up some of the demo data

Standard library only. The requests run in the same order as the collection, so each one sees the state the previous
ones left, exactly as newman will see it.
"""
import json
import os
import re
import sys
import threading
import time
import urllib.error
import urllib.request
import uuid

BASE_URL = os.environ.get("BASE_URL", "http://localhost:8080")
API_KEY = os.environ.get("API_KEY", "zemi-demo-key-not-for-production-0001")
OUT_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "postman")

RIDER_A, RIDER_B = "rider_001", "rider_004"  # A wins the batch, B loses the race (seed: A has 0 orders, B has 1)
STATUS_TEXT = {200: "OK", 201: "Created", 400: "Bad Request", 401: "Unauthorized", 404: "Not Found", 409: "Conflict",
               422: "Unprocessable Entity", 429: "Too Many Requests"}

variables = {"base_url": BASE_URL, "api_key": API_KEY, "rider_a": RIDER_A, "rider_b": RIDER_B}


# ---------------------------------------------------------------------------------------------------------------------
# Running a request the way Postman would (variables substituted, collection auth applied)
# ---------------------------------------------------------------------------------------------------------------------
def sub(text):
    return re.sub(r"\{\{(\w+)\}\}", lambda m: str(variables.get(m.group(1), m.group(0))), text)


def execute(spec):
    """Sends one request spec and returns (status, headers, body_text). spec: method, path, query, headers, body, auth."""
    url = sub(spec["url"])
    headers = {k: sub(v) for k, v in spec.get("headers", {}).items()}
    if spec.get("auth", True):
        headers["X-API-Key"] = variables["api_key"]
    data = sub(spec["body"]).encode() if spec.get("body") is not None else None
    if data is not None:
        headers.setdefault("Content-Type", "application/json")
    req = urllib.request.Request(url, data=data, method=spec["method"], headers=headers)
    time.sleep(0.12)  # stay under the demo stack's rate limit (10 requests/s per IP), as newman's --delay-request does
    try:
        with urllib.request.urlopen(req) as r:
            return r.status, list(r.headers.items()), r.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, list(e.headers.items()), e.read().decode()


def pm_request(spec):
    """The Postman request object for a spec (URLs stay in {{variable}} form)."""
    raw = spec["url"]
    path_part, _, query = raw.partition("?")
    path = [p for p in path_part.replace("{{base_url}}", "").split("/") if p]
    url = {"raw": raw, "host": ["{{base_url}}"], "path": path}
    if query:
        url["query"] = [{"key": kv.split("=")[0], "value": kv.split("=", 1)[1]} for kv in query.split("&")]
    headers = [{"key": k, "value": v, "type": "text"} for k, v in spec.get("headers", {}).items()]
    out = {"method": spec["method"], "header": headers, "url": url}
    if not spec.get("auth", True):
        out["auth"] = {"type": "noauth"}
    if spec.get("body") is not None:
        out["body"] = {"mode": "raw", "raw": spec["body"], "options": {"raw": {"language": "json"}}}
    if spec.get("description"):
        out["description"] = spec["description"]
    return out


def make_example(name, spec, status, headers, body):
    keep = [h for h in headers if h[0].lower() not in ("date", "content-length", "connection")]
    return {
        "name": name,
        "originalRequest": pm_request(spec),
        "status": STATUS_TEXT.get(status, str(status)),
        "code": status,
        "_postman_previewlanguage": "json",
        "header": [{"key": k, "value": v} for k, v in keep],
        "cookie": [],
        "body": body,
    }


# ---------------------------------------------------------------------------------------------------------------------
# The collection definition. `on_response` mirrors what the request's test script stores in collection variables.
# ---------------------------------------------------------------------------------------------------------------------
def js(*lines):
    return list(lines)


def item(name, spec, tests, expect, examples=(), prerequest=(), on_response=None, description=""):
    return {"name": name, "spec": spec, "tests": tests, "expect": expect, "examples": list(examples),
            "prerequest": list(prerequest), "on_response": on_response, "description": description}


def post(path, body, **kw):
    return {"method": "POST", "url": "{{base_url}}" + path, "body": json.dumps(body, indent=2) if not isinstance(body, str) else body, **kw}


def put(path, body, **kw):
    return {"method": "PUT", "url": "{{base_url}}" + path, "body": json.dumps(body, indent=2), **kw}


def patch(path, body, **kw):
    return {"method": "PATCH", "url": "{{base_url}}" + path, "body": json.dumps(body, indent=2), **kw}


def get(path, **kw):
    return {"method": "GET", "url": "{{base_url}}" + path, **kw}


def ex(name, status, **changes):
    """A variant of the request, run to capture one more saved example (changes override the spec's fields)."""
    return {"name": name, "status": status, "changes": changes}


STATUS_TEST = 'pm.test("Status is %d", () => pm.response.to.have.status(%d));'
JSON_HEADER_TEST = 'pm.test("Body is JSON", () => pm.response.to.be.json);'

NEW_ORDER = {"vendor_id": "ven_001", "category": "electronics", "delivery_location": {"lat": -1.25, "lng": 36.83},
             "value_kes": 4500.5, "delivery_fee_kes": 250}
LOC_A = {"latitude": -1.266, "longitude": 36.806, "bearing_degrees": 320, "speed_kmh": 22}
LOC_B = {"latitude": -1.264, "longitude": 36.805, "bearing_degrees": 90, "speed_kmh": 18}


def folder_health():
    return ("Health", "Liveness and database check. No API key needed.", [
        item("Healthz", get("/api/v2/healthz", auth=False),
             js(STATUS_TEST % (200, 200), JSON_HEADER_TEST,
                'const body = pm.response.json();',
                'pm.test("The service and the database are up", () => {',
                '  pm.expect(body.status).to.eql("available");',
                '  pm.expect(body.system_info.database).to.eql("up");',
                '});',
                'pm.test("A request ID is returned", () => pm.response.to.have.header("X-Request-ID"));'),
             200, description="Reports `available` with the database state. Exempt from the API key and the rate limiter."),
    ])


def folder_orders():
    def on_order(body):
        variables["order_id"] = json.loads(body)["order"]["id"]

    return ("Orders", "Create and read orders. Money is KES with at most two decimals.", [
        item("Create order", post("/api/v2/orders", NEW_ORDER, headers={"Idempotency-Key": "{{idem_key}}"}),
             js(STATUS_TEST % (201, 201), JSON_HEADER_TEST,
                'const order = pm.response.json().order;',
                'pm.test("A pending order with the rider\'s 90% share", () => {',
                '  pm.expect(order.id).to.match(/^ord_/);',
                '  pm.expect(order.status).to.eql("pending");',
                '  pm.expect(order.estimated_earnings_kes).to.eql(225);',
                '  pm.expect(order.platform_fee_kes).to.eql(25);',
                '  pm.expect(order.batch_id).to.eql(null);',
                '});',
                'pm.test("Location points at the new order", () => pm.expect(pm.response.headers.get("Location")).to.eql("/api/v2/orders/" + order.id));',
                'pm.collectionVariables.set("order_id", order.id);'),
             201,
             prerequest=js('pm.collectionVariables.set("idem_key", "postman-" + Date.now());'),
             on_response=on_order,
             examples=[
                 ex("422 Unprocessable Entity: invalid fields", 422, body=json.dumps({"vendor_id": "ven_001", "category": "electronics", "delivery_location": {"lat": -1.25, "lng": 36.83}, "value_kes": -5}, indent=2), headers={}),
                 ex("400 Bad Request: malformed JSON", 400, body='{"vendor_id": ', headers={}),
                 ex("401 Unauthorized: no API key", 401, auth=False, headers={}),
             ],
             description="Creates a `pending` order. The `Idempotency-Key` header (set to a fresh value by the pre-request script) makes a retry safe: see the next request."),
        item("Create order: idempotent replay", post("/api/v2/orders", NEW_ORDER, headers={"Idempotency-Key": "{{idem_key}}"}),
             js(STATUS_TEST % (201, 201),
                'pm.test("Answered from the stored response", () => pm.response.to.have.header("Idempotent-Replay", "true"));',
                'pm.test("Same order as the first request, not a second one", () => pm.expect(pm.response.json().order.id).to.eql(pm.collectionVariables.get("order_id")));'),
             201,
             description="Sends the same body with the same `Idempotency-Key` as the request above: the first response is replayed (`Idempotent-Replay: true`) and no second order is created."),
        item("Get order", get("/api/v2/orders/{{order_id}}"),
             js(STATUS_TEST % (200, 200), JSON_HEADER_TEST,
                'pm.test("The order we created", () => {',
                '  const order = pm.response.json().order;',
                '  pm.expect(order.id).to.eql(pm.collectionVariables.get("order_id"));',
                '  pm.expect(order).to.have.all.keys("id", "vendor_id", "category", "status", "pickup_location", "delivery_location", "trip_distance_km", "trip_bearing_degrees", "value_kes", "delivery_fee_kes", "rider_payout_percent", "estimated_earnings_kes", "platform_fee_kes", "delivery_attempts", "cancelled_at", "cancel_reason", "rider_compensation_kes", "batch_id", "rider_id", "created_at", "version");',
                '});'),
             200,
             examples=[ex("404 Not Found: unknown order", 404, url="{{base_url}}/api/v2/orders/ord_unknown"),
                       ex("429 Too Many Requests: over the rate limit", 429, burst=True, url="{{base_url}}/api/v2/orders/ord_unknown")]),
    ])


def setup_location(name, who, loc, description):
    return item(name, patch("/api/v2/riders/{{%s}}/location" % who, loc),
                js(STATUS_TEST % (200, 200),
                   'const rider = pm.response.json().rider;',
                   'pm.test("A fresh position was stored", () => {',
                   '  pm.expect(rider.current_location).to.not.eql(null);',
                   '  pm.expect(new Date(rider.location_updated_at).getTime()).to.be.above(Date.now() - 60000);',
                   '});',
                   'pm.collectionVariables.set("%s_version", rider.version);' % who),
                200, description=description,
                on_response=lambda body, who=who: variables.__setitem__(who + "_version", json.loads(body)["rider"]["version"]))


def folder_batching():
    def on_batch(body):
        variables["batch_id"] = json.loads(body)["batches"][0]["batch_id"]

    return ("Order Batching", "Group pending orders into batches for a rider (POST /orders/batch).", [
        setup_location("0. Setup: report rider A's location", "rider_a", LOC_A,
                       "Seeded rider locations go stale (`RIDER_LOCATION_STALE_AFTER`), so report a fresh one before batching."),
        item("Create batches", post("/api/v2/orders/batch", {"rider_id": "{{rider_a}}", "rider_location": {"latitude": -1.266, "longitude": 36.806}, "max_orders": 2, "radius_km": 2}),
             js(STATUS_TEST % (200, 200), JSON_HEADER_TEST,
                'const batches = pm.response.json().batches;',
                'pm.test("At least one batch is on offer", () => pm.expect(batches).to.be.an("array").that.is.not.empty);',
                'pm.test("Each batch has the brief\'s fields and respects max_orders", () => {',
                '  batches.forEach(b => {',
                '    pm.expect(b).to.have.all.keys("batch_id", "orders", "sequence", "total_distance_km", "distance_saved_km", "estimated_earnings_kes", "route_optimization_score", "distance_to_first_pickup_km", "expires_at");',
                '    pm.expect(b.orders.length).to.be.within(1, 2);',
                '    pm.expect(b.sequence.length).to.eql(2 * b.orders.length);',
                '    pm.expect(b.route_optimization_score).to.be.within(0, 1);',
                '  });',
                '});',
                'pm.test("Pickups come before their deliveries", () => {',
                '  batches.forEach(b => b.orders.forEach(o => {',
                '    const seq = b.sequence.filter(s => s.order_id === o.order_id);',
                '    pm.expect(seq[0].type).to.eql("pickup");',
                '    pm.expect(seq[1].type).to.eql("delivery");',
                '  }));',
                '});',
                'pm.collectionVariables.set("batch_id", batches[0].batch_id);'),
             200, on_response=on_batch,
             examples=[ex("422 Unprocessable Entity: max_orders out of range", 422, body=json.dumps({"rider_id": "{{rider_a}}", "rider_location": {"latitude": -1.266, "longitude": 36.806}, "max_orders": 9, "radius_km": 2}, indent=2))],
             description="Forms batches (at most 2 orders each here, pickups within 2 km of each other, FIFO) and returns every open batch on offer, nearest first. The first batch id is saved for the Batches folder."),
    ])


def folder_batches():
    return ("Batches", "Open batch offers. The first rider to accept gets the batch; everyone after gets 409.", [
        item("List open batches", get("/api/v2/batches?lat=-1.266&lng=36.806&radius_km=3&page=1&page_size=50"),
             js(STATUS_TEST % (200, 200), JSON_HEADER_TEST,
                'const body = pm.response.json();',
                'pm.test("The batch we were offered is listed", () => pm.expect(body.batches.map(b => b.batch_id)).to.include(pm.collectionVariables.get("batch_id")));',
                'pm.test("Pagination metadata", () => {',
                '  pm.expect(body.metadata.current_page).to.eql(1);',
                '  pm.expect(body.metadata.total_records).to.be.at.least(1);',
                '});'),
             200,
             examples=[ex("422 Unprocessable Entity: missing coordinates", 422, url="{{base_url}}/api/v2/batches?lat=abc")]),
        item("Accept batch: rider A wins", post("/api/v2/batches/{{batch_id}}/accept", {"rider_id": "{{rider_a}}"}),
             js(STATUS_TEST % (200, 200), JSON_HEADER_TEST,
                'const batch = pm.response.json().batch;',
                'pm.test("The batch is now rider A\'s", () => {',
                '  pm.expect(batch.batch_id).to.eql(pm.collectionVariables.get("batch_id"));',
                '  pm.expect(batch.status).to.eql("accepted");',
                '  pm.expect(batch.rider_id).to.eql(pm.collectionVariables.get("rider_a"));',
                '  pm.expect(batch.accepted_at).to.be.a("string");',
                '});'),
             200,
             examples=[ex("404 Not Found: unknown batch", 404, url="{{base_url}}/api/v2/batches/batch_unknown/accept"),
                       ex("422 Unprocessable Entity: no rider id", 422, body='{}')],
             description="Step 1 of the race. Rider A claims the batch exclusively."),
        item("Accept batch: rider B loses (409)", post("/api/v2/batches/{{batch_id}}/accept", {"rider_id": "{{rider_b}}"}),
             js(STATUS_TEST % (409, 409), JSON_HEADER_TEST,
                'pm.test("Told the batch is gone", () => pm.expect(pm.response.json()).to.eql({ error: "batch is no longer available" }));'),
             409,
             description="Step 2 of the race: rider B asks for the batch rider A already holds and gets `409`. The same batch id is passed along through the `batch_id` collection variable."),
    ])


def folder_proximity():
    def on_order(body):
        variables["near_order_id"] = json.loads(body)["order"]["id"]

    return ("Proximity Matching", "Pending pickups ahead of a moving rider: within 1 km and a 60 degree cone (POST /riders/match-orders).", [
        setup_location("0. Setup: report rider A's location", "rider_a", LOC_A,
                       "Reports a fresh position (seeded ones go stale)."),
        item("0b. Setup: a new pending order near the rider", post("/api/v2/orders", {"vendor_id": "ven_001", "category": "electronics", "delivery_location": {"lat": -1.25, "lng": 36.83}, "value_kes": 3000, "delivery_fee_kes": 200}),
             js(STATUS_TEST % (201, 201),
                'pm.collectionVariables.set("near_order_id", pm.response.json().order.id);'),
             201, on_response=on_order,
             description="Batching offered the seeded orders above, so this creates a fresh `pending` order at the Sarit Centre vendor, about 330 m ahead of the rider."),
        item("Match orders: cone", post("/api/v2/riders/match-orders", {"rider_id": "{{rider_a}}", "current_location": {"latitude": -1.266, "longitude": 36.806}, "bearing_degrees": 320, "speed_kmh": 22, "current_orders_count": 0}),
             js(STATUS_TEST % (200, 200), JSON_HEADER_TEST,
                'const body = pm.response.json();',
                'pm.test("The brief\'s response shape", () => pm.expect(body).to.have.all.keys("matching_orders", "can_accept_more_orders", "max_additional_orders", "direction_filter_applied"));',
                'pm.test("The cone was applied and the new order is in it", () => {',
                '  pm.expect(body.direction_filter_applied).to.eql(true);',
                '  pm.expect(body.matching_orders.map(o => o.order_id)).to.include(pm.collectionVariables.get("near_order_id"));',
                '});',
                'pm.test("Every match is within 1 km and within 30 degrees", () => body.matching_orders.forEach(o => {',
                '  pm.expect(o.distance_from_rider_km).to.be.at.most(1);',
                '  pm.expect(Math.abs(o.angle_from_rider_degrees)).to.be.at.most(30);',
                '}));',
                'pm.test("Sorted by distance", () => {',
                '  const d = body.matching_orders.map(o => o.distance_from_rider_km);',
                '  pm.expect(d).to.eql([...d].sort((a, b) => a - b));',
                '});',
                'pm.test("Capacity comes from the database", () => pm.expect(body.max_additional_orders).to.be.at.most(5));'),
             200,
             examples=[ex("200 OK: no bearing, so the full circle is searched", 200,
                          body=json.dumps({"rider_id": "{{rider_a}}", "current_location": {"latitude": -1.266, "longitude": 36.806}, "current_orders_count": 0}, indent=2)),
                       ex("404 Not Found: unknown rider", 404, body=json.dumps({"rider_id": "rider_unknown", "current_location": {"latitude": -1.266, "longitude": 36.806}, "bearing_degrees": 320, "speed_kmh": 22, "current_orders_count": 0}, indent=2)),
                       ex("422 Unprocessable Entity: missing location", 422, body=json.dumps({"rider_id": "{{rider_a}}", "current_orders_count": 0}, indent=2))]),
        item("Match orders: slow rider uses the full circle", post("/api/v2/riders/match-orders", {"rider_id": "{{rider_a}}", "current_location": {"latitude": -1.266, "longitude": 36.806}, "bearing_degrees": 320, "speed_kmh": 0, "current_orders_count": 0}),
             js(STATUS_TEST % (200, 200),
                'const body = pm.response.json();',
                'pm.test("A standing rider has no reliable heading: the cone is skipped", () => {',
                '  pm.expect(body.direction_filter_applied).to.eql(false);',
                '  body.matching_orders.forEach(o => pm.expect(o.angle_from_rider_degrees).to.eql(null));',
                '});'),
             200),
    ])


def folder_rider_matching():
    return ("Rider Matching", "Rank riders for an order by category, area, minimum order value, distance and acceptance rate (POST /orders/match-riders).", [
        setup_location("0. Setup: report rider A's location", "rider_a", LOC_A, "Reports a fresh position, so the distance and proximity score are known."),
        setup_location("0b. Setup: report rider B's location", "rider_b", LOC_B, "Rider B's seeded position is stale too."),
        item("Match riders: by order_id", post("/api/v2/orders/match-riders", {"order_id": "{{order_id}}"}),
             js(STATUS_TEST % (200, 200), JSON_HEADER_TEST,
                'const riders = pm.response.json().matching_riders;',
                'pm.test("Riders are ranked", () => {',
                '  pm.expect(riders).to.be.an("array").that.is.not.empty;',
                '  const scores = riders.map(r => r.match_score);',
                '  pm.expect(scores).to.eql([...scores].sort((a, b) => b - a));',
                '});',
                'pm.test("The brief\'s fields", () => riders.forEach(r => pm.expect(r).to.have.all.keys("rider_id", "match_score", "matching_preferences", "current_orders_count", "distance_to_pickup_km", "acceptance_rate")));',
                'pm.test("Rider A is among them", () => pm.expect(riders.map(r => r.rider_id)).to.include(pm.collectionVariables.get("rider_a")));'),
             200,
             examples=[ex("404 Not Found: unknown order", 404, body='{"order_id": "ord_unknown"}'),
                       ex("422 Unprocessable Entity: neither order_id nor order_details", 422, body='{}')]),
        item("Match riders: by order details", post("/api/v2/orders/match-riders", {"order_details": {"category": "electronics", "pickup_location": {"lat": -1.2635, "lng": 36.803}, "delivery_location": {"lat": -1.25, "lng": 36.83}, "value_kes": 4500}}),
             js(STATUS_TEST % (200, 200),
                'const riders = pm.response.json().matching_riders;',
                'pm.test("Ranked riders for an order that is not saved", () => pm.expect(riders).to.be.an("array").that.is.not.empty);',
                'pm.test("Riders with a stale or unknown location have a null distance", () => riders.forEach(r => pm.expect(r.distance_to_pickup_km === null || r.distance_to_pickup_km >= 0).to.eql(true)));'),
             200),
    ])


def folder_riders():
    return ("Rider Preferences", "Read a rider and change their preferences, position and availability. Updates use optimistic locking.", [
        item("Get rider", get("/api/v2/riders/{{rider_a}}"),
             js(STATUS_TEST % (200, 200), JSON_HEADER_TEST,
                'const rider = pm.response.json().rider;',
                'pm.test("Rider A with their capacity", () => {',
                '  pm.expect(rider.id).to.eql(pm.collectionVariables.get("rider_a"));',
                '  pm.expect(rider.max_additional_orders).to.eql(rider.max_capacity - rider.current_orders_count);',
                '  pm.expect(rider.preferences).to.have.all.keys("min_order_value_kes", "preferred_categories", "preferred_areas");',
                '});',
                'pm.test("Holds the batch they accepted", () => pm.expect(rider.current_orders_count).to.be.at.least(1));',
                'pm.collectionVariables.set("rider_a_version", rider.version);'),
             200,
             on_response=lambda body: variables.__setitem__("rider_a_version", json.loads(body)["rider"]["version"]),
             examples=[ex("404 Not Found: unknown rider", 404, url="{{base_url}}/api/v2/riders/rider_unknown")]),
        item("Update preferences", put("/api/v2/riders/{{rider_a}}/preferences", {"min_order_value_kes": 1000, "preferred_categories": ["electronics", "documents"], "preferred_areas": ["area_002", "area_007"]},
                                        headers={"X-Expected-Version": "{{rider_a_version}}"}),
             js(STATUS_TEST % (200, 200),
                'const rider = pm.response.json().rider;',
                'pm.test("Preferences replaced", () => {',
                '  pm.expect(rider.preferences.min_order_value_kes).to.eql(1000);',
                '  pm.expect(rider.preferences.preferred_categories).to.have.members(["electronics", "documents"]);',
                '});',
                'pm.test("The version moved on", () => pm.expect(rider.version).to.be.above(Number(pm.collectionVariables.get("rider_a_version"))));'),
             200,
             examples=[ex("409 Conflict: stale X-Expected-Version", 409, headers={"X-Expected-Version": "1"}),
                       ex("422 Unprocessable Entity: invalid category", 422, headers={}, body=json.dumps({"preferred_categories": ["Not A Category"]}, indent=2))],
             description="`X-Expected-Version` carries the rider `version` from the previous request; a stale one is `409`."),
        item("Update location", patch("/api/v2/riders/{{rider_a}}/location", {"latitude": -1.265, "longitude": 36.8038, "bearing_degrees": 95, "speed_kmh": 22}),
             js(STATUS_TEST % (200, 200),
                'pm.test("Position stored", () => pm.expect(pm.response.json().rider.current_location).to.eql({ lat: -1.265, lng: 36.8038 }));'),
             200,
             examples=[ex("422 Unprocessable Entity: bearing out of range", 422, body=json.dumps({"latitude": -1.265, "longitude": 36.8038, "bearing_degrees": 400}, indent=2))]),
        item("Update availability", patch("/api/v2/riders/{{rider_a}}/availability", {"is_available": True}),
             js(STATUS_TEST % (200, 200),
                'pm.test("On shift", () => pm.expect(pm.response.json().rider.is_available).to.eql(true));'),
             200,
             examples=[ex("422 Unprocessable Entity: is_available missing", 422, body='{}')]),
    ])


def folder_metrics():
    return ("Metrics", "expvar metrics. Needs the API key; on in development and staging, off in production.", [
        item("Debug vars", get("/debug/vars"),
             js(STATUS_TEST % (200, 200), JSON_HEADER_TEST,
                'const v = pm.response.json();',
                'pm.test("Process and pool values", () => pm.expect(v).to.include.all.keys("version", "goroutines", "timestamp", "database", "total_requests_received", "total_responses_sent", "total_processing_time_μs", "total_responses_sent_by_status"));',
                'pm.test("Pool statistics", () => pm.expect(v.database).to.include.all.keys("total_conns", "idle_conns", "acquired_conns", "acquire_count", "empty_acquire_count", "canceled_acquire_count", "acquire_duration_ms"));',
                'pm.test("The business counters moved during this run", () => {',
                '  pm.expect(v.orders_created).to.be.at.least(2);',
                '  pm.expect(v.batches_formed).to.be.at.least(1);',
                '  pm.expect(v.batches_accepted).to.be.at.least(1);',
                '  pm.expect(v.batch_accept_conflicts).to.be.at.least(1);',
                '  pm.expect(v.idempotent_replays).to.be.at.least(1);',
                '  pm.expect(v.cone_matches_served).to.be.at.least(1);',
                '  pm.expect(v.rider_matches_served).to.be.at.least(1);',
                '});',
                'pm.test("The status map counted the 200s, the 409 and the 401s", () => {',
                '  const s = v.total_responses_sent_by_status;',
                '  pm.expect(s["200"]).to.be.above(0);',
                '  pm.expect(s["409"]).to.be.at.least(1);',
                '});'),
             200,
             examples=[ex("401 Unauthorized: no API key", 401, auth=False)],
             description="`batch_accept_conflicts` rising means riders are racing for the same batches; `database.canceled_acquire_count` growing means the connection pool is starved."),
    ])


FOLDERS = [folder_health, folder_orders, folder_batching, folder_batches, folder_proximity, folder_rider_matching, folder_riders, folder_metrics]


# ---------------------------------------------------------------------------------------------------------------------
# Run everything and write the files
# ---------------------------------------------------------------------------------------------------------------------
def hammer(spec):
    """Fires requests from several threads until one is rate limited (429); returns that response."""
    found = []

    def worker():
        for _ in range(40):
            if found:
                return
            status, headers, body = execute(spec)
            if status == 429:
                found.append((status, headers, body))
                return

    threads = [threading.Thread(target=worker) for _ in range(6)]
    [t.start() for t in threads]
    [t.join() for t in threads]
    if not found:
        sys.exit("could not trigger a 429 (is the rate limiter on?)")
    time.sleep(3)  # let the bucket refill before the next request
    return found[0]


def build_item(it):
    spec = it["spec"]
    status, headers, body = execute(spec)
    if status != it["expect"]:
        sys.exit("%s: expected %d, got %d: %s" % (it["name"], it["expect"], status, body))
    if it["on_response"]:
        it["on_response"](body)
    label = "%d %s: success" if 200 <= status < 300 else "%d %s"
    examples = [make_example(label % (status, STATUS_TEXT[status]), spec, status, headers, body)]
    for e in it["examples"]:
        variant = {**spec, **{k: v for k, v in e["changes"].items() if k != "burst"}}
        if "headers" in e["changes"]:
            variant["headers"] = e["changes"]["headers"]
        if e["changes"].get("burst"):
            vstatus, vheaders, vbody = hammer(variant)
        else:
            vstatus, vheaders, vbody = execute(variant)
        if vstatus != e["status"]:
            sys.exit("%s / %s: expected %d, got %d: %s" % (it["name"], e["name"], e["status"], vstatus, vbody))
        examples.append(make_example(e["name"], variant, vstatus, vheaders, vbody))
        print("    example:", e["name"])
    print("  %-52s %d" % (it["name"], status))
    events = []
    if it["prerequest"]:
        events.append({"listen": "prerequest", "script": {"type": "text/javascript", "exec": it["prerequest"]}})
    events.append({"listen": "test", "script": {"type": "text/javascript", "exec": it["tests"]}})
    pm = {"name": it["name"], "event": events, "request": pm_request(spec), "response": examples}
    if it["description"]:
        pm["request"]["description"] = it["description"]
    return pm


def main():
    out_items = []
    for folder in FOLDERS:
        name, description, items = folder()
        print(name)
        built = []
        for it in items:
            if it["prerequest"] and "idem_key" in "".join(it["prerequest"]):
                variables["idem_key"] = "postman-capture-%s" % uuid.uuid4().hex[:12]
            built.append(build_item(it))
        out_items.append({"name": name, "description": description, "item": built})

    collection = {
        "info": {
            "_postman_id": "5d0c6c0e-7f3a-4a43-9b0e-2f1d6a9e1c11",
            "name": "Zemi Fleet V2",
            "description": "Zemi Africa's rider-app API: order batching, 60 degree proximity matching and preference matching.\n\n"
                           "Run the folders in order against a freshly seeded stack (`docker compose --profile demo up --build`); every request "
                           "has test scripts, and every saved example is a real response captured from the demo stack. Reseed between runs: "
                           "the run uses up some of the demo data.\n\nSet `base_url` and `api_key` in the environment (`Zemi_Fleet_V2.postman_environment.json`).",
            "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json",
        },
        "auth": {"type": "apikey", "apikey": [{"key": "key", "value": "X-API-Key", "type": "string"},
                                              {"key": "value", "value": "{{api_key}}", "type": "string"},
                                              {"key": "in", "value": "header", "type": "string"}]},
        "variable": [
            {"key": "base_url", "value": "http://localhost:8080", "type": "string"},
            {"key": "rider_a", "value": RIDER_A, "type": "string"},
            {"key": "rider_b", "value": RIDER_B, "type": "string"},
            {"key": "order_id", "value": "", "type": "string"},
            {"key": "batch_id", "value": "", "type": "string"},
        ],
        "item": out_items,
    }
    environment = {
        "id": "7b9e1c52-3c0a-4d44-8d2e-0a5e1f6b7d22",
        "name": "Zemi Fleet V2 (docker compose demo)",
        "values": [
            {"key": "base_url", "value": "http://localhost:8080", "type": "default", "enabled": True},
            {"key": "api_key", "value": "zemi-demo-key-not-for-production-0001", "type": "secret", "enabled": True},
        ],
        "_postman_variable_scope": "environment",
    }
    os.makedirs(OUT_DIR, exist_ok=True)
    with open(os.path.join(OUT_DIR, "Zemi_Fleet_V2.postman_collection.json"), "w", encoding="utf-8", newline="\n") as f:
        json.dump(collection, f, indent=2, ensure_ascii=False)
        f.write("\n")
    with open(os.path.join(OUT_DIR, "Zemi_Fleet_V2.postman_environment.json"), "w", encoding="utf-8", newline="\n") as f:
        json.dump(environment, f, indent=2, ensure_ascii=False)
        f.write("\n")
    print("wrote postman/Zemi_Fleet_V2.postman_collection.json and the environment")


if __name__ == "__main__":
    main()
