package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

// --- access logging (§3.1) ---

// captureLogger records slog records so tests can assert access-log output.
type captureLogger struct {
	records []map[string]any
}

// newCaptureLogger returns a *slog.Logger that records every Info/Debug/Error
// call into the captureLogger.
func newCaptureLogger(cl *captureLogger) *slog.Logger {
	return slog.New(&captureHandler{cl: cl})
}

type captureHandler struct {
	cl *captureLogger
}

func (h *captureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	rec := map[string]any{"msg": r.Message}
	r.Attrs(func(a slog.Attr) bool {
		rec[a.Key] = a.Value.Any()
		return true
	})
	h.cl.records = append(h.cl.records, rec)
	return nil
}

func (h *captureHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(_ string) slog.Handler       { return h }

func TestAccessLog_LogsEveryRequest(t *testing.T) {
	st := newTestStore(t)
	gen := shortcode.MustNew(6)
	cl := &captureLogger{}
	srv := api.New(st, gen, nil, newCaptureLogger(cl))

	do(t, srv, http.MethodGet, "/api/health", "")
	do(t, srv, http.MethodPut, "/api/shorten", `{"destination":"https://example.com"}`)
	do(t, srv, http.MethodGet, "/api/resolve/nope", "")

	if len(cl.records) != 3 {
		t.Fatalf("expected 3 access-log records, got %d", len(cl.records))
	}
	for i, want := range []struct{ method, path string }{
		{"GET", "/api/health"},
		{"PUT", "/api/shorten"},
		{"GET", "/api/resolve/nope"},
	} {
		got := cl.records[i]
		if got["method"] != want.method || got["path"] != want.path {
			t.Errorf("record %d: method=%v path=%v, want %s %s", i, got["method"], got["path"], want.method, want.path)
		}
		if got["status"] == nil {
			t.Errorf("record %d: missing status", i)
		}
		if got["duration_ms"] == nil {
			t.Errorf("record %d: missing duration_ms", i)
		}
		if got["remote_addr"] == nil {
			t.Errorf("record %d: missing remote_addr", i)
		}
	}
	// The not-found resolve must be logged with status 404.
	if cl.records[2]["status"] != int64(http.StatusNotFound) {
		t.Errorf("resolve not-found logged status %v, want 404", cl.records[2]["status"])
	}
}

// --- duplicate-shortcode protection (§3.1 / task 2.5b) ---

// fakeStore lets a test script the Create outcome per attempt to exercise the
// API's collision-retry loop without a real database.
type fakeStore struct {
	createErrs []error // one per Create call (nil = success)
	creates    int
}

func (f *fakeStore) Create(_ context.Context, _, _ string, _ sql.NullTime) error {
	err := f.createErrs[f.creates%len(f.createErrs)]
	f.creates++
	return err
}

func (f *fakeStore) Get(_ context.Context, _ string) (store.Link, error) {
	return store.Link{}, store.ErrNotFound
}

// scriptedGen returns codes from a fixed list, cycling if exhausted.
type scriptedGen struct {
	codes []string
	n     int
}

func (g *scriptedGen) Generate() string {
	c := g.codes[g.n%len(g.codes)]
	g.n++
	return c
}

// TestShorten_RetriesOnCollision asserts that a unique-constraint violation
// from the store causes the API to regenerate and retry, and that it succeeds
// once a non-colliding code is produced.
func TestShorten_RetriesOnCollision(t *testing.T) {
	st := &fakeStore{createErrs: []error{
		&store.ConstraintError{Err: errors.New("UNIQUE constraint failed: links.shortcode")},
		&store.ConstraintError{Err: errors.New("UNIQUE constraint failed: links.shortcode")},
		nil, // 3rd attempt succeeds
	}}
	gen := &scriptedGen{codes: []string{"collide1", "collide2", "winner"}}
	srv := api.New(st, gen, nil, slog.Default())

	rec := do(t, srv, http.MethodPut, "/api/shorten", `{"destination":"https://example.com"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after retry; body=%s", rec.Code, rec.Body)
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body)
	}
	if resp["shortcode"] != "winner" {
		t.Errorf("shortcode = %q, want 'winner' (the 3rd generated code)", resp["shortcode"])
	}
	if st.creates != 3 {
		t.Errorf("Create called %d times, want 3 (2 collisions + 1 success)", st.creates)
	}
}

// TestShorten_CollisionExhausted asserts that when every attempt collides the
// API responds 409 Conflict rather than looping forever or returning 500.
func TestShorten_CollisionExhausted(t *testing.T) {
	dup := &store.ConstraintError{Err: errors.New("UNIQUE constraint failed: links.shortcode")}
	st := &fakeStore{createErrs: []error{dup}} // always collides
	gen := &scriptedGen{codes: []string{"c1", "c2", "c3", "c4", "c5", "c6"}}
	srv := api.New(st, gen, nil, slog.Default())

	rec := do(t, srv, http.MethodPut, "/api/shorten", `{"destination":"https://example.com"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 Conflict; body=%s", rec.Code, rec.Body)
	}
	var errResp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body)
	}
	if !strings.Contains(errResp["error"], "unique shortcode") {
		t.Errorf("error = %q, want a unique-shortcode message", errResp["error"])
	}
	// The API tries at most 5 times before giving up.
	if st.creates != 5 {
		t.Errorf("Create called %d times, want 5 (maxAttempts)", st.creates)
	}
}
