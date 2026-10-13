package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codercollo/zemi-fleet/internal/clock"
	"github.com/codercollo/zemi-fleet/internal/config"
	"github.com/codercollo/zemi-fleet/internal/logging"
)

// observed is a server whose logs and clock the test can see and move.
type observed struct {
	srv  *Server
	logs *bytes.Buffer
	clk  *clock.Test
}

func newObserved(t *testing.T, mutate ...func(*config.Config)) observed {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := config.Config{Env: config.EnvDevelopment, APIKeys: testAPIKeys}
	for _, m := range mutate {
		m(&cfg)
	}
	logs := &bytes.Buffer{}
	logger := slog.New(logging.NewContextHandler(slog.NewJSONHandler(logs, nil)))
	clk := clock.NewTest(time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC))
	return observed{srv: NewServer(cfg, logger, fakeDB{}, Services{Clock: clk}, "test"), logs: logs, clk: clk}
}

// routes the tests add behind the real middleware chain
func (o observed) handler(extra ...func(*gin.Engine)) http.Handler {
	base := func(e *gin.Engine) {
		e.GET("/api/v2/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })
		e.GET("/api/v2/things/:id", func(c *gin.Context) {
			o.clk.Advance(250 * time.Millisecond) // the handler "takes" 250 ms
			_ = writeJSON(c, http.StatusOK, envelope{"thing": c.Param("id"), "id": logging.RequestID(c.Request.Context())}, nil)
		})
		e.GET("/api/v2/boom", func(c *gin.Context) { panic("boom while handling +254722000001") })
		e.GET("/api/v2/fail", func(c *gin.Context) {
			o.srv.logError(c, fmt.Errorf("db said: \nforged line {\"msg\":\"x\"} for +254722000001"))
			c.Status(http.StatusOK)
		})
	}
	return o.srv.router(append([]func(*gin.Engine){base}, extra...)...)
}

func (o observed) lines(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, raw := range bytes.Split(bytes.TrimSpace(o.logs.Bytes()), []byte("\n")) {
		if len(raw) == 0 {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal(raw, &m), "every log line must be valid JSON: %s", raw)
		out = append(out, m)
	}
	return out
}

func (o observed) byMsg(t *testing.T, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range o.lines(t) {
		if l["msg"] == msg {
			out = append(out, l)
		}
	}
	return out
}

func get(h http.Handler, path string, headers ...string) *httptest.ResponseRecorder {
	req := authed(httptest.NewRequest(http.MethodGet, path, nil))
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// ---- request ID ----

func TestRequestID(t *testing.T) {
	h := newObserved(t).handler()

	t.Run("a request without one gets a generated ID, echoed and in the context", func(t *testing.T) {
		rec := get(h, "/api/v2/things/x")

		id := rec.Header().Get("X-Request-ID")
		assert.Regexp(t, `^[0-9a-f]{32}$`, id)
		assert.Contains(t, rec.Body.String(), `"id":"`+id+`"`, "handlers see the same ID in the request context")
	})

	t.Run("a safe incoming ID is kept", func(t *testing.T) {
		for _, id := range []string{"abc", "req-123_45.6", strings.Repeat("a", 64), "A"} {
			rec := get(h, "/api/v2/ping", "X-Request-ID", id)

			assert.Equal(t, id, rec.Header().Get("X-Request-ID"))
		}
	})

	t.Run("an unsafe or oversized ID is replaced, never echoed", func(t *testing.T) {
		for name, id := range map[string]string{
			"too long": strings.Repeat("a", 65), "a space": "has space", "a slash": "a/b", "a quote": `a"b`,
			"a semicolon": "a;b", "angle brackets": "<script>", "unicode": "café", "a tab": "a\tb",
		} {
			rec := get(h, "/api/v2/ping", "X-Request-ID", id)

			got := rec.Header().Get("X-Request-ID")
			assert.NotEqual(t, id, got, name)
			assert.Regexp(t, `^[0-9a-f]{32}$`, got, name)
		}
	})

	t.Run("generated IDs are unique", func(t *testing.T) {
		seen := map[string]bool{}
		for range 200 {
			id := get(h, "/api/v2/ping").Header().Get("X-Request-ID")
			assert.False(t, seen[id])
			seen[id] = true
		}
	})

	t.Run("it is also on errors and 404s", func(t *testing.T) {
		assert.NotEmpty(t, get(h, "/api/v2/nope").Header().Get("X-Request-ID"))
	})
}

// ---- access log ----

func TestAccessLog_OneLineWithTheRightFields(t *testing.T) {
	o := newObserved(t)
	h := o.handler()
	req := authed(httptest.NewRequest(http.MethodGet, "/api/v2/things/ord_SECRET123", nil))
	req.Header.Set("X-Request-ID", "trace-me-1")
	req.RemoteAddr = "203.0.113.9:5555"
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	lines := o.byMsg(t, "request")
	require.Len(t, lines, 1, "exactly one access line per request")
	l := lines[0]
	assert.Equal(t, "GET", l["method"])
	assert.Equal(t, "/api/v2/things/:id", l["route"], "the route template, not the raw path")
	assert.EqualValues(t, 200, l["status"])
	assert.EqualValues(t, 250, l["duration_ms"], "measured with the injected clock")
	assert.EqualValues(t, rec.Body.Len(), l["bytes"])
	assert.Equal(t, "trace-me-1", l["request_id"])
	assert.Equal(t, "203.0.113.9", l["client_ip"])
	assert.NotContains(t, o.logs.String(), "SECRET123", "the raw path, which carries IDs, is never logged")
	assert.Equal(t, "INFO", l["level"])
}

func TestAccessLog_CoversEveryOutcome(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantRoute  string
	}{
		{"ok", http.MethodGet, "/api/v2/ping", 200, "/api/v2/ping"},
		{"no such route", http.MethodGet, "/api/v2/does/not/exist", 404, "unmatched"},
		{"wrong method", http.MethodDelete, "/api/v2/ping", 405, "unmatched"},
		{"panic is logged as a 500", http.MethodGet, "/api/v2/boom", 500, "/api/v2/boom"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := newObserved(t)
			req := authed(httptest.NewRequest(tc.method, tc.path, nil))

			o.handler().ServeHTTP(httptest.NewRecorder(), req)

			lines := o.byMsg(t, "request")
			require.Len(t, lines, 1)
			assert.EqualValues(t, tc.wantStatus, lines[0]["status"])
			assert.Equal(t, tc.wantRoute, lines[0]["route"])
		})
	}
}

