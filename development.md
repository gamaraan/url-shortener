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
│   │   ├── ratelimit/      # per-IP rate limiter (token bucket) used by the API
│   │   ├── config/           # env parsing + duration parser
│   │   └── shortcode/       # nanoid base62 generator + collision check
│   ├── internal/migrate/migrations/  # *.up.sql / *.down.sql (embedded; Postgres only)
│   ├── Dockerfile
│   └── *_test.go
├── frontend/                # Go server embedding Svelte SPA
│   ├── go.mod
│   ├── cmd/server/main.go
│   ├── internal/
│   │   ├── server/           # serves embed.FS assets, proxy /api/*, redirect page
│   │   │   ├── server.go     # //go:embed all:dist
│   │   │   └── dist/         # Vite build output (gitignored), embedded at build time
│   │   └── config/           # env parsing
│   ├── web/                  # Svelte + Vite source
│   │   ├── src/
│   │   ├── package.json
│   │   ├── vite.config.ts    # builds into ../internal/server/dist
│   │   └── tsconfig.json
│   ├── Dockerfile            # multi-stage: node build → go embed → final
│   └── *_test.go
├── compose.yaml              # local Docker Compose stack for testing
├── compose/                  # compose overrides + seed data
│   └── README.md
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
    returned short URL is correct behind the ingress. **Input sanity check**:
    before any storage work, the backend validates that `destination` is
    present, non-empty, and a valid absolute URL with scheme `http` or `https`
    and a non-empty host (parsed via `net/url`). If the payload is missing,
    not valid JSON, or the URL is invalid, it responds **400 Bad Request** with
    JSON `{"error":"<reason>"}` (e.g. `destination is required`,
    `destination must be an absolute http(s) URL`, `invalid request body`).
    The frontend renders this `error` field verbatim so the user sees the
    exact reason. `ttl_seconds`, when present, must be a non-negative integer;
    a negative or non-integer value is a 400 with a relevant message.
  - `GET /api/resolve/:shortcode` — returns
    `{"shortcode":"…","destination":"…"}` with HTTP 200, or 404 (JSON error)
    when not found / expired. Does **not** record the hit here; the hit counter
    (if implemented) is a separate concern. Resolution returns the destination
    so the frontend can render the redirect page.
  - Errors are always JSON: `{"error":"…"}` with the appropriate HTTP status.
- **Rate limiting**: per-client-IP token-bucket limiter applied to all `/api/*`
  routes. The client IP is taken from `X-Forwarded-For` (last hop) when
  present, else `RemoteAddr`. Default **100 requests per minute**. Configurable
  via `RATE_LIMITS` (see §3.4) as `"<count>/<window>"`, e.g. `"100/1m"`,
  `"30/10s"`, `"1000/1h"`. The window uses the shared duration parser (§3.1).
  On parse failure it logs the error and falls back to `100/1m`. When a client
  exceeds the limit the API responds **429 Too Many Requests** with JSON
  `{"error":"rate limit exceeded"}` and a `Retry-After` header (seconds until
  the bucket refills enough for one request). The limiter is in-process and
  per-instance (sufficient for the single-node k0s target; documented as a
  known limitation for multi-replica Postgres mode).
