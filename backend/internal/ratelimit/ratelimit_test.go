package ratelimit_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gamaraan/url-shortener/backend/internal/config"
	"github.com/gamaraan/url-shortener/backend/internal/ratelimit"
)

func TestAllow_WithinLimit(t *testing.T) {
	lim := ratelimit.New(config.RateLimit{Count: 3, Window: time.Minute})
	for i := 0; i < 3; i++ {
		ok, retry := lim.Allow("1.2.3.4")
		if !ok {
			t.Fatalf("request %d: expected allowed, got denied (retry=%d)", i, retry)
		}
		if retry != 0 {
			t.Fatalf("request %d: retry=%d, want 0", i, retry)
		}
	}
}

func TestAllow_OverLimitReturns429(t *testing.T) {
	lim := ratelimit.New(config.RateLimit{Count: 2, Window: time.Minute})
	lim.Allow("10.0.0.1")
	lim.Allow("10.0.0.1")
	ok, retry := lim.Allow("10.0.0.1")
	if ok {
		t.Fatal("expected denied on 3rd request")
	}
	if retry < 1 {
		t.Errorf("retry=%d, want >=1", retry)
	}
}

func TestAllow_PerClientIsolation(t *testing.T) {
	lim := ratelimit.New(config.RateLimit{Count: 1, Window: time.Minute})
	if ok, _ := lim.Allow("a"); !ok {
		t.Fatal("first request for a should be allowed")
	}
	if ok, _ := lim.Allow("a"); ok {
		t.Fatal("second request for a should be denied")
	}
	// Different client has its own bucket.
	if ok, _ := lim.Allow("b"); !ok {
		t.Fatal("first request for b should be allowed")
	}
}

func TestAllow_RefillsOverTime(t *testing.T) {
	lim := ratelimit.New(config.RateLimit{Count: 1, Window: 100 * time.Millisecond})
	lim.Allow("c")
	if ok, _ := lim.Allow("c"); ok {
		t.Fatal("second immediate request should be denied")
	}
	time.Sleep(120 * time.Millisecond)
	if ok, _ := lim.Allow("c"); !ok {
		t.Fatal("request after refill window should be allowed")
	}
}

func TestMiddleware_429HasRetryAfterAndJSON(t *testing.T) {
	lim := ratelimit.New(config.RateLimit{Count: 1, Window: time.Minute})
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := lim.Middleware(inner)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.RemoteAddr = "5.5.5.5:1234"

	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, req)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first: got %d, want 200", rec1.Code)
	}

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second: got %d, want 429", rec2.Code)
	}
	if ra := rec2.Header().Get("Retry-After"); ra == "" {
		t.Error("missing Retry-After header")
	}
	if ct := rec2.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if body := rec2.Body.String(); !contains(body, "rate limit exceeded") {
		t.Errorf("body = %q, want JSON error", body)
	}
}

func TestMiddleware_XForwardedForLastHop(t *testing.T) {
	lim := ratelimit.New(config.RateLimit{Count: 1, Window: time.Minute})
	h := lim.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Header.Set("X-Forwarded-For", "1.1.1.1, 2.2.2.2")
	req.RemoteAddr = "9.9.9.9:5678"

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first: got %d, want 200", rec.Code)
	}

	// Same XFF last hop -> denied. Different XFF last hop -> allowed.
	req2 := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req2.Header.Set("X-Forwarded-For", "1.1.1.1, 2.2.2.2")
	req2.RemoteAddr = "9.9.9.9:5678"
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Errorf("same XFF last hop: got %d, want 429", rec2.Code)
	}

	req3 := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req3.Header.Set("X-Forwarded-For", "1.1.1.1, 3.3.3.3")
	req3.RemoteAddr = "9.9.9.9:5678"
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Errorf("different XFF last hop: got %d, want 200", rec3.Code)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}