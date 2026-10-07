# Backup, restore, and recovery

Audience: Operators responsible for Gateway recovery

Purpose: Create backups and perform restore or stopped-process recovery safely.

This guide owns Agent Gateway operator procedures for backup lifecycle, restore verification, administrator reset, stopped-process recovery, and uncertain failures. [Installation safety](installation-safety.md) owns executable retirement and retained identities. Renaming a binary never bypasses the installation lock. [Storage and recovery](../design/storage-and-recovery.md) owns normative compatibility, durability, and recovery semantics. Generated help owns exact syntax:

- `agent-gateway backup --help`
- `agent-gateway history --help`
- `agent-gateway maintenance restore-backup --help`
- `agent-gateway maintenance --help`
- `agent-gateway maintenance verify-and-recover-storage --help`
- `agent-gateway maintenance reset-admin-credentials --help`

Gateway must be stopped for `maintenance restore-backup`, `maintenance verify-and-recover-storage`, and `maintenance reset-admin-credentials`. Backup list/get/create/delete require a running Gateway; restore remains offline and never acquires an online administrator bearer.

## Encrypted secret storage

New `init` installations provision `<data-dir>/master-key` as a random, exclusive 0600 file under the 0700 data root. New MCP static/OAuth, HTTP/Git and CA material is authenticated-encrypted in control SQLite, never traffic history. Administrator and agent tokens remain hash-only, and the separate protected `admin-bearer` file is retained.

For an existing installation, stop Gateway and all launchers, then inspect and approve setup without reinitialization:

```bash
agent-gateway maintenance setup-secret-storage --data-dir /path/to/gateway-data --dry-run
agent-gateway maintenance setup-secret-storage --data-dir /path/to/gateway-data --confirm
```

This creates or verifies the master key and binds its identity; it does not migrate native entries, rotate credentials, start services or change trust. Existing records explicitly marked legacy may still read native storage. New writes require setup and use database custody. Selected missing/corrupt encrypted records never fall back to old native values. Database-backed restart does not require native credentials.

Preserve the key and database together. Do not delete, replace, chmod, symlink or regenerate a refused key to bypass validation. A missing/wrong established key, partial key file or uncertain setup needs diagnosis and a separately qualified recovery plan; setup never overwrites it. Encryption protects against database-only theft, not theft of both key and database, or compromise of the Gateway account/root.

Encrypted installations create **format-4** control backups. Safeguard a separate owner-only copy of the matching `master-key` outside the backup artifact and protect it independently. `metadata.json` identifies the required key by its SHA-256 `master_key_id`, never by its value. Losing that key makes its encrypted backups unrecoverable; setup cannot regenerate it. Historical backups retain historical upstream secrets even after online deletion or rotation. Protect their custody and retention accordingly.

Recovery requires the existing installation, its closed current control database and the matching key at `<data-dir>/master-key`. It is not fresh-install import, cloning or host relocation. Do not manually replace the current database with an older copy: its nonrefundable encryption high-water is required for safe continued use of the key. Missing/wrong keys, corruption and incomplete encrypted generations refuse before replacement. Unresolved legacy dependencies or in-progress credential cutovers refuse encrypted backup creation; complete the supported credential operation before trying a new backup. Native migration is a separate operation, not part of backup.

Formats 0, 2 and 3 remain legacy-only recovery, not self-contained secret backups. They cannot restore over encrypted custody, even with `--security-only`; an old encrypted database copy without format-4 metadata is also rejected. `encrypted_backup_unsupported` explains this boundary. Preserve old artifacts rather than relabeling them. Unsupported formats fail closed.

## Choose a recovery task

