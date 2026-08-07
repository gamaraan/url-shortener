# Changelog

All notable changes to this project are recorded here. Entries follow
[Keep a Changelog](https://keepachangelog.com/) and this project adheres to
[Semantic Versioning](https://semver.org/) once releases are cut.

## [Unreleased]

### Added

- `development.md` — detailed implementation plan (architecture, component
  contracts, configuration, Helm chart, CI/CD, and ordered task list).
- `AGENTS.md` — project agent instructions wiring the global guardrails and the
  `follow-development-plan`, `pr-on-instruction-only`, and `lint-github-actions`
  project skills.
- Project skills:
  - `follow-development-plan` — keep `development.md` in sync with reality
    during implementation; facts only.
  - `pr-on-instruction-only` — push to a working branch and create a PR only
    when explicitly instructed.
  - `lint-github-actions` — lint all GitHub Actions workflows and their shell
    snippets with actionlint and shellcheck before committing/PR'd.
- Wired the global `always-add-unit-tests` skill into the project: every new
  feature and bugfix must ship with unit tests, and no `development.md` task is
  `done` until `go test ./...` passes in `backend/` and `frontend/`.
- Local testing via Docker Compose: a `compose.yaml` at the repo root runs the
  full stack locally. Default profile uses the SQLite fallback (no Postgres
  needed); a `postgres` profile adds a `postgres:16` service for testing
  Postgres mode + migrations. Local testing only — production deploys via Helm.
- Phase 1 (database & migrations): `backend/internal/migrate` (golang-migrate
  - `embed.FS`, `Run`/`Down`) for Postgres with `0001_init.up/down.sql`
  creating the `links` table + indexes; `backend/internal/sqlite` bootstrap
  (pure-Go `modernc.org/sqlite`, latest schema, idempotent) for the fallback;
  startup DB selection in `cmd/server/main.go` (Postgres migrations vs SQLite
  fallback with the temporary-storage WARN). Unit tests pass: Postgres
  migrations against a testcontainers `postgres:16-alpine` (Docker-gated) and
  SQLite bootstrap idempotency.
- Plan: per-client-IP rate limiting on all `/api/*` routes, configurable via
  `RATE_LIMITS` (default `100/1m`), 429 + `Retry-After` on exceed; parse
  failure logs an error and falls back to the default.
- Plan: SQL-injection guard unit tests for both Postgres and SQLite stores,
  asserting attacker-controlled `shortcode`/`destination` payloads are stored
  and looked up as literal data (no schema alteration, no lookup bypass).

### Changed

- PostgreSQL is now an **external service** and is never deployed by the Helm
  chart. The chart gates Postgres on `postgres.enabled` (default `true`): when
  enabled it creates a `Secret` and injects `DATABASE_URL` (composed from
  `POSTGRES_HOST`/`POSTGRES_PORT`/`POSTGRES_DB`/`POSTGRES_USER`/`POSTGRES_PASSWORD`,
  or taken verbatim from `DATABASE_URL`) into the backend; the credential
  values are loaded from **GitHub repository secrets** at deploy time and are
  never committed to `values.yaml`. When `postgres.enabled` is false the
  backend falls back to a local SQLite file (`SQLITE_PATH`, default
  `/data/url-shortener.db`), logs a startup WARN that temporary storage is in
  use and data will be lost on restart, and must not run more than one backend
  instance. In SQLite mode the backend never runs migrations and creates the
  latest schema directly; in Postgres mode golang-migrate runs on startup.