// CWE-117: nothing a client controls reaches the message text; hostile values stay inside attribute values.
func TestLogging_UserInputIsNeverInTheMessage(t *testing.T) {
	o := newObserved(t)
	h := o.handler()

	get(h, "/api/v2/things/"+"x%0A%7B%22msg%22:%22forged%22%7D", "X-Request-ID", "ok\n{\"msg\":\"forged\"}", "User-Agent", "evil\r\nINFO forged")
	get(h, "/api/v2/fail", "X-Request-ID", "good-id")

	for _, l := range o.lines(t) {
		assert.Contains(t, []string{"request", "request failed"}, l["msg"], "only fixed messages: %v", l)
	}
	assert.Len(t, o.lines(t), 3, "two access lines and one error line, nothing forged")
	assert.NotContains(t, o.logs.String(), "forged\"}\n{", "no injected line")
}

func TestLogError_FixedMessageEscapedErrorAndMaskedPhone(t *testing.T) {
	o := newObserved(t)

	get(o.handler(), "/api/v2/fail", "X-Request-ID", "fail-1")

	errs := o.byMsg(t, "request failed")
	require.Len(t, errs, 1)
	assert.Equal(t, "ERROR", errs[0]["level"])
	assert.Equal(t, "fail-1", errs[0]["request_id"])
	assert.Equal(t, "/api/v2/fail", errs[0]["route"])
	assert.Contains(t, errs[0]["error"], "forged line", "the text is kept, as an attribute value")
	assert.NotContains(t, o.logs.String(), "+254722000001", "the phone number is masked")
	assert.Contains(t, errs[0]["error"], "+2547****0001")
}

func TestHandlerLogsCarryTheRequestID(t *testing.T) {
	o := newObserved(t)
	h := o.handler(func(e *gin.Engine) {
		e.GET("/api/v2/audit", func(c *gin.Context) {
			o.srv.logger.InfoContext(c.Request.Context(), "something audited", slog.String("who", "rider_001"))
			c.Status(http.StatusOK)
		})
	})

	get(h, "/api/v2/audit", "X-Request-ID", "audit-7")

	lines := o.byMsg(t, "something audited")
	require.Len(t, lines, 1)
	assert.Equal(t, "audit-7", lines[0]["request_id"])
}

