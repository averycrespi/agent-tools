# Migrate a stopped Gateway installation

Audience: Operators responsible for Gateway installation handover

Purpose: Move one existing installation without replacing its authority or recovery state.

**Capability delivery is not host adoption.** Building, testing, or merging this code does not authorize stopping a real service, moving real data, changing Keychain permissions, or deploying a binary. Obtain that authority separately. Development tests use disposable roots and synthetic credentials only. Native launchd, filesystem durability and native-keyring qualification remain separate evidence.

Syntax: `agent-gateway installation --help` and `agent-gateway installation migrate --help`. This is not `storage verify`, `backup restore`, initialization, or a credential reset.

## Selection matrix

The current `agent-gateway` resolves an explicit `--data-dir` first (including an existing custom or legacy root). Otherwise the selected base is absolute `XDG_DATA_HOME`, or the OS-account home plus `.local/share`; shell `HOME` is not authoritative. Relative XDG input fails unless overridden explicitly.

| Selected base contains                                                      | Implicit CLI selection                                                      | Installer                                       |
| --------------------------------------------------------------------------- | --------------------------------------------------------------------------- | ----------------------------------------------- |
| Neither root                                                                | `agent-gateway`; only explicit initialization creates state                 | Canonical defaults; never initializes or starts |
| Canonical directory only                                                    | Canonical root                                                              | Canonical defaults                              |
| Legacy root only                                                            | Refuse with migration guidance                                              | Refuse unless operator selects `--data-dir`     |
| Both roots                                                                  | Refuse; never merge or choose by age                                        | Refuse implicit selection                       |
| Unknown legacy file, link or inspection error                               | Refuse                                                                      | Refuse                                          |
| Valid completed migration tombstone plus original moved canonical directory | Canonical root                                                              | Require explicit destination `--data-dir`       |
| Custom root                                                                 | Explicit selection works; no filesystem search/fallback                     | Explicit selection; preserve archived argv      |
| Legacy plist/service or conflicting canonical service                       | Do not infer a custom root from defaults; select the actual root explicitly | Refuse; reconcile both service identities first |

Default detection covers the selected base, not every past XDG location or custom supervisor. Before an upgrade, recover the actual installed root and service arguments. Changing XDG settings is not migration. An explicit root never grants permission to run a second installation. Keep historical plists outside `LaunchAgents`; never bootstrap archived rollback material.

## Supported automatic cutover

The command supports **macOS per-user launchd handover**, with two distinct absolute, canonical sibling paths in one owner-controlled parent. Linux exercises the atomic filesystem primitive in isolated tests; automatic Linux/systemd or other-supervisor handover is not qualified and refuses. Cross-filesystem relocation, nested roots, symlinked parents/entries, foreign owners, hard links, non-private tree entries, unreadable base identity, unknown destinations and unsupported atomic exchange refuse without a copy fallback. A parent must not be group/other writable; all installation directories must be `0700`, all files `0600`. Tree inspection is bounded to 100,000 entries. Use ordinary local filesystems with qualified atomic exchange and directory-sync semantics; network/cloud-backed or uncertain storage requires a separate operator plan.

The source must have a readable current-schema base installation ID. WAL/SHM, if present and private, move unchanged, but preflight does not replay WAL or claim its revision is current. Upgrade an older source using its explicit existing root before this naming migration; all supported historical backup schemas remain preserved. Do not clear intent or unclean markers merely to enable migration.

Run installation management serially. Disable any additional custom supervisor, scheduled launcher, or foreground launch before proceeding. The command checks both known launchd jobs are absent, both known plists are outside their automatic-load locations, and no other same-account process has either Gateway executable name or the explicitly selected service binary basename. It never signals a PID. Unknown, truncated or failed process/service inspection refuses. These observations plus the existing nonblocking lock prove absence under the serialized-management prerequisite, not against a hostile same-account actor launching or replacing files concurrently. Do not use wrappers or ambiguous process names; reconcile them manually first.

## Operator procedure

