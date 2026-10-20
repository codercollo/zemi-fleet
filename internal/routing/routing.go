// Package routing provides road-aware distance and route providers with a
// deterministic haversine fallback.
package routing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/codercollo/zemi-fleet/internal/clock"
	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/geo"
)

type Router interface {
	Matrix(context.Context, []domain.Location) ([][]float64, error)
	Route(context.Context, []domain.Location) (Result, error)
}

type Result struct {
	DistanceM float64
	DurationS float64
}

type Haversine struct{}

func (Haversine) Matrix(_ context.Context, points []domain.Location) ([][]float64, error) {
	m := make([][]float64, len(points))
	for i := range points {
		m[i] = make([]float64, len(points))
		for j := range points {
			m[i][j] = geo.Haversine(points[i], points[j])
		}
	}
	return m, nil
}

func (h Haversine) Route(ctx context.Context, points []domain.Location) (Result, error) {
	m, err := h.Matrix(ctx, points)
	if err != nil {
		return Result{}, err
	}
	var d float64
	for i := 1; i < len(points); i++ {
		d += m[i-1][i]
	}
	return Result{DistanceM: d}, nil
}

type OSRM struct {
	BaseURL string
	Client  *http.Client
}

func (o OSRM) Matrix(ctx context.Context, points []domain.Location) ([][]float64, error) {
	if len(points) == 0 {
		return [][]float64{}, nil
	}
	resp, err := o.get(ctx, "/table/v1/driving/"+coords(points)+"?annotations=distance")
	if err != nil {
		return nil, err
	}
	var body struct {
		Code      string      `json:"code"`
		Distances [][]float64 `json:"distances"`
	}
	if err := json.Unmarshal(resp, &body); err != nil {
		return nil, err
	}
	if body.Code != "Ok" || len(body.Distances) != len(points) {
		return nil, fmt.Errorf("osrm matrix: %s", body.Code)
	}
	return body.Distances, nil
}

func (o OSRM) Route(ctx context.Context, points []domain.Location) (Result, error) {
	if len(points) < 2 {
		return Result{}, nil
	}
	resp, err := o.get(ctx, "/route/v1/driving/"+coords(points)+"?overview=false")
	if err != nil {
		return Result{}, err
	}
	var body struct {
		Code   string `json:"code"`
		Routes []struct {
			Distance float64 `json:"distance"`
			Duration float64 `json:"duration"`
		} `json:"routes"`
	}
	if err := json.Unmarshal(resp, &body); err != nil {
		return Result{}, err
	}
	if body.Code != "Ok" || len(body.Routes) == 0 {
		return Result{}, fmt.Errorf("osrm route: %s", body.Code)
	}
	return Result{DistanceM: body.Routes[0].Distance, DurationS: body.Routes[0].Duration}, nil
}

func (o OSRM) get(ctx context.Context, path string) ([]byte, error) {
	base := strings.TrimRight(o.BaseURL, "/")
	if base == "" {
		return nil, errors.New("routing base URL is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return nil, err
	}
	client := o.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &HTTPError{StatusCode: resp.StatusCode, Body: string(body)}
	}
	return body, nil
}

type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string { return "routing HTTP " + strconv.Itoa(e.StatusCode) }

type Config struct {
	Timeout         time.Duration
	Retries         int
	BreakerFailures int
	BreakerOpen     time.Duration
	CacheTTL        time.Duration
	RoundDigits     int
}

// Resilient adds timeout, bounded retries, a small cache, a circuit breaker,
// and haversine fallback. Only timeouts and 5xx responses are retried.
type Resilient struct {
	primary  Router
	fallback Router
	cfg      Config
	now      clock.Clock
	mu       sync.Mutex
	cache    map[string]cacheEntry
	failures int
	opened   time.Time
	halfOpen bool
}
type cacheEntry struct {
	matrix [][]float64
	result Result
	at     time.Time
}

func NewResilient(primary Router, fallback Router, cfg Config, c clock.Clock) *Resilient {
	if fallback == nil {
		fallback = Haversine{}
	}
	if cfg.Retries < 0 {
		cfg.Retries = 0
	}
	if cfg.BreakerFailures < 1 {
		cfg.BreakerFailures = 3
	}
	if cfg.RoundDigits < 0 {
		cfg.RoundDigits = 5
	}
	return &Resilient{primary: primary, fallback: fallback, cfg: cfg, now: c, cache: make(map[string]cacheEntry)}
}

