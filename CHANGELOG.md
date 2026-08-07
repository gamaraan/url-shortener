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
