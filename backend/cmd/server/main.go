// Command server is the URL-shortener backend entrypoint.
//
// Phase 2: config → database selection → store → HTTP API server with graceful
// shutdown. The cleanup worker is added in Phase 3. See development.md.
// appVersion: 0.1.0
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gamaraan/url-shortener/backend/internal/api"
	"github.com/gamaraan/url-shortener/backend/internal/config"
	"github.com/gamaraan/url-shortener/backend/internal/migrate"
	"github.com/gamaraan/url-shortener/backend/internal/ratelimit"
	"github.com/gamaraan/url-shortener/backend/internal/shortcode"
	"github.com/gamaraan/url-shortener/backend/internal/sqlite"
	"github.com/gamaraan/url-shortener/backend/internal/store"
	"github.com/gamaraan/url-shortener/backend/internal/worker"
	_ "github.com/jackc/pgx/v5/stdlib" // register "pgx" driver for database/sql
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: parseLogLevel(os.Getenv("LOG_LEVEL")),
	}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, logger); err != nil {
		logger.Error("backend failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger) error {
	cfg := config.Load(logger)

	db, dialect, err := openDB(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer db.Close()

	st := store.New(db, dialect)
	gen := shortcode.MustNew(cfg.ShortcodeLength)
	lim := ratelimit.New(cfg.RateLimit)
	srv := api.New(st, gen, lim, logger)

	// Start the cleanup worker (retention + expiry). It ticks on
	// CLEANUP_FREQUENCY and uses a Postgres advisory lock in Postgres mode.
	wk := worker.New(st, db, dialect, cfg.RetentionPeriod, cfg.CleanupFreq, logger)
	workerCtx, workerCancel := context.WithCancel(ctx)
	go wk.Run(workerCtx)
	logger.Info("backend: cleanup worker started", "frequency", cfg.CleanupFreq, "retention", cfg.RetentionPeriod)

	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Start serving.
	go func() {
		logger.Info("backend: HTTP server listening", "addr", cfg.ListenAddr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("backend: listen failed", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.Info("backend: shutting down", "shutdown_timeout", cfg.ShutdownTimeout)

	// 1. Flip draining so /api/health returns 503 and the kubelet/ingress stop
	//    routing new traffic to this pod.
	srv.SetDraining(true)

	// 2. Stop the cleanup worker concurrently (it may be mid-sweep; the worker's
	//    transaction will be rolled back on context cancellation).
	workerCancel()

	// 3. Stop accepting new connections/requests and wait for in-flight ones to
	//    complete, bounded by SHUTDOWN_TIMEOUT. http.Server.Shutdown closes the
	//    listener and waits for active connections; it does not interrupt the
	//    /api/health 503s above.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		logger.Error("backend: http shutdown did not complete within timeout", "error", err, "timeout", cfg.ShutdownTimeout)
	}
	logger.Info("backend: http server stopped")
	return nil
}

// openDB selects Postgres or SQLite based on config, runs migrations or
// bootstraps the schema, and returns an open *sql.DB plus the store dialect.
func openDB(ctx context.Context, cfg config.Config, logger *slog.Logger) (*sql.DB, store.Dialect, error) {
	if cfg.DatabaseURL != "" {
		logger.Info("database: Postgres mode")
		if err := migrate.Run(ctx, cfg.DatabaseURL); err != nil {
			return nil, store.Dialect{}, fmt.Errorf("postgres migrations: %w", err)
		}
		db, err := sql.Open("pgx", cfg.DatabaseURL)
		if err != nil {
			return nil, store.Dialect{}, fmt.Errorf("postgres open: %w", err)
		}
		if err := db.PingContext(ctx); err != nil {
			return nil, store.Dialect{}, fmt.Errorf("postgres ping: %w", err)
		}
		logger.Info("database: migrations applied")
		return db, store.DialectPostgres, nil
	}

	logger.Warn("database: no DATABASE_URL configured — using temporary SQLite storage; data will be lost on restart; do not start more than one backend instance",
		"sqlite_path", cfg.SQLitePath)
	db, err := sqlite.Bootstrap(ctx, cfg.SQLitePath)
	if err != nil {
		return nil, store.Dialect{}, fmt.Errorf("sqlite bootstrap: %w", err)
	}
	logger.Info("database: SQLite schema created")
	return db, store.DialectSQLite, nil
}

func parseLogLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