func (r *Resilient) Matrix(ctx context.Context, points []domain.Location) ([][]float64, error) {
	key := keyFor("m", points, r.cfg.RoundDigits)
	if v, ok := r.cached(key); ok {
		return v.matrix, nil
	}
	var out [][]float64
	err := r.call(ctx, func(c context.Context) error { var e error; out, e = r.primary.Matrix(c, points); return e })
	if err != nil {
		out, _ = r.fallback.Matrix(ctx, points)
	}
	r.store(key, cacheEntry{matrix: out, at: r.now.Now()})
	return out, nil
}

func (r *Resilient) Route(ctx context.Context, points []domain.Location) (Result, error) {
	key := keyFor("r", points, r.cfg.RoundDigits)
	if v, ok := r.cached(key); ok {
		return v.result, nil
	}
	var out Result
	err := r.call(ctx, func(c context.Context) error { var e error; out, e = r.primary.Route(c, points); return e })
	if err != nil {
		out, _ = r.fallback.Route(ctx, points)
	}
	r.store(key, cacheEntry{result: out, at: r.now.Now()})
	return out, nil
}

func (r *Resilient) call(ctx context.Context, fn func(context.Context) error) error {
	if r.primary == nil {
		return errors.New("routing provider unavailable")
	}
	r.mu.Lock()
	if !r.allowLocked() {
		r.mu.Unlock()
		return errors.New("routing circuit open")
	}
	r.mu.Unlock()
	var err error
	for attempt := 0; attempt <= r.cfg.Retries; attempt++ {
		callCtx, cancel := context.WithTimeout(ctx, r.cfg.Timeout)
		err = fn(callCtx)
		cancel()
		if err == nil {
			r.success()
			return nil
		}
		if !retryable(err) || attempt == r.cfg.Retries {
			break
		}
		select {
		case <-ctx.Done():
			break
		case <-time.After(time.Duration(attempt+1) * 25 * time.Millisecond):
		}
	}
	r.failure()
	return err
}

func (r *Resilient) allowLocked() bool {
	if r.opened.IsZero() {
		return true
	}
	if r.now.Now().Sub(r.opened) >= r.cfg.BreakerOpen && !r.halfOpen {
		r.halfOpen = true
		return true
	}
	return r.halfOpen
}
func (r *Resilient) success() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures, r.opened, r.halfOpen = 0, time.Time{}, false
}
func (r *Resilient) failure() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures++
	if r.failures >= r.cfg.BreakerFailures {
		r.opened = r.now.Now()
		r.halfOpen = false
	}
}
func (r *Resilient) cached(k string) (cacheEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.cache[k]
	if !ok || r.cfg.CacheTTL <= 0 || r.now.Now().Sub(v.at) > r.cfg.CacheTTL {
		return cacheEntry{}, false
	}
	return v, true
}
func (r *Resilient) store(k string, v cacheEntry) { r.mu.Lock(); r.cache[k] = v; r.mu.Unlock() }

func retryable(err error) bool {
	var he *HTTPError
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &he) && he.StatusCode >= 500)
}
func coords(points []domain.Location) string {
	out := make([]string, len(points))
	for i, p := range points {
		out[i] = strconv.FormatFloat(p.Lng, 'f', 6, 64) + "," + strconv.FormatFloat(p.Lat, 'f', 6, 64)
	}
	return strings.Join(out, ";")
}
func keyFor(prefix string, points []domain.Location, digits int) string {
	var b strings.Builder
	b.WriteString(prefix)
	for _, p := range points {
		b.WriteByte('|')
		b.WriteString(strconv.FormatFloat(math.Round(p.Lat*math.Pow10(digits))/math.Pow10(digits), 'f', digits, 64))
		b.WriteByte(',')
		b.WriteString(strconv.FormatFloat(math.Round(p.Lng*math.Pow10(digits))/math.Pow10(digits), 'f', digits, 64))
	}
	return url.QueryEscape(b.String())
}
