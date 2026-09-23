# Installation selection and post-migration safety

Audience: Operators maintaining existing Gateway installations

Purpose: Select the existing installation and retain post-migration safety and recovery artifacts.

The installation-migration CLI and atomic path-exchange capability are retired. The rollout owner attested that all installations have migrated and authorized this source retirement. That attestation is an operator acceptance decision, not new live-host inspection evidence or permission for further host mutation.

## Selection matrix

The current `agent-gateway` resolves an explicit `--data-dir` first, including an existing custom or legacy root. Otherwise the selected base is absolute `XDG_DATA_HOME`, or the OS-account home plus `.local/share`; shell `HOME` is not authoritative. Relative XDG input fails unless overridden explicitly.

| Selected base contains                                            | Implicit CLI selection                                                                |
| ----------------------------------------------------------------- | ------------------------------------------------------------------------------------- |
| Neither root                                                      | Canonical `agent-gateway`; only explicit initialization creates state                 |
| Canonical directory only                                          | Canonical root                                                                        |
| Legacy directory only, or both directories                        | Refuse; select the actual existing root explicitly, never merge or initialize another |
| Unknown legacy file, link or inspection error                     | Refuse; preserve evidence and investigate                                             |
| Valid completed tombstone plus original moved canonical directory | Canonical root                                                                        |
| Custom root                                                       | Use explicit `--data-dir`; no filesystem search or fallback                           |

Default detection covers only the selected base, not every past XDG location or custom supervisor. Changing XDG settings is not relocation. An explicit root does not authorize a second installation. Preserve the actual installed root and service arguments during upgrades; do not reinitialize or rotate credentials for naming.

The canonical [service manager](launchd.md) assumes naming adoption is complete: it does not inspect legacy labels or import archived plists. For an existing installation, select its exact `--data-dir` and preserve installed settings. Reconcile unexpected legacy/custom launchers separately before managing a live service.

## Retain tombstones and recovery evidence

A completed migration left an owner-only `0600` regular-file tombstone at the old source path. Its persisted v1 record binds the exact source/destination paths and original directory device/inode. It permits canonical default selection only while that original owner-only directory remains at the destination. It also blocks older binaries from preparing a new root at the legacy path.

Never delete, rename, rewrite or copy a tombstone to bypass a refusal. The record is not portable backup metadata. A copied/replaced destination, changed paths, foreign or incomplete record, or interrupted destination reservation requires investigation, not another migration invocation. The current binary cannot resume or reverse a path exchange. Keep all evidence and obtain a separately reviewed, stopped operator plan for unexpected residual state.

Retain historical backups/reports, archived plists outside automatic-load paths, logs, staging/rollback material, native-keyring generations and recovery/intent markers. Binary/configuration rollback keeps the same authoritative destination root and tombstone; never restore an archived plist's old source argument or make a runnable clone. Run only one service owner.

Installation IDs, database/backup lineage, lock filenames, credential prefixes/verifiers, native-keyring identifiers and MCP identities remain compatibility contracts. Use [backup and recovery](backup-and-recovery.md) only for actual recovery: `storage verify` can clear recognized markers, while `backup restore` replaces administrator authority and invalidates agent credentials. Neither is a naming repair.

## Retired executable and operator cleanup

Build/install publishes only `agent-gateway` and leaves existing old-name binaries untouched. A renamed **current** binary still uses canonical grammar, completions and the same selected root/lock; an **old standalone binary** is not a supported client for the current control contract. Upgrade service and operator clients together; a filename is not version evidence.

For separately authorized cleanup:

1. Inventory command resolution (`type -a agent-gateway mcp-gateway`), executable revisions, completions, launchers and profiles. Do not run unknown old binaries against real state or dump environments/token contents.
2. Reconcile references to the intended executable/root/service. Prove any old process has exited and one owner remains before changing live services. Source tests and green CI do not prove host adoption or native-keyring access.
3. With **separate exact-path removal authority**, remove only positively identified stale binary/completion artifacts that no supported launcher or rollback procedure needs. Never wildcard-delete by old-name substring or follow unknown links.

Token copies have separate client compatibility and recovery purposes, not automatic removal eligibility. Manual client configuration must retain both canonical and legacy endpoint/token pairs from one authority. Operators must reconcile conflicting copies before launching clients; the retired provisioning scripts no longer enforce these checks. Follow the [client matrix](access-control.md#consumer-compatibility-qualification-and-rollback) rather than deleting legacy exports as installation cleanup.

Return to [administration](administration.md#installation-root), [launchd](launchd.md), or the [documentation map](../README.md).
