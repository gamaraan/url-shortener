package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gamaraan/url-shortener/backend/internal/api"
	"github.com/gamaraan/url-shortener/backend/internal/shortcode"
	"github.com/gamaraan/url-shortener/backend/internal/sqlite"
	"github.com/gamaraan/url-shortener/backend/internal/store"
)

// newTestStore bootstraps a temp SQLite DB and returns a store wired to it.
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := sqlite.Bootstrap(context.Background(), filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return store.New(db, store.DialectSQLite)
}

func newServer(t *testing.T) *api.Server {
	st := newTestStore(t)
	gen := shortcode.MustNew(6)
	return api.New(st, gen, nil, slog.Default())
}

func do(t *testing.T, srv *api.Server, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	r.Host = "short.example"
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, r)
	return rec
}

func TestShorten_Success(t *testing.T) {
	srv := newServer(t)
	rec := do(t, srv, http.MethodPut, "/api/shorten",
		`{"destination":"https://example.com/long/path"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body)
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body)
	}
	if resp["shortcode"] == "" {
		t.Error("empty shortcode")
	}
	if resp["destination"] != "https://example.com/long/path" {
		t.Errorf("destination = %q", resp["destination"])
	}
	if resp["short_url"] != "http://short.example/"+resp["shortcode"] {
		t.Errorf("short_url = %q, want http://short.example/%s", resp["short_url"], resp["shortcode"])
	}
}

func TestShorten_InvalidURL(t *testing.T) {
	srv := newServer(t)
	cases := []struct {
		name string
		body string
	}{
		{"empty destination", `{"destination":""}`},
		{"not a url", `{"destination":"hello world"}`},
		{"relative url", `{"destination":"/path/only"}`},
		{"ftp scheme", `{"destination":"ftp://files.example/x"}`},
		{"no host", `{"destination":"http://"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(t, srv, http.MethodPut, "/api/shorten", c.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body)
			}
			var errResp map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
				t.Fatalf("decode: %v; body=%s", err, rec.Body)
			}
			if errResp["error"] == "" {
				t.Errorf("expected non-empty error field; body=%s", rec.Body)
			}
		})
	}
}

func TestShorten_InvalidBody(t *testing.T) {
	srv := newServer(t)
	rec := do(t, srv, http.MethodPut, "/api/shorten", `{not json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestShorten_NegativeTTL(t *testing.T) {
	srv := newServer(t)
	rec := do(t, srv, http.MethodPut, "/api/shorten",
		`{"destination":"https://example.com","ttl_seconds":-5}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body)
	}
}

func TestShorten_WithTTLThenExpires(t *testing.T) {
	srv := newServer(t)
	rec := do(t, srv, http.MethodPut, "/api/shorten",
		`{"destination":"https://example.com","ttl_seconds":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body)
	}
	var resp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	code := resp["shortcode"]

	// Immediate resolve succeeds.
	rec2 := do(t, srv, http.MethodGet, "/api/resolve/"+code, "")
	if rec2.Code != http.StatusOK {
		t.Fatalf("immediate resolve: %d, want 200; body=%s", rec2.Code, rec2.Body)
	}
}

func TestResolve_NotFound(t *testing.T) {
	srv := newServer(t)
	rec := do(t, srv, http.MethodGet, "/api/resolve/doesnotexist", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body)
	}
	var errResp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if errResp["error"] == "" {
		t.Error("expected non-empty error field")
	}
}

func TestHealth(t *testing.T) {
	srv := newServer(t)
	rec := do(t, srv, http.MethodGet, "/api/health", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["status"] != "ok" {
		t.Errorf("status = %q, want ok", resp["status"])
	}
}

func TestHealth_DrainingReturns503(t *testing.T) {
	srv := newServer(t)
	srv.SetDraining(true)
	rec := do(t, srv, http.MethodGet, "/api/health", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 while draining; body=%s", rec.Code, rec.Body)
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body)
	}
	if resp["status"] != "draining" {
		t.Errorf("status = %q, want draining", resp["status"])
	}

	// Toggling back off restores 200.
	srv.SetDraining(false)
	rec2 := do(t, srv, http.MethodGet, "/api/health", "")
	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after clearing drain", rec2.Code)
	}
}

func TestShorten_RoundTrip(t *testing.T) {
	srv := newServer(t)
	rec := do(t, srv, http.MethodPut, "/api/shorten",
		`{"destination":"https://golang.org/"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("shorten: %d; %s", rec.Code, rec.Body)
	}
	var resp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)

	rec2 := do(t, srv, http.MethodGet, "/api/resolve/"+resp["shortcode"], "")
	if rec2.Code != http.StatusOK {
		t.Fatalf("resolve: %d; %s", rec2.Code, rec2.Body)
	}
	var got map[string]string
	if err := json.Unmarshal(rec2.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode resolve: %v", err)
	}
	if got["destination"] != "https://golang.org/" {
		t.Errorf("destination = %q, want https://golang.org/", got["destination"])
	}
}

// keep sql referenced for future helpers (e.g. NullTime construction in tests).
var _ = sql.NullTime{}
