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
- Phase 4 (frontend): Svelte + Vite SPA (dark/orange GitHub-dark theme) with
  shorten + copy UI calling `PUT /api/shorten` (same-origin, proxied) and
  rendering `${origin}/${shortcode}`; `frontend/internal/server` serves the
  embedded SPA, reverse-proxies `/api/*` to `BACKEND_URL`, renders the themed
  1-second meta-refresh redirect page for `GET /:shortcode`, a themed 404, and
  `/healthz` (503 while draining); `frontend/internal/config` parses
  `LISTEN_ADDR`/`BACKEND_URL`/`SHUTDOWN_TIMEOUT`/`LOG_LEVEL`; `cmd/server`
  wires graceful shutdown (set draining, `http.Server.Shutdown` with
  `SHUTDOWN_TIMEOUT`). Vite builds into `frontend/internal/server/dist`
  (colocated with the server package for `//go:embed`). Unit tests pass:
  redirect page (1s meta-refresh + destination), 404, proxy passthrough,
  asset serving, `/healthz` 503 while draining, and config.
- Phase 5 (Dockerfiles + compose): `frontend/Dockerfile` (node build → Go
  embed → alpine runtime); `compose.yaml` + `compose/README.md` with a
  **default Postgres stack** (`postgres:16` + backend + frontend, `DATABASE_URL`
  wired, `depends_on` healthcheck) and a `sqlite` opt-out profile. Backend
  port 8080 and frontend port 8081 exposed for direct testing. Full stack
  brought up and verified end-to-end (health, SPA UI, shorten via proxy,
  themed 1s redirect page, 404, invalid-URL 400).
- Fixed: blank frontend page — the SPA used the legacy `new App({ target })`
  constructor, which does not mount under Svelte 5. Switched to the canonical
  Svelte 5 `mount(App, { target })` API. Added a Vitest + jsdom +
  `@testing-library/svelte` regression test (`src/main.test.ts`) that
  exercises the bootstrap and asserts `#app` is populated; proven to fail
  with the old API and pass with the fix. Added `src/App.test.ts` component
  tests (heading, button, input). `npm test` green (4 tests).
- Added project skill `regression-test-per-bugfix`: every bugfix must ship
  with a regression test that fails without the fix and passes with it
  (exercises the broken code path; fix reverted to prove it catches the bug).
  Wired into `AGENTS.md` and `development.md` §7/§7.1 alongside the global
  `always-add-unit-tests` skill.
- Duplicate-shortcode protection (made explicit in plan + covered by API tests):
  uniqueness is guaranteed by the `links.shortcode` PRIMARY KEY
  (DB-enforced in both Postgres and SQLite); `store.Create` wraps a
  unique-constraint violation as `*ConstraintError` (Postgres SQLSTATE `23505` /
  SQLite `UNIQUE constraint failed`); the API regenerates and retries up to 5
  attempts, then returns **409 Conflict** if exhausted. Added API tests
  `TestShorten_RetriesOnCollision` (retry-then-succeed) and
  `TestShorten_CollisionExhausted` (409 after 5 attempts) via fake store/generator
  (introduced `Storer`/`Generator` interfaces in `internal/api`).
- Request logging on both frontend and backend: every HTTP request is logged
  at INFO with `method`, `path`, `status`, `bytes`, `duration_ms`,
  `remote_addr` (structured `slog`) via a `statusRecorder` middleware wrapping
  the whole handler tree (so 200/400/404/409/429/502/503 are all logged). Unit
  tests `TestAccessLog_LogsEveryRequest` in both `internal/api` and
  `internal/server`.
- Phase 6 (Helm chart): templates for namespace, backend and frontend
  Deployments+Services+env, Ingress (nginx `/api/*` → backend, rest → frontend,
  cert-manager TLS), a Postgres `Secret` + `DATABASE_URL` injection gated on
  `postgres.enabled` (default true; replicas forced to 1 when false for SQLite
  fallback), and an optional backend PVC for SQLite. Probes & lifecycle on both
  Deployments: liveness + readiness on the health endpoints, `preStop: sleep 10`,
  `terminationGracePeriodSeconds: 45`. `values.yaml` gained `shutdownTimeout`,
  `preStopSleep`, `terminationGracePeriodSeconds`, and `rateLimits`. `helm lint`
  passes; `helm template` renders valid YAML for both `postgres.enabled` true
  and false. Pi-lens YAML findings on the templates are false positives (raw
  Helm `{{- }}` directives parsed as YAML) and suppressed with
  `# pi-lens-ignore: YAML:0`.
- Fixed: Helm deploy failed with "namespaces 'tinyurl' already exists" — the
  chart's `namespace.yaml` template conflicted with `helm --create-namespace`.
  Removed the `Namespace` template; the namespace is created by
  `--create-namespace` and all resources use `namespace: {{ .Release.Namespace }}`.
- Phase 7 (GitHub Actions): two workflows under `.github/workflows/`.
  `unit-tests.yml` runs `go vet`+`go test` (backend, frontend) and `npm ci`+
  `npm test` (frontend/web) on push to non-`main` branches and PRs (no
  build/deploy, concurrency-cancels in-progress runs). `build-deploy.yml` runs
  on push to `main` (+ dispatch): a mandatory unit-test gate, then per-service
  GHCR build+push with the `yyyy.mm.dd-xxxxxxxx` tag (from the latest commit
  touching that folder), a Python-based `values.yaml` tag update committed back
  to `main`, and `helm upgrade --install` into `tinyurl` using the `KUBECONFIG`
  secret + the Postgres repository secrets via `--set`/`--set-string`. A
  chart-only change skips builds and runs only `helm upgrade`. Both workflows
  pass `actionlint` + `shellcheck` clean (zero errors) per the
  `lint-github-actions` skill; zizmor security advisories (unpinned action
  SHAs, broad token permissions) are noted as deferred hardening.

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
