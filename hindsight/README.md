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

Edit `.env` and change `HINDSIGHT_DB_PASSWORD`.

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

Endpoints:

- API: http://localhost:8888
- Control Plane: http://localhost:9999

Check status:

```bash
docker compose ps
curl -fsS http://localhost:8888/health
```

## Backups

The `backup` service writes compressed `pg_dump` files to `HINDSIGHT_BACKUP_DIR` on `BACKUP_INTERVAL_SECONDS` and deletes files older than `BACKUP_RETENTION_DAYS`.

List backups:

```bash
set -a
. ./.env
set +a
ls -lh "$HINDSIGHT_BACKUP_DIR"/*.sql.gz
```

## Restore

Stop Hindsight before restoring into the active database:

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
