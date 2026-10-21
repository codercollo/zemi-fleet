package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"time"
)

// background runs fn in a goroutine that the server waits for at shutdown, and that cannot take the process down:
// a panic is recovered and logged. Let's Go Further ch. 12.
func (s *Server) background(fn func()) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			if v := recover(); v != nil {
				s.logger.LogAttrs(context.Background(), slog.LevelError, "background task panicked",
					slog.String("panic", fmt.Sprint(v)),
					slog.String("stack", string(debug.Stack())),
				)
			}
		}()
		fn()
	}()
}

// Serve runs srv on ln until ctx is cancelled (the caller cancels it on SIGINT or SIGTERM), then shuts down in order:
//
//  1. stop accepting new connections and wait up to timeout for requests already in flight to finish;
//  2. stop the background tasks (the rate limiter's cleanup) and wait for them;
//  3. return, after which the caller closes the database pool.
//
// It returns nil after a clean shutdown, and an error if the server failed to start or if in-flight requests were
// still running when the timeout expired. Let's Go Further ch. 12.
func (s *Server) Serve(ctx context.Context, srv *http.Server, ln net.Listener, timeout time.Duration) error {
	bgCtx, stopBackground := context.WithCancel(context.Background())
	defer stopBackground()
	if s.limiter != nil {
		s.background(func() { s.limiter.runCleanup(bgCtx) })
	}
	if s.batches != nil && s.cfg.Batch.ExpiryInterval > 0 {
		s.background(func() { s.runBatchExpiry(bgCtx) })
	}
	if s.offline != nil && s.cfg.RiderOfflineAfter > 0 && s.cfg.RiderOfflineSweepInterval > 0 {
		s.background(func() { s.runOfflineSweep(bgCtx) })
	}
	if s.webhookDispatcher != nil && s.cfg.Webhooks.Enabled {
		s.background(func() { s.runWebhookDispatch(bgCtx) })
	}

	serveErr := make(chan error, 1)
	go func() {
		// ErrServerClosed is what Serve returns once Shutdown is called: that is the normal path.
		if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr: // the server stopped by itself: it never started, or crashed
		stopBackground()
		s.wg.Wait()
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	shutdownErr := srv.Shutdown(shutdownCtx)

	stopBackground()
	s.wg.Wait()
	<-serveErr

	if shutdownErr != nil {
		return fmt.Errorf("shut down http server: %w", shutdownErr)
	}
	s.logger.Info("stopped server", "addr", srv.Addr)
	return nil
}

func (s *Server) runWebhookDispatch(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := s.webhookDispatcher.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
			s.logger.Error("webhook dispatch failed", "error", err)
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
	}
}

func (s *Server) runBatchExpiry(ctx context.Context) {
	sweep := func() {
		n, err := s.batches.ExpireBatches(ctx)
		if err == nil {
			s.prom.setBatchSweep(s.clock.Now(), n)
		} else if !errors.Is(err, context.Canceled) {
			s.logger.Error("batch expiry sweep failed", "error", err)
		}
	}

	// Sweep immediately on startup, then keep the interval bounded by the
	// configured cadence. The injected clock supplies business time to SQL.
	sweep()
	ticker := time.NewTicker(s.cfg.Batch.ExpiryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			sweep()
		case <-ctx.Done():
			return
		}
	}
}

func (s *Server) runOfflineSweep(ctx context.Context) {
	sweep := func() {
		n, err := s.offline.Sweep(ctx, s.cfg.RiderOfflineAfter)
		if err == nil {
			s.prom.setOfflineSweep(s.clock.Now(), n)
		} else if !errors.Is(err, context.Canceled) {
			s.logger.Error("rider offline sweep failed", "error", err)
		}
	}
	sweep()
	ticker := time.NewTicker(s.cfg.RiderOfflineSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			sweep()
		case <-ctx.Done():
			return
		}
	}
}
