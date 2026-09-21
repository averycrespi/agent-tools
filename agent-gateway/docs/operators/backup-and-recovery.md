# Backup, restore, and recovery

Audience: Operators responsible for Gateway recovery

Purpose: Create backups and perform restore or stopped-process recovery safely.

This guide owns Agent Gateway operator procedures for backup lifecycle, restore verification, administrator reset, stopped-process recovery, and uncertain failures. Use the current `agent-gateway` executable. Retirement preserves the existing process lock, installation identity, database and backup lineage. Renaming a current binary is not a restore or migration and does not bypass a running owner; old standalone binaries are unsupported. [Storage and recovery](../design/storage-and-recovery.md) owns normative compatibility, durability, and recovery semantics. Generated help owns exact syntax:

- `agent-gateway backup --help`
- `agent-gateway backup restore --help`
- `agent-gateway storage --help`
- `agent-gateway storage verify --help`
- `agent-gateway admin reset --help`

Gateway must be stopped for `backup restore`, `storage verify`, and `admin reset`. Backup list/get/create/delete require a running Gateway; restore remains offline and never acquires an online administrator bearer.

## Recovery command cutover

| Retired command                              | Replacement in the current executable               |
| -------------------------------------------- | --------------------------------------------------- |
| `restore --verify-current`                   | `storage verify`                                    |
| `restore BACKUP_ID --secret-output NEW_PATH` | `backup restore BACKUP_ID --secret-output NEW_PATH` |

The retired top-level `restore` and `--verify-current` flag have no execution aliases or completions. Update scripts and use the matching binary's help when rolling operator tooling back. This command-only cutover changes no database schema, backup metadata, credential/keyring identity, root, process lock, service argv, or installed executable path. Switching binaries does not reinitialize or recover an installation. Older binaries must still reject schemas newer than they support; never force a downgrade or edit durable metadata to make one work. Historical acceptance reports are not current qualification.

## Migrate existing invocation storage

This is a separate schema/storage cutover, not the command rename above. Existing
single-store installations cannot serve until explicitly migrated. Obtain consent
to stop the installation; disable its service supervisor and all other launchers.
Retain an existing verified backup and confirm the exact installation ID and root.
Do not run this procedure against a live user installation as a test.

```bash
agent-gateway storage migrate-traffic \
  --data-dir /path/to/gateway-data \
  --installation-id INSTALLATION_ID --confirm \
  --traffic-budget-bytes 4294967296
agent-gateway storage verify --data-dir /path/to/gateway-data \
  --traffic-budget-bytes 4294967296
```

Migration acquires the existing lock without creating storage, checks binding and
headroom, stages a control copy, validates every retained invocation and its
sequence/high-water, and publishes a generation-addressed traffic database. Only
then does one atomic control replacement select the matching traffic generation.
It preserves credentials, installation/keyring/service identity and historical IDs;
it never replays calls or fills missing terminal evidence. The prior control inode
is retained as `gateway.db.previous-*`; interrupted stages and old traffic remain.
These are recovery evidence, not automatically selected backups.

Released schema 17 is a supported single-store migration source; traffic selection
metadata is introduced only in schema 18. Run `storage verify` after migration,
not as a pre-migration schema upgrade. Do not manually remove `run.unclean`, the
installation lock, or SQLite WAL/SHM files to bypass a refusal.

Migration reports validation errors on stderr with exit 2, installation-in-use
with exit 5, and operational failure with exit 7. Failure does not establish that
no changes occurred. After interruption, retain all artifacts and inspect the selected control and
traffic bindings before deciding on another action. Failure before control
replacement leaves the original authoritative; failure afterward may mean the new
pair is selected despite an error. Never assume two file renames are atomic, delete
a stage to bypass a refusal, edit a selector, or fall back to empty traffic. Missing,
foreign or corrupt selected traffic fails closed. Diagnose and use an explicitly
selected verified backup where necessary. Restart with the same budget and restore
service supervision only after stopped verification succeeds. Fresh initialization
creates a matching pair directly and needs no legacy migration. If first-run setup
stops before any administrator credential exists, a deliberate `initialize` retry
completes or validates the pair before publishing authority, retaining abandoned
stages. Any historical administrator credential distinguishes an installed service
and prevents this first-run recovery path from backfilling legacy traffic.

## Create and manage backups

Create and inspect owner-only backup generations through the authenticated public control API:

