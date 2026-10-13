package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/codercollo/zemi-fleet/internal/service"
)

// errIdempotencyDisabled means the server was built without an IdempotencyService but a client sent a key.
var errIdempotencyDisabled = errors.New("Idempotency-Key received but idempotency is not configured")

const (
	// idempotencyKeyHeader is the request header carrying the key.
	idempotencyKeyHeader = "Idempotency-Key"
	// idempotentReplayHeader marks a response that was replayed rather than computed.
	idempotentReplayHeader = "Idempotent-Replay"
)

// idempotent makes a POST safe to retry when the client sends an Idempotency-Key. Without the header the request
// is untouched. With it:
//
//   - the key (1 to 64 visible ASCII characters) is claimed atomically for this endpoint, so two concurrent
//     requests with one key cannot both run;
//   - the same key with the same body after the first finished replays the stored response (same status, body and
//     Location) and adds "Idempotent-Replay: true";
//   - the same key with a different body is a client bug: 422;
//   - the same key while the first request is still running: 409, retry shortly;
//   - only successful (2xx) responses are stored. A failure releases the key, so a retry runs again.
func (s *Server) idempotent() gin.HandlerFunc {
	return func(c *gin.Context) {
		key := c.GetHeader(idempotencyKeyHeader)
		if key == "" {
			c.Next()
			return
		}
		if !idempotencyFormat.MatchString(key) {
			s.errorResponse(c, http.StatusBadRequest, idempotencyKeyHeader+" header must be 1 to 64 visible ASCII characters")
			c.Abort()
			return
		}
		if s.idempotency == nil {
			s.serverErrorResponse(c, errIdempotencyDisabled)
			c.Abort()
			return
		}

		// The body is read here to fingerprint it, then handed back for the handler.
		body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes))
		if err != nil {
			s.badRequestResponse(c, err)
			c.Abort()
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))

		endpoint := c.FullPath()
		ctx := c.Request.Context()
		decision, stored, err := s.idempotency.Begin(ctx, key, endpoint, service.HashBody(body))
		if err != nil {
			s.serverErrorResponse(c, err)
			c.Abort()
			return
		}

		switch decision {
		case service.Replay:
			s.replay(c, stored)
			return
		case service.Mismatch:
			s.failedValidationResponse(c, map[string]string{
				"idempotency_key": "this key was already used with a different request body",
			})
			c.Abort()
			return
		case service.InProgress:
			s.errorResponse(c, http.StatusConflict, "a request with this Idempotency-Key is still being processed; retry shortly")
			c.Abort()
			return
		}

		// We own the key. Capture the response; store it if it succeeded, release the key if it did not (also on a panic).
		capture := &captureWriter{ResponseWriter: c.Writer, status: http.StatusOK}
		c.Writer = capture
		finished := false
		bg := context.WithoutCancel(ctx) // finishing the bookkeeping must not depend on the client staying connected
		defer func() {
			if finished {
				return
			}
			if err := s.idempotency.Release(bg, key, endpoint); err != nil {
				s.logError(c, err)
			}
		}()

		c.Next()

		if !service.Storable(capture.status) {
			return // released by the deferred call
		}
		finished = true // the work is done: never release the key now, or a retry would do it twice
		headers := map[string]string{}
		if loc := capture.Header().Get("Location"); loc != "" {
			headers["Location"] = loc
		}
		if err := s.idempotency.Complete(bg, key, endpoint, service.Stored{Status: capture.status, Body: capture.body.Bytes(), Headers: headers}); err != nil {
			// The work is done and the client has its answer; failing to store it only costs a future replay.
			s.logError(c, err)
		}
	}
}

// replay writes a stored response back exactly as it was first sent.
func (s *Server) replay(c *gin.Context, stored service.Stored) {
	for name, value := range stored.Headers {
		c.Writer.Header().Set(name, value)
	}
	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.Header().Set(idempotentReplayHeader, "true")
	c.Writer.WriteHeader(stored.Status)
	if _, err := c.Writer.Write(stored.Body); err != nil {
		s.logError(c, err)
	}
	c.Abort()
}

// captureWriter records the status and body of a response while passing it through.
type captureWriter struct {
	gin.ResponseWriter
	status int
	body   bytes.Buffer
}

func (w *captureWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *captureWriter) Write(b []byte) (int, error) {
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

func (w *captureWriter) WriteString(s string) (int, error) {
	w.body.WriteString(s)
	return w.ResponseWriter.WriteString(s)
}
