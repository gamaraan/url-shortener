# URL Shortener — Project Agent Instructions

This project is a URL shortener with a Go backend, a Go+embedded-Svelte
frontend, an external PostgreSQL database (with a local SQLite fallback), a
cleanup worker, a Helm chart, and a GitHub Actions build/deploy pipeline. The
plan of record is [`development.md`](./development.md); progress is tracked
there.

## Plan of record: `development.md`

`development.md` is the single source of truth for architecture, component
contracts, configuration, the Helm chart, CI/CD, and the ordered task list.
It contains **only facts** — no narrative, no evolution stories.

- Before starting any implementation task, read `development.md` and work the
  next pending (`[ ]`) task. The **`follow-development-plan`** skill mandates
  this.
- While implementing, keep `development.md` in sync with reality: mark tasks
  `[~]` (in progress) and `[x]` (done), and edit contract/task sections in
  place when the actual implementation diverges or a decision changes. Do not
  append change stories.

## Git / PR discipline

The **`pr-on-instruction-only`** skill is in force for this repo:

- Commit work to feature branches (`feat/…`, `fix/…`, `chore/…`, `docs/…`) per
  the global git-workflow guardrail and the `git-identity` skill
  (`gamaraan` / `gabi.hulea@gmail.com`).
- **Do not push** unless the user explicitly instructs it in the current
  session.
- **Do not create a PR** unless the user explicitly instructs it in the
  current session. Before opening a PR, update `CHANGELOG.md`
  (Added/Changed/Fixed/Removed under `Unreleased`).
- Never merge a PR yourself; the user reviews and merges. After a merge, run
  the `post-merge-cleanup` skill.
- Never push to `main`.

## CHANGELOG.md

`CHANGELOG.md` records what changed per PR (Added/Changed/Fixed/Removed under
an `Unreleased` heading). It is updated **before** a PR is opened, not after.

## GitHub Actions linting

The **`lint-github-actions`** skill is in force for this repo: every file
under `.github/workflows/` and every shell snippet inside a workflow must pass
**actionlint** and **shellcheck** before being committed or PR'd. Both must
exit clean (zero errors).

## Unit tests (mandatory)

The global **`always-add-unit-tests`** skill is in force for this repo: every
new feature and every bugfix must ship with unit tests. A task in
`development.md` is not `done` (`[x]`) until its unit tests are written and
`go test ./...` passes in both `backend/` and `frontend/`. Failing or missing
tests block the task — leave it `[~]` and track the gap as a blocker rather
than marking it done.

## Global guardrails (always apply)

- **Git workflow:** never push to `main`; always use a feature branch and a PR.
- **pi-lens LSP errors are mandatory fixes.** Suppress false positives with a
  `pi-lens-ignore` comment or `lens_diagnostic_mark` — never leave them
  unresolved.
- **Installation approval required** before installing any package, tool, or
  runtime (apt, npm/pip -g, go install, curl|sh, etc.). Present the plan and
  wait for approval.
- **Pip packages** always go into a virtualenv, never system/global.
- **File safety:** never overwrite an existing file with `write`/`>` without
  reading it first and without explicit approval; prefer `edit`.
- **Infra safety:** never create/delete cloud or cluster resources (AWS,
  Terraform, kubectl, helm) unless explicitly asked; present delete commands
  for the user to run. Local Docker containers on this workstation are OK.

## Project conventions

- Two folders, two images: `backend/` and `frontend/`. The cleanup worker is a
  goroutine inside the backend, not a separate image.
- Image tag format: `yyyy.mm.dd-xxxxxxxx` (date + 8-char short SHA of the
  latest commit **touching that folder**). A chart-only change must not bump
  either service's tag.
- `charts/url-shortener/values.yaml` is committed and is updated by CI with the
  deployed image tags; it is the source of truth for deployed versions.
- Duration strings (`RETENTION_PERIOD`, `CLEANUP_FREQUENCY`) accept `s`, `m`,
  `h`, `d`, `w`, `mo`, `y`; `m` = minutes, `mo` = months. Parse failure logs an
  error and falls back to the default.
- Target cluster: k0s single node, nginx ingress, cert-manager + Let's Encrypt.
- PostgreSQL is an **external service**; it is never deployed by this repo's
  Helm chart. The chart gates Postgres on `postgres.enabled` (default `true`):
  when enabled it creates a `Secret` with the credentials and injects
  `DATABASE_URL` (composed from `POSTGRES_HOST`/`POSTGRES_PORT`/`POSTGRES_DB`/`POSTGRES_USER`/`POSTGRES_PASSWORD`,
  or taken verbatim from `DATABASE_URL`) into the backend; the credential
  values come from **GitHub repository secrets** at deploy time and are never
  committed to `values.yaml`. When `postgres.enabled` is false the backend
  falls back to a local SQLite file (`SQLITE_PATH`, default
  `/data/url-shortener.db`), logs a WARN that temporary storage is in use and
  data will be lost on restart, and must not run more than one backend
  instance. In SQLite mode the backend never runs migrations and creates the
  latest schema directly.
- In Postgres mode, migrations (golang-migrate, embedded) run on backend
  startup; migration failure is fatal.
