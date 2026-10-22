package api

import (
	"expvar"
	"net/http"
	"runtime"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/codercollo/zemi-fleet/internal/metrics"
)

// metricsPath is where the expvar JSON is served. It is deliberately outside /api/v2: it is operations data, not API.
const metricsPath = "/debug/vars"

// poolStatter is implemented by *pgxpool.Pool. The metrics read connection-pool statistics through it, so tests that
// fake the database with a plain Pinger simply report an empty object.
type poolStatter interface {
	Stat() *pgxpool.Stat
}

// requestMetrics is the HTTP side of the published values. They are expvar types but live in a per-server
// expvar.Map instead of expvar's global registry, because publishing the same name twice panics and every test
// builds its own server. Let's Go Further ch. 19.
type requestMetrics struct {
	vars         expvar.Map
	received     expvar.Int
	sent         expvar.Int
	processingUS expvar.Int
	byStatus     expvar.Map
}

// newRequestMetrics builds the published set: process values, pool statistics, HTTP totals and the domain counters.
func (s *Server) newRequestMetrics(domain *metrics.Counters) *requestMetrics {
	m := &requestMetrics{}
	m.vars.Init()
	m.byStatus.Init()

	m.vars.Set("version", expvar.Func(func() any { return s.version }))
	m.vars.Set("goroutines", expvar.Func(func() any { return runtime.NumGoroutine() }))
	m.vars.Set("timestamp", expvar.Func(func() any { return s.clock.Now().Unix() }))
	m.vars.Set("memstats", expvar.Func(func() any {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		return ms
	}))
	m.vars.Set("database", expvar.Func(s.databaseStats))

	m.vars.Set("total_requests_received", &m.received)
	m.vars.Set("total_responses_sent", &m.sent)
	m.vars.Set("total_processing_time_μs", &m.processingUS)
	m.vars.Set("total_responses_sent_by_status", &m.byStatus)

	for name, v := range domain.Vars() {
		m.vars.Set(name, v)
	}
	return m
}

// databaseStats reports the pgxpool counters. acquire_duration_ms is the total time spent waiting to acquire.
func (s *Server) databaseStats() any {
	p, ok := s.db.(poolStatter)
	if !ok {
		return map[string]int64{}
	}
	st := p.Stat()
	return map[string]int64{
		"total_conns":            int64(st.TotalConns()),
		"idle_conns":             int64(st.IdleConns()),
		"acquired_conns":         int64(st.AcquiredConns()),
		"acquire_count":          st.AcquireCount(),
		"empty_acquire_count":    st.EmptyAcquireCount(),
		"canceled_acquire_count": st.CanceledAcquireCount(),
		"acquire_duration_ms":    st.AcquireDuration().Milliseconds(),
	}
}

// metricsMiddleware counts every request that reaches it, and every response with its status code. It sits right
// after recoverPanic, outside the limiter and the authentication, so 401s and 429s are counted too. Gin's own writer
// records the status, so no wrapper is needed.
//
// The counting is deferred so a panicking handler is counted as the 500 that recoverPanic (outside this middleware)
// is about to send, the same way accessLog does it.
func (s *Server) metricsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := s.clock.Now()
		s.metrics.received.Add(1)
		completed := false
		defer func() {
			status := c.Writer.Status()
			if !completed {
				status = http.StatusInternalServerError
			}
			s.metrics.sent.Add(1)
			s.metrics.byStatus.Add(strconv.Itoa(status), 1)
			duration := s.clock.Now().Sub(start)
			s.metrics.processingUS.Add(duration.Microseconds())
			route := c.FullPath()
			if route == "" {
				route = "unmatched"
			}
			s.prom.observeRequest(route, c.Request.Method, status, duration)
		}()
		c.Next()
		completed = true
	}
}

// metricsHandler serves the published values as JSON. expvar.Map renders itself as a JSON object.
func (s *Server) metricsHandler(c *gin.Context) {
	c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.Writer.WriteHeader(http.StatusOK)
	if _, err := c.Writer.WriteString(s.metrics.vars.String() + "\n"); err != nil {
		s.logError(c, err)
	}
}
