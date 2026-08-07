# URL Shortener

A self-hosted URL shortener with a Go backend, a Go + embedded-Svelte frontend,
an external PostgreSQL database (with a local SQLite fallback), a retention
cleanup worker, a Helm chart, and a GitHub Actions build/deploy pipeline.

Shorten any URL, get back a short link, and resolve it with a brief themed
1-second redirect page. Dark, GitHub-dark-inspired UI with orange accents.

- **Live**: <https://tinyurl.gamara.ro>
- **Plan of record**: [`development.md`](./development.md)
- **Changelog**: [`CHANGELOG.md`](./CHANGELOG.md)

---

## Features

- **Shorten & resolve** — `PUT /api/shorten` returns a short URL; `GET /{shortcode}` resolves it with a themed 1-second meta-refresh redirect page.
- **Input sanity checks** — `destination` must be a valid absolute `http`/`https` URL with a non-empty host; invalid input returns `400` with a human-readable `error` the UI renders verbatim. `ttl_seconds` (optional) must be a non-negative integer.
- **Per-link TTL** — optional `ttl_seconds` sets an `expires_at`; expired links are invisible to resolution and deleted by the cleanup worker.
- **Duplicate-shortcode protection** — uniqueness is guaranteed by the `links.shortcode` PRIMARY KEY; the backend regenerates and retries on a unique-constraint violation (up to 5 attempts), then returns `409 Conflict` if exhausted.
- **Rate limiting** — per-client-IP token-bucket on all `/api/*` routes (default `100/1m`), `429` + `Retry-After` on exceed; configurable via `RATE_LIMITS`.
- **Retention cleanup worker** — a background goroutine deletes over-retention and expired links; in Postgres mode it uses a transaction-level `pg_try_advisory_xact_lock` so only one replica runs per tick.
- **PostgreSQL (external) or SQLite fallback** — Postgres mode runs golang-migrate on startup; SQLite mode bootstraps the latest schema directly (no migrations) and logs a temporary-storage warning.
- **Graceful shutdown** — on `SIGTERM`, both services stop accepting new requests, return `503` on their health endpoint, and drain in-flight requests before exit (`SHUTDOWN_TIMEOUT`, default `30s`).
- **Request logging** — every HTTP request is logged at INFO with `method`, `path`, `status`, `bytes`, `duration_ms`, `remote_addr` (structured `slog`).
- **SQL-injection safe** — all queries use parameterized arguments; guard tests assert attacker payloads are stored/looked up as literal data.
- **Helm chart** — Kubernetes-native deployment with liveness/readiness probes, `preStop` hooks, `terminationGracePeriodSeconds`, cert-manager TLS, and `postgres.enabled` gating.
- **GitHub Actions CI/CD** — unit-test gate on feature branches; build + push to GHCR + `helm upgrade --install` on `main`, with per-folder image tagging and a chart-only guard.

---

## Architecture

```
                      ┌──────────────────────────┐
                      │  tinyurl.gamara.ro (DNS)  │
                      └──────────────┬───────────┘
                                     │
                          ┌──────────▼──────────┐
                          │  nginx Ingress (TLS)│
                          │  cert-manager/LE    │
                          └──────┬───────┬──────┘
                        /api/*  │       │  /
                                │       │
                 ┌──────────────▼─┐  ┌──▼───────────────┐
                 │  backend (Go)  │  │  frontend (Go +  │
                 │  :8080         │◄─┤  embedded Svelte) │
                 │  API + worker  │  │  :8080            │
                 └──────┬─────────┘  └───────────────────┘
                        │ DATABASE_URL
                        ▼
          ┌──────────────────────────────────┐
          │  PostgreSQL (external, db ns)    │
          │  postgres.db.svc.cluster.local   │
          │  database: urlshortener          │
          └──────────────────────────────────┘
```

