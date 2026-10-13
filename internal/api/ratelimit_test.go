package api

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codercollo/zemi-fleet/internal/config"
)

func limited(rps float64, burst int, proxies ...string) func(*config.Config) {
	return func(c *config.Config) {
		c.Limiter = config.LimiterConfig{Enabled: true, RPS: rps, Burst: burst, TrustedProxies: proxies}
	}
}

// from sends GET path as if it came from the given TCP peer.
func from(h http.Handler, remote, path string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = remote
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRateLimit_ABurstIsAllowedThenTooManyRequests(t *testing.T) {
	o := newObserved(t, limited(1, 20))
	h := o.handler()

	var codes []int
	for range 25 {
		codes = append(codes, from(h, "198.51.100.1:1000", "/api/v2/ping").Code)
	}

	for i, code := range codes {
		if i < 20 {
			assert.Equal(t, http.StatusOK, code, "request %d is inside the burst", i+1)
		} else {
			assert.Equal(t, http.StatusTooManyRequests, code, "request %d is over the limit", i+1)
		}
	}

	rec := from(h, "198.51.100.1:1000", "/api/v2/ping")
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.JSONEq(t, `{"error":"rate limit exceeded"}`, rec.Body.String())
	assert.Equal(t, "1", rec.Header().Get("Retry-After"))
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.NotEmpty(t, rec.Header().Get("X-Request-ID"), "rate-limited responses are traceable too")
}

func TestRateLimit_TokensRefillWithTime(t *testing.T) {
	o := newObserved(t, limited(2, 2)) // 2 per second, burst of 2
	h := o.handler()
	ip := "198.51.100.1:1000"

	assert.Equal(t, 200, from(h, ip, "/api/v2/ping").Code)
	assert.Equal(t, 200, from(h, ip, "/api/v2/ping").Code)
	assert.Equal(t, 429, from(h, ip, "/api/v2/ping").Code)
	assert.Equal(t, 429, from(h, ip, "/api/v2/ping").Code, "rejected requests do not consume tokens or push the wait further out")

	o.clk.Advance(500 * time.Millisecond) // exactly one token at 2 per second
	assert.Equal(t, 200, from(h, ip, "/api/v2/ping").Code)
	assert.Equal(t, 429, from(h, ip, "/api/v2/ping").Code)

	o.clk.Advance(time.Hour) // the bucket never holds more than the burst
	assert.Equal(t, 200, from(h, ip, "/api/v2/ping").Code)
	assert.Equal(t, 200, from(h, ip, "/api/v2/ping").Code)
	assert.Equal(t, 429, from(h, ip, "/api/v2/ping").Code)
}

func TestRateLimit_RetryAfterIsRoundedUpToWholeSeconds(t *testing.T) {
	tests := []struct {
		rps  float64
		want string
	}{
		{10, "1"},   // 100 ms to wait: never 0, which would invite an immediate retry
		{1, "1"},    // 1 s
		{0.4, "3"},  // 2.5 s rounds up
		{0.1, "10"}, // 10 s
	}
	for _, tc := range tests {
		t.Run(strconv.FormatFloat(tc.rps, 'f', -1, 64), func(t *testing.T) {
			o := newObserved(t, limited(tc.rps, 1))
			h := o.handler()
			from(h, "198.51.100.1:1", "/api/v2/ping")

			rec := from(h, "198.51.100.1:1", "/api/v2/ping")

			require.Equal(t, 429, rec.Code)
			assert.Equal(t, tc.want, rec.Header().Get("Retry-After"))
		})
	}
}

func TestRateLimit_SeparateIPsHaveSeparateBuckets(t *testing.T) {
	o := newObserved(t, limited(1, 2))
	h := o.handler()

	for range 2 {
		assert.Equal(t, 200, from(h, "198.51.100.1:1000", "/api/v2/ping").Code)
	}
	assert.Equal(t, 429, from(h, "198.51.100.1:1000", "/api/v2/ping").Code, "the first client is out of tokens")

	assert.Equal(t, 200, from(h, "198.51.100.2:1000", "/api/v2/ping").Code, "a different IP has its own bucket")
	assert.Equal(t, 200, from(h, "198.51.100.2:1000", "/api/v2/ping").Code)
	assert.Equal(t, 429, from(h, "198.51.100.2:1000", "/api/v2/ping").Code)
	assert.Equal(t, 200, from(h, "[2001:db8::1]:1000", "/api/v2/ping").Code, "IPv6 clients too")

	assert.Equal(t, 429, from(h, "198.51.100.1:2000", "/api/v2/ping").Code, "a different PORT is still the same client")
}

func TestRateLimit_DisabledMeansNoLimit(t *testing.T) {
	o := newObserved(t, func(c *config.Config) { c.Limiter = config.LimiterConfig{Enabled: false, RPS: 1, Burst: 1} })
	h := o.handler()

	for i := range 300 {
		require.Equal(t, 200, from(h, "198.51.100.1:1000", "/api/v2/ping").Code, "request %d", i)
	}
	assert.Nil(t, o.srv.limiter)
}

func TestRateLimit_HealthzIsExempt(t *testing.T) {
	o := newObserved(t, limited(1, 1))
	h := o.handler()
	ip := "198.51.100.1:1000"
	require.Equal(t, 200, from(h, ip, "/api/v2/ping").Code)
	require.Equal(t, 429, from(h, ip, "/api/v2/ping").Code)

	for i := range 100 {
		require.Equal(t, 200, from(h, ip, "/api/v2/healthz").Code, "healthz request %d", i)
	}
	assert.Equal(t, 429, from(h, ip, "/api/v2/ping").Code, "and healthz did not touch the bucket")
}

// CWE-348: a client must not be able to pick its own address (and so its own bucket) with a header.
func TestRateLimit_SpoofedForwardedForIsIgnoredWithoutTrustedProxies(t *testing.T) {
	o := newObserved(t, limited(1, 3)) // TRUSTED_PROXIES empty: trust none
	h := o.handler()
	attacker := "198.51.100.66:4444"

	var codes []int
	for i := range 6 {
		codes = append(codes, from(h, attacker, "/api/v2/ping",
			"X-Forwarded-For", "10.0.0."+strconv.Itoa(i+1), "X-Real-IP", "10.9.9."+strconv.Itoa(i+1)).Code)
	}

	assert.Equal(t, []int{200, 200, 200, 429, 429, 429}, codes, "a new fake address per request buys nothing")
	lines := o.byMsg(t, "request")
	assert.Equal(t, "198.51.100.66", lines[0]["client_ip"], "the log records the real peer, not the claimed one")
}

func TestRateLimit_TrustedProxyForwardedForIsHonoured(t *testing.T) {
	o := newObserved(t, limited(1, 1, "192.0.2.1")) // the proxy
	h := o.handler()
	proxy := "192.0.2.1:9999"

	assert.Equal(t, 200, from(h, proxy, "/api/v2/ping", "X-Forwarded-For", "203.0.113.10").Code)
	assert.Equal(t, 429, from(h, proxy, "/api/v2/ping", "X-Forwarded-For", "203.0.113.10").Code, "same real client, same bucket")
	assert.Equal(t, 200, from(h, proxy, "/api/v2/ping", "X-Forwarded-For", "203.0.113.11").Code, "another real client behind the same proxy")

	t.Run("a header from an untrusted peer is still ignored", func(t *testing.T) {
		assert.Equal(t, 200, from(h, "198.51.100.5:1", "/api/v2/ping", "X-Forwarded-For", "203.0.113.50").Code)
		assert.Equal(t, 429, from(h, "198.51.100.5:1", "/api/v2/ping", "X-Forwarded-For", "203.0.113.51").Code)
	})
	t.Run("a CIDR works too", func(t *testing.T) {
		o := newObserved(t, limited(1, 1, "10.0.0.0/8"))
		h := o.handler()
		assert.Equal(t, 200, from(h, "10.1.2.3:1", "/api/v2/ping", "X-Forwarded-For", "203.0.113.10").Code)
		assert.Equal(t, 200, from(h, "10.1.2.3:1", "/api/v2/ping", "X-Forwarded-For", "203.0.113.11").Code)
	})
}

func TestRateLimit_IdleClientsAreCleanedUp(t *testing.T) {
	o := newObserved(t, limited(1, 5))
	h := o.handler()
	from(h, "198.51.100.1:1", "/api/v2/ping")
	from(h, "198.51.100.2:1", "/api/v2/ping")
	require.Equal(t, 2, o.srv.limiter.size())

	o.clk.Advance(2 * time.Minute)
	assert.Equal(t, 0, o.srv.limiter.cleanup(), "nobody has been idle for 3 minutes yet")
	from(h, "198.51.100.2:1", "/api/v2/ping") // client 2 is seen again

	o.clk.Advance(2 * time.Minute) // client 1 idle 4 min, client 2 idle 2 min
	assert.Equal(t, 1, o.srv.limiter.cleanup())
	assert.Equal(t, 1, o.srv.limiter.size())

	o.clk.Advance(time.Minute + time.Second) // client 2 idle just over 3 min
	assert.Equal(t, 1, o.srv.limiter.cleanup())
	assert.Equal(t, 0, o.srv.limiter.size())
}

func TestRateLimit_ConcurrentUse(t *testing.T) {
	o := newObserved(t, limited(1, 50))
	h := o.handler()
	var ok atomic.Int32
	done := make(chan struct{})
	for range 100 {
		go func() {
			if from(h, "198.51.100.1:1", "/api/v2/ping").Code == 200 {
				ok.Add(1)
			}
			done <- struct{}{}
		}()
	}
	for range 100 {
		<-done
	}

	assert.EqualValues(t, 50, ok.Load(), "exactly the burst got through, however the requests interleaved")
}

// ---- background tasks and graceful shutdown ----

func TestBackground_APanicIsRecoveredAndLogged(t *testing.T) {
	o := newObserved(t)
	var ran atomic.Bool

	o.srv.background(func() { panic("worker blew up") })
	o.srv.background(func() { ran.Store(true) })
	o.srv.wg.Wait()

	assert.True(t, ran.Load(), "one task panicking does not stop the others")
	lines := o.byMsg(t, "background task panicked")
	require.Len(t, lines, 1)
	assert.Equal(t, "worker blew up", lines[0]["panic"])
	assert.NotEmpty(t, lines[0]["stack"])
}

func listenLocal(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	return ln
}

func TestServe_AnInFlightRequestFinishesDuringShutdownAndNewConnectionsAreRefused(t *testing.T) {
	o := newObserved(t, limited(10, 20)) // the limiter's cleanup goroutine is a background task to stop
	started, release := make(chan struct{}), make(chan struct{})
	h := o.handler(func(e *gin.Engine) {
		e.GET("/api/v2/slow", func(c *gin.Context) {
			close(started)
			<-release
			c.String(http.StatusOK, "finished")
		})
	})
	ln := listenLocal(t)
	srv := &http.Server{Handler: h, Addr: ln.Addr().String()}
	ctx, shutdown := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- o.srv.Serve(ctx, srv, ln, 10*time.Second) }()

	// A slow request is in flight.
	type result struct {
		body string
		code int
		err  error
	}
	resp := make(chan result, 1)
	go func() {
		res, err := http.Get("http://" + ln.Addr().String() + "/api/v2/slow")
		if err != nil {
			resp <- result{err: err}
			return
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		resp <- result{body: string(b), code: res.StatusCode}
	}()
	<-started

	// The shutdown signal arrives. New connections are refused, yet the slow request is still being served.
	shutdown()
	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", ln.Addr().String(), 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return false
		}
		return true
	}, 5*time.Second, 20*time.Millisecond, "the listener must stop accepting connections")
	select {
	case err := <-served:
		t.Fatalf("Serve returned (%v) while a request was still in flight", err)
	case r := <-resp:
		t.Fatalf("the in-flight request ended early: %+v", r)
	case <-time.After(150 * time.Millisecond): // still waiting for it, as it should
	}

	// The slow request completes; only then does Serve return.
	close(release)
	r := <-resp
	require.NoError(t, r.err)
	assert.Equal(t, http.StatusOK, r.code)
	assert.Equal(t, "finished", r.body, "the in-flight request got its real answer")
	require.NoError(t, <-served)

	assert.Len(t, o.byMsg(t, "stopped server"), 1)
	o.srv.wg.Wait() // returns at once: the limiter cleanup task stopped, nothing is left running
}

