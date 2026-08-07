// Package server implements the frontend HTTP server: it serves the embedded
// Svelte SPA, reverse-proxies /api/* to the backend, renders a themed 1-second
// redirect page for GET /:shortcode, a themed 404, and /healthz (503 while
// draining). See development.md §3.2.
package server

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

//go:embed all:dist
var distFS embed.FS

// Server is the frontend HTTP server.
type Server struct {
	backendURL  *url.URL
	proxy       *httputil.ReverseProxy
	logger      *slog.Logger
	draining    atomic.Bool
	assets      fs.FS
	indexHTML   []byte
}

// New builds a Server. backendURL is the in-cluster backend base URL
// (e.g. http://backend:8080).
func New(backendURL string, logger *slog.Logger) (*Server, error) {
	u, err := url.Parse(backendURL)
	if err != nil {
		return nil, fmt.Errorf("server: parse backend URL: %w", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	// On proxy error, return a JSON-ish 502 so the SPA/Postman sees a clear
	// failure rather than an opaque 500.
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		logger.Error("server: proxy to backend failed", "error", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"backend unavailable"}`))
	}

	assets, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil, fmt.Errorf("server: embed dist: %w", err)
	}
	idx, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		return nil, fmt.Errorf("server: read embedded index.html: %w", err)
	}

	return &Server{
		backendURL: u,
		proxy:     proxy,
		logger:    logger,
		assets:    assets,
		indexHTML: idx,
	}, nil
}

// SetDraining marks the server as draining; /healthz then returns 503.
func (s *Server) SetDraining(draining bool) {
	s.draining.Store(draining)
}

// IsDraining reports whether the server is draining.
func (s *Server) IsDraining() bool { return s.draining.Load() }

// Handler returns the HTTP handler tree.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("/api/", s.handleProxy) // any method under /api/
	mux.HandleFunc("/", s.handleRoot)
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if s.IsDraining() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"draining"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	s.proxy.ServeHTTP(w, r)
}

// handleRoot serves: SPA assets, the SPA index.html for unknown non-api paths,
// and GET /:shortcode which resolves via the backend and renders a themed
// 1-second redirect page (or a themed 404).
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(r.URL.Path, "/")

	// "/" → SPA index.
	if path == "" {
		s.serveIndex(w, r)
		return
	}

	// Known asset path? Serve from the embedded FS.
	if strings.HasPrefix(path, "assets/") {
		s.serveAsset(w, r, path)
		return
	}

	// Otherwise treat the path as a shortcode: resolve via the backend and
	// render the redirect page (or 404). Only GET is meaningful here.
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.handleShortcode(w, r, path)
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(s.indexHTML)
}

func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request, path string) {
	f, err := s.assets.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Long-cache fingerprinted assets.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, st.Name(), st.ModTime(), f.(io.ReadSeeker))
}

// handleShortcode resolves the shortcode via the backend and renders a themed
// 1-second meta-refresh redirect page, or a themed 404.
func (s *Server) handleShortcode(w http.ResponseWriter, r *http.Request, code string) {
	dest, ok := s.resolve(r.Context(), code)
	if !ok {
		s.render404(w)
		return
	}
	s.renderRedirect(w, code, dest)
}

// resolve calls the backend GET /api/resolve/{shortcode} and returns the
// destination and true on success; false on 404 or error.
func (s *Server) resolve(ctx context.Context, code string) (string, bool) {
	backend := s.backendURL.JoinPath("/api/resolve/", code).String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, backend, nil)
	if err != nil {
		return "", false
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		s.logger.Error("server: resolve backend call failed", "shortcode", code, "error", err)
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	var body struct {
		Destination string `json:"destination"`
	}
	if err := decodeJSON(resp, &body); err != nil {
		return "", false
	}
	if body.Destination == "" {
		return "", false
	}
	return body.Destination, true
}

// renderRedirect writes the themed 1-second redirect page. The destination is
// HTML-escaped; the meta refresh value is exactly 1 second (§3.2 hard req).
func (s *Server) renderRedirect(w http.ResponseWriter, code, dest string) {
	safe := html.EscapeString(dest)
	safeCode := html.EscapeString(code)
	page := `<!doctype html>
<html lang="en">
<head>
<meta charset="UTF-8" />
<meta http-equiv="refresh" content="1; url=` + safe + `" />
<title>Redirecting…</title>
<style>
body{margin:0;background:#0d1117;color:#e6edf3;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif;display:flex;min-height:100vh;align-items:center;justify-content:center}
.card{max-width:520px;padding:2.5rem;text-align:center}
a{color:#f0883e}
a:hover{color:#ff9b57}
.muted{color:#7d8590;margin-top:1.5rem;font-size:.9rem}
</style>
</head>
<body>
<div class="card">
<h1>Redirecting…</h1>
<p>Taking you to <a href="` + safe + `">` + safe + `</a> in 1 second.</p>
<p class="muted">Short link: <code>` + safeCode + `</code></p>
</div>
</body>
</html>`
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(page))
}

// render404 writes a themed 404 page.
func (s *Server) render404(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`<!doctype html>
<html lang="en">
<head>
<meta charset="UTF-8" />
<title>Not found</title>
<style>
body{margin:0;background:#0d1117;color:#e6edf3;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif;display:flex;min-height:100vh;align-items:center;justify-content:center}
.card{max-width:520px;padding:2.5rem;text-align:center}
a{color:#f0883e}
</style>
</head>
<body>
<div class="card">
<h1>404</h1>
<p>This short link does not exist or has expired.</p>
<p><a href="/">Create a new short link</a></p>
</div>
</body>
</html>`))
}

// decodeJSON decodes a JSON body into v.
func decodeJSON(resp *http.Response, v any) error {
	return json.NewDecoder(resp.Body).Decode(v)
}