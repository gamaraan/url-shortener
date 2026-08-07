package worker_test

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gamaraan/url-shortener/backend/internal/migrate"
	"github.com/gamaraan/url-shortener/backend/internal/sqlite"
	"github.com/gamaraan/url-shortener/backend/internal/store"
	"github.com/gamaraan/url-shortener/backend/internal/worker"
	_ "github.com/jackc/pgx/v5/stdlib" // register "pgx" driver
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// newSQLiteStore bootstraps a temp SQLite DB and returns the store + its db.
func newSQLiteStore(t *testing.T) (*store.Store, *sql.DB) {
	t.Helper()
	db, err := sqlite.Bootstrap(context.Background(), filepath.Join(t.TempDir(), "worker.db"))
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return store.New(db, store.DialectSQLite), db
}

// insertLink inserts a row with an explicit created_at and optional expires_at
// (both RFC3339 so both backends accept them).
func insertLink(t *testing.T, st *store.Store, key, dest, createdAtRFC3339 string, expiresAt sql.NullTime) {
	t.Helper()
	ctx := context.Background()
	err := st.Create(ctx, key, dest, expiresAt)
	if err != nil {
		t.Fatalf("Create(%q): %v", key, err)
	}
	// Override created_at directly so we can simulate old links (store.Create
	// always sets created_at = now). Use a parameterized UPDATE.
	_, err = st.DB().ExecContext(ctx,
		st.Dialect().Rewrite(`UPDATE links SET created_at = ? WHERE shortcode = ?`),
		createdAtRFC3339, key)
	if err != nil {
		t.Fatalf("UPDATE created_at for %q: %v", key, err)
	}
}

func TestRunOnce_DeletesOverRetention(t *testing.T) {
	st, db := newSQLiteStore(t)
	ctx := context.Background()

	old := time.Now().Add(-4000 * 24 * time.Hour).UTC().Format(time.RFC3339)
	fresh := time.Now().UTC().Format(time.RFC3339)
	insertLink(t, st, "old1", "https://old.example", old, sql.NullTime{})
	insertLink(t, st, "fresh1", "https://fresh.example", fresh, sql.NullTime{})

	wk := worker.New(st, db, store.DialectSQLite, 3650*24*time.Hour, time.Minute, slog.Default())
	wk.RunOnce(ctx)

	n, err := st.CountLinks(ctx)
	if err != nil {
		t.Fatalf("CountLinks: %v", err)
	}
	if n != 1 {
		t.Errorf("after cleanup, count = %d, want 1 (only fresh retained)", n)
	}
	// The fresh link must still resolve.
	if _, err := st.Get(ctx, "fresh1"); err != nil {
		t.Errorf("fresh1 should still resolve, got %v", err)
	}
}

func TestRunOnce_DeletesExpired(t *testing.T) {
	st, db := newSQLiteStore(t)
	ctx := context.Background()

	pastExpiry := sql.NullTime{Time: time.Now().Add(-time.Hour), Valid: true}
	futureExpiry := sql.NullTime{Time: time.Now().Add(time.Hour), Valid: true}
	insertLink(t, st, "expired1", "https://expired.example", time.Now().UTC().Format(time.RFC3339), pastExpiry)
	insertLink(t, st, "alive1", "https://alive.example", time.Now().UTC().Format(time.RFC3339), futureExpiry)

	wk := worker.New(st, db, store.DialectSQLite, 3650*24*time.Hour, time.Minute, slog.Default())
	wk.RunOnce(ctx)

	n, err := st.CountLinks(ctx)
	if err != nil {
		t.Fatalf("CountLinks: %v", err)
	}
	if n != 1 {
		t.Errorf("after cleanup, count = %d, want 1 (only non-expired retained)", n)
	}
	if _, err := st.Get(ctx, "alive1"); err != nil {
		t.Errorf("alive1 should still resolve, got %v", err)
	}
}

func TestRunOnce_RetentionBoundary(t *testing.T) {
	// A link created exactly retention ago is NOT deleted (cutoff is strict <).
	st, db := newSQLiteStore(t)
	ctx := context.Background()

	retention := 3650 * 24 * time.Hour
	justInside := time.Now().Add(-(retention - time.Second)).UTC().Format(time.RFC3339)
	justOutside := time.Now().Add(-(retention + time.Second)).UTC().Format(time.RFC3339)
	insertLink(t, st, "inside", "https://inside.example", justInside, sql.NullTime{})
	insertLink(t, st, "outside", "https://outside.example", justOutside, sql.NullTime{})

	wk := worker.New(st, db, store.DialectSQLite, retention, time.Minute, slog.Default())
	wk.RunOnce(ctx)

	n, err := st.CountLinks(ctx)
	if err != nil {
		t.Fatalf("CountLinks: %v", err)
	}
	if n != 1 {
		t.Errorf("count = %d, want 1 (only the inside-retention link remains)", n)
	}
	if _, err := st.Get(ctx, "inside"); err != nil {
		t.Errorf("inside should still resolve, got %v", err)
	}
}

