// Package store is the persistence layer for shortened links. It defines a
// storage interface backed by both PostgreSQL (via pgx stdlib) and SQLite
// (via modernc.org/sqlite). All queries use parameterized arguments — user
// input is never interpolated into SQL — and the SQL-injection guard tests
// (see Phase 2.8) assert this. See development.md §3.3.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrNotFound is returned by Get when no (non-expired) link matches the shortcode.
var ErrNotFound = errors.New("store: link not found")

// Link is a stored shortened-link row.
type Link struct {
	Shortcode   string
	Destination string
	CreatedAt   time.Time
	ExpiresAt   sql.NullTime
}

// DBTX is the minimal database/exec surface the Store methods need. It is
// satisfied by both *sql.DB and *sql.Tx, so a Store can run its queries
// against a transaction (used by the cleanup worker's advisory-lock path).
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Store persists links. It wraps a DBTX (a *sql.DB or a *sql.Tx) and a
// Dialect that rewrites placeholders for the underlying driver.
type Store struct {
	core    DBTX
	db      *sql.DB // the underlying pool (nil when this Store wraps a Tx)
	dialect Dialect
}

// Dialect describes driver-specific SQL behavior.
type Dialect struct {
	// Name is "postgres" or "sqlite".
	Name string
}

// New wraps an already-open *sql.DB with the given dialect.
func New(db *sql.DB, dialect Dialect) *Store {
	return &Store{core: db, db: db, dialect: dialect}
}

// WithTx returns a new Store that runs its queries against tx (same dialect).
// The caller owns the transaction's lifecycle (Commit/Rollback). Used by the
// cleanup worker to hold a Postgres transaction-level advisory lock across
// the cleanup queries.
func (s *Store) WithTx(tx *sql.Tx) *Store {
	return &Store{core: tx, db: s.db, dialect: s.dialect}
}

// DB returns the underlying *sql.DB pool (used by tests and the worker to
// begin transactions). Returns nil when this Store wraps a Tx.
func (s *Store) DB() *sql.DB { return s.db }

// Dialect returns the store's dialect (used by tests to rewrite placeholders).
func (s *Store) Dialect() Dialect { return s.dialect }

// Dialects for the two supported backends.
var (
	DialectPostgres = Dialect{Name: "postgres"}
	DialectSQLite   = Dialect{Name: "sqlite"}
)

// Rewrite converts `?` placeholders to the driver's native form. SQLite uses
// `?` directly; Postgres (pgx) uses `$1, $2, …`.
func (d Dialect) Rewrite(q string) string {
	if d.Name == DialectSQLite.Name {
		return q
	}
	var b strings.Builder
	n := 0
	for i := 0; i < len(q); i++ {
		if q[i] == '?' {
			n++
			fmt.Fprintf(&b, "$%d", n)
		} else {
			b.WriteByte(q[i])
		}
	}
	return b.String()
}

// Create inserts a new link. created_at is set to now (UTC, RFC3339 so both
// Postgres TIMESTAMPTZ and SQLite TEXT accept it). expiresAt may be invalid
// (NULL). A unique-constraint violation is returned as a *ConstraintError so
// the caller can regenerate the shortcode on collision.
func (s *Store) Create(ctx context.Context, shortcode, destination string, expiresAt sql.NullTime) error {
	created := time.Now().UTC().Format(time.RFC3339)
	var expires any
	if expiresAt.Valid {
		expires = expiresAt.Time.UTC().Format(time.RFC3339)
	} else {
		expires = nil
	}
	_, err := s.core.ExecContext(ctx, s.dialect.Rewrite(
		`INSERT INTO links (shortcode, destination, created_at, expires_at) VALUES (?, ?, ?, ?)`),
		shortcode, destination, created, expires)
	if err != nil {
		if isUniqueViolation(err) {
			return &ConstraintError{Err: err}
		}
		return fmt.Errorf("store: create: %w", err)
	}
	return nil
}

