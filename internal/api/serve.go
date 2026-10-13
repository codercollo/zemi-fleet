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
