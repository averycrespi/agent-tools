# Storage and Recovery

Audience: Maintainers and contributors changing durable storage, backup, restore, and stopped recovery

Authority: Normative product design

This chapter owns the behavior and invariants described below. Operational procedures remain in the linked guides; exact executable contract values remain owned by `internal/contract` and must agree with this chapter.

## Storage durability

An installation is one canonical owner-only `0700` directory guarded by a nonblocking exclusive process lock. A durable run marker distinguishes clean shutdown from an unclean stop; either startup path performs live identity, migration, pragma, and integrity verification before the store can be ready.

SQLite uses application ID `MGW1`, an immutable installation ULID, decimal revision, and an ordered embedded migration history. Schema 4 adds the durable server tables without changing accepted earlier tables.

Schema 8 adds the separately owned `mcp_gateway` synthetic identity, authorization revision singleton, permanent principal/current-credential slots, and immutable grant rows; migration seeds revision zero with no principals or grants and stores no raw bearer, session, credential history, issuer, or reason. Schema 9 adds an empty bounded invocation-audit store with a database-generated monotonic sequence, coherent nullable route/authorization/terminal groups, no foreign keys to mutable authority or catalog rows, and one trigger that permits only the first paired terminal annotation while leaving FIFO deletion available.

It has no bearer, verifier, downstream request ID, successful result, raw error, dispatch-started, retry, or replay column. Schema 10 additively creates an empty grant-request store with permanent identity tombstones and never-reused insertion order, immutable owner/target/requested-policy and versioned canonical dedupe bytes, bounded submitted/approved evidence, a database-maintained global evidence-byte aggregate, partial pending uniqueness, terminal-only deletion, and one exact pending-to-terminal transition.

Historical grant IDs and target identities have no foreign keys to mutable rows. Schema 11 adds only closed OAuth diagnostic stage and bounded HTTP-status columns to retained auth-flow records; the existing flow ID is the correlation ID and the existing public reason is the diagnostic reason. Schema 12 added required bounded grant names and deterministically named existing rows from their stable IDs. Schema 13 replaces names with optional bounded descriptions, preserves every existing name as its row's description, and adds a positive metadata revision used only for conditional description updates. Schema 14 is an additive compatibility fence for version-2 matchers: it changes no grant or request table and rewrites no stored constraint or dedupe bytes, while ensuring binaries that know only the version-1 matcher reject the newer database generation.

Schema 15 adds the independent control-plane audit store and history-generation singleton. Events are bounded canonical JSON with indexed, generated filter columns and no foreign keys to mutable resources. Database triggers reject updates, deletion inside the retained window, sequence reuse/gaps, and decreasing timestamps; insertion atomically retains the newest 65,536 records and records pruning. Sequences remain monotonic after pruning. Storage verifies the exact embedded audit DDL and runs the audit owner's bounded semantic validation before accepting the generation; malformed event JSON, missing members, inconsistent sequence/retention state, or changed enforcement triggers fail closed. `internal/audit` owns online audit SQL and the supplied-transaction append seam. Producer and restore-continuity qualification is tracked explicitly in the [control-plane audit coverage matrix](administrative-control-plane.md#control-plane-audit-coverage).

Schema 16 adds `grants.read_only`, `grant_requests.requested_read_only`, and `grant_requests.approved_read_only` as non-null integers restricted to 0 or 1, defaulting existing rows to 0 without changing legacy dedupe bytes. Checks limit true to server-wide ALLOW/request policy, preserve read-only approval, and require absent approved policies to remain unrestricted. Requested restrictions are immutable; grant restrictions cannot change through metadata updates. Storage verifies the embedded column/trigger definitions, while authorization and grantrequests validate durable policy semantics before readiness. Older binaries reject the newer schema rather than silently broadening restrictions.

Schema 17 adds nullable `invocations.failure_diagnostics`, a maximum 512-byte JSON object limited to failed terminal rows. Invocation validates its closed vocabulary at writes and startup. Existing rows retain NULL, with no reconstructed history. Diagnostics commit with the one terminal transition, remain immutable afterward, and are included in backups; raw errors and payloads remain forbidden.

Schema 19 adds secret-free HTTP credential metadata and permanent tombstones, and expands the keyring fence kind to `http_credential` while preserving existing MCP fences. Structural verification checks the replacement fence table and credential DDL; the HTTP credential owner validates canonical metadata and current material bindings before readiness. Backup restore invokes its stopped-stage invalidation seam, removing restored current HTTP authority and advancing affected metadata/material revisions without reading or deleting keyring contents. An older snapshot therefore cannot reactivate retired material.