1. **Record nonsecret identities before stopping.** Read the installed plist and `launchctl print` for both `dev.agent-tools.mcp-gateway` and `dev.agent-tools.agent-gateway`. Record the exact loaded argv, executable, root, listener, allowed hosts, label and OS-account UID. Require one explainable owner. Use authenticated `status` at that exact root/address to record installation ID, storage posture and credential functionality. Preserve a verified backup according to [backup and recovery](backup-and-recovery.md); this is rollback evidence, not a second runnable root.
2. **Stop once and prove exit.** With separate live-host authority, follow [graceful stop](launchd.md#graceful-stop-and-restart). Match loaded service path/program/argv to the installed plist before one `launchctl bootout`. Confirm the job is absent and its identified old process has exited. A PID number alone is not ownership proof, and a free lock alone is not process-exit proof. Do not force-kill, retry a stop blindly, or proceed after unknown results. Disable any other supervisor. Archive the old plist outside `~/Library/LaunchAgents` using an explicitly chosen, nonexisting owner-only location; do not delete it. Also reconcile any canonical plist. Leave logs and backups intact.
3. **Preflight, without mutation.** Substitute the selections you actually recorded; do not paste placeholders literally:

   ```bash
   agent-gateway installation migrate \
     --source /absolute/account/.local/share/mcp-gateway \
     --destination /absolute/account/.local/share/agent-gateway \
     --installation-id EXISTING_INSTALLATION_ID \
     --service-binary /absolute/bin/mcp-gateway
   ```

   The result identifies source, destination and installation ID with `migrated:false`. No root, lock file, recovery marker, credential, service or reservation is created. Existing locks are opened and acquired nonblockingly, then released. Preflight does not reserve a future operation.

4. **Confirm the exact selection.** After reviewing preflight, repeat those same explicit selections with `--confirm`. This invocation rechecks service/process absence, acquires the existing source lock, validates identity and the private complete tree, and rechecks stop facts. It exclusively creates and locks a synced destination reservation file, then performs **one atomic exchange** of that file and the whole source directory. The original lock inode moves and remains locked through final sync and verification. The old source becomes a permanent `0600` regular-file tombstone that blocks even older binaries from preparing a new directory. No database or bearer is copied, rekeyed or reinitialized. Success reports `migrated:true`; this is not readiness.
5. **Install, inspect, then explicitly start one service.** Install the intended production `agent-gateway` using the separately authorized deployment procedure. The service manager assumes canonical naming and does not import archived legacy plists. Explicitly carry forward the recorded listener, hostname list and diagnostic level alongside the new paths:

   ```bash
   agent-gateway service install \
     --binary /absolute/bin/agent-gateway \
     --data-dir /absolute/account/.local/share/agent-gateway
   ```

   Add the recorded `--listen`, repeatable `--allowed-host` and `--log-level` selections; do not silently substitute defaults. If the archive contains optional output selections, preserve them through a separately authorized stopped plist-aware edit before loading. The canonical reader supports separate-element `--output human|json` or `--json`, but not conflicting output selectors, duplicate singleton flags, unknown arguments or generic passthrough. Verify every literal selection against the archive and the private canonical logs/plist before `agent-gateway service start`. Installation never overwrites or starts a service. Keep the archived old plist outside automatic-load paths. Follow [verification](launchd.md#verify), using the existing bearer and agent credentials, not newly issued substitutes. Expected native permission prompts need separate attended consent for the new binary; service IDs and generation handles do not change.

Failures emit no success result. Filesystem errors may identify paths but never print credential contents. Retain full local evidence securely; share only the nonsecret checklist below.

## Interruption, recovery and rollback

| Observed paths                                                                                   | Meaning and next action                                                                                                                                                                                                                         |
| ------------------------------------------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Source directory, destination absent                                                             | No reservation survived. Reestablish stop/ownership and run a fresh read-only preflight.                                                                                                                                                        |
| Source directory, destination complete matching reservation file                                 | Cutover did not happen. Both paths remain fenced appropriately. After reestablishing stop/identity, explicit preflight and confirmation may resume this exact reservation. No automatic retry.                                                  |
| Source matching tombstone, destination original directory                                        | Cutover happened. A repeated mutation refuses rather than exchanging back. Verify destination and directory-sync evidence; do not initialize or restore the source.                                                                             |
| Incomplete/foreign reservation, two directories, missing/changed identities, or sync uncertainty | Stop. Keep all evidence and services stopped. A qualified operator must inspect directory entries, inodes, filesystem durability and archived configuration; do not remove the blocking file or substitute a copied root to bypass the refusal. |

A crash before reservation publication can leave an incomplete blocking file. A sync failure after exchange is uncertain durability, **not** permission for compensation. The command never deletes a reservation, tombstone, old plist, logs, backup, staging generation or recovery marker. Do not rename the source tombstone, since that would reopen the legacy path for initialization. Do not treat its inode-bound record as portable backup metadata.

Prefer roll-forward verification after an exchanged cutover. For **binary/configuration rollback**, keep the same migrated destination root and tombstone: stop and prove the canonical process exited, archive its plist, then deliberately install the prior compatible binary/service configuration with an explicit destination root. Preserve the listener and allowed hosts; run only one label. Never restore the old plist's original source argument. An actual reverse path exchange is not an automatic command in this release and requires a separately reviewed stopped operator plan; copying a retained backup into the old root is not rollback.

Run `storage verify` only when the recovery guide calls for it: it can apply recognized recovery and clear markers. `backup restore` intentionally invalidates agent credentials and replaces administrator authority, so it is **not** a naming-migration step. Closed backup verification reads the digest-bound database immutably without creating sidecars; old sidecars remain evidence and are never deleted by migration. Existing non-private sidecars cause a refusal and need separate authorized inspection, not automatic chmod.

## Nonsecret adoption checklist

Record these facts for each separately authorized host adoption; retain earlier evidence as historical:

- [ ] Code revision, platform/filesystem and operator authority recorded; production (not E2E) binaries selected.
- [ ] Source/destination and unchanged installation ID recorded; tombstone retained, one authoritative directory, no runnable clone.
- [ ] Both service identities inspected; old process exited; archived plists outside automatic-load paths; exactly one loaded canonical label and expected executable/argv.
- [ ] Existing administrator credential authenticates; existing principal/agent credential and grant policy still function; no replacement credential used to mask failure.
- [ ] Native-keyring credential access/OAuth functionality checked with attended consent; immutable native service IDs/handles retained.
- [ ] Historical backup metadata/lineage retained and recovery verification recorded separately; unresolved intent/unclean state remains visible.
- [ ] Listener, allowed hosts, `/mcp`, `/oauth/callback`, MCP identities and `mcp_gateway.*` schemas unchanged; provisioning follows the [current client matrix](access-control.md#consumer-compatibility-qualification-and-rollback).
- [ ] Readiness, logs, storage posture and rollback location verified; unrun native/external checks explicitly recorded.

## Retired executable and operator cleanup

Build/install publishes only `agent-gateway`. The old output copy and basename-specific help/completion registration are removed. If someone renames a **current** binary to `mcp-gateway`, it still executes the canonical command tree, emits canonical help/completions and uses the same selected root/lock. This is not a second supported installation entry point or an old-grammar adapter. An **old standalone binary** left on PATH has its own old implementation: do not use it against the current installation/control contract. Upgrade service and operator clients together; a filename is not version evidence.

| Installation/client case              | Supported action                                                                                                                                                           |
| ------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| New canonical installation            | Build/install `agent-gateway`; initialize explicitly and configure one canonical service                                                                                   |
| Existing canonical or custom root     | Use current `agent-gateway` with the exact existing `--data-dir`; no reinitialization                                                                                      |
| Unmigrated legacy root/service        | Implicit selection refuses; use the stopped migration above or explicitly selected current-binary operation                                                                |
| Stale executable or script references | Update PATH, completions, launchers and profiles to canonical selections; no automatic cleanup                                                                             |
| Current Pi provisioning               | Canonical script and token path, with both canonical and legacy export pairs; see the [client matrix](access-control.md#consumer-compatibility-qualification-and-rollback) |

For each separately authorized operator cleanup:

1. Inventory command resolution (`type -a agent-gateway mcp-gateway` in each relevant shell), executable paths/revisions and completion registrations. Inspect both known launchd labels/plists and every supported custom launcher, profile `copy_paths`/script and fresh shell/Pi environment using the nonsecret checklists. Do not execute an unknown old binary to discover its version against real state; do not dump environments or token contents.
2. Reconcile references to the selected canonical executable/root/service/script/token path. Prove the old process is stopped and one intended owner remains before changing any live service. Requalify existing credentials and recovery as above; source tests and green CI do not prove machine adoption.
3. Under **separate exact-path removal authority**, remove only positively identified stale binary/completion artifacts after checking no supported launcher or rollback procedure needs them. Never wildcard-delete by old-name substring, follow unknown links, or overwrite an unclassified file. Neither `make build` nor `make install` deletes or overwrites the old output; they leave existing `mcp-gateway` bytes untouched, including in a reused output directory.

Retain old data-root tombstones/reservations, historical backups and reports, archived plists outside auto-load paths, old logs, token copies, native-keyring generations and rollback material. They have distinct safety/recovery purposes, not automatic removal eligibility. The migration command never removes them. A retained old token copy must still match the current canonical file or provisioning fails closed; cleanup/rotation must be separately reconciled. Never remove the source tombstone to make an old root runnable.

The rollout owner explicitly attested migration of their supported scope and authorized this source retirement without a separately enumerated inventory. This is an operator acceptance decision, not native/live inspection evidence; it does not authorize further host mutation or classify unknown installations as adopted.

Return to [administration](administration.md#installation-root), [launchd](launchd.md), or the [documentation map](../README.md).
