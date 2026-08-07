// Package migrate runs versioned SQL migrations against PostgreSQL.
//
// Migrations are embedded from backend/migrations via embed.FS and applied
// with golang-migrate. This package is Postgres-only; the SQLite fallback
// (see backend/internal/sqlite) bootstraps the latest schema directly and
// never runs migrations. See development.md §3.1/§3.3.
package migrate

import (
	"context"
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // register driver
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migrationSubdir is the embedded directory holding the .sql files.
const migrationSubdir = "migrations"

// Run applies all pending up-migrations to the PostgreSQL database at dbURL.
// It returns nil if the database is already up to date. A nil context uses
// the background context. Migration failure is fatal to the caller (the
// backend process exits non-zero on startup — see development.md §3.1).
func Run(ctx context.Context, dbURL string) error {
	return run(ctx, dbURL, func(m *migrate.Migrate) error {
		if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			return err
		}
		return nil
	})
}

// Down rolls back all migrations (drops the schema). It returns nil if the
// database is already empty. A nil context uses the background context.
func Down(ctx context.Context, dbURL string) error {
	return run(ctx, dbURL, func(m *migrate.Migrate) error {
		if err := m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			return err
		}
		return nil
	})
}

func run(ctx context.Context, dbURL string, op func(*migrate.Migrate) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	_ = ctx // golang-migrate does not accept a context; left for API stability.

	src, err := iofs.New(&migrationFS, migrationSubdir)
	if err != nil {
		return fmt.Errorf("migrate: open embedded source: %w", err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", src, dbURL)
	if err != nil {
		return fmt.Errorf("migrate: init: %w", err)
	}
	defer func() {
		_, _ = m.Close()
	}()

	if err := op(m); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}