Schema 20 adds HTTP default and grant tables without changing MCP rows. Defaults backfill to block and an exact verified principal-insert trigger seeds each new default atomically. Grant rows contain only canonical policy configuration, bounded descriptions, immutable principal/ID, revisions and timestamps. The authorization owner validates all defaults, policies and credential references during startup and staged restore. Restored unavailable credential material remains unavailable without deleting its valid grant references.

Storage owns only this DDL, seeding, and structural migration boundary. Authorization owns online SQL and validates every bounded principal-authority singleton and row coherently before the rest of the production graph is constructed; server target existence and synthetic collision checks remain delegated to the servers package on that same transaction.

Every connection installs a two-second busy policy, enables foreign keys, verifies WAL and `synchronous=FULL`, and derives `max_page_count` from the compiled 1 GiB database limit and that connection's actual page size. Foreign, newer, partial, corrupt, unsafe-permission, and over-limit generations fail closed.

## Isolated traffic store

Schema 18 adds the control-owned `traffic_selection` singleton. Production selects
exactly one independently bound `invocation.TrafficStore` for MCP and HTTP evidence,
completion and history. Control retains identities, credentials, policy, requests,
configuration and administrative audit. Storage owns traffic DDL; invocation owns
its evidence, SQL, validation, writer and reads. There is one composition graph,
installation lock and authenticator, with no dual writes, protocol registry, execution
queue or replay. Composition retains installation ownership through traffic close.
A missing, foreign or invalid selected generation fails closed; an unselected
legacy installation requires explicit stopped migration, never live backfill.

An explicitly created `traffic-<generation>.db` uses application ID `MGT1`, schema
2, and exact installation/generation bindings. Control schema 20 retains the
existing installation/generation selector: its binding format does not change.
Traffic schema 2 adds a distinct `http_traffic` table, stored indexed query facts,
and immutable-admission/one-terminal triggers in the same database. MCP tables,
rows, diagnostics and public representations are unchanged. Both domains share
traffic metadata, monotonic sequence allocation, retention and physical budget.
The MCP sequence high-water includes HTTP insertions without inventing MCP rows.

Before readiness, under existing installation ownership and before constructing
readers or starting the writer, a schema-1 selected generation receives exactly
one complete schema/binding/evidence/accounting validation. A bounded transaction
then adds only empty HTTP tables/indexes/triggers and advances user_version to 2.
This uses the existing writer connection, physical reservation and FULL durability;
after commit, exact new DDL and file bounds are verified without rescanning
unchanged evidence. Current schema-2 startup performs one complete validation of
both domains. A failed or uncertain migration never produces a ready store;
a fresh startup validates the atomic version that actually settled. No online
backfill, new operator command, file replacement or implicit empty initialization
is involved. Existing selected pairs still reject `migrate-traffic`; schema-17
single-store extraction remains the explicit stopped operation. Older readers
reject traffic schema 2. Immutable paired backup verification accepts exact
schema-1 and schema-2 definitions, and restore copies both domains into a fresh
generation while preserving missing completions. Creation checkpoints and closes an
owner-only stage, syncs its file, publishes without replacing an existing name,
and syncs the directory before and after removing the staging name. Failed
publication retains evidence; opening a missing generation never creates it.
Only one traffic store per installation can be open in the process. The existing
installation process lock remains the interprocess owner; control marker and
keyring recovery are unchanged. Traffic DDL includes the schema-17 optional bounded
failure-diagnostics column so shared invocation reads and semantic validation keep
the current evidence shape. The unselected completion seam still accepts only the
common time/class pair; production completion additionally persists validated,
immutable encoded diagnostics in the same terminal update. The fixed retention
charge reserves the maximum diagnostic size before admission. Control schema 18
adds traffic selection after schema 17 diagnostics; stopped migration and restore
preserve diagnostics, while older schema-9-through-16 history has none. Unsupported experimental traffic schemas are rejected, not silently recreated.

The writer has one connection, WAL, `synchronous=FULL`, a 50 ms busy bound,
foreign keys, disabled cache spilling and automatic checkpointing, and a verified
page ceiling. Every physical connection receives its settings; read connections
are read-only/query-only. At most two readers (configurable 1–4) return at most 256
materialized records per call, with a one-second maximum lifetime and immediate
capacity refusal. No live SQL rows or snapshots escape. Checkpoint pressure fences
new readers and makes one bounded TRUNCATE attempt when no snapshot is pinned; a
busy checkpoint refuses admission rather than growing the WAL indefinitely.

