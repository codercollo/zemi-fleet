package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/codercollo/zemi-fleet/internal/clock"
	"github.com/codercollo/zemi-fleet/internal/domain"
	"github.com/codercollo/zemi-fleet/internal/repository"
	"github.com/codercollo/zemi-fleet/internal/repository/sqlc"
)

// IdempotencyService makes retried POSTs safe. A request that carries an Idempotency-Key is CLAIMED before it
// runs, so concurrent requests with one key cannot both execute, and its response is stored and replayed to
// later requests with the same key and the same body.
type IdempotencyService struct {
	store repository.Store
	clock clock.Clock
	// ttl is how long a key is remembered (IDEMPOTENCY_TTL). Older keys are ignored and deleted on write.
	ttl time.Duration
	// processingTimeout is how long a claim may stay unfinished before another request may take it over
	// (IDEMPOTENCY_PROCESSING_TIMEOUT): the first attempt probably crashed.
	processingTimeout time.Duration
}

// NewIdempotencyService wires the service.
func NewIdempotencyService(store repository.Store, clk clock.Clock, ttl, processingTimeout time.Duration) *IdempotencyService {
	return &IdempotencyService{store: store, clock: clk, ttl: ttl, processingTimeout: processingTimeout}
}

// Decision says what to do with a request that carries a key.
type Decision int

const (
	// Proceed: this request owns the key; run the handler, then Complete (success) or Release (anything else).
	Proceed Decision = iota
	// Replay: the same request already completed; send Stored back.
	Replay
	// Mismatch: the key was used with a different body (a client bug); answer 422.
	Mismatch
	// InProgress: the same request is still running; answer 409 and let the client retry shortly.
	InProgress
)

// Stored is a response saved for replay.
type Stored struct {
	Status  int
	Body    []byte
	Headers map[string]string
}

// HashBody fingerprints a request body. JSON is compacted first so a retry that differs only in whitespace is
// still "the same body"; anything else is hashed as it is.
func HashBody(body []byte) string {
	var compact bytes.Buffer
	if err := json.Compact(&compact, body); err == nil {
		body = compact.Bytes()
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// Begin decides what to do with a request. It first deletes keys older than the TTL (cleanup on write), then
// tries to claim the key atomically.
func (s *IdempotencyService) Begin(ctx context.Context, key, endpoint, requestHash string) (Decision, Stored, error) {
	now := s.clock.Now()

	if err := s.store.DeleteExpiredIdempotencyKeys(ctx, now.Add(-s.ttl)); err != nil {
		return 0, Stored{}, fmt.Errorf("delete expired idempotency keys: %w", err)
	}

	claimed, err := s.store.ClaimIdempotencyKey(ctx, sqlc.ClaimIdempotencyKeyParams{
		Key: key, Endpoint: endpoint, RequestHash: requestHash, CreatedAt: now,
	})
	if err != nil {
		return 0, Stored{}, fmt.Errorf("claim idempotency key: %w", err)
	}
	if claimed == 1 {
		return Proceed, Stored{}, nil
	}

	row, err := s.store.GetIdempotencyKey(ctx, sqlc.GetIdempotencyKeyParams{Key: key, Endpoint: endpoint})
	if errors.Is(err, domain.ErrNotFound) {
		// Deleted between our claim and our read (released, or expired and cleaned). Treat as free: try once more.
		return s.claimAgain(ctx, key, endpoint, requestHash, now)
	}
	if err != nil {
		return 0, Stored{}, fmt.Errorf("get idempotency key: %w", err)
	}

	if row.RequestHash != requestHash {
		return Mismatch, Stored{}, nil
	}
	if row.ResponseStatus != nil && row.ResponseBody != nil {
		stored := Stored{Status: int(*row.ResponseStatus), Body: []byte(*row.ResponseBody), Headers: map[string]string{}}
		_ = json.Unmarshal(row.ResponseHeaders, &stored.Headers)
		return Replay, stored, nil
	}

	// Same body, still unfinished. A claim that has been stuck for longer than the processing timeout is a
	// crashed request: take it over instead of making the client wait for the whole TTL.
	took, err := s.store.TakeOverIdempotencyKey(ctx, sqlc.TakeOverIdempotencyKeyParams{
		RequestHash: requestHash, Now: now, Key: key, Endpoint: endpoint, StaleBefore: now.Add(-s.processingTimeout),
	})
	if err != nil {
		return 0, Stored{}, fmt.Errorf("take over idempotency key: %w", err)
	}
	if took == 1 {
		return Proceed, Stored{}, nil
	}
	return InProgress, Stored{}, nil
}

func (s *IdempotencyService) claimAgain(ctx context.Context, key, endpoint, requestHash string, now time.Time) (Decision, Stored, error) {
	claimed, err := s.store.ClaimIdempotencyKey(ctx, sqlc.ClaimIdempotencyKeyParams{
		Key: key, Endpoint: endpoint, RequestHash: requestHash, CreatedAt: now,
	})
	if err != nil {
		return 0, Stored{}, fmt.Errorf("claim idempotency key: %w", err)
	}
	if claimed == 1 {
		return Proceed, Stored{}, nil
	}
	return InProgress, Stored{}, nil
}

// Complete stores the response of a request that succeeded, for replay.
func (s *IdempotencyService) Complete(ctx context.Context, key, endpoint string, resp Stored) error {
	headers, err := json.Marshal(resp.Headers)
	if err != nil {
		return fmt.Errorf("encode headers: %w", err)
	}
	status, body := int32(resp.Status), string(resp.Body)
	if err := s.store.CompleteIdempotencyKey(ctx, sqlc.CompleteIdempotencyKeyParams{
		ResponseStatus: &status, ResponseBody: &body, ResponseHeaders: headers, Key: key, Endpoint: endpoint,
	}); err != nil {
		return fmt.Errorf("complete idempotency key: %w", err)
	}
	return nil
}

// Release drops an unfinished claim after a request that failed, so the client can retry with the same key.
func (s *IdempotencyService) Release(ctx context.Context, key, endpoint string) error {
	if err := s.store.ReleaseIdempotencyKey(ctx, sqlc.ReleaseIdempotencyKeyParams{Key: key, Endpoint: endpoint}); err != nil {
		return fmt.Errorf("release idempotency key: %w", err)
	}
	return nil
}

// Storable reports whether a response is worth remembering: only successes. A failed request had no effect, so a
// retry should run again (a 422 may have been fixed, a 404 may exist by now, a 5xx is transient).
func Storable(status int) bool { return status >= http.StatusOK && status < http.StatusMultipleChoices }
