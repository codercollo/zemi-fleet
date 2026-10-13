// Command api runs the Zemi Fleet V2 HTTP server.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"
	_ "time/tzdata" // embed the IANA database so APP_TIMEZONE works in scratch/minimal images

	"github.com/gin-gonic/gin"

	"github.com/codercollo/zemi-fleet/internal/api"
	"github.com/codercollo/zemi-fleet/internal/clock"
	"github.com/codercollo/zemi-fleet/internal/config"
	"github.com/codercollo/zemi-fleet/internal/repository"
	"github.com/codercollo/zemi-fleet/internal/service"
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

	defer pool.Close()
	logger.Info("database connection pool established")

	if cfg.Env != config.EnvDevelopment {
		gin.SetMode(gin.ReleaseMode)
	}

	store := repository.NewStore(pool, cfg.DB.QueryTimeout)
	clk := clock.Real{}
	orders := service.NewOrderService(store, clk, cfg.RiderPayoutBPS, cfg.OrderMinTripM)
	riders := service.NewRiderService(store, clk)
	matching := service.NewMatchingService(store, clk, logger, cfg.Cone)
	batches := service.NewBatchService(store, clk, cfg.Batch)
	preferences := service.NewPreferenceService(store, clk, cfg.Match, cfg.OrderMinTripM)
	app := api.NewServer(cfg, logger, pool, orders, riders, matching, batches, preferences, version)

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      app.Routes(),
		IdleTimeout:  time.Minute,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		ErrorLog:     slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	logger.Info("starting server", "addr", srv.Addr, "env", cfg.Env, "version", version, "timezone", cfg.Timezone)

	// Start server
	return srv.ListenAndServe()
}

// Create logger based on environment
func newLogger(env string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	if env == config.EnvDevelopment {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}
