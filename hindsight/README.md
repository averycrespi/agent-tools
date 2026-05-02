# Hindsight

Local Hindsight stack for agent memory.

## Prerequisites

- Docker Compose
- OpenAI Codex CLI authenticated on the host:

```bash
codex auth login
test -f "$HOME/.codex/auth.json"
```

## Configure

```bash
cp .env.example .env
```

Edit `.env` and change `HINDSIGHT_DB_PASSWORD` and `HINDSIGHT_API_KEY`.

Backups default to:

```bash
$HOME/.local/state/hindsight/backups
```

Override `HINDSIGHT_BACKUP_DIR` in `.env` if needed.

## Run

From this directory:

```bash
docker compose up -d
```

Endpoints are bound to localhost only:

- API: http://localhost:8888
- Control Plane: http://localhost:9999

The API requires bearer authentication using `HINDSIGHT_API_KEY`. The Control Plane is configured to use that key when it calls the API, but it is not a browser login; keep the localhost-only binding unless you add a browser-facing auth layer.

Check status:

```bash
set -a
. ./.env
set +a
docker compose ps
curl -fsS -H "Authorization: Bearer $HINDSIGHT_API_KEY" http://localhost:8888/health
```

## Clients

Configure local Hindsight clients with the same API URL and key:

```bash
set -a
. ./.env
set +a
export HINDSIGHT_API_URL=http://localhost:8888
export HINDSIGHT_API_KEY
```

## Backups

The `backup` service writes compressed `pg_dump` files to `HINDSIGHT_BACKUP_DIR` on `BACKUP_INTERVAL_SECONDS` and deletes files older than `BACKUP_RETENTION_DAYS`. Dumps include `--clean --if-exists` so they can replace objects in the target database during restore.

List backups:

```bash
set -a
. ./.env
set +a
ls -lh "$HINDSIGHT_BACKUP_DIR"/*.sql.gz
```

## Troubleshooting

### `schemas_with_pending_work() does not exist`

New databases install this helper from `initdb/02-worker-poller.sql`. If the database volume already existed before that file was added, apply it manually:

```bash
set -a
. ./.env
set +a
docker compose exec -T db psql -U "$HINDSIGHT_DB_USER" -d "$HINDSIGHT_DB_NAME" < initdb/02-worker-poller.sql
```

## Restore

Stop Hindsight before restoring into the active database. Restore will drop and recreate dumped database objects.

```bash
docker compose stop hindsight backup
```

Restore a backup:

```bash
set -a
. ./.env
set +a
gunzip -c "$HINDSIGHT_BACKUP_DIR"/<backup>.sql.gz | docker compose exec -T db psql -U "$HINDSIGHT_DB_USER" -d "$HINDSIGHT_DB_NAME"
```

Restart services:

```bash
docker compose up -d
```