The default combined database-plus-WAL budget is **4,294,967,296 bytes**; the
`serve` and persisted service configuration accept 1 MiB–16 GiB through
`--traffic-budget-bytes`, validated before storage mutation. File lengths, not allocated filesystem
blocks or logical SQLite page counts alone, are measured. For 4096-byte pages,
64 KiB is safety headroom; at most one third of the remainder is database pages.
Before each transaction the writer reserves `32 + (maximum_pages + 2) * 4120`
additional WAL bytes against the remaining WAL partition, checkpointing only when
that reservation cannot fit. With cache spilling disabled, fixed-schema DML can
write each dirty page once at commit; the reservation covers the entire possible
database plus commit padding for the pinned default VFS. No arbitrary SQL,
attachments, online VACUUM, cache flush, or alternate VFS is exposed. Eviction
never subtracts from physical occupancy. A post-settlement size check detects
violations; I/O uncertainty faults the writer. This conservative policy may refuse
work well below the combined limit; it is not a throughput guarantee or a hard
bound on uninterruptible filesystem I/O. Ownership remains held until settlement.

Logical retention charges include encoded evidence plus 1024 bytes and the
protocol's reserved completion payload: 512 bytes for MCP diagnostics or HTTP
terminal facts. HTTP admission JSON is at most 65,536 bytes; its charge includes
that entire immutable payload. MCP retains its existing 16,384-byte charged-record
ceiling. Shared batching reserves for the largest accepted domain member. The logical allowance is one quarter of the
database partition, leaving index/fragmentation headroom. A separately configurable
1–1,000,000 retained-row ceiling bounds full semantic validation work; it is not a
promised history window. Each pruning scan is bounded by active capacity plus
batch capacity plus 256; insufficient eligible space refuses the whole batch.
The oldest eligible records are removed transactionally, after checking every
incoming identity for collision. Process-local pins protect active admissions,
including earlier members of a group commit, through disposition and the sole
completion attempt. Historical incomplete rows are unknown and unpinned after
restart. Sequence high-water and a cumulative deleted-record count commit with
rows and byte/count accounting. Generation plus pruning metadata invalidates a
prior history snapshot even for non-prefix holes around pinned rows.

The database/WAL budget excludes control storage's existing 1 GiB limit, the
bounded SHM coordination file (16 MiB maximum), memory, and creation/backup/restore
staging. Creation, paired backup and migration must preflight and reserve their
complete stage, backup and rollback costs before selection. A process-owned cooperative filesystem allowance is retained through settlement;
headroom checks account for full bounded stages, retained originals and concurrent
Gateway staging on the same filesystem. This is not physical preallocation or protection
against concurrent noncooperating filesystem consumers. Any later I/O uncertainty
fails closed. Deterministic tests are not power-loss qualification.

Commit acknowledgment—not row readability or marker cleanup—is the new traffic
evidence boundary. Statement/storage failures, commit errors, lost acknowledgment,
and uncertain rollback issue no affected receipt and fault future admission.
There is no split, retry, replay, or traffic-only persistent manual-verification
latch. Restart restores write authority only after exact schema/application/binding,
physical-budget, complete structural and every-row semantic validation, including
nullable groups, chronology, accounting and sequence/pruning consistency. Validation
is streaming and has a 30-second cooperative deadline; incomplete validation is
failure, never partial readiness. Composition relies on this complete `OpenTraffic`
validation rather than repeating the legacy invocation scan through an online reader
with its one-second deadline. Reads may remain available while write authority
is faulted; readable history alone cannot acknowledge or resume execution.

## Installation selection after migration retirement

`internal/paths` owns canonical root selection and process locks. New defaults use `agent-gateway`; legacy entries cause implicit selection to fail unless they are an exact completed tombstone bound to the original moved directory's device/inode. The current executable uses this selection and `gateway.lock` regardless of an accidental basename change. Explicit roots remain authoritative, including legacy/custom roots; there is no filesystem search, automatic relocation, merge or second owner.

The installation-migration CLI, stopped host inspection and atomic whole-directory exchange are retired after rollout-owner attestation that all installations migrated. Only read-only recognition of the persisted v1 tombstone remains: an owner-only regular file with the exact source, destination, device and inode record, and the original owner-only directory at the destination. Unknown, incomplete, oversized, unsafe or mismatched records refuse implicit selection. The source tombstone still blocks old binaries from recreating a root; it is not portable backup metadata.

Nothing deletes or rewrites tombstones, interrupted reservations, old plists, logs, backups or recovery markers. There is no resume or reverse-exchange command. Unexpected residual state or durability uncertainty requires operator investigation and a separately reviewed stopped plan. Binary/configuration rollback retains the same destination root. See [installation safety](../operators/installation-safety.md).