// ---- panic recovery ----

func TestRecoverPanic(t *testing.T) {
	o := newObserved(t)
	h := o.handler()

	rec := get(h, "/api/v2/boom", "X-Request-ID", "panic-1")

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "close", rec.Header().Get("Connection"), "do not reuse a connection that may be in a bad state")
	assert.JSONEq(t, `{"error":"the server encountered a problem and could not process your request"}`, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "boom", "the panic value is never sent to the client")
	assert.Equal(t, "panic-1", rec.Header().Get("X-Request-ID"))

	logged := o.byMsg(t, "panic recovered")
	require.Len(t, logged, 1, "exactly one recovery")
	assert.Equal(t, "ERROR", logged[0]["level"])
	assert.Equal(t, "panic-1", logged[0]["request_id"])
	assert.Contains(t, logged[0]["panic"], "boom while handling")
	assert.NotContains(t, logged[0]["panic"], "+254722000001", "even a panic message is scrubbed of phone numbers")
	assert.Contains(t, logged[0]["stack"], "middleware_test.go", "the stack points at the panicking handler")
	assert.Equal(t, "/api/v2/boom", logged[0]["route"])

	t.Run("the server keeps working", func(t *testing.T) {
		assert.Equal(t, http.StatusOK, get(h, "/api/v2/ping").Code)
	})
}

func TestRecoverPanic_ThereIsExactlyOneRecovery(t *testing.T) {
	// gin.Default() would add Gin's own Recovery: a panic would then be logged twice. Ours is the only one.
	o := newObserved(t)

	get(o.handler(), "/api/v2/boom")

	assert.Len(t, o.byMsg(t, "panic recovered"), 1)
	assert.NotContains(t, o.logs.String(), "[Recovery]")
}

func TestRecoverPanic_AlsoCatchesAPanicInAMiddlewareBehindIt(t *testing.T) {
	o := newObserved(t)
	h := o.srv.router(func(e *gin.Engine) {
		e.GET("/api/v2/mw", func(c *gin.Context) { var p *int; *p = 1 }) // nil pointer dereference
	})

	rec := get(h, "/api/v2/mw")

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// ---- security headers ----

func TestSecurityHeaders(t *testing.T) {
	o := newObserved(t, func(c *config.Config) { c.Limiter = config.LimiterConfig{Enabled: true, RPS: 1, Burst: 1} })
	h := o.handler(func(e *gin.Engine) {
		e.GET("/api/v2/sets-server", func(c *gin.Context) { c.Status(http.StatusOK) })
	})
	get(h, "/api/v2/ping") // use the only token so the next request is a 429

	tests := []struct {
		name string
		do   func() *httptest.ResponseRecorder
	}{
		{"429 from the limiter", func() *httptest.ResponseRecorder { return get(h, "/api/v2/ping") }},
		{"healthz", func() *httptest.ResponseRecorder { return get(h, "/api/v2/healthz") }},
		{"404", func() *httptest.ResponseRecorder { return get(h, "/nowhere") }},
		{"405", func() *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, authed(httptest.NewRequest(http.MethodDelete, "/api/v2/healthz", nil)))
			return rec
		}},
		{"a recovered panic", func() *httptest.ResponseRecorder { return get(newObserved(t).handler(), "/api/v2/boom") }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := tc.do()

			assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
			assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
			assert.Empty(t, rec.Header().Values("Server"), "no Server header")
		})
	}
}

// Over a real connection (Go's own server adds no Server header).
func TestSecurityHeadersOverTheWire(t *testing.T) {
	o := newObserved(t)
	ts := httptest.NewServer(o.handler())
	defer ts.Close()

	res, err := getAuthed(ts.URL + "/api/v2/ping")
	require.NoError(t, err)
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)

	assert.Equal(t, "nosniff", res.Header.Get("X-Content-Type-Options"))
	assert.Equal(t, "no-store", res.Header.Get("Cache-Control"))
	assert.Empty(t, res.Header.Values("Server"))
}
