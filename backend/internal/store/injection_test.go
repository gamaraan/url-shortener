package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gamaraan/url-shortener/backend/internal/migrate"
	"github.com/gamaraan/url-shortener/backend/internal/sqlite"
	"github.com/gamaraan/url-shortener/backend/internal/store"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	_ "github.com/jackc/pgx/v5/stdlib" // register "pgx" driver
)

// payloads are attacker-controlled shortcode/destination values that would
// break an interpolating query builder. All must be stored/looked up as
// literal data.
var payloads = []string{
	`' OR '1'='1`,
	`'; DROP TABLE links;--`,
	`""; --`,
	`' UNION SELECT destination FROM links--`,
	`%27%20OR%20%271%27%3D%271`, // URL-encoded single quotes
	`❰❱`,
	`\x27 OR \x271\x27=\x271`,
	`admin'--`,
	`https://example.com/' OR '1'='1`,
}

func TestSQLInjection_SQLite(t *testing.T) {
	db, err := sqlite.Bootstrap(context.Background(), filepath.Join(t.TempDir(), "inj.db"))
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	defer db.Close()
	runInjectionSuite(t, store.New(db, store.DialectSQLite), db)
}

func TestSQLInjection_Postgres(t *testing.T) {
	if _, err := os.Stat("/var/run/docker.sock"); err != nil {
		t.Skip("docker not available; skipping Postgres injection test")
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
	defer func() { _ = pgC.Terminate(context.Background()) }()

	connStr, err := pgC.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	if err := migrate.Run(ctx, connStr); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	db, err := sql.Open("pgx", connStr)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	runInjectionSuite(t, store.New(db, store.DialectPostgres), db)
}

// runInjectionSuite inserts each payload as both shortcode and destination,
// then reads it back and asserts the schema is intact and lookups return the
// literal payload (no bypass, no table drop).
func runInjectionSuite(t *testing.T, st *store.Store, db *sql.DB) {
	t.Helper()
	ctx := context.Background()

	for i, p := range payloads {
		// Use a unique literal key so each insert succeeds; the *payload itself*
		// is what we then look up to prove it is treated as data.
		key := fmt.Sprintf("k%d", i)
		if err := st.Create(ctx, key, p, sql.NullTime{}); err != nil {
			t.Fatalf("Create(%q as destination) failed: %v", p, err)
		}
		// Also store the payload as a shortcode to exercise the WHERE clause.
		payloadKey := "p" + p
		if err := st.Create(ctx, payloadKey, "https://safe.example", sql.NullTime{}); err != nil {
			t.Fatalf("Create(%q as shortcode) failed: %v", p, err)
		}

		// Lookup by the payload-as-shortcode must return exactly that row.
		got, err := st.Get(ctx, payloadKey)
		if err != nil {
			t.Fatalf("Get(%q) failed: %v", payloadKey, err)
		}
		if got.Destination != "https://safe.example" {
			t.Errorf("payload %q as shortcode: destination = %q, want literal", p, got.Destination)
		}

		// Lookup by the safe key must return the payload as destination verbatim.
		got2, err := st.Get(ctx, key)
		if err != nil {
			t.Fatalf("Get(%q) failed: %v", key, err)
		}
		if got2.Destination != p {
			t.Errorf("payload %q as destination: got %q, want literal", p, got2.Destination)
		}
	}

	// Schema intact: links table still exists and has 2 rows per payload.
	exists, err := st.TableExists(ctx)
	if err != nil {
		t.Fatalf("TableExists: %v", err)
	}
	if !exists {
		t.Fatal("links table missing after injection payloads — schema altered")
	}
	n, err := st.CountLinks(ctx)
	if err != nil {
		t.Fatalf("CountLinks: %v", err)
	}
	if want := int64(len(payloads) * 2); n != want {
		t.Errorf("row count = %d, want %d (payloads should not have dropped/added rows)", n, want)
	}

	// A lookup for a non-existent shortcode must NOT be bypassed by a payload.
	if _, err := st.Get(ctx, "' OR '1'='1"); err != store.ErrNotFound {
		t.Errorf("Get(injection-as-missing-key) err = %v, want ErrNotFound", err)
	}
}