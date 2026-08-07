// Package config parses the backend's environment configuration, including the
// shared duration-string parser used by RETENTION_PERIOD, CLEANUP_FREQUENCY,
// and the RATE_LIMITS window. Parse failures log an error and fall back to the
// documented default for that variable. See development.md §3.1/§3.4.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Defaults.
const (
	DefaultListenAddr       = ":8080"
	DefaultSQLitePath       = "/data/url-shortener.db"
	DefaultRetention        = "3650d"
	DefaultCleanupFreq      = "60m"
	DefaultShortcodeLen     = 7
	DefaultLogLevel         = "info"
	DefaultRateLimit        = "100/1m"
	DefaultPostgresPort     = "5432"
	DefaultPostgresDB       = "urlshortener"
	DefaultShutdownTimeout = "30s"
)

// Config is the resolved backend configuration.
type Config struct {
	DatabaseURL     string
	SQLitePath      string
	ListenAddr      string
	RetentionPeriod time.Duration
	CleanupFreq     time.Duration
	ShortcodeLength int
	LogLevel        slog.Level
	RateLimit       RateLimit
	ShutdownTimeout time.Duration
}

// RateLimit is a parsed "<count>/<window>" rate-limit spec.
type RateLimit struct {
	Count  int
	Window time.Duration
}

// Load reads the environment and returns the resolved Config. Invalid duration
// / rate-limit values log an error and fall back to the documented default.
func Load(logger *slog.Logger) Config {
	c := Config{
		DatabaseURL:     os.Getenv("DATABASE_URL"),
		SQLitePath:      envOr("SQLITE_PATH", DefaultSQLitePath),
		ListenAddr:      envOr("LISTEN_ADDR", DefaultListenAddr),
		RetentionPeriod: parseDurationOr(logger, "RETENTION_PERIOD", envOr("RETENTION_PERIOD", DefaultRetention), DefaultRetention),
		CleanupFreq:     parseDurationOr(logger, "CLEANUP_FREQUENCY", envOr("CLEANUP_FREQUENCY", DefaultCleanupFreq), DefaultCleanupFreq),
		ShortcodeLength:  parseIntOr(envOr("SHORTCODE_LENGTH", strconv.Itoa(DefaultShortcodeLen)), DefaultShortcodeLen),
		LogLevel:         parseLogLevel(envOr("LOG_LEVEL", DefaultLogLevel)),
		RateLimit:        parseRateLimitOr(logger, envOr("RATE_LIMITS", DefaultRateLimit)),
		ShutdownTimeout:  parseDurationOr(logger, "SHUTDOWN_TIMEOUT", envOr("SHUTDOWN_TIMEOUT", DefaultShutdownTimeout), DefaultShutdownTimeout),
	}
	if c.DatabaseURL == "" {
		c.DatabaseURL = composePostgresURL()
	}
	return c
}

// composePostgresURL builds a libpq URL from the individual POSTGRES_* vars
// when DATABASE_URL is unset. Returns "" if POSTGRES_HOST is also unset (the
// caller then uses the SQLite fallback).
func composePostgresURL() string {
	host := os.Getenv("POSTGRES_HOST")
	if host == "" {
		return ""
	}
	port := envOr("POSTGRES_PORT", DefaultPostgresPort)
	db := envOr("POSTGRES_DB", DefaultPostgresDB)
	user := os.Getenv("POSTGRES_USER")
	pass := os.Getenv("POSTGRES_PASSWORD")
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
		urlEscape(user), urlEscape(pass), host, port, db)
}

func urlEscape(s string) string {
	// libpq percent-encoding for user/password. Keep it simple; the common
	// case is alphanumeric credentials.
	var b strings.Builder
	for _, r := range s {
		if isURLSafe(r) {
			b.WriteRune(r)
		} else {
			fmt.Fprintf(&b, "%%%02X", r)
		}
	}
	return b.String()
}

func isURLSafe(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' || r == '~'
}

// ParseDuration parses a duration string with the units documented in
// development.md §3.1: s, m (minutes), h, d, w, mo (months), y. It is the
// shared parser used by all duration-bearing config values.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	// Must start with digits.
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9') {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("duration %q has no leading number", s)
	}
	n, err := strconv.Atoi(s[:i])
	if err != nil {
		return 0, fmt.Errorf("duration %q: %w", s, err)
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
		// Approximate a month as 30 days (config granularity, not calendar math).
		return time.Duration(n) * 30 * 24 * time.Hour, nil
	case "y":
		return time.Duration(n) * 365 * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("duration %q: unknown unit %q (want s/m/h/d/w/mo/y)", s, unit)
	}
}

func parseDurationOr(logger *slog.Logger, name, raw, fallback string) time.Duration {
	d, err := ParseDuration(raw)
	if err != nil {
		if logger != nil {
			logger.Error("config: invalid duration, using fallback",
				"name", name, "value", raw, "fallback", fallback, "error", err)
		}
		d, _ = ParseDuration(fallback) // fallback is a known-good constant
		return d
	}
	return d
}

// parseRateLimitOr parses "<count>/<window>"; on error logs and falls back to
// 100/1m.
func parseRateLimitOr(logger *slog.Logger, raw string) RateLimit {
	rl, err := ParseRateLimit(raw)
	if err != nil {
		if logger != nil {
			logger.Error("config: invalid rate limit, using fallback",
				"value", raw, "fallback", DefaultRateLimit, "error", err)
		}
		rl, _ = ParseRateLimit(DefaultRateLimit)
		return rl
	}
	return rl
}

// ParseRateLimit parses a "<count>/<window>" rate-limit spec (e.g. "100/1m",
// "30/10s", "1000/1h"). The window uses the shared duration parser.
func ParseRateLimit(raw string) (RateLimit, error) {
	parts := strings.SplitN(raw, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return RateLimit{}, fmt.Errorf("rate limit %q: expected \"<count>/<window>\"", raw)
	}
	count, err := strconv.Atoi(parts[0])
	if err != nil || count <= 0 {
		return RateLimit{}, fmt.Errorf("rate limit %q: count must be a positive integer", raw)
	}
	window, err := ParseDuration(parts[1])
	if err != nil {
		return RateLimit{}, fmt.Errorf("rate limit %q: %w", raw, err)
	}
	if window <= 0 {
		return RateLimit{}, fmt.Errorf("rate limit %q: window must be positive", raw)
	}
	return RateLimit{Count: count, Window: window}, nil
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

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func parseIntOr(raw string, def int) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
