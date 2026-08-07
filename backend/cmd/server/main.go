// Command server is the URL-shortener backend entrypoint.
//
// Phase 1: database selection + schema bootstrap only. The HTTP server, store,
// shortcode, and cleanup worker are added in Phases 2-3 per development.md.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/gamaraan/url-shortener/backend/internal/migrate"
	"github.com/gamaraan/url-shortener/backend/internal/sqlite"
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
	dbURL := os.Getenv("DATABASE_URL")
	sqlitePath := os.Getenv("SQLITE_PATH")
	if sqlitePath == "" {
		sqlitePath = "/data/url-shortener.db"
	}

	var db *sql.DB
	if dbURL != "" {
		logger.Info("database: Postgres mode", "database_url_set", true)
		if err := migrate.Run(ctx, dbURL); err != nil {
			return fmt.Errorf("postgres migrations: %w", err)
		}
		logger.Info("database: migrations applied")
		// The HTTP server (Phase 2.5) will open its own pgx pool; here we only
		// verify connectivity. Migrations already succeeded above.
	} else {
		logger.Warn("database: no DATABASE_URL configured — using temporary SQLite storage; data will be lost on restart; do not start more than one backend instance",
			"sqlite_path", sqlitePath)
		var err error
		db, err = sqlite.Bootstrap(ctx, sqlitePath)
		if err != nil {
			return fmt.Errorf("sqlite bootstrap: %w", err)
		}
		defer db.Close()
		logger.Info("database: SQLite schema created")
	}

	// Phase 2.5 wires the HTTP server here. For now, exit cleanly after DB init.
	logger.Info("backend: database ready (HTTP server not implemented yet)")
	return nil
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