Retirement preserves installation ULID, SQLite application/schema identity, database/backup lineage and metadata, `gateway.db`/`gateway.lock`, bearer/verifier/fingerprint domains, native-keyring services/generation handles, policy/history/OAuth material and all MCP/provisioning contracts. Deterministic source tests are not native service/keyring or live-host adoption evidence.

## Security mutation and stopped recovery

### Mutation intent and latch

Security mutations share one actual control-storage owner. Ordinary and recovery-bearing mutation APIs attempt immediate acquisition and never queue. The following invocation FIFO seam is retained for legacy migration/test owners only, not production traffic persistence. Invocation admission and synchronous best-effort terminal annotation alone may retain at most 31 FIFO waiters, for at most 250 ms per acquisition, shortened by caller cancellation/deadline or invocation drain. Full capacity rejects immediately. Handoff reserves the same owner for the oldest eligible waiter, so neither later invocation arrivals nor nonqueueing foreign writers can barge. Canceled, expired, drained, or latched waiters leave occupancy exactly once; active ownership includes reserved handoff and lasts through transaction and marker settlement. Foreign writers may therefore reject during a reservation and have no starvation-freedom guarantee.

Waiting runs in the caller before any intent, transaction, or callback. After acquisition, cancellation, invocation drain, and latch are rechecked before mutation work. Pre-mutation rejection creates no intent or row and never latches storage. Closed internal sentinel errors distinguish wait capacity, wait expiry, invocation stop, caller cancellation/deadline, and storage latch; public MCP vocabulary is unchanged. The acquisition timer never supplies an active transaction deadline. Composition stops only invocation waiting, leaving producer cleanup's ordinary writes available. No worker pool, deferred terminal backlog, callback retry, recursive audit diagnostic, or global storage fence is introduced.

Before a transaction begins, Gateway writes an installation-bound owner-only intent through temp write, file sync, atomic rename, and directory sync. A known commit or rollback moves the intent through a synced tombstone deletion. Marker I/O failures, storage-class statement failures, busy begin, commit errors, and post-commit uncertainty latch mutations; elapsed time, restart, or successful reads cannot clear the latch. The at-most-512-byte marker has one closed recovery union: the existing keyring fence action or one agent credential candidate containing only principal ID, credential ID, and captured positive principal/credential revisions. Recovery-bearing tombstone deletion restores the tombstone if final directory sync fails.

### Diagnostic observations

The startup-bound storage observer reports actual mutation wait/acquire/release/reject facts, acquisition and hold durations, and process-local mutation-attempt correlation. Invocation context distinguishes admission from terminal annotation; unrelated mutations use the coarse `foreign` writer class without tracing their resource or workflow. Waiting/rejected attempts never acquire an acknowledged invocation ID from storage. Facts are sampled under short owner locks where needed, but encoding and output run only on the separate diagnostic worker, outside storage/authority/catalog locks.

