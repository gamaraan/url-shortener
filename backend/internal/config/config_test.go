package config_test

import (
	"log/slog"
	"testing"
	"time"

	"github.com/gamaraan/url-shortener/backend/internal/config"
)

func TestParseDuration(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"300s", 300 * time.Second},
		{"60m", 60 * time.Minute},
		{"1h", time.Hour},
		{"2d", 48 * time.Hour},
		{"1w", 7 * 24 * time.Hour},
		{"1mo", 30 * 24 * time.Hour},
		{"1y", 365 * 24 * time.Hour},
	}
	for _, c := range cases {
		got, err := config.ParseDuration(c.in)
		if err != nil {
			t.Errorf("ParseDuration(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseDuration(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParseDuration_Errors(t *testing.T) {
	bad := []string{"", "abc", "10", "10x", "-5m", "1.5h"}
	for _, b := range bad {
		if _, err := config.ParseDuration(b); err == nil {
			t.Errorf("ParseDuration(%q): expected error, got nil", b)
		}
	}
}

func TestParseRateLimit(t *testing.T) {
	rl, err := config.ParseRateLimit("100/1m")
	if err != nil {
		t.Fatalf("ParseRateLimit: %v", err)
	}
	if rl.Count != 100 || rl.Window != time.Minute {
		t.Errorf("got %+v, want count=100 window=1m", rl)
	}
}

func TestParseRateLimit_Errors(t *testing.T) {
	bad := []string{"", "100", "100/", "/1m", "0/1m", "100/0m", "abc/1m", "100/xx"}
	for _, b := range bad {
		if _, err := config.ParseRateLimit(b); err == nil {
			t.Errorf("ParseRateLimit(%q): expected error, got nil", b)
		}
	}
}

func TestLoad_Fallbacks(t *testing.T) {
	// No env set: defaults apply (except durations which default to their
	// documented values via envOr).
	t.Setenv("DATABASE_URL", "")
	t.Setenv("POSTGRES_HOST", "")
	c := config.Load(slog.Default())
	if c.ListenAddr != config.DefaultListenAddr {
		t.Errorf("ListenAddr = %q, want %q", c.ListenAddr, config.DefaultListenAddr)
	}
	if c.ShortcodeLength != config.DefaultShortcodeLen {
		t.Errorf("ShortcodeLength = %d, want %d", c.ShortcodeLength, config.DefaultShortcodeLen)
	}
	if c.RateLimit.Count != 100 || c.RateLimit.Window != time.Minute {
		t.Errorf("RateLimit = %+v, want 100/1m", c.RateLimit)
	}
	if c.DatabaseURL != "" {
		t.Errorf("DatabaseURL = %q, want empty (SQLite fallback)", c.DatabaseURL)
	}
}

func TestLoad_ComposesPostgresURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("POSTGRES_HOST", "db.example")
	t.Setenv("POSTGRES_PORT", "5432")
	t.Setenv("POSTGRES_DB", "urls")
	t.Setenv("POSTGRES_USER", "u")
	t.Setenv("POSTGRES_PASSWORD", "p")
	c := config.Load(slog.Default())
	if c.DatabaseURL == "" {
		t.Fatal("expected composed DATABASE_URL, got empty")
	}
	if !contains(c.DatabaseURL, "db.example") || !contains(c.DatabaseURL, "urls") {
		t.Errorf("composed URL missing expected parts: %q", c.DatabaseURL)
	}
}

func TestLoad_InvalidDurationFallsBack(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("POSTGRES_HOST", "")
	t.Setenv("RETENTION_PERIOD", "garbage")
	c := config.Load(slog.Default())
	if c.RetentionPeriod != 3650*24*time.Hour {
		t.Errorf("RetentionPeriod = %v, want default 3650d", c.RetentionPeriod)
	}
}

func TestLoad_InvalidRateLimitFallsBack(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("POSTGRES_HOST", "")
	t.Setenv("RATE_LIMITS", "nonsense")
	c := config.Load(slog.Default())
	if c.RateLimit.Count != 100 || c.RateLimit.Window != time.Minute {
		t.Errorf("RateLimit = %+v, want default 100/1m", c.RateLimit)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || (len(s) > 0 && containsStr(s, sub)))
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
