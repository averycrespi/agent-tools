# Backup, restore, and recovery

Audience: Operators responsible for Gateway recovery

Purpose: Create backups and perform restore or stopped-process recovery safely.

This guide owns backup lifecycle, restore verification, administrator reset, stopped-process requirements, compatibility, and uncertain recovery failures. Generated help owns exact syntax:

- `mcp-gateway backup --help`
- `mcp-gateway restore --help`
- `mcp-gateway admin reset --help`

Gateway must be stopped for `restore`, `restore --verify-current`, and `admin reset`. Online backup commands require a running Gateway. The legacy hyphenated spelling has no alias and performs no work.

## Create and manage backups

Create and inspect owner-only backup generations through the authenticated public control API:

```bash
mcp-gateway backup create
mcp-gateway backup list
mcp-gateway backup get BACKUP_ID
mcp-gateway backup delete BACKUP_ID --yes
```

Backup creation uses SQLite's online backup facility, integrity-checks the staged database, records installation, schema, source-revision, size, and SHA-256 metadata, then publishes atomically. A backup contains safe durable Gateway state but no raw administrator bearer, agent bearer, keyring value, browser session, MCP session, runtime handle, or in-flight work.

Creation generates an idempotency key unless one is supplied. If the response is uncertain, retain the reported key and canonical `{}` digest and use a backup read before deciding whether deliberate same-tuple replay is necessary. The CLI never retries automatically. Deletion requires confirmation and read-before-retry recovery.

## Verify the current installation

After stopping every Gateway process that owns the installation, verify storage and clear a recoverable latch without replacing the database:

```bash
mcp-gateway restore --verify-current \
  --data-dir /path/to/gateway-data \
  --output json
```

`--verify-current` forbids `--secret-output`. It acquires the exclusive process lock; verifies installation identity, schema and migration history, SQLite durability, size, and integrity; applies only recognized marker recovery; and clears the marker durably before success. Unknown, conflicting, oversized, foreign-installation, or failed recovery remains latched.

A recognized uncertain agent-credential candidate is cleared only when its principal, credential, and captured revisions are still current. The affected revisions advance once and no prior credential is restored. The command does not start Gateway; return ownership to the service before any online read:

```bash
mcp-gateway serve --data-dir /path/to/gateway-data
```

## Restore a backup

Stop Gateway, choose one published generation, and prepare a fresh owner-only output path for replacement administrator authority:

```bash
mcp-gateway restore BACKUP_ID \
  --data-dir /path/to/gateway-data \
  --secret-output /safe/new/restored-admin-bearer
```

Restore verifies the artifact ID, installation binding, supported schema, source revision, size, digest, and full SQLite integrity. It stages and immediately forward-migrates accepted schema lineages, then revalidates authorization and grant-request semantics before atomically selecting only the current schema. There is no legacy-schema runtime or compatibility mode. Restore removes stale WAL/SHM sidecars; failure before selection leaves the original database generation authoritative. `restore --verify-current` validates the current generation rather than providing an obsolete-form migration path.

A successful restore preserves safe principals, grants, requests, request evidence, server configuration, and compatible history. It invalidates every restored agent credential, revokes restored administrator verifiers, and publishes one new administrator bearer to the required `--secret-output` file. Sessions, cursors, runtime state, OAuth transient state, and in-flight work do not resume.

Restore does not rewrite the default `admin-bearer`. Start the verified replacement generation, then explicitly select its replacement authority for online recovery:

```bash
mcp-gateway serve --data-dir /path/to/gateway-data
# In another terminal:
mcp-gateway --data-dir /path/to/gateway-data \
  status --admin-bearer-file /safe/new/restored-admin-bearer
```

Issue fresh agent credentials after reviewing restored principal and policy state.

## Reset administrator authority

With Gateway stopped, publish replacement administrator authority to a fresh path:

```bash
mcp-gateway admin reset \
  --data-dir /path/to/gateway-data \
  --secret-output /safe/new/replacement-admin-bearer
```

A successful reset revokes every prior administrator bearer and activates the published replacement in one storage transaction. It does not rewrite the default `admin-bearer` or promote the replacement into that path. A failed secret publication activates nothing and leaves existing known authority valid. Start Gateway and select the replacement explicitly:

```bash
mcp-gateway serve --data-dir /path/to/gateway-data
# In another terminal:
mcp-gateway status --admin-bearer-file /safe/new/replacement-admin-bearer
```

Use reset for stopped-process all-authority recovery without replacing durable product state. Use online `admin credential rotate` for routine replacement-first rollover of one named administrator credential. Use restore only for a verified backup generation, and use `--verify-current` only to validate and recover the current stopped generation.

## Failure handling

Failed commands leave stdout empty and emit one bounded human or JSON problem on stderr with a stable typed exit class.

- `gateway_running` means another process still owns the installation. Stop it; do not bypass the process lock.
- `secret_output_unavailable` means the one-time replacement sink was not completed. Do not assume new authority is active.
- Storage and verification failures intentionally omit filesystem, SQLite, and secret details. Preserve the original generation and diagnose the reported safe class.
- A post-handoff online backup result may be uncertain. Read before deliberate same-tuple replay.

Never copy a replacement bearer into arguments, environment variables, logs, or the old default file as a shortcut. Keep fresh secret outputs owner-only and remove obsolete bearer files after authority has been confirmed.

See [CLI and local administration](cli-local-administration.md) for path resolution, output modes, and authentication selection. See [DESIGN](../DESIGN.md) for normative storage, backup, migration, authority-invalidation, and failure semantics. Return to the [Gateway README](../README.md) for ordinary startup and status checks.