```bash
agent-gateway backup create
agent-gateway backup list
agent-gateway backup get BACKUP_ID
agent-gateway backup delete BACKUP_ID --yes
```

Backup creation uses SQLite's online backup facility to capture a consistent pair.
Format-2 internal metadata binds the control selector, installation, schema and
revision to traffic generation, budget, sizes and SHA-256 digests. Admission and
writers pause only to pin stable snapshots, with a one-second acquisition ceiling;
overrun fails the backup. Copying then proceeds outside that fence with a 30-second
snapshot bound. Capacity/headroom refusal leaves healthy stores usable. Already
admitted work may finish after the snapshot, so missing completion stays unknown.
Both staged databases are verified before atomic directory publication. A backup contains safe durable Gateway state but no raw administrator bearer, agent bearer, keyring value, browser session, MCP session, runtime handle, or in-flight work.

Creation generates an idempotency key unless one is supplied. If the response is uncertain, retain the reported key and canonical `{}` digest and use a backup read before deciding whether deliberate same-tuple replay is necessary. The CLI never retries automatically. Deletion requires confirmation and read-before-retry recovery.

## Verify the current installation

After stopping every Gateway process that owns the installation, verify storage and clear a recoverable latch without replacing the database:

```bash
agent-gateway storage verify \
  --data-dir /path/to/gateway-data \
  --output json
```

`storage verify` accepts neither a backup ID nor `--secret-output`. Supply the
installation's `--traffic-budget-bytes` value when it differs from 4 GiB. It fully
validates the selected traffic generation as well as control storage, without
manufacturing missing traffic or requiring a persistent traffic-only latch. It acquires the exclusive process lock; verifies installation identity, schema and migration history, SQLite durability, size, and integrity; applies only recognized marker recovery; and clears the marker durably before success. Unknown, conflicting, oversized, foreign-installation, or failed recovery remains latched.

A recognized uncertain agent-credential candidate is cleared only when its principal, credential, and captured revisions are still current. The affected revisions advance once and no prior credential is restored. The command does not start Gateway; return ownership to the service before any online read:

```bash
agent-gateway serve --data-dir /path/to/gateway-data
```

## Restore a backup

While Gateway is running, use `backup list` and `backup get BACKUP_ID` to select a published backup from the same installation and inspect its schema, revision, size, and digest. Retain that exact ID; there is no implicit latest-backup selection. Stop every owner (and any service supervisor that would restart it), then select a fresh owner-only output path for replacement administrator authority. Obtain authorization before stopping a live service:

```bash
agent-gateway backup restore BACKUP_ID \
  --data-dir /path/to/gateway-data \
  --secret-output /safe/new/restored-admin-bearer
```

Restore verifies the artifact ID, installation binding, supported schema, source revision, size, digest, and full SQLite integrity. It accepts schemas 3 through the current schema 18, stages and immediately forward-migrates historical lineages, then revalidates authorization and grant-request semantics before atomically selecting only the current schema. There is no legacy-schema runtime or compatibility mode. Restore removes stale WAL/SHM sidecars; failure before selection leaves the original database generation authoritative. `storage verify` requires the current schema and validates the current generation rather than providing an obsolete-form migration path.

Format-2 restore verifies both stores before selecting a fresh traffic generation.
Accepted legacy single-database backups receive staged extraction; pre-invocation
schemas legitimately restore empty history. Restored traffic preserves IDs and
unknown outcomes but deliberately invalidates cursor continuity. No execution pin
or pending call is restored. Retain enough free space for original, staging and
rollback generations; the traffic database/WAL budget is not a total disk quota.

A successful restore preserves safe principals, grants, requests, request evidence, server configuration, and compatible history. It invalidates every restored agent credential, revokes restored administrator verifiers, and publishes one new administrator bearer to the required `--secret-output` file. Sessions, cursors, runtime state, OAuth transient state, and in-flight work do not resume.

Restore does not rewrite the default `admin-bearer`. Start the verified replacement generation, then explicitly select its replacement authority for online recovery:

```bash
agent-gateway serve --data-dir /path/to/gateway-data
# In another terminal:
agent-gateway --data-dir /path/to/gateway-data \
  status --admin-bearer-file /safe/new/restored-admin-bearer
```

Issue fresh agent credentials after reviewing restored principal and policy state.

## Reset administrator authority