- **Shortcode generation**: random base62 code (crypto/rand), length from
  `SHORTCODE_LENGTH` (default 7). **Uniqueness is guaranteed by the database**
  via the `links.shortcode` PRIMARY KEY (a unique constraint in both Postgres
  and SQLite). The backend **retries on a duplicate insert**: if `store.Create`
  returns a unique-constraint violation (`*store.ConstraintError`, detected
  via Postgres SQLSTATE `23505` or SQLite's `UNIQUE constraint failed`), the
  API generates a fresh shortcode and retries, up to **5 attempts**. If all 5
  attempts collide (astronomically unlikely for a 7-char base62 space), it
  responds **409 Conflict** with `{"error":"could not generate a unique
  shortcode"}`. Supports an optional per-link `expires_at` derived from
  `ttl_seconds` when provided.
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
  Postgres **transaction-level** advisory lock (`pg_try_advisory_xact_lock`
  on a constant key, held across the cleanup queries and auto-released on
  commit) so only one backend replica runs cleanup per tick in a multi-replica
  deployment. In SQLite mode the advisory lock is skipped (single-instance is
  already mandated). Logs each run's deleted count. Ticks are bounded and the
  next tick waits for the previous to finish (does not overlap). Logs (does
  not panic on) per-run errors.
- **Duration parsing** (shared module, used by retention + cleanup frequency):
  accepts integers with unit suffixes: `300s`, `60m`, `1h`, `2d`, `1w`, `1mo`,
  `1y`. `m` = minutes, `mo` = months. On parse failure it logs the error and
  falls back to the documented default for that variable.
- **Graceful shutdown**: on SIGINT/SIGTERM the server **stops accepting new
  connections/requests**, the `/api/health` endpoint starts returning **503
  Service Unavailable** (so kubelet/ingress stop sending traffic — see §4
  readiness probe), and the process **does not exit until all in-flight
  requests have completed** or a bounded drain timeout elapses
  (`SHUTDOWN_TIMEOUT`, default `30s`). The cleanup worker is canceled
  concurrently and the DB is closed after the HTTP server returns. A hard
  exit occurs only after the drain timeout.
- **Request logging**: every HTTP request is logged at INFO with `method`,
  `path`, `status`, `bytes`, `duration_ms`, and `remote_addr` (structured
  `slog`). The access log wraps the whole handler tree (outside the rate
  limiter) so 200/400/404/409/429/503 responses are all logged. Lifecycle
  events (startup, shutdown, worker ticks) and error paths are logged as
  before.

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
- **Graceful shutdown**: on SIGINT/SIGTERM the server **stops accepting new
  connections/requests**, the `/healthz` endpoint starts returning **503
  Service Unavailable** (so kubelet/ingress stop sending traffic — see §4
  readiness probe), and the process **does not exit until all in-flight
  requests have completed** (including proxied `/api/*` calls and in-progress
  `/:shortcode` resolves) or a bounded drain timeout elapses
  (`SHUTDOWN_TIMEOUT`, default `30s`). A hard exit occurs only after the drain
  timeout.
- **Request logging**: every HTTP request is logged at INFO with `method`,
  `path`, `status`, `bytes`, `duration_ms`, and `remote_addr` (structured
  `slog`). The access log wraps the whole handler tree so SPA serves, asset
  requests, `/api/*` proxy calls, `/:shortcode` resolves, `/healthz`, and
  502/503 responses are all logged. Lifecycle events (startup, shutdown) and
  proxy/resolve errors are logged as before.

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
| `RATE_LIMITS` | `100/1m` | Per-client-IP rate limit as `"<count>/<window>"` (e.g. `"100/1m"`, `"30/10s"`). Window uses the duration parser; parse failure logs an error and falls back to `100/1m`. |
| `SHUTDOWN_TIMEOUT` | `30s` | Graceful drain deadline after SIGTERM: stop accepting new requests, return 503 on `/api/health`, and exit only after in-flight requests finish or this timeout. Duration string. |
| `LOG_LEVEL` | `info` | `debug`/`info`/`warn`/`error`. |

Frontend:

| Variable | Default | Notes |
| --- | --- | --- |
| `LISTEN_ADDR` | `:8080` | HTTP listen address. |
| `BACKEND_URL` | `http://backend:8080` | Backend base URL for the reverse proxy. |
| `SHUTDOWN_TIMEOUT` | `30s` | Graceful drain deadline after SIGTERM: stop accepting new requests, return 503 on `/healthz`, and exit only after in-flight requests finish or this timeout. Duration string. |

Duration strings use the shared parser (§3.1). Invalid values log an error and
fall back to the default.

### 3.5 Local testing (Docker Compose)

A `compose.yaml` at the repo root runs the full stack locally for manual and
integration testing. The **default stack includes a Postgres container** so the
full functionality — migrations on startup, the Postgres advisory-lock cleanup
worker, and the Postgres store path — can be exercised locally with no
external database. A SQLite-only profile is available as an opt-out for the
zero-dependency path.

- **Default stack (Postgres)**: `docker compose up` starts `postgres`,
  `backend`, and `frontend`. The `postgres` service uses image `postgres:16`
  with a named volume for persistence; the `backend` service is wired to it
  via `DATABASE_URL` (composed from `POSTGRES_USER`/`POSTGRES_PASSWORD`/
  `POSTGRES_DB`) → Postgres mode: golang-migrate runs on startup, the cleanup
  worker uses `pg_try_advisory_xact_lock`, and the Postgres store path is
  exercised. This is the primary local testing path.
- **SQLite profile**: `docker compose --profile sqlite up` starts `backend`
  and `frontend` only with `DATABASE_URL` unset → SQLite fallback
  (`SQLITE_PATH` on a named volume). The backend logs the SQLite WARN on
  startup (§3.1). This is the zero-dependency path and exercises the SQLite
  store/worker paths (advisory lock skipped).
- Services:
  - `frontend` — builds `frontend/Dockerfile`, exposes `:8080` on host port
    `8081` (or as configured).
  - `backend` — builds `backend/Dockerfile`, exposes `:8080` on host port
    `8080`; depends on `postgres` (default stack).
  - `postgres` — image `postgres:16`, exposes `:5432` on host port `5432`,
    named volume for `/var/lib/postgresql/data`.
- `frontend.BACKEND_URL` points at `http://backend:8080` (in-compose DNS).
- `backend` uses `depends_on: postgres` with a healthcheck so migrations do
  not race the DB startup.
- A `compose/README.md` documents both stacks, the exposed ports, the
  volumes, the env vars, and how to reset data (`docker compose down -v`).
- Both `backend` and `frontend` services use `build:` pointing at their
  respective `Dockerfile` (Phase 5), so `docker compose up --build` exercises
  the real production images locally.
- The compose stack is for **local testing only**; it is never used for
  production (production deploys via the Helm chart, §4/§6).

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
- Probes & lifecycle (both Deployments): a **liveness** probe and a
  **readiness** probe. The readiness probe hits the service health endpoint
  (backend `/api/health`, frontend `/healthz`); during graceful shutdown that
  endpoint returns **503**, which removes the pod from the Service
  endpoints (and the nginx ingress) **before** the process exits, so
  in-flight requests are not dropped. Each container has a **`preStop` hook**
  (`exec: sleep 10` by default, configurable) that gives the load balancer time
  to deregister the pod after readiness flips to failing and before SIGTERM is
  delivered. `terminationGracePeriodSeconds` is set to `SHUTDOWN_TIMEOUT +
  preStop` headroom (default `45s`). This is the Kubernetes-native
  implementation of the §3.1/§3.2 graceful-shutdown contract.
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
  `lint-github-actions`, `always-add-unit-tests`, and
  `regression-test-per-bugfix`.
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
- **Every bugfix must ship with a regression test that fails without the fix
  and passes with it.** This is enforced by the project
  `regression-test-per-bugfix` skill. The test must exercise the code path
  that contained the bug (not a different layer), and the fix must be
  temporarily reverted to prove the test genuinely catches the bug. For
  frontend render/mount bugs the regression test is a Vitest + jsdom +
  `@testing-library/svelte` DOM test (`npm test` in `frontend/web/`), not
  just a build/type-check.
- Per-phase test tasks already enumerate the minimum coverage (1.5, 2.6, 3.2,
  4.6). Any additional feature/bugfix introduced during implementation must add
  its own unit tests even if no explicit test task is listed for it.
- Run the full test suites before considering any task `done`: `go test ./...`
  in `backend/` and `frontend/`, and `npm test` in `frontend/web/`. Failing
  tests block the task.
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

- [x] 1.1 Write `backend/internal/migrate/migrations/0001_init.up.sql` /
      `…down.sql` creating the `links` table, indexes, and the partial
      `expires_at` index for Postgres (§3.3). Migrations are colocated with
      the `migrate` package so `//go:embed migrations/*.sql` resolves.
- [x] 1.2 Implement `backend/internal/migrate` using golang-migrate with the
      migrations embedded via `embed.FS`; expose `Run(ctx, dbURL) error` and
      `Down(ctx, dbURL) error` (Down supports the down+up idempotency test).
      Postgres-only.
- [x] 1.3 Implement `backend/internal/sqlite`: bootstrap the SQLite file at
      `SQLITE_PATH` with the latest schema directly (`CREATE TABLE IF NOT
      EXISTS` + indexes); no migration files are read. Idempotent.
- [x] 1.4 Implement the startup database selection in `cmd/server/main.go`:
      if `DATABASE_URL` set → Postgres mode (run migrations); else → SQLite
      fallback (bootstrap latest schema) and log the WARN that temporary
      storage is in use, data will be lost on restart, and only one backend
      instance may run.
- [x] 1.5 Unit tests: Postgres migrations against an ephemeral Postgres
      (testcontainers or CI service) assert schema exists and down+up is
      idempotent; SQLite bootstrap is idempotent and creates the expected
      schema.

### Phase 2 — Backend core (store, shortcode, API)

- [x] 2.1 Implement `backend/internal/config`: parse `DATABASE_URL`,
      `POSTGRES_HOST`/`POSTGRES_PORT`/`POSTGRES_DB`/`POSTGRES_USER`/`POSTGRES_PASSWORD`,
      `SQLITE_PATH`, `LISTEN_ADDR`, `SHORTCODE_LENGTH`, `LOG_LEVEL`,
      `RETENTION_PERIOD`, `CLEANUP_FREQUENCY`, `RATE_LIMITS`, `SHUTDOWN_TIMEOUT`;
      compose `DATABASE_URL` from the `POSTGRES_*` vars when `DATABASE_URL` is
      unset; central duration parser (§3.1) with fallback + error logging.
- [x] 2.2 Implement `backend/internal/shortcode`: base62 generator keyed
      on `SHORTCODE_LENGTH` (crypto/rand) with a collision-regenerate helper.
- [x] 2.3 Implement `backend/internal/store`: a storage interface with a
      `pgx` (Postgres) implementation and a SQLite implementation;
      `Create(shortcode, destination, expiresAt)`, `Get(shortcode)`,
      `DeleteOlderThan(retention)`, `DeleteExpired()`. All queries use
      parameterized arguments (no string interpolation of user input); the
      SQL-injection guard tests (2.8) assert this.
- [x] 2.4 Implement `backend/internal/ratelimit`: per-client-IP token-bucket
      limiter (§3.1) with `RATE_LIMITS` parsing (`"<count>/<window>"`),
      fallback to `100/1m` on parse failure, and a `Retry-After` hint.
- [x] 2.5 Implement `backend/internal/api`:
      `PUT /api/shorten`, `GET /api/resolve/:shortcode`, `GET /api/health`;
      JSON error contract; **destination input sanity check** (present,
      non-empty, valid absolute `http`/`https` URL with non-empty host, else
      400 with a human-readable `error` the frontend renders verbatim);
      `ttl_seconds` (optional, non-negative integer) → `expires_at`; build
      `short_url` from request host; wrap `/api/*` in the rate-limiter
      middleware (429 + `Retry-After` on exceed).
- [x] 2.5b **Duplicate-shortcode protection**: uniqueness is guaranteed by the
      `links.shortcode` PRIMARY KEY (DB-enforced in both Postgres and SQLite).
      `store.Create` wraps a unique-constraint violation as `*ConstraintError`
      (Postgres SQLSTATE `23505` / SQLite `UNIQUE constraint failed`); the API
      regenerates the shortcode and retries up to 5 attempts, then responds
      **409 Conflict** if exhausted. API test `TestShorten_RetriesOnCollision`
      asserts the retry-then-succeed path and `TestShorten_CollisionExhausted`
      asserts the 409 path.
- [x] 2.6 `cmd/server/main.go`: load config → run migrations → start HTTP
      server. The cleanup worker is started in Phase 3; Phase 2 ships the HTTP
      API without the worker. Graceful shutdown is implemented in task 2.10.
- [x] 2.7 Unit tests: shortcode uniqueness/collision; store CRUD against
      ephemeral Postgres; API handlers (httptest) for success/404/validation
      paths (including invalid-URL 400 responses with the `error` field);
      rate-limiter allow/deny + `Retry-After` + parse-fallback behavior.
- [x] 2.8 SQL-injection guard tests: against both the Postgres and SQLite
      store implementations, assert that attacker-controlled `shortcode` /
      `destination` payloads (e.g. `' OR '1'='1`, `'; DROP TABLE links;--`,
      `""; --`, unicode/hex escapes) are stored/looked up as literal data and
      never alter the schema or bypass lookups. Verify the `links` table still
      exists and contains exactly the inserted rows after the payloads.
- [x] 2.9 `backend/Dockerfile` (pulled forward from Phase 5.1): multi-stage
      Go build (CGO disabled, pure-Go `modernc.org/sqlite`), alpine runtime,
      expose 8080. Built `url-shortener-backend:dev` and ran the container
      exposed on host port 8080 for manual/Postman testing.
- [x] 2.10 Graceful shutdown (§3.1): `SHUTDOWN_TIMEOUT` (default `30s`) wired
      in config; `api.Server` has a draining flag so `/api/health` returns
      **503** `{"status":"draining"}` while draining; `cmd/server` on
      SIGINT/SIGTERM sets draining, cancels the cleanup worker concurrently,
      and calls `http.Server.Shutdown` with the `SHUTDOWN_TIMEOUT` context so
      in-flight requests complete before exit (DB closed via defer). Unit
      test `TestHealth_DrainingReturns503`; live smoke test confirmed an
      in-flight shorten completed and the container exited 0.
- [x] 2.11 Request logging (§3.1): every HTTP request is logged at INFO with
      `method`, `path`, `status`, `bytes`, `duration_ms`, `remote_addr` via a
      `statusRecorder` middleware wrapping the whole handler tree (outside the
      rate limiter, so 429/503 are logged). Unit test
      `TestAccessLog_LogsEveryRequest` asserts method/path/status/duration/
      remote_addr for health, shorten, and a 404 resolve.

### Phase 3 — Cleanup worker

- [x] 3.1 Implement `backend/internal/worker`: ticker on
      `CLEANUP_FREQUENCY`; per tick, in Postgres mode acquire a
      **transaction-level** `pg_try_advisory_xact_lock` (held across the
      cleanup queries, auto-released on commit; run via `store.WithTx`), run
      `DeleteOlderThan(now - RETENTION_PERIOD)` and `DeleteExpired()`, log
      counts; skip in SQLite mode (single-instance). Non-overlapping ticks.
      Wired into `cmd/server/main.go` (started after DB ready, canceled on
      shutdown).
- [x] 3.2 Unit tests: lock contention (only one worker deletes) in Postgres
      mode (held xact lock forces the worker to skip), retention boundary,
      expiry deletion, parse-fallback behavior.

### Phase 4 — Frontend (Svelte SPA + Go server)

- [x] 4.1 Scaffold `frontend/web` with Svelte + Vite (TS), themed UI (§3.2):
      input, shorten button, result + copy button, dark/orange palette.
- [x] 4.2 SPA calls `PUT /api/shorten` (same origin, proxied) and renders
      `${window.location.origin}/${shortcode}`.
- [x] 4.3 Implement `frontend/internal/server`: serve embedded SPA assets,
      reverse proxy `/api/*` to `BACKEND_URL`, `GET /:shortcode` →
      resolve + themed 1-second meta-refresh redirect page, themed 404,
      `/healthz` (503 while draining).
- [x] 4.4 `//go:embed all:dist` in `frontend/internal/server/server.go`; the
      Vite build outputs to `frontend/internal/server/dist` (colocated with the
      server package so the embed path resolves; `//go:embed` cannot use `..`).
      `frontend/embed.go` is not a separate file — the directive lives in the
      server package. Build step: `npm run build` in `frontend/web`.
- [x] 4.5 `cmd/server/main.go`: config → HTTP server → graceful shutdown
      (stop accepting new requests, return 503 on `/healthz`, drain
      in-flight requests before exit per §3.2).
- [x] 4.6 Unit tests: redirect-page HTML contains the destination and
      `content="1"`, 404 path, proxy passthrough (httptest), asset serving,
      `/healthz` 503 while draining; plus frontend `config` package tests.
- [x] 4.7 Request logging (§3.2): every HTTP request is logged at INFO with
      `method`, `path`, `status`, `bytes`, `duration_ms`, `remote_addr` via a
      `statusRecorder` middleware wrapping the whole handler tree. Unit test
      `TestAccessLog_LogsEveryRequest` asserts method/path/status/duration/
      remote_addr for `/healthz`, `/api/health`, and `/`.

### Phase 5 — Dockerfiles

- [x] 5.1 `backend/Dockerfile`: multi-stage Go build (CGO disabled, pure-Go
      `modernc.org/sqlite`), alpine runtime, expose 8080 (done in Phase 2.9).
- [x] 5.2 `frontend/Dockerfile`: stage 1 Node build of the Svelte SPA (into
      `internal/server/dist`), stage 2 Go build embedding `dist/`, stage 3
      alpine runtime, expose 8080.
- [x] 5.3 Verified both images build locally; backend starts in Postgres mode
      (migrations applied) and, with `DATABASE_URL` unset, in SQLite fallback
      mode (logs the WARN).
- [x] 5.4 `compose.yaml` + `compose/README.md` (§3.5): **default Postgres
      stack** (`postgres:16` + `backend` + `frontend`, `DATABASE_URL` wired
      so migrations + advisory-lock cleanup run) and a `sqlite` opt-out
      profile (`backend-sqlite` + `frontend-sqlite` only, `DATABASE_URL` unset).
      `backend` uses `depends_on: postgres` with a healthcheck. Backend port
      8080 and frontend port 8081 exposed for direct testing; Postgres 5432.
      Full stack brought up with `docker compose up --build`; verified
      `/api/health`, the SPA UI, shorten via proxy, the themed 1s redirect
      page, 404, and invalid-URL 400 end to end.

### Phase 6 — Helm chart

- [x] 6.1 `Chart.yaml` with chart metadata and a starting `version`.
- [x] 6.2 `values.yaml` with backend/frontend image + tag fields, ingress
      host, TLS, env vars, replicas, resources, `postgres.enabled` toggle
      (default `true`) plus `postgres.*` credential placeholders (values never
      committed — supplied from GitHub secrets at deploy time), optional backend
      PVC for SQLite mode. Added `shutdownTimeout`, `preStopSleep`, and
      `terminationGracePeriodSeconds` for both services.
- [x] 6.3 Templates: namespace, backend Deployment+Service+env (replicas
      forced to 1 when `postgres.enabled: false`), frontend
      Deployment+Service+env, Ingress (nginx, `/api/*` → backend, rest →
      frontend, cert-manager TLS), probes, a Postgres `Secret` + env injection
      created only when `postgres.enabled: true`, optional backend PVC for
      SQLite `SQLITE_PATH`. `_helpers.tpl` provides name/label/DATABASE_URL
      helpers.
- [x] 6.4 Probes & lifecycle (both Deployments): liveness + readiness probes
      hitting the service health endpoint (backend `/api/health`, frontend
      `/healthz`); a `preStop: exec: sleep 10` hook; `terminationGracePeriodSeconds`
      set to `SHUTDOWN_TIMEOUT + preStop` headroom (default `45s`). The
      readiness probe + 503-during-shutdown (§3.1/§3.2) removes the pod from
      Service/ingress endpoints before the process exits, so in-flight
      requests are not dropped.
- [x] 6.5 `helm lint` and `helm template` pass for both `postgres.enabled: true`
      and `postgres.enabled: false`; rendered YAML validated as parseable.
      (Dry-run against the target cluster is Phase 8.3 — deferred until the
      cluster + secrets are ready.) Pi-lens YAML findings on the templates
      are false positives (raw Helm `{{- }}` directives parsed as YAML) and
      suppressed with `# pi-lens-ignore: YAML:0` per file.

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
- Deploying PostgreSQL via this repo's Helm chart (Postgres is always an
  external service).
- Multi-region or HA Postgres.
- A distributed/cluster-wide rate limiter — the in-process per-instance
  limiter (§3.1) is sufficient for the single-node k0s target and is a known
  approximation under multi-replica Postgres mode.