func TestNew_ParseFallbackBehavior(t *testing.T) {
	// The worker itself does not parse env; it receives already-parsed
	// durations from config. Verify it runs with the documented defaults
	// when those defaults are passed through (the config layer's fallback is
	// tested in config_test.go).
	st, db := newSQLiteStore(t)
	wk := worker.New(st, db, store.DialectSQLite, 3650*24*time.Hour, 60*time.Minute, slog.Default())
	// A single sweep with an empty store must be a no-op (no error, no panic).
	wk.RunOnce(context.Background())
	n, err := st.CountLinks(context.Background())
	if err != nil {
		t.Fatalf("CountLinks: %v", err)
	}
	if n != 0 {
		t.Errorf("count = %d, want 0", n)
	}
}

// --- Postgres (Docker-gated) ---

func newPostgresStore(t *testing.T) (*store.Store, *sql.DB, func()) {
	t.Helper()
	if _, err := os.Stat("/var/run/docker.sock"); err != nil {
		t.Skip("docker not available; skipping Postgres worker test")
	}
	ctx := context.Background()
	pgC, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("urlshortener"),
		postgres.WithUsername("urlshortener"),
		postgres.WithPassword("urlshortener"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Skipf("postgres testcontainer unavailable: %v", err)
	}
	cleanup := func() { _ = pgC.Terminate(context.Background()) }
	connStr, err := pgC.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		cleanup()
		t.Fatalf("connection string: %v", err)
	}
	if err := migrate.Run(ctx, connStr); err != nil {
		cleanup()
		t.Fatalf("migrate up: %v", err)
	}
	db, err := sql.Open("pgx", connStr)
	if err != nil {
		cleanup()
		t.Fatalf("open: %v", err)
	}
	return store.New(db, store.DialectPostgres), db, cleanup
}

func TestRunOnce_PostgresAdvisoryLock_Contention(t *testing.T) {
	st, db, cleanup := newPostgresStore(t)
	defer cleanup()
	ctx := context.Background()

	// Hold the advisory lock for the duration of the worker sweep by keeping an
	// open transaction that acquired pg_try_advisory_xact_lock. Because the
	// lock is transaction-scoped, the worker's own attempt (on a different
	// pooled connection/transaction) returns false and it must skip.
	lockTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin lock tx: %v", err)
	}
	defer func() { _ = lockTx.Rollback() }()
	var got bool
	if err := lockTx.QueryRowContext(ctx, "SELECT pg_try_advisory_xact_lock($1)", worker.AdvisoryLockKey).Scan(&got); err != nil {
		t.Fatalf("acquire xact lock: %v", err)
	}
	if !got {
		t.Fatal("could not acquire advisory xact lock in test setup")
	}

	// Insert a link that SHOULD be deleted if the worker ran. Because we hold
	// the lock, the worker must skip and leave the row intact.
	old := time.Now().Add(-4000 * 24 * time.Hour).UTC().Format(time.RFC3339)
	insertLink(t, st, "locked1", "https://locked.example", old, sql.NullTime{})

	wk := worker.New(st, db, store.DialectPostgres, 3650*24*time.Hour, time.Minute, slog.Default())
	wk.RunOnce(ctx)

	n, err := st.CountLinks(ctx)
	if err != nil {
		t.Fatalf("CountLinks: %v", err)
	}
	if n != 1 {
		t.Errorf("count = %d, want 1 (worker should have skipped while lock is held)", n)
	}
}

func TestRunOnce_PostgresAdvisoryLock_RunsWhenFree(t *testing.T) {
	st, db, cleanup := newPostgresStore(t)
	defer cleanup()
	ctx := context.Background()

	old := time.Now().Add(-4000 * 24 * time.Hour).UTC().Format(time.RFC3339)
	insertLink(t, st, "oldpg1", "https://oldpg.example", old, sql.NullTime{})

	wk := worker.New(st, db, store.DialectPostgres, 3650*24*time.Hour, time.Minute, slog.Default())
	wk.RunOnce(ctx)

	n, err := st.CountLinks(ctx)
	if err != nil {
		t.Fatalf("CountLinks: %v", err)
	}
	if n != 0 {
		t.Errorf("count = %d, want 0 (worker should delete over-retention link when lock is free)", n)
	}
}

// keep fmt referenced for future helpers.
var _ = fmt.Sprintf
