-- 0001_init.down.sql — reverse of 0001_init.up.sql.

DROP INDEX IF EXISTS idx_links_expires_at;
DROP INDEX IF EXISTS idx_links_created_at;
DROP TABLE IF EXISTS links;