- **backend/** — Go service: `PUT /api/shorten`, `GET /api/resolve/{shortcode}`, `GET /api/health`; runs the cleanup worker; API-only (JSON).
- **frontend/** — Go service embedding a Svelte SPA (built by Vite); serves the UI, reverse-proxies `/api/*` to the backend, renders the themed 1s redirect page for `GET /{shortcode}`, and `/healthz`.
- **PostgreSQL** — external service in the `db` namespace; the backend connects via `DATABASE_URL`. When unset, the backend falls back to a local SQLite file.

---

## API

All responses are JSON. Errors: `{"error":"<reason>"}` with the appropriate HTTP status.

| Method | Path | Body | Success | Errors |
| --- | --- | --- | --- | --- |
| `PUT` | `/api/shorten` | `{"destination":"https://…","ttl_seconds":60}` | `200 {"short_url","shortcode","destination"}` | `400` invalid URL / missing / bad TTL; `409` shortcode collision exhausted; `429` rate limited |
| `GET` | `/api/resolve/{shortcode}` | — | `200 {"shortcode","destination"}` | `404` not found / expired; `429` rate limited |
| `GET` | `/api/health` | — | `200 {"status":"ok"}` | `503` while draining |
| `GET` | `/{shortcode}` | — | `200` themed 1s redirect page (HTML) | `404` themed 404 page |
| `GET` | `/healthz` | — | `200 {"status":"ok"}` (frontend) | `503` while draining |

The returned `short_url` is built from the request host (`X-Forwarded-Host`/`Host`), so it is correct behind the ingress.

### Example

```bash
curl -X PUT https://tinyurl.gamara.ro/api/shorten \
  -H 'Content-Type: application/json' \
  -d '{"destination":"https://go.dev"}'
# => {"short_url":"https://tinyurl.gamara.ro/J1EedAV","shortcode":"J1EedAV","destination":"https://go.dev"}

curl https://tinyurl.gamara.ro/api/resolve/J1EedAV
# => {"shortcode":"J1EedAV","destination":"https://go.dev"}
```

---

## Configuration

### Backend (`backend/`)

| Variable | Default | Notes |
| --- | --- | --- |
| `DATABASE_URL` | — | Optional libpq URL. Set → Postgres mode; unset → SQLite fallback. |
| `POSTGRES_HOST` | — | Used with the other `POSTGRES_*` to compose `DATABASE_URL` when `DATABASE_URL` is unset. |
| `POSTGRES_PORT` | `5432` | Postgres port. |
| `POSTGRES_DB` | `urlshortener` | Postgres database name. |
| `POSTGRES_USER` | — | Postgres username. |
| `POSTGRES_PASSWORD` | — | Postgres password. |
| `SQLITE_PATH` | `/data/url-shortener.db` | SQLite file path (fallback mode only). |
| `LISTEN_ADDR` | `:8080` | HTTP listen address. |
| `RETENTION_PERIOD` | `3650d` | Global retention; links older than this are deleted. Duration string. |
| `CLEANUP_FREQUENCY` | `60m` | Cleanup worker tick interval. Duration string. |
| `SHORTCODE_LENGTH` | `7` | Base62 shortcode length. |
| `RATE_LIMITS` | `100/1m` | Per-client-IP rate limit as `"<count>/<window>"`. |
| `SHUTDOWN_TIMEOUT` | `30s` | Graceful drain deadline after SIGTERM. Duration string. |
| `LOG_LEVEL` | `info` | `debug`/`info`/`warn`/`error`. |

### Frontend (`frontend/`)

| Variable | Default | Notes |
| --- | --- | --- |
| `LISTEN_ADDR` | `:8080` | HTTP listen address. |
| `BACKEND_URL` | `http://backend:8080` | Backend base URL for the reverse proxy. (The chart sets this to `http://url-shortener-backend:8080`.) |
| `SHUTDOWN_TIMEOUT` | `30s` | Graceful drain deadline after SIGTERM. Duration string. |
| `LOG_LEVEL` | `info` | `debug`/`info`/`warn`/`error`. |

### Duration strings

`RETENTION_PERIOD`, `CLEANUP_FREQUENCY`, and `SHUTDOWN_TIMEOUT` use the shared
duration grammar: an integer with a unit suffix — `s`, `m` (minutes), `h`,
`d`, `w`, `mo` (months, 30 days), `y` (365 days). Examples: `300s`, `60m`,
`1h`, `2d`, `1w`, `1mo`, `1y`. **`m` = minutes, `mo` = months.** On parse
failure the backend logs an error and falls back to the documented default for
that variable.

---

## Repository layout

```
.
├── backend/                 # Go service: API + cleanup worker + migrations
│   ├── cmd/server/main.go
│   ├── internal/{api,store,worker,migrate,sqlite,ratelimit,config,shortcode}
│   ├── internal/migrate/migrations/  # *.up.sql / *.down.sql (Postgres only)
│   └── Dockerfile
├── frontend/                # Go server embedding Svelte SPA
│   ├── cmd/server/main.go
│   ├── internal/{server,config}
│   ├── internal/server/dist/          # Vite build output (gitignored), embedded
│   ├── web/                # Svelte + Vite source
│   └── Dockerfile
├── charts/url-shortener/    # Helm chart (Chart.yaml, values.yaml, templates/)
├── compose.yaml             # local Docker Compose stack
├── compose/README.md        # local testing docs
├── .github/workflows/       # unit-tests.yml, build-deploy.yml
├── development.md           # plan of record
├── AGENTS.md                # project guardrails + skills
└── CHANGELOG.md
```

---

## Local testing (Docker Compose)

The default stack includes a Postgres container so the full functionality
(migrations, advisory-lock cleanup, Postgres store path) is exercised locally.

```sh
docker compose up --build        # postgres + backend + frontend
```

| Service | Host port | Notes |
| --- | --- | --- |
| backend | `8080` | Postgres mode, `DATABASE_URL` wired |
| frontend | `8081` | UI + reverse proxy |
| postgres | `5432` | `postgres:16`, named volume |

- API (direct, e.g. Postman): `http://localhost:8080/api/...`
- UI: `http://localhost:8081/`
- Redirect page: `http://localhost:8081/{shortcode}`

SQLite opt-out profile (no Postgres):

```sh
docker compose --profile sqlite up --build
```

Reset data: `docker compose down -v`. See [`compose/README.md`](./compose/README.md)
for full details.

---

## Development

### Tests

```sh
# Backend (Go) — includes testcontainers Postgres tests (Docker-gated)
cd backend && go vet ./... && go test ./...

# Frontend Go server tests
cd frontend/web && npm ci && npm run build   # generate internal/server/dist for //go:embed
cd frontend && go vet ./... && go test ./...

# Frontend SPA tests (Vitest + jsdom + @testing-library/svelte)
cd frontend/web && npm ci && npm test
```

> The frontend Go tests require the built SPA output at
> `frontend/internal/server/dist/` (gitignored) because the server embeds it
> via `//go:embed all:dist`. Run `npm run build` first (the CI workflows do
> this automatically).

### Migrations

Postgres migrations are embedded in the backend and run automatically on
startup in Postgres mode. In SQLite mode the latest schema is created directly
(no migrations). To run migrations manually against a local Postgres:

```sh
cd backend && DATABASE_URL=postgres://... go run ./cmd/server
```

### Linting

- Go: `go vet ./...`
- Helm: `helm lint charts/url-shortener`, `helm template charts/url-shortener`
- GitHub Actions: `actionlint` + `shellcheck` (per the `lint-github-actions` skill)

---

## Deployment

### Helm chart (`charts/url-shortener/`)

Targets a k0s single-node cluster with nginx ingress and cert-manager + Let's
Encrypt. The chart deploys two Deployments (backend, frontend), their Services,
an Ingress (`/api` → backend, `/` → frontend, TLS via cert-manager), and a
Postgres `Secret` + `DATABASE_URL` injection gated on `postgres.enabled`
(default `true`). When `postgres.enabled` is false the backend falls back to
SQLite and `replicas` is forced to 1.

Probes & lifecycle: liveness + readiness on the health endpoints, `preStop:
sleep 10`, `terminationGracePeriodSeconds: 45` — the Kubernetes-native half of
the graceful-shutdown contract.

`values.yaml` is committed and is the source of truth for deployed image
tags; CI updates the `tag` fields after each build. **Postgres credential
values are never committed** — they are supplied at deploy time from GitHub
repository secrets.

### Manual deploy

```sh
helm upgrade --install url-shortener ./charts/url-shortener \
  --namespace tinyurl --create-namespace \
  -f ./charts/url-shortener/values.yaml \
  --set-string postgres.host=... \
  --set-string postgres.port=... \
  --set-string postgres.database=... \
  --set-string postgres.user=... \
  --set-string postgres.password=...
```

> Use `--set-string` (not `--set`) for all credential values so Helm keeps
> them as strings (a bare `--set` parses numeric-looking values like the port
> as an int, which corrupts the composed `DATABASE_URL`).

### CI/CD (GitHub Actions)

Two workflows (see [`development.md`](./development.md) §6):

- **`unit-tests.yml`** — on push to non-`main` branches: `go vet`+`go test`
  (backend, frontend) and `npm ci`+`npm test` (frontend/web). No build/deploy.
- **`build-deploy.yml`** — on push to `main` (+ manual dispatch): a mandatory
  unit-test gate, then per-service GHCR build+push with the
  `yyyy.mm.dd-xxxxxxxx` tag (date + 8-char short SHA of the latest commit
  **touching that folder**), a `values.yaml` tag update committed back to
  `main`, and `helm upgrade --install` into `tinyurl`. A chart-only change
  skips the image builds and runs only `helm upgrade` (tags unchanged).

#### Image tagging

Tag format: `yyyy.mm.dd-xxxxxxxx` — calendar date (commit date) plus the 8-digit
short commit SHA of the latest commit **touching that folder** (`backend/` or
`frontend/`). A chart-only change does not bump either service's tag.

#### Required GitHub secrets

| Secret | Purpose |
| --- | --- |
| `KUBECONFIG` | Base64 kubeconfig for the cluster, used by `helm`. |
| `GITHUB_TOKEN` | Push images to GHCR (the default workflow token with `packages: write`). |
| `POSTGRES_HOST` | External Postgres host (`postgres.db.svc.cluster.local`). |
| `POSTGRES_PORT` | `5432` |
| `POSTGRES_DB` | `urlshortener` |
| `POSTGRES_USER` | `urlshortener` |
| `POSTGRES_PASSWORD` | App password. |
| `DATABASE_URL` | Optional full libpq URL; overrides the composed `POSTGRES_*` form. |

GHCR packages are public (the repo is public), so the cluster pulls them
anonymously — no `imagePullSecret` is needed.

---

## Live environment

- **URL**: <https://tinyurl.gamara.ro>
- **Cluster**: `gamara.ro` (k0s single-node, nginx ingress, cert-manager + Let's Encrypt `letsencrypt-prod`).
- **App namespace**: `tinyurl`.
- **PostgreSQL**: external, in the `db` namespace (`postgres.db.svc.cluster.local:5432`); dedicated `urlshortener` database + user, credentials in GitHub secrets (and locally in the gitignored `charts/url-shortener/.env`).
- **Images**: `ghcr.io/gamaraan/url-shortener/backend:<tag>` and `…/frontend:<tag>`, built and pushed by the `build-deploy.yml` workflow on merge to `main`.

### Verify the live deployment

```sh
curl https://tinyurl.gamara.ro/api/health          # {"status":"ok"}
curl https://tinyurl.gamara.ro/healthz             # {"status":"ok"}
curl -X PUT https://tinyurl.gamara.ro/api/shorten \
  -H 'Content-Type: application/json' \
  -d '{"destination":"https://go.dev"}'
# open the returned short_url in a browser → themed 1s redirect
```

---

## Project skills & guardrails

This repo follows the guardrails in [`AGENTS.md`](./AGENTS.md) and the project
skills: `follow-development-plan`, `pr-on-instruction-only`,
`lint-github-actions`, and `regression-test-per-bugfix` (every bugfix ships
with a regression test that fails without the fix and passes with it). The
global `always-add-unit-tests` skill is also in force.

## License

Personal project. See the repository for details.
