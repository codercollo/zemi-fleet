package routing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codercollo/zemi-fleet/internal/clock"
	"github.com/codercollo/zemi-fleet/internal/domain"
)

var routePoints = []domain.Location{{Lat: -1.29, Lng: 36.82}, {Lat: -1.30, Lng: 36.83}}

func TestHaversineRouteAndMatrix(t *testing.T) {
	h := Haversine{}
	m, err := h.Matrix(context.Background(), routePoints)
	if err != nil || len(m) != 2 || m[0][1] <= 0 {
		t.Fatalf("matrix=%v err=%v", m, err)
	}
	r, err := h.Route(context.Background(), routePoints)
	if err != nil || r.DistanceM != m[0][1] {
		t.Fatalf("route=%+v err=%v", r, err)
	}
}

func TestOSRMRetries5xxButNot4xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		if calls.Load() == 1 {
			http.Error(w, "temporary", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":"Ok","distances":[[0,12],[12,0]]}`))
	}))
	defer srv.Close()
	testNow := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	r := NewResilient(OSRM{BaseURL: srv.URL, Client: srv.Client()}, Haversine{}, Config{Timeout: time.Second, Retries: 1, CacheTTL: time.Minute}, clock.NewTest(testNow))
	m, err := r.Matrix(context.Background(), routePoints)
	if err != nil || m[0][1] != 12 || calls.Load() != 2 {
		t.Fatalf("matrix=%v err=%v calls=%d", m, err, calls.Load())
	}

	var badCalls atomic.Int32
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		badCalls.Add(1)
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer bad.Close()
	r = NewResilient(OSRM{BaseURL: bad.URL, Client: bad.Client()}, Haversine{}, Config{Timeout: time.Second, Retries: 2, CacheTTL: time.Minute}, clock.NewTest(testNow))
	_, _ = r.Matrix(context.Background(), routePoints)
	if badCalls.Load() != 1 {
		t.Fatalf("4xx calls=%d, want 1", badCalls.Load())
	}
}

func TestResilientBreakerFallsBackAndReopensAfterClockAdvance(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer srv.Close()
	c := clock.NewTest(time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC))
	r := NewResilient(OSRM{BaseURL: srv.URL, Client: srv.Client()}, Haversine{}, Config{Timeout: time.Second, BreakerFailures: 1, BreakerOpen: time.Minute, CacheTTL: 0}, c)
	_, _ = r.Matrix(context.Background(), routePoints)
	_, _ = r.Matrix(context.Background(), routePoints)
	if calls.Load() != 1 {
		t.Fatalf("open breaker called provider %d times", calls.Load())
	}
	c.Advance(2 * time.Minute)
	_, _ = r.Matrix(context.Background(), routePoints)
	if calls.Load() != 2 {
		t.Fatalf("half-open provider calls=%d", calls.Load())
	}
}