| Task                                                         | Service state | Procedure                                                                   |
| ------------------------------------------------------------ | ------------- | --------------------------------------------------------------------------- |
| Capture or inspect a backup                                  | Running       | [Create and manage backups](#create-and-manage-backups)                     |
| Verify selected storage or recover a recognized latch        | Stopped       | [Verify the current installation](#verify-the-current-installation)         |
| Replace durable state from an exact backup                   | Stopped       | [Restore a backup](#restore-a-backup)                                       |
| Replace all administrator authority, retaining product state | Stopped       | [Reset administrator authority](#reset-administrator-authority)             |
| Extract legacy single-store history                          | Stopped       | [Migrate existing invocation storage](#migrate-existing-invocation-storage) |
| Create or replace interception signing authority             | Stopped       | [CA commands](#stopped-interception-ca-commands)                            |

Stop service supervisors as well as Gateway before offline work; obtain authorization before disrupting a live service. Unknown outcomes require inspection, not replay.

All four `maintenance` operations support `--dry-run`, `--confirm`, and `--json`. A dry run takes existing stopped ownership, inspects identity, closed storage, recognized marker actions and applicable backup metadata, and writes no installation files, audit records or recovery markers. Nonempty WAL/journal state in an operation's inspected targets prevents a proven immutable read: preserve it and obtain a qualified WAL-aware recovery plan, never delete it or checkpoint it merely to make a preview pass. Execution shows the target and consequences, requires default-no interactive consent or explicit noninteractive `--confirm`, then revalidates the plan under the same lock. Dry-run output is not later authority. Unknown recovery actions refuse. No confirmation bypasses safety checks.

## Optional history and serving

Gateway starts security-ready serving while one owned history opener validates the
selected generation. Status reports history as `opening`, `ready`, `unavailable`,
`faulted`, or `disabled`; API/MCP admission is independent. A NULL selector disables
capture and preserves legacy rows until explicit stopped migration. Control database,
mandatory audit, security markers and malformed selectors still fail closed.

Unavailable artifacts are retained without replacement or permission repair. Serving
refuses nonempty history WAL/journals rather than silently repairing them; preserve
those files for a qualified stopped WAL-aware plan. Do not edit selectors or remove
sidecars to make history open. Security-only recovery does not inspect these files.
Existing configuration, grants and credentials survive the lifecycle upgrade.

Shutdown may report unconfirmed cleanup while still retaining the opener/writer and
installation lock. Wait for actual settlement; a timeout does not cancel fsync or
permit another owner. This is not isolation from shared-filesystem stalls,
uninterruptible I/O or physical disk exhaustion. Security backup and restore do
not require ready history.

## Create and manage backups

Create and inspect owner-only backup generations through the authenticated public control API:

```bash
agent-gateway backup create
agent-gateway backup list
agent-gateway backup get BACKUP_ID
agent-gateway backup delete BACKUP_ID --yes
```

New encrypted backups use **format 4** (legacy-only installations still use format 3), explicitly reporting `history:"omitted"`. They capture
configuration, authority and administrative audit from one control-only SQLite
snapshot, without pausing or opening optional traffic history. Embedded legacy
invocation rows are removed from the private copy and the copy is compacted; the
live installation is not rewritten. Creation has a 30-second cooperative snapshot
bound and reserves up to 4 GiB for bounded control staging, never the traffic budget.
Capacity refusal leaves existing authority intact. The closed control copy is
verified and digested before atomic publication. Format 4 includes authenticated encrypted MCP static/OAuth, HTTP/Git credentials and CA signing material, plus the public CA certificate. The master key, raw administrator/agent bearers, plaintext upstream secrets, browser/MCP sessions, runtime handles and in-flight work are excluded.

Existing format-0 single-store and format-2 paired artifacts keep their original
meaning. Full verification still includes their claimed history. They are never
silently converted, rewritten or declared valid when a payload is missing.

Startup and `backup list` discover bounded metadata only; a list entry does not
certify payload integrity. `backup get`, delete, idempotent creation and restore
fully verify the selected artifact. Damaged historical payloads do not prevent
serving. Malformed or unsafe inventory makes backup administration unavailable,
never an empty inventory or permission to delete artifacts.

Creation generates an idempotency key unless one is supplied. If the response is uncertain, retain the reported key and canonical `{}` digest and use a backup read before deciding whether deliberate same-tuple replay is necessary. The CLI never retries automatically. Deletion requires confirmation and read-before-retry recovery.

## Export optional history

History is separate from security backups. Use the authenticated read-only command:

```bash
agent-gateway history export --json
agent-gateway history export --after-sequence 123 --limit 100 --json
```

For a browser file, use **System → Backups → Traffic history export → Download JSON**.
The always-visible card is separate from the complete Control-plane backups card.
It reads all records retained at the first page's high-water, excluding later arrivals,
then hands one JSON file to your browser. Progress reports actual received records,
pages and bytes. Cancel, navigation and sign-out stop the export. Backup controls
remain independently usable while reading or after a history failure. The file can
contain identifying data; choose private storage and do not share it indiscriminately.

Browser traversal is limited to 64 MiB of response data, two minutes and 4,096 pages;
each request is limited to five seconds and each response to 900 KiB. These are safety
limits, not a promise that every retained history fits. Changed generation/pruning,
invalid coverage, interruption, read failure or a limit stops the export with **no
partial file**. The filename contains only a timestamp, not installation or record IDs.
The file's `agent-gateway-retained-traffic-v1` envelope retains original JSON pages,
including number tokens, plus initial/final coverage and non-atomicity metadata.

If the browser limit is reached, use the bounded CLI commands above to retain
separate page files. Redirect each response to a new protected file using your shell's
no-clobber and owner-only permissions. Record the first high-water and generation;
advance `--after-sequence` only to the preceding response's `next_sequence`, checking
pruning/generation and forward progress each time. Stop on a change, error or stall,
and stop once the initial high-water is reached rather than chasing arrivals. The CLI
reads a new bounded snapshot each time and can include newer arrivals in the final
page: keep that coverage visible, never concatenate pages and label them an atomic or
complete audit. It does not currently automate a full-history file. Neither interface
repairs or removes history.

Each response identifies generation, capture time, shared high-water/pruning, retained
count, returned records, next sequence and truncation. It contains at most 256 total
MCP/HTTP/Git records and 900 KiB from one read transaction. A later request is a **new
snapshot**, not continuation of the previous transaction; compare generation and
coverage before combining output. Missing records do not prove nonexecution, missing
completion is unknown, and no export is a complete traffic audit. Unavailable history
returns `history_unavailable`, not empty success; occupied capacity or an expired read
returns `history_busy`. Neither failure blocks security backup/restore or forwarding.

## Verify the current installation

After stopping every Gateway process that owns the installation, verify storage and clear a recoverable latch without replacing the database:

```bash
agent-gateway maintenance verify-and-recover-storage \
  --data-dir /path/to/gateway-data \
  --json --confirm
```

`maintenance verify-and-recover-storage` accepts neither a backup ID nor `--secret-output`.
It verifies the installation's control store for security recovery. The compatibility
`--traffic-budget-bytes` option does not cause history inspection. Neither this
operation nor administrator reset reads or repairs untouched optional history,
even if it is missing, corrupt or has nonempty WAL. Consent binds control state,
its selector and security markers. It acquires the exclusive process lock; verifies installation identity, schema and migration history, SQLite durability, size, and integrity; applies only recognized marker recovery; and clears the marker durably before success. Unknown, conflicting, oversized, foreign-installation, or failed recovery remains latched.

A recognized uncertain agent-credential candidate is cleared only when its agent, credential, and captured revisions are still current. The affected revisions advance once and no prior credential is restored. The command does not start Gateway; return ownership to the service before any online read:

```bash
agent-gateway serve --data-dir /path/to/gateway-data
```

## Restore a backup

While Gateway is running, use `backup list` and `backup get BACKUP_ID` to select a published backup from the same installation and inspect its schema, revision, size, and digest. Retain that exact ID; there is no implicit latest-backup selection. Stop every owner (and any service supervisor that would restart it), then select a fresh owner-only output path for replacement administrator authority. Obtain authorization before stopping a live service:

```bash
agent-gateway maintenance restore-backup BACKUP_ID \
  --data-dir /path/to/gateway-data \
  --secret-output /safe/new/restored-admin-bearer
```

Format-4 restore recovers the exact backed-up CA certificate and signing identity; it never automatically replaces the CA. Existing clients trusting that certificate need no trust change. If trust changed after the backup, explicitly reconcile it against the recovered certificate. The managed `http-ca.pem` export is updated after database replacement only if absent or matching the previously selected certificate; unrelated files refuse before replacement. A crash or publication error can leave the database selected while the derived PEM is stale: inspect `http ca export --stdout` and the reported effect, never assume rollback or replay restore. Client trust and installed proxy settings are never mutated.

Legacy-only restore still invalidates CA and HTTP/Git credential authority. It requires explicit CA replacement and client trust updates before interception; use `serve --clear-http-proxy-listen` for MCP-only recovery. Surviving retired native keys are not restored authority. See [stopped CA management](#stopped-interception-ca-commands) and [proxy activation](http-proxy.md).

Restore verifies artifact ID, installation, supported schema, revision, size, digest,
SQLite integrity and staged authority. Formats 3 and 4 restore security only and disable
history capture without selecting any retained traffic file. Current control and
artifact control must be closed; nonempty control WAL/journals, unknown security
markers, unsafe mutation targets and conflicting stages refuse. Consent binds the
actual control state (including selector), markers and selected artifact, not untouched
optional history or unrelated retained artifacts. Existing history, links and journals
are preserved without validation, repair or cleanup.

Legacy schemas 3 through current schema 23 are supported through staged forward migration
and full authorization/grant-request validation. Failure before selection leaves the
original control authoritative. There is no legacy-schema runtime. Ordinary upgrade
preserves configuration, grants and credentials; restore deliberately invalidates
restored authority.

For an explicit security-only import of an old artifact:

```bash
agent-gateway maintenance restore-backup BACKUP_ID --security-only \
  --data-dir /path/to/gateway-data \
  --secret-output /safe/new/restored-admin-bearer --dry-run
```

The plan reports `history:"omitted-not-verified"`, including when paired history is
missing or corrupt. This is not a whole-pair-valid claim. Original artifacts are
unchanged; embedded history is omitted only in bounded control staging. Execute the
same command without `--dry-run` after inspecting it, with confirmation as usual.
Capture stays disabled until an explicit stopped migration creates/selects a new
history generation; old unrelated history is never automatically adopted.

Without `--security-only`, format-0/format-2 restore retains full history verification,
closed-history evidence and fresh-generation extraction/restoration. Missing/corrupt
claimed history refuses. Pre-invocation schemas legitimately restore empty history.
IDs and unknown outcomes are preserved but cursor continuity changes. Security-only
staging reserves 4 GiB independently of traffic; history-inclusive legacy restore
additionally reserves the traffic stage. Originals and interrupted stages remain
recovery evidence, not permission to delete them or retry blindly.

A successful restore preserves safe agents, grants, requests, request evidence, server configuration and administrative audit; optional history follows the explicit plan. It invalidates every restored agent credential, revokes restored administrator verifiers, and publishes one new administrator bearer to the required `--secret-output` file. Sessions, cursors, runtime state, OAuth transient state, and in-flight work do not resume.

Restore does not rewrite or promote into the default `admin-bearer`. That retained file is **invalid**, not usable recovery access: every old administrator and agent bearer is rejected, including credentials issued after the backup. Select `--admin-bearer-file` explicitly for every online recovery command; ordinary default-file authentication must fail. The fresh protected sink is published before installation, so a pre-install failure can leave an output file whose bearer is not active. Start the verified replacement generation (MCP-only if recovering legacy custody), then explicitly select its replacement authority:

```bash
agent-gateway serve --clear-http-proxy-listen --data-dir /path/to/gateway-data
# In another terminal:
agent-gateway --data-dir /path/to/gateway-data \
  doctor --online --admin-bearer-file /safe/new/restored-admin-bearer
```

Issue fresh agent credentials after reviewing restored agent and policy state. Format-4 upstream material is restored **as of backup time**, not proven valid upstream: expiration, revocation and refresh-token rotation can require reauthentication. Recovery performs no upstream calls or retries.

## Reset administrator authority

With Gateway stopped, publish replacement administrator authority to a fresh path:

```bash
agent-gateway maintenance reset-admin-credentials \
  --data-dir /path/to/gateway-data \
  --secret-output /safe/new/replacement-admin-bearer
```

A successful reset revokes every prior administrator bearer and activates the published replacement in one storage transaction. It does not rewrite the default `admin-bearer` or promote the replacement into that path. A failed secret publication activates nothing and leaves existing known authority valid. Start Gateway and select the replacement explicitly:

```bash
agent-gateway serve --data-dir /path/to/gateway-data
# In another terminal:
agent-gateway doctor --online --admin-bearer-file /safe/new/replacement-admin-bearer
```

Use reset for stopped-process all-authority recovery without replacing durable product state. Use online `admin credential rotate` for routine replacement-first rollover of one named administrator credential. Use `maintenance restore-backup` only for a verified backup generation, and use `maintenance verify-and-recover-storage` only to validate and recover the current stopped generation. Maintenance uses `--confirm`, not `--yes`; restore requires an explicit backup ID and fresh `--secret-output`. Inspect first with `--dry-run`. Without `--confirm`, noninteractive execution refuses without prompting. Existing online backup deletion still requires consequence confirmation (`--yes` for automation).

## Stopped interception CA commands

See `agent-gateway http ca --help`. Disable every service launcher and stop Gateway
before using these commands. They require an existing installation and exclusive
process ownership. `--installation-id` is an optional identity assertion.
They do not enable the proxy, install trust, or export a private key.

```bash
agent-gateway init --data-dir /path/to/gateway-data --confirm
agent-gateway http ca export --data-dir /path/to/gateway-data \
  --output /safe/path/gateway-ca.pem
# Explicit rotation, unrecoverable key loss, or after legacy-only restore:
agent-gateway http ca replace --data-dir /path/to/gateway-data \
  --installation-id ID --confirm
```

`http ca create` is removed. `init` owns first creation and never replaces an existing or restored CA.
`replace` supports verified absence or selects new protected signing material and a new public certificate.
Creation/replacement writes `<data-dir>/http-ca.pem` and reports its SHA-256 fingerprint.
Replacement updates that managed file only when it matches the previously selected certificate; unrelated files are retained and publication failure is reported separately from authority change. Explicitly update client trust before interception. Ordinary restart never calls either mutation. New signing material requires configured encrypted secret custody; only explicitly legacy CA reads use the native keyring. There is no plaintext or stale-generation fallback.

`export` writes `<data-dir>/http-ca.pem` by default, accepts `--output PATH` as a file destination, or streams only public PEM with `--stdout`. Identical re-export is safe; different existing files and links are refused. `--json` selects structured results, not the certificate destination; it cannot be combined with `--stdout`. Export never reads the keyring. Its success
is not proof that signing material is available or that client trust is installed;
restored historical public metadata can still be exported. Errors are bounded,
redacted stderr with typed exits: usage 2, installation in use 5, unavailable 7.
A mutation failure or lost output can follow authority fencing or replacement:
inspect before another deliberate attempt; no automatic retry or rollback occurs.

## Migrate existing invocation storage

This is a separate schema/storage cutover, not the command rename above. Existing
single-store installations can serve security-ready with capture disabled until explicitly migrated. Obtain consent
to stop the installation; disable its service supervisor and all other launchers.
Retain an existing verified backup and confirm the exact installation ID and root.
Do not run this procedure against a live user installation as a test.

```bash
agent-gateway maintenance migrate-traffic-storage \
  --data-dir /path/to/gateway-data \
  --installation-id INSTALLATION_ID --confirm \
  --traffic-budget-bytes 4294967296
agent-gateway maintenance verify-and-recover-storage --data-dir /path/to/gateway-data \
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
metadata is introduced only in schema 18. Run `maintenance verify-and-recover-storage` after migration,
not as a pre-migration schema upgrade. Do not manually remove `run.unclean`, the
installation lock, or SQLite WAL/SHM files to bypass a refusal.

Migration reports validation errors on stderr with exit 2, installation-in-use
with exit 5, and operational failure with exit 7. Failure does not establish that
no changes occurred. After interruption, retain all artifacts and inspect the selected control and
traffic bindings before deciding on another action. Failure before control
replacement leaves the original authoritative; failure afterward may mean the new
pair is selected despite an error. Never assume two file renames are atomic, delete
a stage to bypass a refusal, edit a selector, or fall back to empty traffic. Missing,
foreign or corrupt selected traffic disables optional history, not security-ready
serving. Diagnose and use an explicitly
selected verified backup where necessary. Restart with the same budget and restore
service supervision only after stopped verification succeeds. Fresh initialization
creates a matching pair directly and needs no legacy migration. If first-run setup
stops before any administrator credential exists, a deliberate `init --confirm` attempt
completes or validates the pair before publishing authority, retaining abandoned
stages. Any historical administrator credential distinguishes an installed service
and prevents this first-run recovery path from backfilling legacy traffic.

## Failure handling

Failed commands leave stdout empty and emit one bounded human or JSON problem on stderr with a stable typed exit class. A displayed plan can precede that problem; in JSON mode, parse stderr as JSON lines rather than one object.

- `gateway_running` means another process still owns the installation. Stop it; do not bypass the process lock.
- `secret_output_unavailable` means the one-time replacement sink was not completed. Do not assume new authority is active.
- Storage and verification failures intentionally omit filesystem, SQLite, and secret details. Preserve the original generation and diagnose the reported safe class.
- A post-handoff online backup result may be uncertain. Read before deliberate same-tuple replay.
- After an interrupted offline command, retain the selected backup ID, original generation evidence, and any replacement output securely. A pre-install restore failure leaves the original generation authoritative, but a failure after installation can leave the replacement selected with an unfinished audit attempt or marker cleanup. Do not assume an emitted bearer is active, an error rolled back, or a missing result permits replay. Diagnose the selected generation and recovery marker first; use separately authorized current-storage verification only for recognized recovery, then normal startup revalidates readiness. Do not automatically repeat restore, reset authority, or overwrite the replacement file.

Never copy a replacement bearer into arguments, environment variables, logs, or the old default file as a shortcut. Keep fresh secret outputs owner-only and remove obsolete bearer files after authority has been confirmed.

See [Administrator CLI and local administration](administration.md) for path resolution, output modes, and authentication selection. Return to the [documentation map](../README.md) or [Gateway README](../../README.md) for ordinary startup and status checks.

## Structured results and exits

Maintenance defaults to human output; `--json` selects one safe result on stdout. Execution plans and errors use stderr; dry-run plans are results on stdout. The old `operation:"restore"` and `mode:"verify_current"`/`mode:"backup"` projection is retired. `mode` is absent; operation is now unambiguous. The exact success members are:

```json
{"ok":true,"operation":"verify-and-recover-storage","installation_id":"01ARZ3NDEKTSV4RRFFQ69G5FAV","revision":"0"}
{"ok":true,"operation":"restore-backup","installation_id":"01ARZ3NDEKTSV4RRFFQ69G5FAV","revision":"2","backup_id":"01ARZ3NDEKTSV4RRFFQ69G5FAW","history":"omitted-not-verified"}
```

IDs and decimal-string revisions above are illustrative. Verification omits `backup_id` and `history`; neither result contains secret values or paths. History-inclusive legacy restore reports `history:"restored"`. New backup metadata/API representations add `history:"omitted"`; old artifacts omit that member. Durable audit category/action pairs are unchanged.

Success exits 0. Invalid arguments/output/flags and unusable replacement sinks exit 2 (`client_invalid_input` / `secret_output_unavailable`); missing, invalid, corrupt, or foreign backups exit 4 (`invalid_backup`); a running owner exits 5 (`gateway_running`); other recovery/storage failures exit 7 (including `storage_latched`, `inspection_unavailable`, or `maintenance_unavailable`). Output delivery failure exits 1 and can leave incomplete output after work already occurred. JSON problems have exactly `status` (null), `code`, `title`, `exit_code`, and `uncertain` (true when the owner cannot establish a mutation or selection outcome). For example:

```json
{
  "status": null,
  "code": "gateway_running",
  "title": "No maintenance changes made. Stop the selected installation and its launchers first. Next: agent-gateway doctor --data-dir /path/to/gateway-data",
  "exit_code": 5,
  "uncertain": false
}
```

Error titles distinguish unchanged authority, retained staging, completed selection with a later failure, and uncertain mutation. An output-delivery failure can still occur after completed work; never infer rollback from missing output. Nothing is replayed or compensated automatically.

## Recovery command cutover

| Retired command                                   | Replacement in the current executable                           |
| ------------------------------------------------- | --------------------------------------------------------------- |
| `storage verify` or `restore --verify-current`    | `maintenance verify-and-recover-storage`                        |
| `admin reset`                                     | `maintenance reset-admin-credentials --secret-output NEW_PATH`  |
| `backup restore BACKUP_ID` or `restore BACKUP_ID` | `maintenance restore-backup BACKUP_ID --secret-output NEW_PATH` |
| `storage migrate-traffic`                         | `maintenance migrate-traffic-storage --installation-id ID`      |

The retired top-level `restore` and `--verify-current` flag have no execution aliases or completions. Update scripts and use the matching binary's help when rolling operator tooling back. This command-only cutover changes no database schema, backup metadata, credential/keyring identity, root, process lock, service argv, or installed executable path. Switching binaries does not reinitialize or recover an installation. Older binaries must still reject schemas newer than they support; never force a downgrade or edit durable metadata to make one work. Historical acceptance reports are not current qualification.