// Get returns the link for shortcode. Expired links are treated as not found
// (ErrNotFound). Timestamps are stored as RFC3339 text and parsed here so the
// same scan path works for both Postgres and SQLite.
func (s *Store) Get(ctx context.Context, shortcode string) (Link, error) {
	var (
		dest     string
		createdS sql.NullString
		expiresS sql.NullString
	)
	err := s.core.QueryRowContext(ctx, s.dialect.Rewrite(
		`SELECT destination, created_at, expires_at FROM links WHERE shortcode = ?`),
		shortcode).Scan(&dest, &createdS, &expiresS)
	if errors.Is(err, sql.ErrNoRows) {
		return Link{}, ErrNotFound
	}
	if err != nil {
		return Link{}, fmt.Errorf("store: get: %w", err)
	}

	created, err := parseTime(createdS)
	if err != nil {
		return Link{}, fmt.Errorf("store: parse created_at: %w", err)
	}
	expires, err := parseNullTime(expiresS)
	if err != nil {
		return Link{}, fmt.Errorf("store: parse expires_at: %w", err)
	}
	// Expired rows are invisible to resolution.
	if expires.Valid && !expires.Time.IsZero() && !time.Now().Before(expires.Time) {
		return Link{}, ErrNotFound
	}
	return Link{
		Shortcode:   shortcode,
		Destination: dest,
		CreatedAt:   created,
		ExpiresAt:   expires,
	}, nil
}

// DeleteOlderThan deletes links created before cutoff. Returns the number of
// rows deleted.
func (s *Store) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.core.ExecContext(ctx, s.dialect.Rewrite(
		`DELETE FROM links WHERE created_at < ?`),
		cutoff.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, fmt.Errorf("store: delete older than: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: rows affected: %w", err)
	}
	return n, nil
}

// DeleteExpired deletes links whose expires_at has passed. Returns the number
// of rows deleted.
func (s *Store) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.core.ExecContext(ctx, s.dialect.Rewrite(
		`DELETE FROM links WHERE expires_at IS NOT NULL AND expires_at <= ?`),
		now.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, fmt.Errorf("store: delete expired: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: rows affected: %w", err)
	}
	return n, nil
}

// TableExists reports whether the links table is present (used by the
// SQL-injection guard tests to assert the schema is intact after payloads).
func (s *Store) TableExists(ctx context.Context) (bool, error) {
	var query string
	switch s.dialect.Name {
	case DialectPostgres.Name:
		query = `SELECT to_regclass('public.links')`
	case DialectSQLite.Name:
		query = `SELECT name FROM sqlite_master WHERE type='table' AND name='links'`
	default:
		return false, fmt.Errorf("store: unknown dialect %q", s.dialect.Name)
	}
	var got sql.NullString
	if err := s.core.QueryRowContext(ctx, query).Scan(&got); err != nil {
		return false, fmt.Errorf("store: table exists: %w", err)
	}
	return got.Valid && got.String != "", nil
}

// CountLinks returns the number of rows in the links table (used by the
// SQL-injection guard tests).
func (s *Store) CountLinks(ctx context.Context) (int64, error) {
	var n int64
	if err := s.core.QueryRowContext(ctx, `SELECT COUNT(*) FROM links`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count: %w", err)
	}
	return n, nil
}

// ConstraintError wraps a unique-constraint violation from Create so the caller
// can regenerate the shortcode on collision.
type ConstraintError struct{ Err error }

func (e *ConstraintError) Error() string { return e.Err.Error() }
func (e *ConstraintError) Unwrap() error { return e.Err }

// IsConstraintError reports whether err is a *ConstraintError.
func IsConstraintError(err error) bool {
	var ce *ConstraintError
	return errors.As(err, &ce)
}

func isUniqueViolation(err error) bool {
	// pgx and modernc/sqlite both surface unique violations via the stdlib
	// pq-style error message; match broadly on the SQLSTATE / message.
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "23505") || // postgres unique_violation SQLSTATE
		strings.Contains(msg, "UNIQUE constraint failed") || // sqlite
		strings.Contains(msg, "duplicate key value violates unique constraint")
}

// parseTime parses an RFC3339 (or SQLite datetime) timestamp string.
func parseTime(s sql.NullString) (time.Time, error) {
	if !s.Valid || s.String == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}
	return parseTimeLayouts(s.String)
}

// parseNullTime parses an optional timestamp string into sql.NullTime.
func parseNullTime(s sql.NullString) (sql.NullTime, error) {
	if !s.Valid || s.String == "" {
		return sql.NullTime{}, nil
	}
	t, err := parseTimeLayouts(s.String)
	if err != nil {
		return sql.NullTime{}, err
	}
	return sql.NullTime{Time: t, Valid: true}, nil
}

func parseTimeLayouts(s string) (time.Time, error) {
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05.999999999",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable timestamp %q", s)
}
