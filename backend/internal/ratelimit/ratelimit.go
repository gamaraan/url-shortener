// Package ratelimit implements a per-client-IP token-bucket limiter for the
// backend API. It is in-process and per-instance (sufficient for the
// single-node k0s target; documented as an approximation under multi-replica
// Postgres mode — see development.md §3.1/§9). Configured via RATE_LIMITS as
// "<count>/<window>"; defaults to 100/1m on parse failure.
package ratelimit

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gamaraan/url-shortener/backend/internal/config"
)

// Limiter is a per-IP token-bucket limiter. The zero value is not usable; use
// New.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket

	count  int
	window time.Duration
	// refillPerSec is the continuous refill rate (tokens per second).
	refillPerSec float64
}

type bucket struct {
	tokens   float64
	last     time.Time
}

// New builds a Limiter from a parsed RateLimit spec.
func New(rl config.RateLimit) *Limiter {
	return &Limiter{
		buckets:      make(map[string]*bucket),
		count:        rl.Count,
		window:       rl.Window,
		refillPerSec: float64(rl.Count) / rl.Window.Seconds(),
	}
}

// Allow reports whether one request is allowed for clientID, and returns the
// recommended Retry-After in seconds when it is not (0 when allowed).
func (l *Limiter) Allow(clientID string) (allowed bool, retryAfter int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	b, ok := l.buckets[clientID]
	if !ok {
		b = &bucket{tokens: float64(l.count), last: now}
		l.buckets[clientID] = b
	}

	// Refill continuously since the last request, capped at count.
	elapsed := now.Sub(b.last).Seconds()
	b.tokens += elapsed * l.refillPerSec
	if b.tokens > float64(l.count) {
		b.tokens = float64(l.count)
	}
	b.last = now

	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	// Not enough tokens: time until 1 token refills.
	missing := 1 - b.tokens
	wait := missing / l.refillPerSec
	return false, max(1, int(wait+0.999))
}

// Middleware wraps h with per-client-IP rate limiting. On exceed it responds
// 429 with JSON {"error":"rate limit exceeded"} and a Retry-After header.
func (l *Limiter) Middleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		ok, retry := l.Allow(ip)
		if !ok {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate limit exceeded"}`))
			return
		}
		h.ServeHTTP(w, r)
	})
}

// clientIP extracts the client IP from X-Forwarded-For (last hop) when present,
// else from RemoteAddr.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := splitComma(xff)
		if len(parts) > 0 {
			return parts[len(parts)-1]
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func splitComma(s string) []string {
	var out []string
	cur := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			if i > cur {
				out = append(out, trimSpace(s[cur:i]))
			}
			cur = i + 1
		}
	}
	if cur < len(s) {
		out = append(out, trimSpace(s[cur:]))
	}
	return out
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}