func TestServe_GivesUpWhenInFlightRequestsOutlastTheTimeout(t *testing.T) {
	o := newObserved(t)
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	h := o.handler(func(e *gin.Engine) {
		e.GET("/api/v2/stuck", func(c *gin.Context) { close(started); <-release })
	})
	ln := listenLocal(t)
	srv := &http.Server{Handler: h}
	ctx, shutdown := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- o.srv.Serve(ctx, srv, ln, 100*time.Millisecond) }()
	go func() { _, _ = http.Get("http://" + ln.Addr().String() + "/api/v2/stuck") }()
	<-started

	shutdown()

	select {
	case err := <-served:
		require.Error(t, err)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Contains(t, err.Error(), "shut down http server")
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not give up at the shutdown timeout")
	}
	assert.Empty(t, o.byMsg(t, "stopped server"), "a forced stop is not reported as a clean one")
}

func TestServe_ReportsAServerThatCannotRun(t *testing.T) {
	o := newObserved(t, limited(10, 20))
	ln := listenLocal(t)
	require.NoError(t, ln.Close()) // serving on a closed listener fails at once
	srv := &http.Server{Handler: o.handler()}

	err := o.srv.Serve(context.Background(), srv, ln, time.Second)

	require.Error(t, err)
	o.srv.wg.Wait() // the cleanup task was stopped too
}

func TestServe_ShutsDownCleanlyWithNothingInFlight(t *testing.T) {
	o := newObserved(t)
	ln := listenLocal(t)
	srv := &http.Server{Handler: o.handler()}
	ctx, shutdown := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- o.srv.Serve(ctx, srv, ln, time.Second) }()

	res, err := http.Get("http://" + ln.Addr().String() + "/api/v2/ping")
	require.NoError(t, err)
	_ = res.Body.Close()
	shutdown()

	require.NoError(t, <-served)
	assert.Len(t, o.byMsg(t, "stopped server"), 1)
}
