// Command server is the URL-shortener frontend entrypoint.
//
// Phase 4: config → frontend HTTP server (embedded Svelte SPA + reverse proxy
// + redirect page) with graceful shutdown. See development.md §3.2.
// appVersion: 0.1.0
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gamaraan/url-shortener/frontend/internal/config"
	"github.com/gamaraan/url-shortener/frontend/internal/server"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: parseLogLevel(os.Getenv("LOG_LEVEL")),
	}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, logger); err != nil {
		logger.Error("frontend failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger) error {
	cfg := config.Load(logger)

	srv, err := server.New(cfg.BackendURL, logger)
	if err != nil {
		return fmt.Errorf("build server: %w", err)
	}

	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("frontend: HTTP server listening", "addr", cfg.ListenAddr, "backend", cfg.BackendURL)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("frontend: listen failed", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.Info("frontend: shutting down", "shutdown_timeout", cfg.ShutdownTimeout)

	// 1. Flip draining so /healthz returns 503 and the kubelet/ingress stop
	//    routing new traffic to this pod.
	srv.SetDraining(true)

	// 2. Stop accepting new connections/requests and wait for in-flight ones
	//    (including proxied /api/* calls and in-progress /:shortcode resolves)
	//    to complete, bounded by SHUTDOWN_TIMEOUT.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		logger.Error("frontend: http shutdown did not complete within timeout", "error", err, "timeout", cfg.ShutdownTimeout)
	}
	logger.Info("frontend: http server stopped")
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
