# Local testing (Docker Compose)

This directory documents the local testing stack defined in
[`../compose.yaml`](../compose.yaml). It is for **local testing only**;
production deploys via the Helm chart (see `../charts/`).

## Default stack (Postgres)

```sh
docker compose up --build
```

Brings up three services:

| Service   | Image                          | Host port | Container port |
| --------- | ------------------------------ | --------- | -------------- |
| postgres  | `postgres:16-alpine`           | 5432      | 5432           |
| backend   | built from `backend/`          | 8080      | 8080           |
| frontend  | built from `frontend/`         | 8081      | 8080           |

The backend runs in **Postgres mode**: `DATABASE_URL` is wired to the
`postgres` service, golang-migrate runs on startup, and the cleanup worker uses
the Postgres transaction-level advisory lock. The backend depends on `postgres`
with a healthcheck, so migrations do not race DB startup.

- API (direct, e.g. Postman): `http://localhost:8080/api/...`
- UI: `http://localhost:8081/`
- Resolve a shortcode (themed 1s redirect page): `http://localhost:8081/{shortcode}`

## SQLite opt-out profile

```sh
docker compose --profile sqlite up --build
```

Brings up `backend-sqlite` + `frontend-sqlite` only (no Postgres). The backend
runs with `DATABASE_URL` unset → SQLite fallback at `/data/url-shortener.db`
(on a named volume). The backend logs the temporary-storage WARN on startup.
Use this for the zero-dependency path and to exercise the SQLite store/worker
(advisory lock skipped).

## Reset data

```sh
docker compose down -v        # removes volumes (postgres-data, backend-sqlite-data)
```

`docker compose down` (without `-v`) keeps the data; the databases survive a
restart.

## Environment

Service env vars are set in `compose.yaml` (see the file for the full list and
defaults). The shared duration grammar (`s`/`m`/`h`/`d`/`w`/`mo`/`y`) applies to
`RETENTION_PERIOD`, `CLEANUP_FREQUENCY`, and `SHUTDOWN_TIMEOUT`.

## Notes

- `CLEANUP_FREQUENCY` is set to `2m` in compose (for visibility during testing);
  the production default is `60m`.
- The frontend container's `BACKEND_URL` points at the backend service via
  in-compose DNS (`http://backend:8080` for the default stack,
  `http://backend-sqlite:8080` for the SQLite profile).
