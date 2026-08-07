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
- Local testing via Docker Compose: a `compose.yaml` at the repo root runs
  the full stack locally. The **default stack includes a Postgres container**
  (`postgres:16` + backend + frontend, `DATABASE_URL` wired) so migrations,
  the advisory-lock cleanup worker, and the Postgres store path are exercised
  locally; a `sqlite` opt-out profile runs backend + frontend only with
  `DATABASE_URL` unset. Local testing only — production deploys via Helm.
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
- Plan: input sanity check on `PUT /api/shorten` — `destination` must be a
  valid absolute `http`/`https` URL with a non-empty host; invalid/missing
  payloads return 400 with a human-readable `error` the frontend renders
  verbatim. `ttl_seconds`, when present, must be a non-negative integer.
- Plan: graceful shutdown for both frontend and backend — on SIGTERM stop
  accepting new requests, return 503 on the health endpoint (backend
  `/api/health`, frontend `/healthz`), and do not exit until in-flight
  requests complete or `SHUTDOWN_TIMEOUT` (default `30s`) elapses. The Helm
  chart implements this via readiness probes (503 removes the pod from
  Service/ingress endpoints) + a `preStop: sleep 10` hook +
  `terminationGracePeriodSeconds` headroom.
- Phase 2 (backend core): `internal/config` (env + duration parser +
  `RATE_LIMITS`/`POSTGRES_*` composition with fallbacks), `internal/shortcode`
  (crypto/rand base62), `internal/store` (Postgres + SQLite, parameterized
  queries), `internal/ratelimit` (per-IP token-bucket, 429 + `Retry-After`),
  `internal/api` (`PUT /api/shorten` with URL sanity check, `GET /api/resolve/`,
  `GET /api/health`), `cmd/server` HTTP server with graceful shutdown.
  Unit tests pass: config, shortcode, ratelimit, API handlers, and
  SQL-injection guard tests for both Postgres (testcontainers) and SQLite.
  Backend Docker image built and running on host port 8080 for manual testing.
- Phase 3 (cleanup worker): `internal/worker` ticker on `CLEANUP_FREQUENCY`
  that deletes over-retention and expired links; Postgres mode uses a
  .transaction-level `pg_try_advisory_xact_lock` (held across cleanup, auto-
  released on commit) so only one replica runs per tick; SQLite mode skips the
  lock. Wired into `cmd/server` (started after DB ready, canceled on shutdown).
  Unit tests pass: retention boundary, expiry deletion, parse-fallback, and
  Postgres advisory-lock contention (held xact lock forces the worker to skip).

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
