// Package api implements the backend HTTP API: PUT /api/shorten,
// GET /api/resolve/:shortcode, GET /api/health. All responses are JSON.
// Input sanity checks validate the destination URL before any storage work;
// invalid input returns 400 with a human-readable error the frontend renders
// verbatim. See development.md §3.1.
package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gamaraan/url-shortener/backend/internal/ratelimit"
	"github.com/gamaraan/url-shortener/backend/internal/store"
)

// Storer is the subset of the store the API needs. *store.Store satisfies it.
type Storer interface {
	Create(ctx context.Context, shortcode, destination string, expiresAt sql.NullTime) error
	Get(ctx context.Context, shortcode string) (store.Link, error)
}

// Generator produces shortcodes. *shortcode.Generator satisfies it.
type Generator interface {
	Generate() string
}

// Server wires the API handlers to a store and shortcode generator.
type Server struct {
	store     Storer
	generator Generator
	limiter   *ratelimit.Limiter
	logger    *slog.Logger

	// draining is set to 1 during graceful shutdown so /api/health returns 503
	// and the kubelet/ingress stop sending traffic before the process exits.
	draining atomic.Bool
}

// New builds a Server. The limiter may be nil to disable rate limiting (tests).
func New(st Storer, gen Generator, lim *ratelimit.Limiter, logger *slog.Logger) *Server {
	return &Server{store: st, generator: gen, limiter: lim, logger: logger}
}

// SetDraining marks the server as draining. Subsequent /api/health requests
// return 503; in-flight requests are allowed to complete. It is idempotent.
func (s *Server) SetDraining(draining bool) {
	if draining {
		s.draining.Store(true)
	} else {
		s.draining.Store(false)
	}
}

// IsDraining reports whether the server is in drain mode.
func (s *Server) IsDraining() bool { return s.draining.Load() }

// Handler returns an http.Handler for the API, with rate-limiting applied to
// /api/* when a limiter is configured, and per-request access logging wrapped
// around everything (so 429s and 503s are logged too).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/shorten", s.handleShorten)
	mux.HandleFunc("GET /api/resolve/{shortcode}", s.handleResolve)
	mux.HandleFunc("GET /api/health", s.handleHealth)

	h := http.Handler(mux)
	if s.limiter != nil {
		h = s.limiter.Middleware(h)
	}
	return s.logRequests(h)
}

// logRequests wraps h with a per-request access log: method, path, status,
// duration_ms, remote_addr, bytes. It logs at INFO for every request.
func (s *Server) logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		h.ServeHTTP(rec, r)
		s.logger.Info("http: request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"bytes", rec.bytes,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote_addr", r.RemoteAddr,
		)
	})
}

// statusRecorder wraps http.ResponseWriter to capture the response status and
// bytes written for access logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// shortenRequest is the PUT /api/shorten body.
type shortenRequest struct {
	Destination string `json:"destination"`
	TTLSeconds  *int64 `json:"ttl_seconds,omitempty"`
}

// shortenResponse is the success body.
type shortenResponse struct {
	ShortURL    string `json:"short_url"`
	Shortcode   string `json:"shortcode"`
	Destination string `json:"destination"`
}

func (s *Server) handleShorten(w http.ResponseWriter, r *http.Request) {
	var req shortenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Destination = strings.TrimSpace(req.Destination)
	if req.Destination == "" {
		writeError(w, http.StatusBadRequest, "destination is required")
		return
	}
	if err := validateURL(req.Destination); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.TTLSeconds != nil && *req.TTLSeconds < 0 {
		writeError(w, http.StatusBadRequest, "ttl_seconds must be a non-negative integer")
		return
	}

	var expiresAt time.Time
	if req.TTLSeconds != nil && *req.TTLSeconds > 0 {
		expiresAt = time.Now().Add(time.Duration(*req.TTLSeconds) * time.Second)
	}

	// Try up to 5 times in case of a shortcode collision.
	const maxAttempts = 5
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		code := s.generator.Generate()
		err := s.store.Create(r.Context(), code, req.Destination, nullTime(expiresAt))
		if err == nil {
			respond(w, http.StatusOK, shortenResponse{
				ShortURL:    buildShortURL(r, code),
				Shortcode:   code,
				Destination: req.Destination,
			})
			return
		}
		if store.IsConstraintError(err) {
			lastErr = err
			continue
		}
		s.logger.Error("api: create link failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create short link")
		return
	}
	s.logger.Error("api: shortcode collision exhausted", "attempts", maxAttempts, "error", lastErr)
	writeError(w, http.StatusConflict, "could not generate a unique shortcode")
}

func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("shortcode")
	if code == "" {
		writeError(w, http.StatusBadRequest, "shortcode is required")
		return
	}
	link, err := s.store.Get(r.Context(), code)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "short link not found")
		return
	}
	if err != nil {
		s.logger.Error("api: resolve failed", "shortcode", code, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to resolve short link")
		return
	}
	respond(w, http.StatusOK, map[string]string{
		"shortcode":   link.Shortcode,
		"destination": link.Destination,
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if s.IsDraining() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "draining"})
		return
	}
	respond(w, http.StatusOK, map[string]string{"status": "ok"})
}

// validateURL ensures destination is an absolute http(s) URL with a non-empty host.
func validateURL(s string) error {
	u, err := url.Parse(s)
	if err != nil {
		return errors.New("destination must be an absolute http(s) URL")
	}
	if !u.IsAbs() {
		return errors.New("destination must be an absolute http(s) URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("destination must be an absolute http(s) URL")
	}
	if u.Host == "" {
		return errors.New("destination must have a non-empty host")
	}
	return nil
}

// buildShortURL constructs the returned short URL from the request host plus
// the shortcode. It honors X-Forwarded-Host / Host so it is correct behind an
// ingress. The scheme is https when TLS is detected (X-Forwarded-Proto), else http.
func buildShortURL(r *http.Request, code string) string {
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = h
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + host + "/" + code
}

func nullTime(t time.Time) sql.NullTime {
	if t.IsZero() {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: t, Valid: true}
}

func respond(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
