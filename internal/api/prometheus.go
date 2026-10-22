package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// prometheusMetrics is deliberately owned by one Server. Registering these
// collectors in prometheus.DefaultRegisterer would panic when tests build a
// second server in the same process.
type prometheusMetrics struct {
	registry *prometheus.Registry

	requestDuration   *prometheus.HistogramVec
	algorithmDuration *prometheus.HistogramVec
	webhookAttempts   *prometheus.CounterVec
	webhookFailed     prometheus.Gauge
	batchSweepLast    prometheus.Gauge
	batchSweepRelease prometheus.Counter
	offlineSweepLast  prometheus.Gauge
	offlineRelease    prometheus.Counter
	breakerState      prometheus.Gauge
}

type metricRow interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func newPrometheusMetrics(s *Server, breakerState func() int) *prometheusMetrics {
	p := &prometheusMetrics{
		registry: prometheus.NewRegistry(),
		requestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "zemi_http_request_duration_seconds", Help: "HTTP request duration by route template.",
			Buckets: prometheus.DefBuckets,
		}, []string{"route", "method", "status"}),
		algorithmDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "zemi_algorithm_duration_seconds", Help: "Algorithm duration by algorithm name.", Buckets: prometheus.DefBuckets,
		}, []string{"algorithm"}),
		webhookAttempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zemi_webhook_attempts_total", Help: "Webhook delivery attempts by committed result.",
		}, []string{"result"}),
		webhookFailed:     prometheus.NewGauge(prometheus.GaugeOpts{Name: "zemi_webhook_failed", Help: "Failed webhook deliveries."}),
		batchSweepLast:    prometheus.NewGauge(prometheus.GaugeOpts{Name: "zemi_batch_expiry_sweep_last_timestamp_seconds", Help: "Last successful batch expiry sweep timestamp."}),
		batchSweepRelease: prometheus.NewCounter(prometheus.CounterOpts{Name: "zemi_batch_expiry_released_total", Help: "Batches released by committed expiry sweeps."}),
		offlineSweepLast:  prometheus.NewGauge(prometheus.GaugeOpts{Name: "zemi_offline_sweep_last_timestamp_seconds", Help: "Last successful offline sweep timestamp."}),
		offlineRelease:    prometheus.NewCounter(prometheus.CounterOpts{Name: "zemi_offline_release_total", Help: "Riders released by committed offline sweeps."}),
		breakerState:      prometheus.NewGauge(prometheus.GaugeOpts{Name: "zemi_routing_breaker_state", Help: "Routing breaker state: 0 closed, 1 open, 2 half-open."}),
	}
	for _, collector := range []prometheus.Collector{p.requestDuration, p.algorithmDuration, p.webhookAttempts, p.webhookFailed, p.batchSweepLast, p.batchSweepRelease, p.offlineSweepLast, p.offlineRelease, p.breakerState} {
		p.registry.MustRegister(collector)
	}
	p.registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "zemi_database_pool_total_connections", Help: "Database pool total connections."}, func() float64 { return poolStat(s, func(v *pgxpoolStat) int64 { return v.total }) }))
	p.registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "zemi_database_pool_idle_connections", Help: "Database pool idle connections."}, func() float64 { return poolStat(s, func(v *pgxpoolStat) int64 { return v.idle }) }))
	p.registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "zemi_database_pool_acquired_connections", Help: "Database pool acquired connections."}, func() float64 { return poolStat(s, func(v *pgxpoolStat) int64 { return v.acquired }) }))
	p.registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "zemi_database_pool_canceled_acquire_count", Help: "Canceled database pool acquisitions."}, func() float64 { return poolStat(s, func(v *pgxpoolStat) int64 { return v.canceled }) }))
	p.registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "zemi_outbox_pending", Help: "Pending webhook deliveries."}, func() float64 { return outboxCount(s) }))
	p.registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "zemi_outbox_oldest_pending_age_seconds", Help: "Age of the oldest pending webhook delivery."}, func() float64 { return outboxAge(s) }))
	p.registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "zemi_webhook_failed_current", Help: "Current failed webhook deliveries."}, func() float64 { return webhookFailed(s) }))
	p.registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "zemi_routing_breaker_state_current", Help: "Current routing breaker state."}, func() float64 {
		if breakerState == nil {
			return 0
		}
		return float64(breakerState())
	}))
	return p
}

// pgxpool.Stat is intentionally copied into this tiny shape so tests need not
// implement a concrete pgx pool just to exercise the metrics endpoint.
type pgxpoolStat struct{ total, idle, acquired, canceled int64 }

func poolStat(s *Server, pick func(*pgxpoolStat) int64) float64 {
	// Keep the concrete assertion in metrics.go, where pgxpool is already part
	// of the API's dependency boundary.
	if st, ok := s.db.(poolStatter); ok {
		v := st.Stat()
		return float64(pick(&pgxpoolStat{total: int64(v.TotalConns()), idle: int64(v.IdleConns()), acquired: int64(v.AcquiredConns()), canceled: v.CanceledAcquireCount()}))
	}
	return 0
}

func outboxCount(s *Server) float64 {
	var n int64
	if db, ok := s.db.(metricRow); ok && db.QueryRow(context.Background(), `SELECT count(*) FROM webhook_deliveries WHERE status = 'pending'`).Scan(&n) == nil {
		return float64(n)
	}
	return 0
}
func outboxAge(s *Server) float64 {
	var age float64
	if db, ok := s.db.(metricRow); ok && db.QueryRow(context.Background(), `SELECT COALESCE(EXTRACT(EPOCH FROM ($1 - min(created_at))), 0) FROM webhook_deliveries WHERE status = 'pending'`, s.clock.Now()).Scan(&age) == nil {
		return age
	}
	return 0
}
func webhookFailed(s *Server) float64 {
	var n int64
	if db, ok := s.db.(metricRow); ok && db.QueryRow(context.Background(), `SELECT count(*) FROM webhook_deliveries WHERE status = 'failed'`).Scan(&n) == nil {
		return float64(n)
	}
	return 0
}

func (p *prometheusMetrics) handler() http.Handler {
	return promhttp.HandlerFor(p.registry, promhttp.HandlerOpts{})
}
func (p *prometheusMetrics) observeRequest(route, method string, status int, d time.Duration) {
	p.requestDuration.WithLabelValues(route, method, strconv.Itoa(status)).Observe(d.Seconds())
}
func (p *prometheusMetrics) observeAlgorithm(name string, d time.Duration) {
	p.algorithmDuration.WithLabelValues(name).Observe(d.Seconds())
}
func (p *prometheusMetrics) setBatchSweep(now time.Time, released int) {
	p.batchSweepLast.Set(float64(now.Unix()))
	if released > 0 {
		p.batchSweepRelease.Add(float64(released))
	}
}
func (p *prometheusMetrics) setOfflineSweep(now time.Time, released int) {
	p.offlineSweepLast.Set(float64(now.Unix()))
	if released > 0 {
		p.offlineRelease.Add(float64(released))
	}
}
func (p *prometheusMetrics) recordWebhook(result string) {
	p.webhookAttempts.WithLabelValues(result).Inc()
}
