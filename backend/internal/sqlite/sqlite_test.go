package sqlite_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/gamaraan/url-shortener/backend/internal/sqlite"
)

// schemaObjects returns the names of tables and indexes present in the
// SQLite database at path (from sqlite_master).
func schemaObjects(t *testing.T, path string) map[string]bool {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatalf("open ro: %v", err)
	}
	defer db.Close()

	rows, err := db.QueryContext(context.Background(),
		`SELECT name FROM sqlite_master WHERE type IN ('table','index') AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[name] = true
	}
	return out
}

func TestBootstrap_CreatesSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	ctx := context.Background()

	db, err := sqlite.Bootstrap(ctx, path)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer db.Close()

	got := schemaObjects(t, path)
	for _, want := range []string{"links", "idx_links_created_at", "idx_links_expires_at"} {
		if !got[want] {
			t.Errorf("expected schema object %q; got %v", want, got)
		}
	}
}

func TestBootstrap_Idempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	ctx := context.Background()

	// First bootstrap creates the schema.
	db1, err := sqlite.Bootstrap(ctx, path)
	if err != nil {
		t.Fatalf("first Bootstrap: %v", err)
	}
	if err := db1.Close(); err != nil {
		t.Fatalf("close db1: %v", err)
	}

	// Second bootstrap on the same file must succeed (CREATE ... IF NOT EXISTS).
	db2, err := sqlite.Bootstrap(ctx, path)
	if err != nil {
		t.Fatalf("second Bootstrap: %v", err)
	}
	defer db2.Close()

	got := schemaObjects(t, path)
	for _, want := range []string{"links", "idx_links_created_at", "idx_links_expires_at"} {
		if !got[want] {
			t.Errorf("after second bootstrap, expected %q; got %v", want, got)
		}
	}
}

func TestBootstrap_CreatesParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deep", "test.db")
	ctx := context.Background()

	db, err := sqlite.Bootstrap(ctx, path)
	if err != nil {
		t.Fatalf("Bootstrap with nested path: %v", err)
	}
	defer db.Close()

	got := schemaObjects(t, path)
	if !got["links"] {
		t.Errorf("expected links table in nested-path db; got %v", got)
	}
}
