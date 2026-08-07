// Package worker runs the background retention/expiry cleanup goroutine. On
// each tick it deletes links older than RETENTION_PERIOD and links whose
// expires_at has passed. In Postgres mode it uses a Postgres transaction-level
// advisory lock (pg_try_advisory_xact_lock) so only one backend replica runs
// cleanup per tick in a multi-replica deployment; the lock auto-releases on
// commit/rollback. In SQLite mode the lock is skipped (single-instance is
// already mandated). Ticks do not overlap. See development.md §3.1.
package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/gamaraan/url-shortener/backend/internal/store"
)

// AdvisoryLockKey is the constant key used with pg_try_advisory_xact_lock so
// that only one backend replica runs cleanup per tick in a multi-replica
// Postgres deployment. It is an 8-byte value ("url-shor") to fit the bigint
// argument.
const AdvisoryLockKey = int64(0x75726c2d73686f72) // "url-shor"

// Worker periodically deletes expired and over-retention links.
type Worker struct {
	store     *store.Store
	db        *sql.DB // used to begin the advisory-lock transaction (Postgres)
	dialect   store.Dialect
	retention time.Duration
	frequency time.Duration
	logger    *slog.Logger
}

// New builds a Worker. db is used to begin the Postgres advisory-lock
// transaction; in SQLite mode it is only used for cleanup via the store.
func New(st *store.Store, db *sql.DB, dialect store.Dialect, retention, frequency time.Duration, logger *slog.Logger) *Worker {
	return &Worker{
		store:     st,
		db:        db,
		dialect:   dialect,
		retention: retention,
		frequency: frequency,
		logger:    logger,
	}
}

// Run starts the ticker loop and blocks until ctx is canceled. The first tick
// fires after `frequency` (standard ticker behavior); callers wanting an
// immediate sweep should call RunOnce first.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.frequency)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.RunOnce(ctx)
		}
	}
}

// RunOnce performs a single cleanup sweep. It is exported so tests and an
// optional startup sweep can invoke it directly.
func (w *Worker) RunOnce(ctx context.Context) {
	if w.dialect.Name == store.DialectPostgres.Name {
		w.runOncePostgres(ctx)
		return
	}
	w.cleanup(ctx, w.store)
}

// runOncePostgres wraps the cleanup in a transaction that first acquires a
// transaction-level advisory lock. If another replica holds the lock, the
// transaction is rolled back and the sweep is skipped.
func (w *Worker) runOncePostgres(ctx context.Context) {
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		w.logger.Error("worker: begin tx failed", "error", err)
		return
	}
	defer func() { _ = tx.Rollback() }() // no-op if committed

	var locked bool
	if err := tx.QueryRowContext(ctx, "SELECT pg_try_advisory_xact_lock($1)", AdvisoryLockKey).Scan(&locked); err != nil {
		w.logger.Error("worker: advisory lock query failed", "error", err)
		return
	}
	if !locked {
		w.logger.Debug("worker: another replica holds the advisory lock; skipping")
		return
	}

	w.cleanup(ctx, w.store.WithTx(tx))

	if err := tx.Commit(); err != nil {
		w.logger.Error("worker: commit cleanup tx failed", "error", err)
	}
}

// cleanup runs the retention + expiry deletes against st (which may be the
// base store or a transaction-scoped store) and logs the counts.
func (w *Worker) cleanup(ctx context.Context, st *store.Store) {
	now := time.Now()
	cutoff := now.Add(-w.retention)

	deletedOld, err := st.DeleteOlderThan(ctx, cutoff)
	if err != nil {
		w.logger.Error("worker: delete older than retention failed", "error", err)
	} else if deletedOld > 0 {
		w.logger.Info("worker: deleted over-retention links", "count", deletedOld, "retention", w.retention)
	}

	deletedExp, err := st.DeleteExpired(ctx, now)
	if err != nil {
		w.logger.Error("worker: delete expired failed", "error", err)
	} else if deletedExp > 0 {
		w.logger.Info("worker: deleted expired links", "count", deletedExp)
	}
}

// ErrLockHeld is returned by tryLockForTest when another session holds the
// advisory lock. Exposed for tests that simulate contention.
var ErrLockHeld = errors.New("worker: advisory lock held by another session")

// keep fmt referenced for future formatting helpers.
var _ = fmt.Sprintf