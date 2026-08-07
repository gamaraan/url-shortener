// Package sqlite bootstraps the SQLite fallback database with the latest
// schema directly. It is used only when DATABASE_URL is unset (SQLite fallback
// mode). Migrations are never run in SQLite mode — this package creates the
// schema idempotently via CREATE TABLE IF NOT EXISTS + CREATE INDEX IF NOT
// EXISTS. See development.md §3.1/§3.3.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // register the pure-Go "sqlite" driver
)

// latestSchema is applied directly on every Bootstrap call. It is idempotent.
const latestSchema = `
CREATE TABLE IF NOT EXISTS links (
    shortcode     TEXT NOT NULL PRIMARY KEY,
    destination   TEXT NOT NULL,
    created_at    TEXT NOT NULL DEFAULT (datetime('now')),
    expires_at    TEXT,
    created_by_ip TEXT
);

CREATE INDEX IF NOT EXISTS idx_links_created_at ON links (created_at);
CREATE INDEX IF NOT EXISTS idx_links_expires_at ON links (expires_at);
`

// Bootstrap opens (creating if missing) the SQLite database at path and applies
// the latest schema. It returns a *sql.DB the caller owns and must close. The
// directory of path is created with mode 0o755 if it does not exist. A nil
// context uses the background context.
func Bootstrap(ctx context.Context, path string) (*sql.DB, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("sqlite: create db dir %q: %w", dir, err)
		}
	}

	// Use a query-string DSN to enable WAL + foreign_keys + busy_timeout, and
	// to use the modernc driver's shared cache off (default). _txlock=immediate
	// avoids "database is locked" on the first write of a transaction.
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_txlock=immediate"
	// Ensure the DSN parses; if it ever fails, fall back to the bare path.
	if _, err := url.Parse(dsn); err != nil {
		dsn = path
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open %q: %w", path, err)
	}
	// Single writer is the intended SQLite usage (single backend instance).
	db.SetMaxOpenConns(1)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: ping %q: %w", path, err)
	}

	if _, err := db.ExecContext(ctx, latestSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: apply latest schema: %w", err)
	}
	return db, nil
}