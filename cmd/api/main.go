// Command api runs the Zemi Fleet V2 HTTP server.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // embed the IANA database so APP_TIMEZONE works in scratch/minimal images

	"github.com/gin-gonic/gin"

	"github.com/codercollo/zemi-fleet/internal/app"
	"github.com/codercollo/zemi-fleet/internal/clock"
	"github.com/codercollo/zemi-fleet/internal/config"
	"github.com/codercollo/zemi-fleet/internal/logging"
	"github.com/codercollo/zemi-fleet/internal/repository"
)

// version is overridden at build time with
// -ldflags "-X main.version=$(git describe --always --dirty --tags)".
var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}

	cfg, err := config.Load("app.env")
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	logger := newLogger(cfg.Env)

	if err := run(cfg, logger); err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}
}

func run(cfg config.Config, logger *slog.Logger) error {
	pool, err := repository.OpenPool(context.Background(), cfg.DB)
	if err != nil {
		return err
	}
	// Closed last: Serve returns only after the HTTP server has drained and the background tasks have stopped.
	defer pool.Close()
	logger.Info("database connection pool established")

	if cfg.Env != config.EnvDevelopment {
		gin.SetMode(gin.ReleaseMode)
	}

	server := app.New(cfg, logger, pool, clock.Real{}, version)

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      server.Routes(),
		IdleTimeout:  time.Minute,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		ErrorLog:     slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", srv.Addr, err)
	}

	// SIGINT (Ctrl+C) and SIGTERM (what Docker and systemd send) start a graceful shutdown. Windows delivers only
	// the interrupt; it has no SIGTERM.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		sig := <-quit
		logger.Info("shutting down server", "signal", sig.String(), "timeout", cfg.ShutdownTimeout.String())
		cancel()
	}()

	logger.Info("starting server", "addr", srv.Addr, "env", cfg.Env, "version", version, "timezone", cfg.Timezone)
	return server.Serve(ctx, srv, ln, cfg.ShutdownTimeout)
}

// Create logger based on environment
func newLogger(env string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	// The context handler stamps request_id on every record logged with a request's context.
	if env == config.EnvDevelopment {
		return slog.New(logging.NewContextHandler(slog.NewTextHandler(os.Stdout, opts)))
	}
	return slog.New(logging.NewContextHandler(slog.NewJSONHandler(os.Stdout, opts)))
}
