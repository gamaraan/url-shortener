package config_test

import (
	"log/slog"
	"testing"
	"time"

	"github.com/gamaraan/url-shortener/frontend/internal/config"
)

func TestLoad_Defaults(t *testing.T) {
	t.Setenv("LISTEN_ADDR", "")
	t.Setenv("BACKEND_URL", "")
	t.Setenv("SHUTDOWN_TIMEOUT", "")
	t.Setenv("LOG_LEVEL", "")
	c := config.Load(slog.Default())
	if c.ListenAddr != config.DefaultListenAddr {
		t.Errorf("ListenAddr = %q, want %q", c.ListenAddr, config.DefaultListenAddr)
	}
	if c.BackendURL != config.DefaultBackendURL {
		t.Errorf("BackendURL = %q, want %q", c.BackendURL, config.DefaultBackendURL)
	}
	if c.ShutdownTimeout != 30*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 30s", c.ShutdownTimeout)
	}
}

func TestLoad_Overrides(t *testing.T) {
	t.Setenv("LISTEN_ADDR", ":9090")
	t.Setenv("BACKEND_URL", "http://backend.svc:8080")
	t.Setenv("SHUTDOWN_TIMEOUT", "15s")
	c := config.Load(slog.Default())
	if c.ListenAddr != ":9090" {
		t.Errorf("ListenAddr = %q, want :9090", c.ListenAddr)
	}
	if c.BackendURL != "http://backend.svc:8080" {
		t.Errorf("BackendURL = %q", c.BackendURL)
	}
	if c.ShutdownTimeout != 15*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 15s", c.ShutdownTimeout)
	}
}

func TestLoad_InvalidShutdownTimeoutFallsBack(t *testing.T) {
	t.Setenv("SHUTDOWN_TIMEOUT", "garbage")
	c := config.Load(slog.Default())
	if c.ShutdownTimeout != 30*time.Second {
		t.Errorf("ShutdownTimeout = %v, want default 30s", c.ShutdownTimeout)
	}
}

func TestParseDuration(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"30s", 30 * time.Second},
		{"5m", 5 * time.Minute},
		{"1h", time.Hour},
		{"2d", 48 * time.Hour},
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
	for _, bad := range []string{"", "abc", "10", "10x"} {
		if _, err := config.ParseDuration(bad); err == nil {
			t.Errorf("ParseDuration(%q): expected error, got nil", bad)
		}
	}
}
