# Recovery and fresh initialization

Audience: Operators responsible for Gateway recovery

Purpose: Preserve installation evidence, recover recognized stopped state, and create fresh installations from portable configuration.

[Storage and recovery](../design/storage-and-recovery.md) owns normative durability and recovery semantics. [Installation safety](installation-safety.md) owns root selection and retained identities. Renaming a binary never bypasses the installation lock. Generated help owns exact syntax:

- `agent-gateway export config --help`
- `agent-gateway init --help`
- `agent-gateway history --help`
- `agent-gateway http ca --help`
- `agent-gateway maintenance --help`
- `agent-gateway maintenance reset-admin-credentials --help`

## Backup and restore removal

Installation-snapshot backup and restore support is deliberately removed. The backup create/list/get/delete commands, stopped restore command, restore-only flags, APIs and browser controls have no aliases or compatibility endpoints. This binary does not verify or restore historical backup formats. There is no migration layer or transition procedure.

**Existing artifacts are untouched.** Startup, initialization/resume, upgrades and ordinary status do not traverse, validate, rewrite, move, chmod or delete historical backup inventory or restore-specific residue. Retain `backups/`, `gateway.db.restore` and its sidecars/markers, and any other historical artifacts. Malformed inventory does not gate normal readiness or status. This is not permission to remove unknown markers: historical control mutation intents, initialization evidence, `run.unclean`, rotation bundles and native cleanup provenance retain their actual safety owners and fail-closed rules.

The replacement for portable configuration is export plus fresh initialization:

```bash
(umask 077; agent-gateway export config > gateway-config.json)
agent-gateway init --from gateway-config.json --data-dir /new/empty/gateway --confirm
```

See [configuration export and import](administration.md) for the full contract. Export contains definitions only, not installation authority, credentials, CA signing identity, history or in-flight work. Import requires an absent or empty selected directory, creates fresh authority and CA identity, leaves secrets unset and requires explicit upstream activation. Source tokens and CA trust do not transfer. It is not installation recovery, cloning, merge or rollback. Never run init over an established installation to bypass a refusal.

Historical encrypted artifacts can retain secrets retired from current use. Protect their keys and permissions; removal of executable support does not erase that sensitive material. Do not manually roll back the current database or key: nonrefundable encryption usage must remain monotonic.

## Choose a recovery task

