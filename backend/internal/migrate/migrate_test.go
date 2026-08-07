package migrate_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/gamaraan/url-shortener/backend/internal/migrate"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	_ "github.com/jackc/pgx/v5/stdlib" // register "pgx" driver for database/sql
)

// dockerAvailable reports whether the Docker daemon is reachable. The
// Postgres migration test is skipped (not failed) when Docker is absent, so
// `go test ./...` stays green on machines without Docker.
func dockerAvailable(t *testing.T) bool {
	t.Helper()
	if _, err := os.Stat("/var/run/docker.sock"); err == nil {
		return true
	}
	// Docker Desktop / custom hosts: try a quick container start below.
	return false
}

func newPostgresURL(t *testing.T) (string, func()) {
	t.Helper()
	ctx := context.Background()
	pgC, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("urlshortener"),
		postgres.WithUsername("urlshortener"),
		postgres.WithPassword("urlshortener"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Skipf("postgres testcontainer unavailable (Docker not running?): %v", err)
	}
	cleanup := func() {
		_ = pgC.Terminate(context.Background())
	}
	connStr, err := pgC.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		cleanup()
		t.Fatalf("container connection string: %v", err)
	}
	// golang-migrate's postgres driver registers under the "postgres" scheme;
	// pgx's stdlib driver also accepts "postgres://" DSNs, so use it as-is.
	return connStr, cleanup
}

func init() {
	// Keep fmt used in case future helpers add formatting.
	_ = fmt.Sprintf("")
}

func TestRun_UpCreatesSchema(t *testing.T) {
	if !dockerAvailable(t) {
		t.Skip("docker not available; skipping Postgres migration test")
	}
	dbURL, cleanup := newPostgresURL(t)
	defer cleanup()
	ctx := context.Background()

	if err := migrate.Run(ctx, dbURL); err != nil {
		t.Fatalf("Run up: %v", err)
	}

	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	assertExists(t, db, "SELECT to_regclass('public.links')")
	assertExists(t, db, "SELECT to_regclass('public.idx_links_created_at')")
	assertExists(t, db, "SELECT to_regclass('public.idx_links_expires_at')")
}

func TestRun_IdempotentDownUp(t *testing.T) {
	if !dockerAvailable(t) {
		t.Skip("docker not available; skipping Postgres migration test")
	}
	dbURL, cleanup := newPostgresURL(t)
	defer cleanup()
	ctx := context.Background()

	if err := migrate.Run(ctx, dbURL); err != nil {
		t.Fatalf("first up: %v", err)
	}
	if err := migrate.Down(ctx, dbURL); err != nil {
		t.Fatalf("down: %v", err)
	}
	// Re-running up after a full down must succeed (ErrNoChange path also OK).
	if err := migrate.Run(ctx, dbURL); err != nil {
		t.Fatalf("second up: %v", err)
	}

	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	assertExists(t, db, "SELECT to_regclass('public.links')")
}

func assertExists(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	var got sql.NullString
	if err := db.QueryRowContext(context.Background(), query).Scan(&got); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	if !got.Valid || got.String == "" {
		t.Fatalf("expected non-empty regclass for %q, got %v", query, got)
	}
}

func init() {
	// Keep fmt used in case future helpers add formatting.
	_ = fmt.Sprintf("")
}