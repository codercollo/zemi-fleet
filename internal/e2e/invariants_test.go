//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// assertInvariants checks, with SQL, the rules that must hold in the database whatever the API calls did, then
// checks the captured responses. Every scenario ends with it. Each query selects the VIOLATIONS: an empty result
// means the invariant holds.
func assertInvariants(t *testing.T, pool *pgxpool.Pool, responses []string) {
	t.Helper()

	invariants := []struct{ name, violations string }{
		{
			"every batched order belongs to exactly one open batch, and order.batch_id points at it",
			`SELECT o.id FROM orders o
			  WHERE o.status = 'batched'
			    AND ( (SELECT count(*) FROM batch_stops s JOIN batches b ON b.id = s.batch_id
			            WHERE s.order_id = o.id AND s.type = 'pickup' AND b.status = 'open') <> 1
			       OR NOT EXISTS (SELECT 1 FROM batches b WHERE b.id = o.batch_id AND b.status = 'open'))`,
		},
		{
			"every assigned order belongs to an accepted batch with the same rider",
			`SELECT o.id FROM orders o
			  WHERE o.status = 'assigned'
			    AND NOT EXISTS (SELECT 1 FROM batches b WHERE b.id = o.batch_id
			                      AND b.status IN ('accepted', 'in_progress', 'completed') AND b.rider_id = o.rider_id)`,
		},
		{
			"every rider's current_orders_count equals their active orders and is within max_capacity",
			`SELECT r.id FROM riders r
			  WHERE r.current_orders_count <> (SELECT count(*) FROM orders o WHERE o.rider_id = r.id AND o.status IN ('assigned', 'picked_up', 'returning'))
			     OR r.current_orders_count > r.max_capacity`,
		},
		{
			"delivery attempts and cancellation compensation stay within lifecycle bounds",
			`SELECT id FROM orders
			  WHERE delivery_attempts < 0 OR delivery_attempts > 3
			     OR rider_compensation_cents < 0
			     OR rider_compensation_cents + platform_fee_cents > delivery_fee_cents
			     OR (status IN ('delivered', 'returned', 'cancelled') AND rider_id IS NOT NULL
			         AND EXISTS (SELECT 1 FROM riders r WHERE r.id = orders.rider_id AND r.current_orders_count < 0))`,
		},
		{
			"no order is in two non-expired batches",
			`SELECT s.order_id FROM batch_stops s JOIN batches b ON b.id = s.batch_id
			  WHERE s.type = 'pickup' AND b.status NOT IN ('expired', 'cancelled')
			  GROUP BY s.order_id HAVING count(*) > 1`,
		},
		{
			"every pickup comes before its delivery",
			`SELECT p.batch_id || ':' || p.order_id FROM batch_stops p
			   JOIN batch_stops d ON d.batch_id = p.batch_id AND d.order_id = p.order_id AND d.type = 'delivery'
			  WHERE p.type = 'pickup' AND p.seq >= d.seq`,
		},
		{
			"every order in a batch has exactly one pickup and one delivery stop",
			`SELECT batch_id || ':' || order_id FROM batch_stops GROUP BY batch_id, order_id
			  HAVING count(*) FILTER (WHERE type = 'pickup') <> 1 OR count(*) FILTER (WHERE type = 'delivery') <> 1`,
		},
		{
			"every batch's orders_count matches its stops",
			`SELECT b.id FROM batches b
			  WHERE b.orders_count <> (SELECT count(DISTINCT s.order_id) FROM batch_stops s WHERE s.batch_id = b.id)
			     OR b.orders_count * 2 <> (SELECT count(*) FROM batch_stops s WHERE s.batch_id = b.id)`,
		},
		{
			"rider earnings + platform fee = delivery fee, on every order",
			`SELECT id FROM orders WHERE rider_earnings_cents + platform_fee_cents <> delivery_fee_cents`,
		},
		{
			"every batch's estimated earnings = the sum of its orders' rider earnings",
			`SELECT b.id FROM batches b
			  WHERE b.estimated_earnings_cents <> COALESCE((SELECT sum(o.rider_earnings_cents) FROM orders o
			                                                 WHERE o.id IN (SELECT s.order_id FROM batch_stops s WHERE s.batch_id = b.id)), 0)`,
		},
		{
			"no idempotency key is left claimed but unfinished",
			`SELECT key FROM idempotency_keys WHERE response_status IS NULL`,
		},
		{
			"an open or expired batch has no rider; an accepted one has a rider and a time",
			`SELECT id FROM batches
			  WHERE (status IN ('open', 'expired') AND (rider_id IS NOT NULL OR accepted_at IS NOT NULL))
			     OR (status = 'accepted' AND (rider_id IS NULL OR accepted_at IS NULL))`,
		},
	}

	for _, inv := range invariants {
		rows, err := pool.Query(context.Background(), inv.violations)
		require.NoError(t, err, inv.name)
		var bad []string
		for rows.Next() {
			var id string
			require.NoError(t, rows.Scan(&id))
			bad = append(bad, id)
		}
		require.NoError(t, rows.Err())
		rows.Close()
		assert.Empty(t, bad, "INVARIANT BROKEN: %s\nviolators: %v", inv.name, bad)
	}

	assertTimestampsAreUTC(t, responses)
}

var timestampShape = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}`)

// assertTimestampsAreUTC checks that every string in the captured JSON that looks like a timestamp is RFC 3339
// in UTC, ending in Z.
func assertTimestampsAreUTC(t *testing.T, responses []string) {
	t.Helper()
	checked := 0
	for _, body := range responses {
		// A cheap scan of quoted strings is enough: any quoted value shaped like a timestamp must be UTC.
		for _, quoted := range regexp.MustCompile(`"(\d{4}-\d{2}-\d{2}T[^"]*)"`).FindAllStringSubmatch(body, -1) {
			ts := quoted[1]
			if !timestampShape.MatchString(ts) {
				continue
			}
			checked++
			assert.True(t, strings.HasSuffix(ts, "Z"), "timestamp %q is not UTC (no Z suffix) in %s", ts, truncate(body))
			parsed, err := time.Parse(time.RFC3339Nano, ts)
			if assert.NoError(t, err, "timestamp %q is not RFC 3339", ts) {
				assert.Equal(t, time.UTC, parsed.Location(), ts)
			}
		}
	}
	t.Logf("checked %d timestamps in %d responses", checked, len(responses))
}

func truncate(s string) string {
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

// scalar runs a query that returns one integer, for scenarios that check a single fact in the database.
func scalar(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), sql, args...).Scan(&n), fmt.Sprintf("query: %s", sql))
	return n
}
