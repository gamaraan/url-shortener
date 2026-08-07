// Package config parses the frontend's environment configuration. See
// development.md §3.2/§3.4.
package config

import (
	"log/slog"
	"os"
	"strings"
	"time"
)

// Defaults.
const (
	DefaultListenAddr      = ":8080"
	DefaultBackendURL      = "http://backend:8080"
	DefaultShutdownTimeout = "30s"
	DefaultLogLevel        = "info"
)

// Config is the resolved frontend configuration.
type Config struct {
	ListenAddr      string
	BackendURL      string
	ShutdownTimeout time.Duration
	LogLevel        slog.Level
}

// Load reads the environment and returns the resolved Config. Invalid
// SHUTDOWN_TIMEOUT values log an error and fall back to the default.
func Load(logger *slog.Logger) Config {
	return Config{
		ListenAddr:      envOr("LISTEN_ADDR", DefaultListenAddr),
		BackendURL:      envOr("BACKEND_URL", DefaultBackendURL),
		ShutdownTimeout: parseDurationOr(logger, "SHUTDOWN_TIMEOUT", envOr("SHUTDOWN_TIMEOUT", DefaultShutdownTimeout), DefaultShutdownTimeout),
		LogLevel:        parseLogLevel(envOr("LOG_LEVEL", DefaultLogLevel)),
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
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

// parseDurationOr parses a duration string with the units s/m/h/d/w/mo/y
// (shared grammar with the backend). On error it logs and falls back.
func parseDurationOr(logger *slog.Logger, name, raw, fallback string) time.Duration {
	d, err := ParseDuration(raw)
	if err != nil {
		if logger != nil {
			logger.Error("config: invalid duration, using fallback",
				"name", name, "value", raw, "fallback", fallback, "error", err)
		}
		d, _ = ParseDuration(fallback)
		return d
	}
	return d
}

// ParseDuration parses a duration string with units s/m/h/d/w/mo/y. This is a
// copy of the backend's shared grammar so the frontend module stays
// self-contained (no import of the backend module).
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, &durationError{raw: s, reason: "empty duration"}
	}
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9') {
		i++
	}
	if i == 0 {
		return 0, &durationError{raw: s, reason: "no leading number"}
	}
	n := 0
	for _, c := range s[:i] {
		n = n*10 + int(c-'0')
	}
	unit := strings.TrimSpace(s[i:])
	switch unit {
	case "s":
		return time.Duration(n) * time.Second, nil
	case "m":
		return time.Duration(n) * time.Minute, nil
	case "h":
		return time.Duration(n) * time.Hour, nil
	case "d":
		return time.Duration(n) * 24 * time.Hour, nil
	case "w":
		return time.Duration(n) * 7 * 24 * time.Hour, nil
	case "mo":
		return time.Duration(n) * 30 * 24 * time.Hour, nil
	case "y":
		return time.Duration(n) * 365 * 24 * time.Hour, nil
	default:
		return 0, &durationError{raw: s, reason: "unknown unit " + unit}
	}
}

type durationError struct {
	raw   string
	reason string
}

func (e *durationError) Error() string {
	return "duration " + e.raw + ": " + e.reason
}