-- 0001_init.up.sql — initial schema for PostgreSQL.
-- See development.md §3.3. In SQLite fallback mode this file is NOT used;
-- backend/internal/sqlite bootstraps the latest schema directly.

CREATE TABLE IF NOT EXISTS links (
    shortcode     TEXT        PRIMARY KEY,
    destination   TEXT        NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at    TIMESTAMPTZ NULL,
    created_by_ip TEXT        NULL
);

-- Cleanup scan index (retention deletion by created_at).
CREATE INDEX IF NOT EXISTS idx_links_created_at ON links (created_at);

-- Partial index for expiry-based cleanup; only rows with an explicit expiry.
CREATE INDEX IF NOT EXISTS idx_links_expires_at
    ON links (expires_at)
    WHERE expires_at IS NOT NULL;