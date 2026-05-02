# Hindsight Docker Compose Setup Plan

## Goal

Add a local Docker Compose setup for Hindsight in this repo that:

- Runs Hindsight API and Control Plane locally.
- Uses `openai-codex` OAuth auth instead of an OpenAI Platform API key.
- Preserves memory state in an external PostgreSQL/pgvector database, not embedded pg0.
- Performs automatic logical PostgreSQL backups with retention.
- Keeps secrets and generated backup artifacts out of git.

## Research Summary

- Hindsight quickstart exposes:
  - API at `http://localhost:8888`
  - Control Plane at `http://localhost:9999`
- Hindsight Docker images:
  - Standalone full image: `ghcr.io/vectorize-io/hindsight:latest`
  - API + Control Plane in one container.
- Hindsight supports `HINDSIGHT_API_LLM_PROVIDER=openai-codex`.
  - No `HINDSIGHT_API_LLM_API_KEY` is required.
  - Hindsight source confirms `openai-codex` is in the provider allowlist that does not require an API key.
  - Hindsight source confirms the Codex provider loads OAuth credentials from `Path.home() / ".codex" / "auth.json"`.
  - Hindsight's standalone Dockerfile creates and runs as user `hindsight` with home `/home/hindsight`, so mount host `~/.codex/auth.json` read-only at `/home/hindsight/.codex/auth.json`.
  - Current host check: `/Users/avery/.codex` does not exist yet, so Codex CLI login is a prerequisite before runtime verification.
- Hindsight defaults to embedded pg0, but docs say pg0 is convenient for development and external PostgreSQL is recommended when persistence matters.
- Hindsight requires PostgreSQL 14+ with a vector extension; `pgvector` is the default supported extension.
- Upstream example Compose uses `pgvector/pgvector` and sets `HINDSIGHT_API_DATABASE_URL=postgresql://...@db:5432/...`.
- The upstream Postgres 18 Dockerfile sets `PGDATA=/var/lib/postgresql/18/docker` and `VOLUME /var/lib/postgresql`; Hindsight's upstream Compose example mounts `pg_data:/var/lib/postgresql/${HINDSIGHT_DB_VERSION:-18}/docker`, matching that PGDATA path.
- Logical backups can be automated by a sidecar container running `pg_dump`, gzip compression, and retention cleanup against the Compose Postgres service. Dumps include `--clean --if-exists` so restore can replace existing dumped objects.
- Hindsight's worker poller probes `schemas_with_pending_work()` on PostgreSQL and falls back if it is missing, but that fallback causes noisy Postgres error logs. Install the helper in `initdb` for new databases.

## Constraints

- Do not commit `~/.codex/auth.json`, database files, backup files, or real secrets.
- The setup should fail clearly if Codex auth has not been created yet.
- Memory must survive `docker compose down` as long as named volumes are not removed.
- Backups must survive container recreation by writing to a host bind mount under the user's XDG state directory.
- Prefer simple local development ergonomics over production orchestration.

## Acceptance Criteria

1. `docker compose --env-file hindsight/.env.example -f hindsight/docker-compose.yml config` succeeds.
2. The Hindsight service is configured with `HINDSIGHT_API_LLM_PROVIDER=openai-codex` and no required LLM API key.
3. The Hindsight container mounts `${HOME}/.codex/auth.json` read-only at `/home/hindsight/.codex/auth.json`.
4. PostgreSQL uses a named Docker volume and Hindsight points to it through `HINDSIGHT_API_DATABASE_URL`.
5. A backup service creates timestamped compressed dumps under the configured XDG state backup directory and deletes old dumps after the configured retention period.
6. The API health endpoint at `http://localhost:8888/health` becomes reachable after `docker compose up -d` when Codex auth exists.
7. A documented restore command can load a `.sql.gz` backup into the Compose Postgres database.

## Chosen Approach

Use a three-service Docker Compose stack:

1. `db`
   - Image: `pgvector/pgvector:pg18` or pinned compatible tag.
   - Environment:
     - `POSTGRES_USER`
     - `POSTGRES_PASSWORD`
     - `POSTGRES_DB`
   - Named volume for `/var/lib/postgresql/data` or the image-specific Postgres data path after verifying the tag.
   - Healthcheck using `pg_isready`.

2. `hindsight`
   - Image: `ghcr.io/vectorize-io/hindsight:${HINDSIGHT_VERSION:-latest}`.
   - Ports:
     - `8888:8888`
     - `9999:9999`
   - Environment:
     - `HINDSIGHT_API_LLM_PROVIDER=openai-codex`
     - optional `HINDSIGHT_API_LLM_MODEL`, defaulting to Hindsight's openai-codex default unless explicitly set.
     - `HINDSIGHT_API_DATABASE_URL=postgresql://...@db:5432/...`
     - `HINDSIGHT_API_VECTOR_EXTENSION=pgvector`
   - Mount:
     - `${HOME}/.codex/auth.json:/home/hindsight/.codex/auth.json:ro`
   - Depends on healthy `db`.