Durability failure and latch events classify size check, identity check, intent arm, transaction begin/body/commit/rollback, and intent cleanup without serializing errors or marker contents. The closed diagnostic queue is not an audit queue and cannot change intent settlement, latch/recovery, actual slot ownership, or mandatory audit-before-dispatch. Sink loss or flush expiry never marks otherwise settled storage unclean. See [serve diagnostics](administrative-control-plane.md#serve-diagnostics) for bounds and output failure semantics.

### Agent-candidate recovery

`storage verify` reacquires stopped-process ownership, requires the current schema, and applies a recognized recovery action before clearing marker artifacts. Agent candidate cleanup is the sole stopped-process agent-credential SQL exception in storage: it clears only an exact current ID/revision tuple and advances principal and credential revisions once, while absent, replaced, or stale candidates are no-ops and no prior credential is restored. Unknown, mixed, disagreeing, foreign-installation, oversized, or failed recovery remains latched. The command closes SQLite before durable marker removal, emits one safe machine JSON result, and does not make the service ready; normal startup must verify the generation again.

### Restore credential invalidation

Authorization separately owns general stopped-stage credential surgery on a supplied verified current-schema replacement store. One marker-armed transaction validates all synthetic, principal, grant, target, capacity, and revision semantics before writing; clears every complete current slot while advancing only each affected principal and credential revision once; then revalidates the complete authority and absence of current slots before commit. Zero credentials is an exact no-op. Principal metadata and timestamps, grants, synthetic identity, authorization revision, Gateway revision, server facts, and admin authority remain unchanged. Backup owns no principal, credential, or grant DML. For every accepted schema-3-through-current (currently 20) restore lineage, orchestration migrates and initially verifies the stage, invokes this authorization seam, rekeys admin authority, checkpoints and closes, then reruns closed SQLite verification plus complete authorization and grant-request semantics through the server-target and request supplied-transaction inspectors before installation. Every pre-install fault leaves the original generation authoritative.

### Operator command boundary

Both installed executable names expose `storage verify` and `backup restore BACKUP_ID` from one CLI implementation. Retired top-level `restore` and `restore --verify-current` spellings have no execution aliases. Verification is current-schema recovery, not reset, initialization, backup selection, or service startup. Restore requires one explicit valid backup ID and a fresh exclusive owner-only replacement bearer sink; neither offline command introduces a confirmation prompt, and existing online consequence confirmations are unchanged.

CLI success projections use `operation:"storage_verify"` or `operation:"backup_restore"`, with no `mode` member. Both retain `ok`, `installation_id`, and decimal-string `revision`; only restore has `backup_id`. This projection is separate from durable backup metadata and audit vocabulary. Exact JSON, typed exits, stopped-service prerequisites, and rollback/uncertainty procedures are published in the [operator recovery guide](../operators/backup-and-recovery.md#structured-results-and-exits). No command rename alters data-root precedence, schema support, installed service argv, installation/credential/keyring identities, lineage, or recovery markers. No automatic replay or compensation is added.

## Backup and generation replacement

### Backup publication

Format 2 binds the control selector and installation to a verified traffic
generation, with independent sizes, SHA-256 digests and traffic budget metadata.
Under a one-second maximum admission/writer pause, Gateway pins exact coherent
SQLite read connections for both stores. Actual acquisition overruns fail even
when SQL succeeds; refusal never latches otherwise healthy traffic. Locks release
only after acquisition settles. Copying uses those stable snapshots outside the
pause, with a 30-second lifetime; checkpoint pressure refuses traffic work rather
than waiting for the backup. Already admitted work may complete later, leaving
unknown terminal evidence in the snapshot. Pins and execution are never backed up.

Stopped-process and backup procedures are canonical in [backup and recovery](../operators/backup-and-recovery.md). On-demand backup uses SQLite's online backup API under one nonblocking global work slot. Gateway stages an owner-only closed generation, verifies identity/schema/revision/full integrity and the 1 GiB bound, computes SHA-256, writes safe internal metadata, and atomically publishes it under a 26-character ID. The artifact-bound authority/key digest provides durable retry identity without storing a bearer or replaying a secret; 64 retained artifacts are the fixed record bound. Verification of a closed artifact reads its digest-bound database immutably: it neither creates sidecars under ambient permissions nor consults an unrelated WAL. Existing sidecars are not deleted by verification or installation naming cleanup.

### Generation replacement

Stopped migration stages the control copy without upgrading the original, validates
and publishes a generation-addressed traffic file first, then installs its matching
control selector by one atomic rename. The original control inode is checkpointed
and retained under a unique `.previous-*` name before replacement; old traffic and
interrupted stages remain. Two fixed-file renames are never described as atomic.
Initial setup creates the matching pair before publishing authority. Restore uses
a fresh traffic generation, invalidating history cursors while preserving IDs,
high-water, pruning and unknown outcomes. Legacy single-store backups receive
bounded semantic evidence validation and stopped extraction; accepted schemas
before invocation history legitimately extract an empty store. No path restores
execution pins or falls back to empty history.

Backup restore holds stopped-process ownership, validates the published artifact and current installation binding, and copies one complete generation. Accepted schema-3-through-current (currently 20) artifacts are forward-migrated as necessary and fully verified while staged before restored agent credentials are invalidated, admin authority is rekeyed, and replacement is published. The staged database resets all restored admin verifiers only after publishing a replacement non-expiring bearer. Before checkpointing, the staged database atomically assigns a fresh audit history generation and appends an offline restore-installation attempt. This preserves the backup's retained history and pruning marker while explicitly breaking consumer continuity. A checkpointed staged database atomically replaces the active generation without prior WAL/SHM sidecars. Only after successful installation does Gateway reopen the installed database, append the correlated successful installation outcome, and checkpoint it. Pre-install failures leave current history authoritative; a crash after installation may expose the new generation with a pending attempt and no outcome. A successful audit outcome establishes installation, not completion of subsequent marker cleanup or readiness. Desired servers and safe server history reconstruct as stopped durable facts; runtime, process/session/route state, OAuth transients, events, raw secrets, and keyring values are never restored. Marker clearing and readiness still require completed replacement verification and a fresh normal startup.