With Gateway stopped, publish replacement administrator authority to a fresh path:

```bash
agent-gateway admin reset \
  --data-dir /path/to/gateway-data \
  --secret-output /safe/new/replacement-admin-bearer
```

A successful reset revokes every prior administrator bearer and activates the published replacement in one storage transaction. It does not rewrite the default `admin-bearer` or promote the replacement into that path. A failed secret publication activates nothing and leaves existing known authority valid. Start Gateway and select the replacement explicitly:

```bash
agent-gateway serve --data-dir /path/to/gateway-data
# In another terminal:
agent-gateway status --admin-bearer-file /safe/new/replacement-admin-bearer
```

Use reset for stopped-process all-authority recovery without replacing durable product state. Use online `admin credential rotate` for routine replacement-first rollover of one named administrator credential. Use `backup restore` only for a verified backup generation, and use `storage verify` only to validate and recover the current stopped generation. Neither offline command adds a confirmation prompt or supports `--yes`; restore requires an explicit backup ID and fresh `--secret-output`. Existing online backup deletion still requires consequence confirmation (`--yes` for automation).

## Structured results and exits

Both commands default to human output; `--output json` or `--json` selects one safe result on stdout. The old `operation:"restore"` and `mode:"verify_current"`/`mode:"backup"` projection is retired. `mode` is absent; operation is now unambiguous. The exact success members are:

```json
{"ok":true,"operation":"storage_verify","installation_id":"01ARZ3NDEKTSV4RRFFQ69G5FAV","revision":"0"}
{"ok":true,"operation":"backup_restore","installation_id":"01ARZ3NDEKTSV4RRFFQ69G5FAV","revision":"2","backup_id":"01ARZ3NDEKTSV4RRFFQ69G5FAW"}
```

IDs and decimal-string revisions above are illustrative. Verification omits `backup_id`; neither result contains secret values or paths. These are CLI-only projections: backup files and API backup representations retain their existing fields, as do durable audit category/action pairs.

Success exits 0. Invalid arguments/output/flags and unusable replacement sinks exit 2 (`client_invalid_input` / `secret_output_unavailable`); missing, invalid, corrupt, or foreign backups exit 4 (`invalid_backup`); a running owner exits 5 (`gateway_running`); other recovery/storage failures exit 7 (`storage_unavailable`). Output delivery failure exits 1 and can leave incomplete output after work already occurred. JSON problems have exactly `status` (null), `code`, `title`, `exit_code`, and `uncertain` (false for these offline problems). For example:

```json
{
  "status": null,
  "code": "gateway_running",
  "title": "The Gateway is running. Stop it before verifying current storage.",
  "exit_code": 5,
  "uncertain": false
}
```

Restore's corresponding title is `The Gateway is running. Stop it before restoring a backup.` The offline `uncertain:false` field is not proof of rollback: exit 7 or output loss can occur after generation installation or marker work. Nothing is replayed or compensated automatically.

## Failure handling

Failed commands leave stdout empty and emit one bounded human or JSON problem on stderr with a stable typed exit class.

- `gateway_running` means another process still owns the installation. Stop it; do not bypass the process lock.
- `secret_output_unavailable` means the one-time replacement sink was not completed. Do not assume new authority is active.
- Storage and verification failures intentionally omit filesystem, SQLite, and secret details. Preserve the original generation and diagnose the reported safe class.
- A post-handoff online backup result may be uncertain. Read before deliberate same-tuple replay.
- After an interrupted offline command, retain the selected backup ID, original generation evidence, and any replacement output securely. A pre-install restore failure leaves the original generation authoritative, but a failure after installation can leave the replacement selected with an unfinished audit attempt or marker cleanup. Do not assume an emitted bearer is active, an error rolled back, or a missing result permits replay. Diagnose the selected generation and recovery marker first; use separately authorized current-storage verification only for recognized recovery, then normal startup revalidates readiness. Do not automatically repeat restore, reset authority, or overwrite the replacement file.

Never copy a replacement bearer into arguments, environment variables, logs, or the old default file as a shortcut. Keep fresh secret outputs owner-only and remove obsolete bearer files after authority has been confirmed.

See [Administrator CLI and local administration](administration.md) for path resolution, output modes, and authentication selection. Return to the [documentation map](../README.md) or [Gateway README](../../README.md) for ordinary startup and status checks.