3. `backup`
   - Use a Postgres-compatible image with `pg_dump` available, ideally matching the DB major version.
   - Runs a small shell loop:
     - `pg_dump --clean --if-exists -h db -U "$POSTGRES_USER" "$POSTGRES_DB" | gzip > /backups/hindsight-$(date +%Y%m%d-%H%M%S).sql.gz`
     - `find /backups -name 'hindsight-*.sql.gz' -mtime +$BACKUP_RETENTION_DAYS -delete`
     - sleep for `$BACKUP_INTERVAL_SECONDS`.
   - Mounts `${HINDSIGHT_BACKUP_DIR}:/backups`, defaulting in `hindsight/.env.example` to `${HOME}/.local/state/hindsight/backups`.
   - Uses `PGPASSWORD` from the same env source as the DB.

Add supporting local config:

- `hindsight/.env.example` with safe placeholders/defaults:
  - `HINDSIGHT_DB_USER=hindsight_user`
  - `HINDSIGHT_DB_PASSWORD=change-me`
  - `HINDSIGHT_DB_NAME=hindsight_db`
  - `HINDSIGHT_VERSION=latest`
  - `HINDSIGHT_API_LLM_PROVIDER=openai-codex`
  - optional `HINDSIGHT_API_LLM_MODEL=`
  - `HINDSIGHT_BACKUP_DIR=${HOME}/.local/state/hindsight/backups`
  - `BACKUP_INTERVAL_SECONDS=86400`
  - `BACKUP_RETENTION_DAYS=30`
- Update `.gitignore` to exclude:
  - `.env`
  - `hindsight/.env`

## Assumptions / Open Questions

- Decision: local backups under `${HOME}/.local/state/hindsight/backups` are sufficient for this setup. They protect against DB volume corruption, bad migrations, and accidental local DB changes, but not full disk loss.
- Assumption: using `latest` is acceptable initially; for stronger reproducibility, pin to a Hindsight release tag after first successful smoke test.
- Assumption: full Hindsight image is acceptable despite its larger size because it includes local embeddings/reranker and avoids extra providers.

## Ordered Tasks

1. Add `hindsight/docker-compose.yml` with `db`, `hindsight`, and `backup` services plus init scripts for `vector` and `schemas_with_pending_work()`.
2. Add `hindsight/.env.example` with non-secret defaults and placeholders.
3. Update `.gitignore` for `.env` and `hindsight/.env`.
4. Validate Compose syntax with a temporary `.env` or exported variables.
5. Use the verified Postgres 18 data path `/var/lib/postgresql/18/docker` for the DB volume when using `pgvector/pgvector:pg18`; if selecting a non-18 tag later, re-check that tag's `PGDATA`.
6. Start the stack with a local Codex auth file present.
7. Verify:
   - `docker compose ... ps` shows healthy/running services.
   - `curl http://localhost:8888/health` succeeds.
   - `curl http://localhost:9999` returns the Control Plane.
8. Force a backup interval or run the backup command manually once to prove a `.sql.gz` dump is created.
9. Test restore instructions against a disposable database/container or document the exact restore command after confirming it parses.

## Verification Checklist

- `docker compose --env-file hindsight/.env.example -f hindsight/docker-compose.yml config`
- `test -f "$HOME/.codex/auth.json"`
- `cd hindsight && docker compose up -d`
- `curl -fsS http://localhost:8888/health`
- `curl -fsS http://localhost:9999 >/dev/null`
- `ls -lh "$HINDSIGHT_BACKUP_DIR"/*.sql.gz`
- Restore command shape:
  - `gunzip -c "$HINDSIGHT_BACKUP_DIR"/<backup>.sql.gz | (cd hindsight && docker compose exec -T db psql -U "$HINDSIGHT_DB_USER" -d "$HINDSIGHT_DB_NAME")`

## Known Issues / Follow-ups

- If `${HOME}/.codex/auth.json` does not exist, run Codex CLI login on the host before starting Hindsight.
- Compose automatically reads `hindsight/.env` when run from the `hindsight/` directory. Use `hindsight/.env.example` only for syntax validation or as the template for creating local `hindsight/.env`.
- Local backup sidecar is not a substitute for off-machine backup. Remote sync is intentionally out of scope for this setup.
- For unattended recovery confidence, add a periodic restore smoke test into a disposable Postgres service later.