| Task                                                         | Service state  | Procedure                                                       |
| ------------------------------------------------------------ | -------------- | --------------------------------------------------------------- |
| Export portable configuration                                | Running        | [Administration](administration.md)                             |
| Create a fresh installation from configuration               | New empty root | `init --from PATH`                                              |
| Export retained traffic                                      | Running        | [Export optional history](#export-optional-history)             |
| Rotate the master key while retaining recovery keys          | Stopped        | [Rotate the master key](#rotate-the-master-key)                 |
| Replace all administrator authority, retaining product state | Stopped        | [Reset administrator authority](#reset-administrator-authority) |
| Replace interception signing authority or export public CA   | Stopped        | [CA commands](#stopped-interception-ca-commands)                |

Gateway must be stopped for maintenance. Stop service supervisors as well as Gateway; obtain authorization before disrupting a live service. Unknown outcomes require inspection, not replay.

All maintenance operations support `--dry-run`, `--confirm`, and `--json`. Dry runs inspect existing stopped ownership without writing installation files, audit or recovery markers. Nonempty WAL/journal state prevents ordinary immutable planning: preserve it, never delete or checkpoint it just to make a preview pass. Dedicated rotation reconciliation instead validates committed WAL under its retained-key protocol and exclusive ownership. Execution shows consequences, requires default-no consent or explicit `--confirm`, then revalidates under the same lock. Dry-run output is not later authority; confirmation never bypasses safety checks. Unknown recovery actions refuse.

## Encrypted secret storage

New init installations provision `<data-dir>/master-key` as a random, exclusive 0600 file under the 0700 root. MCP static/OAuth, HTTP/Git and CA material is authenticated-encrypted in control SQLite, never traffic history. Administrator and agent tokens remain hash-only; the protected `admin-bearer` file is separate.

Ordinary `init` and `init --from` provision custody and both databases before publishing authority. Recognized interrupted initialization continues only through `init --resume`. There is no separate maintenance setup or legacy traffic cutover command, alias or automatic startup migration. Existing installations must already have completed those retired workflows; unresolved older state requires a qualified stopped plan, not reinitialization.

Explicit legacy generations and unresolved native cleanup refuse without changing references or accessing native storage. All protected reads and writes require encrypted custody; missing/corrupt selected records never fall back to native values.

Preserve the key and database together. Do not delete, replace, chmod, symlink or regenerate a refused key. A missing/wrong established key, partial key file or uncertain setup requires diagnosis and a separately qualified recovery plan; initialization never overwrites it. Encryption protects against database-only theft, not theft of both key and database, or account/root compromise.

## Legacy custody upgrade boundary

The temporary native migration/verification/cleanup commands are removed, without aliases. Gateway no longer reads, probes or deletes native Keychain/Secret Service material.

**No verified, version-pinned self-service predecessor procedure is published here.** If legacy generations or unresolved native cleanup remain, stop the upgrade and contact the repository maintainers through [GitHub issues](https://github.com/averycrespi/agent-tools/issues) for a qualified plan. Report the installed version/revision and safe refusal code; do not attach databases, keys, credentials, native objects or unreviewed logs. Source tests do not authorize live credential access or service changes.

A separately authorized plan must establish predecessor/schema compatibility, verify complete custody and representative operations, safeguard existing evidence, resolve native leftovers and obtain operator approval. Until then, preserve databases, keys, references, markers and native objects. Never delete inventory, drop handles, regenerate keys or reinitialize to bypass refusal. No downgrade is recommended or qualified. Completed cleanup provenance remains durable and survives credential deletion; it is not an automatic cleanup queue.

## Rotate the master key

Stop Gateway and its launchers. Rotation requires complete encrypted custody; unresolved legacy material, credential cutovers and unrecognized historical security evidence refuse without migration or deletion. It preserves credential authority and the exact CA, so rotation alone does not require updating client trust.

```bash
agent-gateway maintenance rotate-master-key --data-dir /path/to/gateway-data --retain-recovery-keys --dry-run
agent-gateway maintenance rotate-master-key --data-dir /path/to/gateway-data --retain-recovery-keys --confirm
```

`--retain-recovery-keys` acknowledges retention rather than destruction of superseded keys. Successful output names an owner-only `master-key-retained-<new-key-id>` directory containing `old-key`, `new-key` and the installation ID. Safeguard it with 0700/0600 permissions. Do not reuse abandoned candidates or reactivate an old key by replacing `master-key`. Gateway never deletes retained keys or rewrites historical artifacts. Historical keys remain sensitive decrypt-only material, not a supported backup restoration path. Any disposal needs separate authorization and an inventory of dependent retained material.

Rotation cannot protect copies already stolen. If an attacker obtained both key and database, consider replacing upstream credentials and the CA through their explicit workflows. Account/root compromise defeats file-key custody.

### Interrupted rotation

Preserve all keys, databases, sidecars and recovery directories. Do not replay rotation after a lost response. Any object at `master-key.rotation` blocks startup and ordinary storage changes until explicit reconciliation:

```bash
agent-gateway maintenance rotate-master-key --data-dir /path/to/gateway-data --retain-recovery-keys --recover --dry-run
agent-gateway maintenance rotate-master-key --data-dir /path/to/gateway-data --retain-recovery-keys --recover --confirm
```

| Observed durable boundary                                                                              | Reconciliation                                                                                                      |
| ------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------- |
| Only `.master-key-preparation-*`; no armed bundle                                                      | Preserve incomplete preparations; they are never adopted. Inspect before a fresh deliberate operation.              |
| Armed bundle; database selects old key                                                                 | Verify old key/data, abandon candidate permanently and retain both keys. No encryption is replayed.                 |
| Database selects new key; active file is old or new                                                    | Verify all data with retained new key, finish/seal key-file publication and archive bundle.                         |
| Bundle already retained                                                                                | Inspect current key/data and retained directory; absence of a pending bundle does not authorize rerunning rotation. |
| Missing/wrong key, partial/foreign bundle, unknown DB identity or unsafe/unrecognized storage evidence | Fail closed; preserve evidence and obtain a qualified stopped plan.                                                 |

Rotation reconciliation validates committed WAL under exclusive ownership and authenticates the database-selected key with the retained bundle; it never repeats encryption. Recognized historical SQL intents remain untouched and do not require manual clearance. Unknown, malformed, conflicting or foreign evidence still refuses. A missing shared-memory index beside nonempty WAL, an unresolved journal or unsafe sidecar requires a qualified stopped plan, not deletion. Atomic rename and successful file/directory sync define durability; source fault injection does not qualify physical power loss or installed resources.

## Optional history and serving

One owned history opener validates selected storage independently of security readiness. History status is `opening`, `ready`, `unavailable`, `faulted`, or `disabled`. NULL selection disables capture and retains legacy rows unchanged; no command in this version extracts or selects them. Control integrity, mandatory audit, malformed selectors and security markers still fail closed.

Valid committed traffic WAL opens normally under exclusive installation ownership. SQLite's default autocheckpointing and bounded operation-scoped connections own ordinary cleanup; readers may delay it. Nonempty WAL or a stopped process alone proves neither corruption nor checkpoint completion. Startup validates WAL-aware identity, schema and every retained row before recording resumes. Unsafe paths, foreign bindings, corruption and unresolved journals require action. No database, selector or sidecar is deleted, recreated or chmodded to make history open. Administrator reset and key rotation do not inspect optional history files.

Doctor online and System Status show process-local recording health and incidents. `recovering` pauses capture while the actual owner settles and validates storage; `degraded` means validation passed but no fresh observation was acknowledged; `recovered` requires a new acknowledgment, not reconstruction of lost history. First cause/stage/settlement/time survives repeated refusals. Counts are submissions, not executions. Transient eligible lock/deadline/I/O/full failures use one-to-30-second backoff without batch or request replay. Budget reservation pressure discards a batch before mutation; only new independent traffic can establish recovery.

For `operator_action_required`, preserve history and inspect the reported ownership, integrity or capacity cause. Default-level failure/recovery diagnostics are bounded best effort; loss of stderr never blocks serving or storage recovery. Incidents reset with the process. Shutdown retains actual opener/writer and installation ownership until settlement; a timeout does not cancel fsync or permit another owner. Shared-filesystem stalls and physical exhaustion remain outside isolation guarantees.

## Export optional history

Existing history export is unchanged; it is independent of configuration export:

```bash
agent-gateway history export --json
agent-gateway history export --after-sequence 123 --limit 100 --json
```

For a browser file, use **System → History export → Download JSON**. The shared session/view read owner traverses records retained at the first page's high-water, excluding later arrivals, and downloads one JSON file. Progress reports actual records/pages/bytes. Cancel, navigation and sign-out stop export and fence late results. The file may contain identifying data; store it privately.

Browser traversal is limited to 64 MiB, two minutes and 4,096 pages; each request has a five-second/900-KiB bound. Changed generation/pruning, invalid coverage, interruption, read failure or limits save **no partial file**. The timestamp-only filename reveals no record IDs. The `agent-gateway-retained-traffic-v1` envelope retains original JSON pages and number tokens, initial/final coverage and non-atomicity metadata.

For larger exports, retain separate CLI page files with shell no-clobber and owner-only permissions. Record initial high-water/generation, advance only to the prior `next_sequence`, verify pruning/generation/progress and stop at the initial boundary or any error/change/stall. Each CLI response is a new bounded transaction, not a frozen continuation; the last page can contain later arrivals. Never concatenate pages and claim an atomic or complete audit. Each response contains at most 256 aggregate MCP/HTTP/Git records and 900 KiB. Missing records do not prove nonexecution; missing completion is unknown. `history_unavailable` and `history_busy` are failures, not empty success, and do not block forwarding. Neither interface repairs or removes history.

## Committed storage and lost responses

Ordinary SQL mutations no longer create filesystem intent/tombstone files or require a generic recovery command. Startup validates committed SQLite state, including WAL, schema, identity, integrity, current policy and encrypted custody under exclusive ownership. Domain changes and required audit commit atomically. Missing acknowledgment does not mean rollback and never permits replay.

A committed agent issuance or credential activation remains current when its transactional prerequisites and current policy/custody validations hold. Lost bearer output is not recoverable: inspect current credential metadata, then deliberately revoke and reissue as needed. Never restore prior credentials or fall back to superseded secrets. A genuinely pending credential cutover or external OAuth handoff remains denied and requires reauthentication; SQL rollback cannot undo a remote token effect.

Recognized historical SQL intent and recovery-bearing marker files are validated against the installation and retained unchanged. They are not instructions to invalidate a committed candidate or refence a committed activation. Unknown, malformed, mixed, conflicting, unsafe or foreign evidence blocks startup; preserve it and obtain a qualified stopped plan. No catch-all repair, unlock or marker-clear command exists. Corruption, wrong keys and invalid authority also remain blockers. An unresolved live transaction remains process-locally fenced until actual driver settlement or quarantine; readable rows, elapsed time and cancellation do not prove settlement.

## Reset administrator authority

With Gateway stopped, publish replacement authority to a new protected path:

```bash
agent-gateway maintenance reset-admin-credentials --data-dir /path/to/gateway-data --secret-output /safe/new/replacement-admin-bearer
```

Reset revokes every previous administrator bearer and activates the published replacement atomically, retaining product state. It does not rewrite the default `admin-bearer` or promote the new file. Failed publication activates nothing. Start Gateway and select the new file explicitly:

```bash
agent-gateway serve --data-dir /path/to/gateway-data
# In another terminal:
agent-gateway doctor --online --admin-bearer-file /safe/new/replacement-admin-bearer
```

Use online `admin credential rotate` for routine replacement-first rollover of one named credential. Maintenance uses `--confirm`, not `--yes`. Inspect with `--dry-run`; noninteractive execution without consent refuses. Never put a bearer in arguments, environment, logs or the old default file as a shortcut.

## Stopped interception CA commands

Disable launchers and stop Gateway. These commands require an existing installation and exclusive ownership. They neither enable the proxy nor install trust or export a private key. `--installation-id` is an optional assertion.

```bash
agent-gateway http ca export --data-dir /path/to/gateway-data --output /safe/path/gateway-ca.pem
agent-gateway http ca replace --data-dir /path/to/gateway-data --installation-id ID --confirm
```

Init owns first CA creation; `http ca create` is removed. Replace accepts verified absence or selects fresh protected signing material and certificate. It updates managed `http-ca.pem` only when absent or matching the previously selected certificate; unrelated files remain. Publication failure is distinct from authority change. Explicitly update client trust after replacement. Ordinary restart never mutates CA authority. Encrypted custody is required; unresolved legacy references refuse without native access or stale-generation fallback.

Export defaults to managed public PEM, accepts `--output PATH`, or streams public PEM with `--stdout`. Identical re-export is safe; different existing files and links refuse. JSON selects result presentation and cannot combine with stdout PEM. Export never reads protected signing material and proves neither signing availability nor client trust. Errors are bounded masked stderr with typed exits: usage 2, in-use 5, unavailable 7. Inspect changed/uncertain effects before another attempt; no automatic retry or rollback occurs.

## Retained installation-era artifacts

The retired maintenance setup and traffic cutover workflows have no aliases or replacement repair command. Removal never deletes or mutates old control generations (`gateway.db.previous-*`), traffic databases, interrupted stages, master keys, tombstones or backups. Preserve them and their identities. Normal startup still validates the supported selected control/traffic pair, including supported traffic schema upgrades; it never extracts old embedded history, replaces a selector or falls back to empty history. A qualified stopped plan is required for unresolved historical state.

## Failure handling

Failures keep stdout empty and emit a bounded human or JSON problem on stderr. Plans can precede problems: in JSON mode parse stderr as JSON lines. `gateway_running` requires stopping the actual owner, not bypassing locks. `secret_output_unavailable` does not prove new authority activated. Local diagnostics preserve masked filesystem/SQLite causes; public JSON retains its separate disclosure contract.

After interruption, retain original generations, keys, markers and any output. An error, timeout or missing response does not establish rollback or authorize replay. Use only the operation-specific procedure for pending rotation or initialization; normal startup validates committed SQL state. Never automatically reset authority, overwrite replacement files, discard unknown markers or manipulate encryption counts. Live service changes and installed-resource repair need separate authorization.

## Structured results and exits

Maintenance defaults to human output; JSON success is one safe stdout result. Execution plans/errors go to stderr; dry-run plans go to stdout. Administrator reset results identify `ok`, `operation`, `installation_id` and decimal-string `revision`. Rotation reports its dedicated disposition and retained-key path. There is no backup result or restore projection. Success exits 0; invalid input/flags/sinks 2; running owner 5; storage/recovery failure 7; output delivery failure 1, possibly after work completed. JSON problems have `status` (null), `code`, `title`, `exit_code`, and `uncertain`. Titles distinguish unchanged authority, retained staging, completed selection with later failure, and uncertainty. Nothing is replayed or compensated automatically.

See [administration](administration.md), the [documentation map](../README.md), and [Gateway README](../../README.md) for normal operation.
