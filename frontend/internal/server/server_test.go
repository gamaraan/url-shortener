package server_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gamaraan/url-shortener/frontend/internal/server"
)

// newServer builds a frontend Server whose backend is the given test server.
func newServer(t *testing.T, backendURL string) *server.Server {
	t.Helper()
	srv, err := server.New(backendURL, slog.Default())
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	return srv
}

func do(t *testing.T, srv *server.Server, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, r)
	return rec
}

// --- health ---

func TestHealth_OK(t *testing.T) {
	srv := newServer(t, "http://backend.invalid:8080")
	rec := do(t, srv, http.MethodGet, "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["status"] != "ok" {
		t.Errorf("status = %q, want ok", got["status"])
	}
}
func TestHealth_DrainingReturns503(t *testing.T) {
	srv := newServer(t, "http://backend.invalid:8080")
	srv.SetDraining(true)
	rec := do(t, srv, http.MethodGet, "/healthz")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["status"] != "draining" {
		t.Errorf("status = %q, want draining", got["status"])
	}
	// Clearing drain restores 200.
	srv.SetDraining(false)
	if do(t, srv, http.MethodGet, "/healthz").Code != http.StatusOK {
		t.Fatal("expected 200 after clearing drain")
	}
}

// --- SPA index + assets ---

func TestRoot_ServesIndexHTML(t *testing.T) {
	srv := newServer(t, "http://backend.invalid:8080")
	rec := do(t, srv, http.MethodGet, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "id=\"app\"") {
		t.Errorf("index body missing #app mount point; body=%s", body)
	}
}

func TestAsset_ServedWithCacheHeaders(t *testing.T) {
	srv := newServer(t, "http://backend.invalid:8080")
	var assetPath string
	for _, name := range distAssets(t) {
		if strings.HasSuffix(name, ".js") {
			assetPath = "/assets/" + name
			break
		}
	}
	if assetPath == "" {
		t.Skip("no .js asset in embedded dist")
	}
	rec := do(t, srv, http.MethodGet, assetPath)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for %s", rec.Code, assetPath)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age=") {
		t.Errorf("Cache-Control = %q, want a max-age directive", cc)
	}
}

// --- proxy ---

func TestProxy_PassesThroughToBackend(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			t.Errorf("backend saw path %q, want /api/health", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer backend.Close()

	srv := newServer(t, backend.URL)
	rec := do(t, srv, http.MethodGet, "/api/health")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body)
	}
	if got := rec.Body.String(); !strings.Contains(got, "ok") {
		t.Errorf("body = %q, want backend response passthrough", got)
	}
}

func TestProxy_BackendDownReturns502(t *testing.T) {
	// Point at a port that refuses connections.
	srv := newServer(t, "http://127.0.0.1:1")
	rec := do(t, srv, http.MethodGet, "/api/health")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

// --- redirect page ---

func TestShortcode_RendersRedirectPage(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/resolve/") {
			t.Errorf("backend saw path %q, want /api/resolve/{code}", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"shortcode":"abc123","destination":"https://example.com/dest"}`))
	}))
	defer backend.Close()

	srv := newServer(t, backend.URL)
	rec := do(t, srv, http.MethodGet, "/abc123")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	// 1-second meta refresh (hard requirement §3.2).
	if !strings.Contains(body, `http-equiv="refresh"`) || !strings.Contains(body, `content="1;`) {
		t.Errorf("redirect page missing 1s meta-refresh; body=%s", body)
	}
	// Destination is present and HTML-escaped into the page.
	if !strings.Contains(body, "https://example.com/dest") {
		t.Errorf("redirect page missing destination; body=%s", body)
	}
}

func TestShortcode_Renders404WhenBackendMissing(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"short link not found"}`))
	}))
	defer backend.Close()

	srv := newServer(t, backend.URL)
	rec := do(t, srv, http.MethodGet, "/nope")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "404") {
		t.Errorf("404 page missing 404 text; body=%s", rec.Body)
	}
}

func TestShortcode_BackendDownRenders404(t *testing.T) {
	srv := newServer(t, "http://127.0.0.1:1")
	rec := do(t, srv, http.MethodGet, "/abc")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when backend is down", rec.Code)
	}
}

// distAssets lists the .js/.css files the Vite build emitted into the
// embedded dist/assets directory, by reading the on-disk build output (which
// //go:embed mirrors into the binary).
func distAssets(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join("..", "server", "dist", "assets")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("no embedded assets to test: %v", err)
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}