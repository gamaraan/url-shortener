# URL Shortener — Development Plan

This document is the single source of truth for the project's implementation.
It contains only facts: the architecture, the component contracts, and the
ordered task list. Implementation progress is tracked by marking tasks
`[x]` / `[~]` / `[ ]` (done / in-progress / pending) and by updating this file
in place as decisions change. See the `follow-development-plan` and
`pr-on-instruction-only` skills for how this file is maintained.

## 1. Overview

A URL shortener composed of two services plus an external database:

- **backend/** — a Go service exposing the shorten/resolve JSON API and running
  a background cleanup worker. API-only; returns JSON, never HTML.
- **frontend/** — a Go service that embeds a Svelte SPA (built by Vite) via
  `embed.FS`, serves the SPA UI, renders the themed 1-second redirect page, and
  reverse-proxies `/api/*` calls to the backend. The "frontend is in Go" because
  the server is Go; the UI is Svelte, compiled to static assets and embedded in
  the Go binary at build time.
- **Database** — PostgreSQL is an **external service** and is never deployed by
  this repository's Helm chart. When `DATABASE_URL` is configured the backend
  connects to that external Postgres. When `DATABASE_URL` is **not** configured
  the backend falls back to a **local SQLite file** in the backend container's
  own filesystem (see §3.3 for the warnings and behavior).

There are **two folders and two images** (`backend`, `frontend`) so the CI can
build and deploy only the service that changed. The cleanup worker is a
background goroutine inside the backend service (not a separate folder/image).

## 2. Repository layout

```
.
├── development.md           # this file — the plan of record
├── AGENTS.md                # project guardrails + skill wiring
├── CHANGELOG.md             # appended on each PR
├── requirements.md          # original product requirements
├── README.md                # operator + developer documentation
├── backend/                 # Go service: API + cleanup worker + migrations
│   ├── go.mod
│   ├── cmd/server/main.go
│   ├── internal/
│   │   ├── api/              # HTTP handlers (shorten, resolve)
│   │   ├── store/            # storage interface + pgx (Postgres) and sqlite drivers
│   │   ├── worker/           # retention cleanup goroutine
│   │   ├── migrate/          # embed.FS + golang-migrate runner (Postgres only)
│   │   ├── sqlite/          # latest-schema bootstrap for the SQLite fallback
│   │   ├── config/           # env parsing + duration parser
│   │   └── shortcode/       # nanoid base62 generator + collision check
│   ├── migrations/           # *.up.sql / *.down.sql (embedded; Postgres only)
│   ├── Dockerfile
│   └── *_test.go
├── frontend/                # Go server embedding Svelte SPA
│   ├── go.mod
│   ├── cmd/server/main.go
│   ├── internal/
│   │   ├── server/           # serves embed.FS assets, proxy /api/*, redirect page
│   │   └── config/           # env parsing
│   ├── web/                  # Svelte + Vite source
│   │   ├── src/
│   │   ├── package.json
│   │   ├── vite.config.ts
│   │   └── tsconfig.json
│   ├── embed.go             # //go:embed dist
│   ├── Dockerfile            # multi-stage: node build → go embed → final
│   └── *_test.go
└── charts/url-shortener/    # Helm chart
    ├── Chart.yaml
    ├── values.yaml          # committed; image tags updated by CI
    └── templates/
```

## 3. Component contracts

### 3.1 Backend (`backend/`)

- **Listen address**: `LISTEN_ADDR` (default `:8080`).
- **API** (all under `/api`, JSON in/out):
  - `PUT /api/shorten` — body `{"destination":"…","ttl_seconds":<int, optional>}`.
    Returns `{"short_url":"https://<host>/<shortcode>","shortcode":"…","destination":"…"}`.
    `host` is taken from the `X-Forwarded-Host` / `Host` request header so the
    returned short URL is correct behind the ingress. Validates the destination
    is an absolute URL with scheme `http`/`https`.
  - `GET /api/resolve/:shortcode` — returns
    `{"shortcode":"…","destination":"…"}` with HTTP 200, or 404 (JSON error)
    when not found / expired. Does **not** record the hit here; the hit counter
    (if implemented) is a separate concern. Resolution returns the destination
    so the frontend can render the redirect page.
  - Errors are always JSON: `{"error":"…"}` with the appropriate HTTP status.
- **Shortcode generation**: random base62 nanoid, length from
  `SHORTCODE_LENGTH` (default 7). On insert collision (unique constraint
  violation) regenerate up to N times, then fail with 500/conflict. Supports an
  optional per-link `expires_at` derived from `ttl_seconds` when provided.
- **Database selection on startup**:
  - If `DATABASE_URL` is set → **Postgres mode**. Run golang-migrate `up` with
    the SQL files embedded via `embed.FS` before the HTTP server starts; on a
    fresh DB this creates the schema. Migration failure is fatal (process
    exits non-zero).
  - If `DATABASE_URL` is **not** set → **SQLite fallback mode**. **Never run
    migrations.** Always create the SQLite database file with the **latest
    schema** directly (idempotent `CREATE TABLE IF NOT EXISTS` + indexes), at
    `SQLITE_PATH` (default `/data/url-shortener.db`). At startup log a **WARN**:
    no `DATABASE_URL` configured, temporary SQLite storage is in use, **data
    will be lost on restart** (unless `SQLITE_PATH` is backed by a persistent
    volume), and **do not start more than one backend instance** because there
    is no shared database. SQLite mode is intended for local/evaluation use
    only.
- **Cleanup worker**: a goroutine started after the database is ready. On each
  tick it deletes links whose `created_at` is older than the retention period
  **and** links whose `expires_at` has passed. In Postgres mode it uses a
  Postgres advisory lock (`pg_try_advisory_lock` on a constant key) so only one
  backend replica runs cleanup per tick in a multi-replica deployment. In
  SQLite mode the advisory lock is skipped (single-instance is already
  mandated). Logs each run's deleted count. Ticks are bounded and the next tick
  waits for the previous to finish (does not overlap). Logs (does not panic on)
  per-run errors.
- **Duration parsing** (shared module, used by retention + cleanup frequency):
  accepts integers with unit suffixes: `300s`, `60m`, `1h`, `2d`, `1w`, `1mo`,
  `1y`. `m` = minutes, `mo` = months. On parse failure it logs the error and
  falls back to the documented default for that variable.

### 3.2 Frontend (`frontend/`)

- **Listen address**: `LISTEN_ADDR` (default `:8080`).
- **Backend URL**: `BACKEND_URL` (default `http://backend:8080`) — used for the
  reverse proxy.
- **Serving**:
  - `GET /` and any non-`/api`, non-asset path that is not a known route → the
    SPA `index.html`.
  - `GET /assets/*` and other build artifacts → embedded static assets with
    long-cache headers.
  - `GET /api/*` → reverse proxy to `BACKEND_URL/api/*` (in-cluster). Errors
    from the backend are forwarded as-is.
  - `GET /:shortcode` → calls backend `GET /api/resolve/:shortcode`. On success
    it renders a server-side themed HTML page showing the destination and
    auto-redirecting via `<meta http-equiv="refresh" content="1; url=…">` plus a
    visible 1-second "Redirecting to …" message and a manual link. On 404 it
    renders a themed 404 page. The brief 1-second redirect is a hard
    requirement; the meta-refresh value is `1`.
- **Short URL construction**: the SPA calls `PUT /api/shorten` (proxied) and
  builds the displayed short URL from the current `window.location.origin` plus
  the returned `shortcode`. The backend's `short_url` field is informational;
  the SPA uses the host it was loaded from.
- **Theme**: dark, GitHub-dark-inspired, orange accents. Colors:
  - Background `#0d1117`, surface `#161b22`, border `#30363d`.
  - Text `#e6edf3`, muted `#7d8590`.
  - Accent orange `#f0883e` (hover `#ff9b57`), used for the button and links.
- **UI**: single page with a heading, a text input for the URL, a "Shorten"
  button, and a results area showing the generated short URL with a copy
  button. Responsive, minimal. No auth.

### 3.3 Database

- **PostgreSQL** is an external service, never deployed by this repo's Helm
  chart. Connection via `DATABASE_URL` (libpq format), or via the individual
  `POSTGRES_HOST`/`POSTGRES_PORT`/`POSTGRES_DB`/`POSTGRES_USER`/`POSTGRES_PASSWORD`
  variables which the backend composes into a `DATABASE_URL`. The chart gates
  Postgres on `postgres.enabled` (default `true`): when enabled it injects the
  credentials (sourced from GitHub repository secrets at deploy time) into the
  backend; when disabled the backend falls back to SQLite. When set, the
  backend runs golang-migrate on startup (§3.1).
- **SQLite fallback**: when `DATABASE_URL` is not set, the backend uses a local
  SQLite file at `SQLITE_PATH` (default `/data/url-shortener.db`). In SQLite
  mode the backend **never runs migrations** and instead creates the database
  with the latest schema directly (`CREATE TABLE IF NOT EXISTS` + indexes). At
  startup it logs a WARN that temporary storage is in use, data will be lost on
  restart, and that more than one backend instance must not be started.
- **Schema** (same shape for both backends; types map to the closest SQLite
  equivalents — `TEXT` for timestamps, `datetime('now')` default in SQLite):
  - `links`:
    - `shortcode TEXT PRIMARY KEY`
    - `destination TEXT NOT NULL`
    - `created_at` — `TIMESTAMPTZ NOT NULL DEFAULT now()` (Postgres) /
      `TEXT NOT NULL DEFAULT (datetime('now'))` (SQLite)
    - `expires_at` — `TIMESTAMPTZ NULL` (Postgres) / `TEXT NULL` (SQLite); NULL
      means governed only by global retention
    - `created_by_ip TEXT NULL` (optional audit)
  - Indexes: `idx_links_created_at` on `created_at` (cleanup scan);
    `idx_links_expires_at` partial index on `expires_at WHERE expires_at IS NOT NULL`
    (Postgres). In SQLite this is a plain index on `expires_at` (SQLite has no
    partial-index-with-where in the bootstrap path; a normal index is created).
- In Postgres mode, all schema changes are versioned SQL migration files under
  `backend/migrations/`, embedded and applied on backend startup. In SQLite
  mode, the latest schema is created directly from a Go bootstrap function in
  `backend/internal/sqlite` (no migration files are read).

### 3.4 Configuration (environment variables)

Backend:

| Variable | Default | Notes |
| --- | --- | --- |
| `DATABASE_URL` | — | Optional. libpq format. When set → Postgres mode. When unset → SQLite fallback. |
| `POSTGRES_HOST` | — | Optional. Used with the other `POSTGRES_*` vars to compose a `DATABASE_URL` when `DATABASE_URL` is not set. |
| `POSTGRES_PORT` | `5432` | Optional. Postgres port. |
| `POSTGRES_DB` | `urlshortener` | Optional. Postgres database name. |
| `POSTGRES_USER` | — | Optional. Postgres username. |
| `POSTGRES_PASSWORD` | — | Optional. Postgres password. |
| `SQLITE_PATH` | `/data/url-shortener.db` | SQLite file path used only in SQLite fallback mode. |
| `LISTEN_ADDR` | `:8080` | HTTP listen address. |
| `RETENTION_PERIOD` | `3650d` | Global retention; links older than this are deleted. Duration string. |
| `CLEANUP_FREQUENCY` | `60m` | Cleanup worker tick interval. Duration string. |
| `SHORTCODE_LENGTH` | `7` | nanoid length. |
| `LOG_LEVEL` | `info` | `debug`/`info`/`warn`/`error`. |

Frontend:

| Variable | Default | Notes |
| --- | --- | --- |
| `LISTEN_ADDR` | `:8080` | HTTP listen address. |
| `BACKEND_URL` | `http://backend:8080` | Backend base URL for the reverse proxy. |

Duration strings use the shared parser (§3.1). Invalid values log an error and
fall back to the default.

## 4. Helm chart (`charts/url-shortener/`)

- Targets a **k0s single-node cluster with nginx ingress** and
  **cert-manager + a Let's Encrypt ClusterIssuer** already installed.
- `values.yaml` is committed and is the file the CI updates with image tags
  before deploying.
- Deployments (not separate Releases): two `Deployment` objects —
  `backend` and `frontend` — each with its own image tag and env config.
  **PostgreSQL is never deployed by this chart** — it is an external service.
  Postgres is gated by `postgres.enabled` (default `true`):
  - **`postgres.enabled: true`** — the chart creates a `Secret` containing the
    Postgres credentials and injects `DATABASE_URL` (composed from
    `POSTGRES_HOST`/`POSTGRES_PORT`/`POSTGRES_DB`/`POSTGRES_USER`/`POSTGRES_PASSWORD`,
    or taken verbatim from `DATABASE_URL`) into the backend Deployment. The
    credential values are supplied at deploy time from **GitHub repository
    secrets** (see §6) via `--set`/`--set-string` or a generated values overlay;
    they are **never committed** to `values.yaml`. The backend runs in Postgres
    mode and runs migrations on startup.
  - **`postgres.enabled: false`** — no Postgres Secret is created and no
    `DATABASE_URL`/`POSTGRES_*` env is injected; the backend falls back to
    SQLite. The chart forces `backend.replicas: 1` in this mode.
  A `PersistentVolumeClaim` for the backend is optional and only useful when
  running in SQLite fallback mode (to persist `SQLITE_PATH`); by default no PVC
  is created and SQLite data is ephemeral.
- Services: `backend` (port 8080), `frontend` (port 8080).
- **Ingress** (single host): path rules `/api/*` → backend, everything else →
  frontend. TLS via cert-manager annotation
  `cert-manager.io/cluster-issuer: letsencrypt-prod` and a `tls` entry per host.
- Probes: backend `/api/health` (added to the API), frontend `/healthz`
  (returns 200 from the embedded server).
- Resources, replicas, and image pull policy are configurable in
  `values.yaml`; defaults are small/sane for a single node.
- Image tag fields in `values.yaml`:
  - `backend.image.repository` / `backend.image.tag`
  - `frontend.image.repository` / `frontend.image.tag`
- `backend.replicas` defaults to `1`. When `postgres.enabled: false` (SQLite
  mode) the chart must enforce `replicas: 1` and log the single-instance
  warning; the backend also enforces this at startup.

## 5. Image tagging

- Tag format: `yyyy.mm.dd-xxxxxxxx` — calendar date (commit date) plus the
  8-digit short commit SHA.
- The date and SHA are taken from **the latest commit that touched the
  specific folder** (`backend/` or `frontend/`), not the repo head. So a
  change to only the Helm chart does not bump either service's tag.
  - Computed via:
    `git log -1 --format=%cd --date=format:'%Y.%m.%d' --date=format-short -- <folder>`
    for the date, and
    `git rev-parse --short=8 $(git rev-list -1 -- <folder>)`
    for the 8-char SHA.
- The CI writes the resulting tag into `charts/url-shortener/values.yaml`
  (the `tag` field for the service(s) that changed) and commits that update
  back to the branch (or applies it via `--set` at deploy time) so
  `values.yaml` always reflects what is deployed.

## 6. CI/CD (GitHub Actions)

Workflow file: `.github/workflows/build-deploy.yml`. Triggers on push to
`main` and on manual dispatch.

Steps:

1. **Detect changes** (dorny/paths-filter or `git diff` against the previous
   commit): determine whether `backend/**` and/or `frontend/**` changed. If
   neither changed, skip build/deploy for both.
2. **Per-service build & push** (only for changed folders):
   - Log in to GHCR using `GITHUB_TOKEN` (or a PAT secret).
   - Compute the folder-specific date + 8-char SHA tag (§5).
   - `docker build` the folder's image and push it to
     `ghcr.io/<owner>/<repo>/<service>:<tag>`.
3. **Update `values.yaml`**: rewrite the `tag` for each changed service in
     `charts/url-shortener/values.yaml` and commit the file back to the
     branch (or pass via `--set` at deploy time). The committed `values.yaml`
     is the source of truth for deployed versions.
4. **Deploy with Helm**: configure kubeconfig from the `KUBECONFIG` secret
     (base64), then
     `helm upgrade --install url-shortener ./charts/url-shortener \
       --namespace url-shortener --create-namespace \
       -f ./charts/url-shortener/values.yaml`.
     When `postgres.enabled` is true, pass the Postgres credentials from the
     GitHub repository secrets (§6 secrets table) via `--set`/`--set-string`
     (or a generated, never-committed values overlay) so they are injected into
     the chart's Postgres `Secret` and from there into the backend. When
     `postgres.enabled` is false, do not pass any Postgres credentials.
5. The workflow runs on `ubuntu-latest` and uses the GitHub-provided
   `GITHUB_TOKEN` for GHCR. Helm is preinstalled on the runner; kubectl is
   configured from the secret.

### Required GitHub secrets

| Secret | Purpose |
| --- | --- |
| `KUBECONFIG` | Base64 kubeconfig for the k0s cluster, used by `helm`. |
| `GHCR_TOKEN` (or `GITHUB_TOKEN`) | Push images to GHCR. The default `GITHUB_TOKEN` is used when sufficient; a PAT secret is used when cross-repo or longer-lived access is needed. |
| `POSTGRES_HOST` | External Postgres host. Used only when `postgres.enabled` is true. |
| `POSTGRES_PORT` | External Postgres port (default `5432`). Used only when `postgres.enabled` is true. |
| `POSTGRES_DB` | External Postgres database name (default `urlshortener`). Used only when `postgres.enabled` is true. |
| `POSTGRES_USER` | External Postgres username. Used only when `postgres.enabled` is true. |
| `POSTGRES_PASSWORD` | External Postgres password. Used only when `postgres.enabled` is true. |
| `DATABASE_URL` | Optional full libpq URL; overrides the composed `POSTGRES_*` form when provided. Used only when `postgres.enabled` is true. |

## 7. Skill and guardrail wiring

- `AGENTS.md` (project root) restates the global guardrails and wires the
  project skills: `follow-development-plan`, `pr-on-instruction-only`,
  `lint-github-actions`, and `always-add-unit-tests`.
- `CHANGELOG.md` records, per PR, what changed (Added/Changed/Fixed/Removed
  sections under an `Unreleased` heading until a release is cut).
- GitHub Actions workflows are always linted with **shellcheck** and
  **actionlint** before being committed/PR'd (see the `lint-github-actions`
  skill).

### 7.1 Testing policy (mandatory)

- **Every new feature and every bugfix must ship with unit tests.** This is
  enforced by the global `always-add-unit-tests` skill and is a hard
  requirement for this project. A task is not `done` (`[x]`) until its unit
  tests are written and passing.
- Per-phase test tasks already enumerate the minimum coverage (1.5, 2.6, 3.2,
  4.6). Any additional feature/bugfix introduced during implementation must add
  its own unit tests even if no explicit test task is listed for it.
- Run the full Go test suite (`go test ./...` in `backend/` and `frontend/`)
  before considering any task `done`; failing tests block the task.
- Do not mark a task `[x]` while tests are failing or missing — leave it `[~]`
  and track the failing/missing tests as a blocker.

## 8. Task list

Tasks are grouped into phases. Each task is a single, verifiable unit of work.
Mark progress inline: `[ ]` pending, `[~]` in progress, `[x]` done. When a task
is completed or a decision changes, update this file to reflect reality (per
the `follow-development-plan` skill) — facts only, no narrative.

### Phase 0 — Scaffolding

- [x] 0.1 Create the `backend/`, `frontend/`, and `charts/url-shortener/`
      directories with `go.mod`/`package.json` stubs and an initial module
      path.
- [x] 0.2 Create `.gitignore` (Go binaries, `frontend/web/node_modules`,
      `frontend/web/dist`, `dist/`).
- [x] 0.3 Set up git identity per the `git-identity` skill
      (`gamaraan` / `gabi.hulea@gmail.com`).

### Phase 1 — Database & migrations

- [ ] 1.1 Write `backend/migrations/0001_init.up.sql` / `…down.sql` creating
      the `links` table, indexes, and the partial `expires_at` index for
      Postgres (§3.3).
- [ ] 1.2 Implement `backend/internal/migrate` using golang-migrate with the
      migrations embedded via `embed.FS`; expose `Run(ctx, dbURL) error`.
      Postgres-only.
- [ ] 1.3 Implement `backend/internal/sqlite`: bootstrap the SQLite file at
      `SQLITE_PATH` with the latest schema directly (`CREATE TABLE IF NOT
      EXISTS` + indexes); no migration files are read. Idempotent.
- [ ] 1.4 Implement the startup database selection in `cmd/server/main.go`:
      if `DATABASE_URL` set → Postgres mode (run migrations); else → SQLite
      fallback (bootstrap latest schema) and log the WARN that temporary
      storage is in use, data will be lost on restart, and only one backend
      instance may run.
- [ ] 1.5 Unit tests: Postgres migrations against an ephemeral Postgres
      (testcontainers or CI service) assert schema exists and down+up is
      idempotent; SQLite bootstrap is idempotent and creates the expected
      schema.

### Phase 2 — Backend core (store, shortcode, API)

- [ ] 2.1 Implement `backend/internal/config`: parse `DATABASE_URL`,
      `POSTGRES_HOST`/`POSTGRES_PORT`/`POSTGRES_DB`/`POSTGRES_USER`/`POSTGRES_PASSWORD`,
      `SQLITE_PATH`, `LISTEN_ADDR`, `SHORTCODE_LENGTH`, `LOG_LEVEL`,
      `RETENTION_PERIOD`, `CLEANUP_FREQUENCY`; compose `DATABASE_URL` from the
      `POSTGRES_*` vars when `DATABASE_URL` is unset; central duration parser
      (§3.1) with fallback + error logging.
- [ ] 2.2 Implement `backend/internal/shortcode`: nanoid base62 generator
      keyed on `SHORTCODE_LENGTH`; helper to regenerate on collision.
- [ ] 2.3 Implement `backend/internal/store`: a storage interface with a
      `pgx` (Postgres) implementation and a SQLite implementation;
      `Create(shortcode, destination, expiresAt)`, `Get(shortcode)`,
      `DeleteOlderThan(retention)`, `DeleteExpired()`.
- [ ] 2.4 Implement `backend/internal/api`:
      `PUT /api/shorten`, `GET /api/resolve/:shortcode`, `GET /api/health`;
      JSON error contract; destination validation; `ttl_seconds` →
      `expires_at`; build `short_url` from request host.
- [ ] 2.5 `cmd/server/main.go`: load config → run migrations → start
      worker → start HTTP server (graceful shutdown on SIGTERM).
- [ ] 2.6 Unit tests: shortcode uniqueness/collision, store CRUD against
      ephemeral Postgres, API handlers (httptest) for success/404/validation
      paths.

### Phase 3 — Cleanup worker

- [ ] 3.1 Implement `backend/internal/worker`: ticker on
      `CLEANUP_FREQUENCY`; per tick, in Postgres mode acquire
      `pg_try_advisory_lock` (skip in SQLite mode), run
      `DeleteOlderThan(now - RETENTION_PERIOD)` and `DeleteExpired()`, log
      counts, release lock (Postgres only); non-overlapping ticks.
- [ ] 3.2 Unit tests: lock contention (only one worker deletes) in Postgres
      mode, retention boundary, expiry deletion, parse-fallback behavior.

### Phase 4 — Frontend (Svelte SPA + Go server)

- [ ] 4.1 Scaffold `frontend/web` with Svelte + Vite (TS), themed UI (§3.2):
      input, shorten button, result + copy button, dark/orange palette.
- [ ] 4.2 SPA calls `PUT /api/shorten` (same origin, proxied) and renders
      `${window.location.origin}/${shortcode}`.
- [ ] 4.3 Implement `frontend/internal/server`: serve embedded SPA assets,
      reverse proxy `/api/*` to `BACKEND_URL`, `GET /:shortcode` →
      resolve + themed 1-second meta-refresh redirect page, themed 404,
      `/healthz`.
- [ ] 4.4 `frontend/embed.go` with `//go:embed dist` and a build step that
      runs `npm run build` into `frontend/dist`.
- [ ] 4.5 `cmd/server/main.go`: config → HTTP server → graceful shutdown.
- [ ] 4.6 Unit tests: redirect-page HTML contains the destination and
      `content="1"`, 404 path, proxy passthrough (httptest), asset serving.

### Phase 5 — Dockerfiles

- [ ] 5.1 `backend/Dockerfile`: multi-stage Go build (static-ish binary),
      minimal runtime image (e.g. `gcr.io/distroless/static` or `alpine`),
      expose 8080.
- [ ] 5.2 `frontend/Dockerfile`: stage 1 Node build of the Svelte SPA,
      stage 2 Go build embedding `dist/`, stage 3 minimal runtime, expose 8080.
- [ ] 5.3 Verify both images build locally and the backend starts against a
      local Postgres and, with `DATABASE_URL` unset, against a local SQLite
      file (and logs the fallback WARN).

### Phase 6 — Helm chart

- [ ] 6.1 `Chart.yaml` with chart metadata and a starting `version`.
- [ ] 6.2 `values.yaml` with backend/frontend image + tag fields, ingress
      host, TLS, env vars, replicas, resources, `postgres.enabled` toggle
      (default `true`) plus `postgres.*` credential placeholders (values never
      committed — supplied from GitHub secrets at deploy time), optional backend
      PVC for SQLite mode.
- [ ] 6.3 Templates: namespace, backend Deployment+Service+env (replicas
      forced to 1 when `postgres.enabled: false`), frontend
      Deployment+Service+env, Ingress (nginx, `/api/*` → backend, rest →
      frontend, cert-manager TLS), probes, a Postgres `Secret` + env injection
      created only when `postgres.enabled: true`, optional backend PVC for
      SQLite `SQLITE_PATH`.
- [ ] 6.4 `helm lint` and `helm template` pass for both `postgres.enabled: true`
      and `postgres.enabled: false`; dry-run against the target cluster (per
      the development-workflow skill).

### Phase 7 — GitHub Actions

- [ ] 7.1 `.github/workflows/build-deploy.yml`: change detection
      (`backend/`, `frontend/`), per-service build + push to GHCR with the
      date+8-SHA tag (§5), `values.yaml` tag update, `helm upgrade --install`
      using the `KUBECONFIG` secret (§6); when `postgres.enabled` is true,
      pass `POSTGRES_HOST`/`POSTGRES_PORT`/`POSTGRES_DB`/`POSTGRES_USER`/`POSTGRES_PASSWORD`
      (or `DATABASE_URL`) from GitHub repository secrets to helm via
      `--set`/`--set-string` (never committed to `values.yaml`).
- [ ] 7.2 Add a `Dockerfile`-only guard so a chart-only change redeploys
      (helm upgrade) without rebuilding images (tags unchanged).
- [ ] 7.3 Verify the workflow runs green on a feature branch push to main
      merge simulation (act or a real dry-run).

### Phase 8 — Cluster secrets & first deploy

- [ ] 8.1 Create GitHub secrets: `KUBECONFIG` (base64 of the k0s kubeconfig)
      and the `POSTGRES_*` (and/or `DATABASE_URL`) credentials used when
      `postgres.enabled` is true.
- [ ] 8.2 Confirm cert-manager ClusterIssuer `letsencrypt-prod` exists on the
      cluster; set `ingress.tls.host` and cluster-issuer annotation in
      `values.yaml`.
- [ ] 8.3 First real deploy via the workflow; verify ingress + TLS + API +
      UI + redirect end to end.

### Phase 9 — Documentation

- [ ] 9.1 `README.md`: architecture diagram (ASCII), local dev, env vars,
      how to run migrations locally, how to build images, how to deploy
      with helm, how the CI tags and deploys.
- [ ] 9.2 Keep `CHANGELOG.md` updated per PR (Added/Changed/Fixed/Removed).
- [ ] 9.3 Document the duration-string grammar and fallback behavior.

## 9. Out of scope (explicit)

- Authentication / per-user link ownership.
- Analytics / click counting beyond what is needed for redirect.
- Custom/vanity shortcodes.
- Rate limiting beyond basic safety (can be added later).
- Deploying PostgreSQL via this repo's Helm chart (Postgres is always an
  external service).
- Multi-region or HA Postgres.
