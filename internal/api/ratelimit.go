package api

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"

	"github.com/codercollo/zemi-fleet/internal/clock"
)

const (
	// idleClientTimeout is how long a client IP may go unseen before its bucket is forgotten.
	idleClientTimeout = 3 * time.Minute
	// cleanupInterval is how often the background cleanup looks for idle clients.
	cleanupInterval = time.Minute
	// retryAfterHeader tells a limited client when to try again.
	retryAfterHeader = "Retry-After"
)

// limiter is a per-client-IP token bucket (golang.org/x/time/rate), as in Let's Go Further ch. 11. Every IP gets
// its own bucket: it holds up to burst tokens and refills at rps tokens per second. Time comes from the injected
// clock, so tests are deterministic and never sleep.
type limiter struct {
	rps   rate.Limit
	burst int
	clock clock.Clock

	mu      sync.Mutex
	clients map[string]*client
}

type client struct {
	bucket   *rate.Limiter
	lastSeen time.Time
}

func newLimiter(rps float64, burst int, clk clock.Clock) *limiter {
	return &limiter{rps: rate.Limit(rps), burst: burst, clock: clk, clients: make(map[string]*client)}
}

// allow spends one token from the IP's bucket. When there is none it returns false and how long the client should
// wait (at least one second, rounded up, for the Retry-After header).
func (l *limiter) allow(ip string) (ok bool, retryAfter time.Duration) {
	now := l.clock.Now()

	l.mu.Lock()
	defer l.mu.Unlock()
	cl, found := l.clients[ip]
	if !found {
		cl = &client{bucket: rate.NewLimiter(l.rps, l.burst)}
		l.clients[ip] = cl
	}
	cl.lastSeen = now

	res := cl.bucket.ReserveN(now, 1)
	if delay := res.DelayFrom(now); !res.OK() || delay > 0 {
		res.CancelAt(now) // a rejected request must not consume a token
		wait := time.Second
		if res.OK() {
			wait = max(time.Second, time.Duration(math.Ceil(delay.Seconds()))*time.Second)
		}
		return false, wait
	}
	return true, 0
}

// cleanup forgets clients not seen for idleClientTimeout, so the map cannot grow forever.
func (l *limiter) cleanup() int {
	cutoff := l.clock.Now().Add(-idleClientTimeout)
	l.mu.Lock()
	defer l.mu.Unlock()
	removed := 0
	for ip, cl := range l.clients {
		if cl.lastSeen.Before(cutoff) {
			delete(l.clients, ip)
			removed++
		}
	}
	return removed
}

// size is the number of clients currently tracked (for tests and diagnostics).
func (l *limiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.clients)
}

// runCleanup calls cleanup every cleanupInterval until ctx is cancelled (at shutdown).
func (l *limiter) runCleanup(ctx context.Context) {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.cleanup()
		}
	}
}

// rateLimit rejects a client that has used up its tokens with 429 and a Retry-After header. The client is
// identified by c.ClientIP(), which is the TCP peer unless the peer is a configured trusted proxy, so a client
// cannot choose its own bucket with an X-Forwarded-For header. /healthz is exempt: load balancers poll it.
func (s *Server) rateLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s.limiter == nil || c.FullPath() == apiPrefix+"/healthz" {
			c.Next()
			return
		}
		ok, retryAfter := s.limiter.allow(c.ClientIP())
		if !ok {
			c.Writer.Header().Set(retryAfterHeader, strconv.Itoa(int(retryAfter/time.Second)))
			c.Abort()
			s.errorResponse(c, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		c.Next()
	}
}
