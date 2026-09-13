# Migrate a stopped Gateway installation

Audience: Operators responsible for Gateway installation handover

Purpose: Move one existing installation without replacing its authority or recovery state.

**Capability delivery is not host adoption.** Building, testing, or merging this code does not authorize stopping a real service, moving real data, changing Keychain permissions, or deploying a binary. Obtain that authority separately. Development tests use disposable roots and synthetic credentials only. Native launchd, filesystem durability and native-keyring qualification remain separate evidence.

Syntax: `agent-gateway installation --help` and `agent-gateway installation migrate --help`. Both installed executable names expose the same command. This is not `storage verify`, `backup restore`, initialization, or a credential reset.

## Selection matrix

Both names resolve an explicit `--data-dir` first (including an existing custom or legacy root). Otherwise the selected base is absolute `XDG_DATA_HOME`, or the OS-account home plus `.local/share`; shell `HOME` is not authoritative. Relative XDG input fails unless overridden explicitly.

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
5. **Install, inspect, then explicitly start one service.** Install the intended production binaries using the separately authorized deployment procedure. The installer can preserve the archived literal custom argv, replacing only binary and data root:

   ```bash
   ./scripts/install-launchd-agent.sh \
     --from-plist /absolute/private/archive/legacy.plist \
     --binary /absolute/bin/agent-gateway \
     --data-dir /absolute/account/.local/share/agent-gateway
   ```

   The installer never overwrites or starts. It rejects simultaneous listener/allowed-host overrides with `--from-plist`; unknown argument layouts require explicit reconciliation. Verify every selection, including nondefault flags, and the canonical logs/plist before an explicit bootstrap. Keep the archived old plist outside automatic-load paths. Follow [verification](launchd.md#verify), using the existing bearer and agent credentials, not newly issued substitutes. Expected native permission prompts need separate attended consent for the new binary; service IDs and generation handles do not change.

Failures emit no success result. Filesystem errors may identify paths but never print credential contents. Retain full local evidence securely; share only the nonsecret checklist below.

## Interruption, recovery and rollback

| Observed paths                                                                                   | Meaning and next action                                                                                                                                                                                                                         |
| ------------------------------------------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Source directory, destination absent                                                             | No reservation survived. Reestablish stop/ownership and run a fresh read-only preflight.                                                                                                                                                        |
| Source directory, destination complete matching reservation file                                 | Cutover did not happen. Both names remain fenced appropriately. After reestablishing stop/identity, explicit preflight and confirmation may resume this exact reservation. No automatic retry.                                                  |
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
- [ ] Listener, allowed hosts, `/mcp`, `/oauth/callback`, MCP identities, `mcp_gateway.*` schemas and provisioning variables/markers/token paths unchanged.
- [ ] Readiness, logs, storage posture and rollback location verified; unrun native/external checks explicitly recorded.

Return to [administration](administration.md#installation-root), [launchd](launchd.md), or the [documentation map](../README.md).
