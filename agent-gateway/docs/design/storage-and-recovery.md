# Storage and Recovery

Audience: Maintainers and contributors changing durable storage, backup, restore, and stopped recovery

Authority: Normative product design

This chapter owns the behavior and invariants described below. Operational procedures remain in the linked guides; exact executable contract values remain owned by `internal/contract` and must agree with this chapter.

## Storage durability

An installation is one canonical owner-only `0700` directory guarded by a nonblocking exclusive process lock. A durable run marker distinguishes clean shutdown from an unclean stop; either startup path performs live identity, migration, pragma, and integrity verification before the store can be ready.

SQLite WAL and SHM files may have owner read/write plus optional group/other read permissions (`0600`, `0640`, `0604`, or `0644`) inside a verified owner-only `0700` directory. Validation requires the current owner, a regular single-link file, and no execute or shared-write bits; symlinks and foreign ownership refuse. The directory remains the confidentiality boundary. Validation never changes file permissions, and supported sidecar modes do not produce warnings. Databases, rollback journals, credentials and recovery markers retain their strict owner-only rules. Accepting WAL permissions never authorizes ignoring nonempty WAL data during closed-generation inspection.

SQLite uses application ID `MGW1`, an immutable installation ULID, decimal revision, and an ordered embedded migration history. Schema 4 adds the durable server tables without changing accepted earlier tables.

Schema 8 adds the separately owned `mcp_gateway` synthetic identity, authorization revision singleton, permanent principal/current-credential slots, and immutable grant rows; migration seeds revision zero with no principals or grants and stores no raw bearer, session, credential history, issuer, or reason. Schema 9 adds an empty bounded invocation-audit store with a database-generated monotonic sequence, coherent nullable route/authorization/terminal groups, no foreign keys to mutable authority or catalog rows, and one trigger that permits only the first paired terminal annotation while leaving FIFO deletion available.

It has no bearer, verifier, downstream request ID, successful result, raw error, dispatch-started, retry, or replay column. Schema 10 additively creates an empty grant-request store with permanent identity tombstones and never-reused insertion order, immutable owner/target/requested-policy and versioned canonical dedupe bytes, bounded submitted/approved evidence, a database-maintained global evidence-byte aggregate, partial pending uniqueness, terminal-only deletion, and one exact pending-to-terminal transition.

Historical grant IDs and target identities have no foreign keys to mutable rows. Schema 11 adds only closed OAuth diagnostic stage and bounded HTTP-status columns to retained auth-flow records; the existing flow ID is the correlation ID and the existing public reason is the diagnostic reason. Schema 12 added required bounded grant names and deterministically named existing rows from their stable IDs. Schema 13 replaces names with optional bounded descriptions, preserves every existing name as its row's description, and adds a positive metadata revision used only for conditional description updates. Schema 14 is an additive compatibility fence for version-2 matchers: it changes no grant or request table and rewrites no stored constraint or dedupe bytes, while ensuring binaries that know only the version-1 matcher reject the newer database generation.

Schema 15 adds the independent control-plane audit store and history-generation singleton. Events are bounded canonical JSON with indexed, generated filter columns and no foreign keys to mutable resources. Database triggers reject updates, deletion inside the retained window, sequence reuse/gaps, and decreasing timestamps; insertion atomically retains the newest 65,536 records and records pruning. Sequences remain monotonic after pruning. Storage verifies the exact embedded audit DDL and runs the audit owner's bounded semantic validation before accepting the generation; malformed event JSON, missing members, inconsistent sequence/retention state, or changed enforcement triggers fail closed. `internal/audit` owns online audit SQL and the supplied-transaction append seam. Producer and restore-continuity qualification is tracked explicitly in the [control-plane audit coverage matrix](administrative-control-plane.md#control-plane-audit-coverage).

Schema 16 adds `grants.read_only`, `grant_requests.requested_read_only`, and `grant_requests.approved_read_only` as non-null integers restricted to 0 or 1, defaulting existing rows to 0 without changing legacy dedupe bytes. Checks limit true to server-wide ALLOW/request policy, preserve read-only approval, and require absent approved policies to remain unrestricted. Requested restrictions are immutable; grant restrictions cannot change through metadata updates. Storage verifies the embedded column/trigger definitions, while authorization and grantrequests validate durable policy semantics before readiness. Older binaries reject the newer schema rather than silently broadening restrictions.

Schema 17 adds nullable `invocations.failure_diagnostics`, a maximum 512-byte JSON object limited to failed terminal rows. Invocation validates its closed vocabulary at writes and startup. Existing rows retain NULL, with no reconstructed history. Diagnostics commit with the one terminal transition, remain immutable afterward, and are included in backups; raw errors and payloads remain forbidden.

Schema 19 adds secret-free HTTP credential metadata and permanent tombstones, and expands the keyring fence kind to `http_credential` while preserving existing MCP fences. Structural verification checks the replacement fence table and credential DDL; the HTTP credential owner validates canonical metadata and current material bindings before readiness. Backup stages with unresolved legacy HTTP material refuse before authority invalidation or installation. An older snapshot cannot reactivate retired native material.

Schema 20 adds HTTP default and grant tables without changing MCP rows. Defaults backfill to block and an exact verified principal-insert trigger seeds each new default atomically. Grant rows contain only canonical policy configuration, bounded descriptions, immutable principal/ID, revisions and timestamps. The authorization owner validates all defaults, policies and credential references during startup and staged restore. Restored unavailable credential material remains unavailable without deleting its valid grant references.

Schema 21 adds the installation CA's safe singleton revision, opaque handle and
public certificate, and expands the closed keyring fence kind to `http_ca`.
The CA owner validates metadata/bindings; signing bytes remain only in protected
credential generations (encrypted control records for new writes). Staged restore refuses unresolved legacy CA references without dropping authority or replacing keys. Format-4 restore recovers the encrypted signing identity and matching certificate without replacement. See [CA lifecycle](downstream-servers.md#installation-interception-ca).

Schema 23 adds `secret_custody` (the installation key identity/version and nonrefundable encryption count) and bounded `secret_generations` (explicit legacy selection or authenticated ciphertext). Migration labels only existing keyring handles as legacy and never reads native material. Secret selection still commits atomically with domain metadata through the existing coordinator. Admin and agent tokens remain hash-only; the protected administrator-bearer file is unchanged.

The fixed `master-key` file is a 32-byte random key, not a password. Creation uses exclusive no-follow creation relative to a verified 0700 owner directory, a regular single-link 0600 owner file, file sync and directory sync before recording key identity. Setup retains partial/uncertain files rather than overwriting or replaying creation. A complete unbound key from interrupted initial setup can be adopted explicitly; an established database key identity or encrypted material never permits generating a replacement for a missing/wrong key. Reads check ownership, mode, file type, link count and exact size. Initialization provisions custody before administrator/CA authority. Existing installations use explicit stopped `maintenance setup-secret-storage`, without reinitialization or native migration. Established missing/wrong keys fail startup closed.

### Legacy custody refusal

Native Keychain/Secret Service adapters, probes, fallback and the temporary `migrate-secrets`, `verify-secrets` and `cleanup-native-secrets` commands are removed. Storage open, restore-stage opening and immutable maintenance inspection reject any explicit legacy generation or unresolved cleanup item (`retained`, `uncertain` or `reappeared`). Custody setup, binding and deletion independently enforce that refusal. No provider scan, reference invalidation, key replacement or native cleanup occurs.

Schema 24 adds permanent, secret-free `native_cleanup` provenance and per-item disposition. Only successfully copied authoritative generations acquire cleanup ownership. This historical inventory remains recognition evidence; the current binary cannot enumerate or delete native entries. Cleanup inventory is not foreign-keyed to mutable credential authority and survives later credential deletion, replacement, backup and restore. Restore merges the current closed installation's inventory into its private stage before installation: ownership tuples must agree, and item states retain the strongest evidence (`reappeared`, then `deleted`, `uncertain`, `retained`). Missing/unreadable current evidence or conflicting ownership refuses rather than erasing it. It is not an automatic cleanup queue.

Encrypted backup and offline rotation retain complete authentication and domain-dependency validation. Missing required handles, malformed payloads, candidates, fences and unresolved legacy generations fail completeness. Expired credentials and certificates remain custody material; remote validity, token refresh, interception and client trust require separate operational checks.

Preserve refused installations and their keys, references, provenance and native objects. Complete the separately authorized pre-removal upgrade procedure with a compatible predecessor before adopting this binary; do not delete evidence or regenerate secrets to bypass refusal. Completed cleanup provenance remains compatible. No startup, migration, shutdown or support-removal path invokes native cleanup. Source tests do not authorize installed-resource mutations or promise binary downgrade safety.

### Offline master-key rotation

`maintenance rotate-master-key --retain-recovery-keys` is an explicitly confirmed stopped operation, never startup repair. It requires exclusive installation ownership, a valid existing key, complete authenticated encrypted custody and no pending candidates, authority fences, legacy generations or storage recovery markers. It preserves every generation handle, protected value, domain selection, administrator/agent verifier and exact CA identity. It never consults or migrates native custody. The `keyring/rotate` audit event and revision advance commit with the ciphertext transaction.

The crash protocol has one authority decision: the key identity in the control database. Both keys are retained before that decision:

1. Create a unique private preparation directory. Exclusively write and sync the installation ID, old key and fresh random candidate key; sync that directory. Partial preparations remain unselected and are never adopted or reused.
2. Atomically rename the complete directory to `master-key.rotation`, then sync the installation directory. Any object at that reserved path fences normal storage opening, migration and restore. No encryption starts before successful durable arming.
3. In one ordinary latched storage transaction, authenticate every retained generation, re-encrypt it with fresh random nonces and new key-bound AAD, and replace the custody identity/count. The new key's count equals the number of published generations. A failed or uncertain transaction never retries encryption with this candidate. Checkpoint and close the database before key publication.
4. Exclusively write and sync a private replacement key file, then atomically rename it over the verified old `master-key` and sync the installation directory. The recovery bundle still holds both complete keys. Verify all committed ciphertext with the selected file.
5. Rename the recovery bundle to `master-key-retained-<new-key-id>` and sync the installation directory. This removes the startup fence without deleting either key. Explicit retention acknowledgement is required; retained keys are not backup artifacts.

`--recover` is separately confirmed reconciliation, not replay. It verifies the complete bundle, installation identity, closed database and every ciphertext. If the database still selects the old key and the active file matches it, abandon the candidate permanently and archive the bundle. If it selects the new key, the active file must match the old or new key; finish/seal only key-file publication, then archive. A third identity, missing/wrong key, incomplete evidence, live WAL or unresolved storage marker refuses. A recognized storage-mutation marker must first pass the existing separately confirmed storage recovery; that operation never clears the rotation bundle. A lost completion response is inspected, not repeated. Atomic rename and successful file/directory sync provide the filesystem durability boundary; injected failures are not physical power-loss qualification.

Rotation starts a new nonce budget, never refunds an old key's usage and never makes an abandoned candidate writable. Retained historical keys are decrypt-only recovery material. Rotation cannot protect database/key copies already stolen; upstream credentials and the CA may also need explicit replacement after compromise. Account/root compromise remains outside file-key protection.

Storage owns only this DDL, seeding, and structural migration boundary. Authorization owns online SQL and validates every bounded principal-authority singleton and row coherently before the rest of the production graph is constructed; server target existence and synthetic collision checks remain delegated to the servers package on that same transaction.

Every connection installs a two-second busy policy, enables foreign keys, verifies WAL and `synchronous=FULL`, and derives `max_page_count` from the compiled 1 GiB database limit and that connection's actual page size. Foreign, newer, partial, corrupt, unsafe-permission, and over-limit generations fail closed.

## Isolated traffic store

Schema 18 adds the control-owned `traffic_selection` singleton. Production selects
one stable optional-history facade for MCP, HTTP and Git evidence,
completion and history. Its one asynchronous opener validates a selected generation
without gating security-ready serving. Control retains identities, credentials, policy, requests,
configuration and administrative audit. Storage owns traffic DDL; invocation owns
its evidence, SQL, validation, writer and reads. There is one composition graph,
installation lock and authenticator, with no dual writes, protocol registry, execution
queue or replay. Composition retains installation ownership through traffic close.
Missing, foreign, corrupt, unsafe or unreadable history is unavailable, not a
security-readiness failure. A NULL selector explicitly disables capture, retaining
legacy rows without live backfill; stopped migration can subsequently select a new
generation. Malformed control-owned selectors, control integrity, mandatory audit
and security markers remain fail-closed. Selector commits include durable
administrative audit in the same transaction. Existing configuration, grants and
credentials are unchanged by this lifecycle cutover.

An explicitly created `traffic-<generation>.db` uses application ID `MGT1`, schema
3, and exact installation/generation bindings. Control schema 20 retains the
existing installation/generation selector: its binding format does not change.
Traffic schema 2 adds a distinct `http_traffic` table, stored indexed query facts,
and immutable-admission/one-terminal triggers in the same database. MCP tables,
rows, diagnostics and public representations are unchanged. Both domains share
traffic metadata, monotonic sequence allocation, retention and physical budget.
The MCP sequence high-water includes HTTP insertions without inventing MCP rows.
Optional closed HTTP rejection, CONNECT context, response-provenance and
response-transfer termination fields
extend the existing bounded JSON payloads without DDL or historical rewrites.
Absent fields retain their original canonical encoding and accounting. Readers,
backup verification and restore validate the new vocabulary together; older
binaries cannot read newly recorded fields, so rollback against that history is
unsupported. Missing historical diagnostics, correlation and response source
remain absent. HTTP termination remains within the existing 512-byte completion
bound and fixed charge; nested stage/condition/context vocabulary is validated by
the same canonical encoder on writes, startup, reads, backup and restore. Missing
historical termination is never synthesized from status or outcome.

Traffic schema 3 adds a distinct `git_traffic` table with immutable admission and
one-terminal triggers to that same generation. Its minimal bounded admission
contains identities/revisions, operation, command count, allowed disposition and
selected material generation, never observed refs/OIDs, policy selectors, URLs,
prefixes, hashes, packs, arbitrary messages or secrets. Completion contains only
closed transport facts: prestart failure, unknown outcome or nonmutation, byte
counts, duration, status and transfer-complete flag. Missing terminal stays unknown;
even a complete HTTP 200 push remains outcome_unknown, not Git success. Git rows
share sequence allocation, cross-domain identity uniqueness, retention and
physical budgets with MCP and HTTP. Every retained row is semantically validated.

Before history attachment, under existing installation ownership and before exposing
readers or starting the writer, a schema-1 or schema-2 selected generation receives
one complete schema/binding/evidence/accounting validation. A bounded transaction
adds only the missing empty HTTP/Git tables/indexes/triggers and advances
user_version to 3.
This uses the existing writer connection, physical reservation and FULL durability;
after commit, exact new DDL and file bounds are verified without rescanning
unchanged evidence. Current schema-3 startup performs one complete validation of
all three domains. A failed or uncertain migration never produces a ready store;
a fresh startup validates the atomic version that actually settled. No online
backfill, new operator command, file replacement or implicit empty initialization
is involved. Existing selected pairs still reject `migrate-traffic`; schema-17
single-store extraction remains the explicit stopped operation. Older readers
reject traffic schema 3. Immutable paired backup verification accepts exact
schema-1, schema-2 and schema-3 definitions, and restore copies present domains into a fresh
generation while preserving missing completions. Creation checkpoints and closes an
owner-only stage, syncs its file, publishes without replacing an existing name,
and syncs the directory before and after removing the staging name. Failed
publication retains evidence; opening a missing generation never creates it.
Only one traffic store per installation can be open in the process. The existing
installation process lock remains the interprocess owner; control marker and
keyring recovery are unchanged. Traffic DDL includes the schema-17 optional bounded
failure-diagnostics column so shared invocation reads and semantic validation keep
the current evidence shape. Historical control-store write builders are test-only; production completion
persists validated, immutable encoded diagnostics in the same terminal update. The fixed retention
charge reserves the maximum diagnostic size before admission. Control schema 18
adds traffic selection after schema 17 diagnostics; stopped migration and restore
preserve diagnostics, while older schema-9-through-16 history has none. Unsupported experimental traffic schemas are rejected, not silently recreated.

The writer has one connection, WAL, `synchronous=FULL`, a 50 ms busy bound,
foreign keys, disabled cache spilling, SQLite's supported-build default WAL
autocheckpointing, and a verified page ceiling. Every physical connection receives its settings; read connections
are read-only/query-only. At most two readers (configurable 1–4) return at most 256
materialized records per call, with a one-second maximum lifetime and immediate
capacity refusal. No live SQL rows or snapshots escape. Physical SQL handles are
operation-scoped: neither reader nor writer pools retain idle connections. Each
new physical connection reapplies the durability, busy, page and read-only settings.
SQLite alone owns ordinary autocheckpointing and last-connection-close cleanup;
there is no application checkpoint scheduler, tuned threshold or capacity-triggered
checkpoint/reopen. The installation owner and unresolved transactions remain held
independently of connection pooling. This supports budgets below SQLite's default
autocheckpoint threshold without retaining an ever-growing idle WAL. A reader may
still delay cleanup; a retained WAL is valid restart state, not shutdown failure.

The default combined database-plus-WAL budget is **4,294,967,296 bytes**; the
`serve` and persisted service configuration accept 1 MiB–16 GiB through
`--traffic-budget-bytes`, validated before storage mutation. The CLI accepts integer bytes and exact-case B/KB/MB/GB/KiB/MiB/GiB units with overflow checks; installed settings retain canonical decimal bytes. File lengths, not allocated filesystem
blocks or logical SQLite page counts alone, are measured. For 4096-byte pages,
64 KiB is safety headroom; at most one third of the remainder is database pages.
Before each transaction the writer reserves `32 + (maximum_pages + 2) * 4120`
additional WAL bytes against the remaining WAL partition. If that reservation
cannot fit, the batch is discarded before mutation with `budget_reservation`
pressure. The ordinary batch-scoped handle is released after actual settlement,
including admission refusal; no discarded batch is retried. Once a pinned reader
releases, SQLite's normal connection cleanup permits subsequent independently
submitted observations to persist. A first fresh batch can still be refused before
its handle closes; refusal is not a permanent fault or a recording acknowledgment. With cache spilling disabled, fixed-schema DML can
write each dirty page once at commit; the reservation covers the entire possible
database plus commit padding for the pinned default VFS. No arbitrary SQL,
attachments, online VACUUM, cache flush, or alternate VFS is exposed. Eviction
never subtracts from physical occupancy. A post-settlement size check detects
violations; I/O uncertainty faults the writer. This conservative policy may refuse
work well below the combined limit; it is not a throughput guarantee or a hard
bound on uninterruptible filesystem I/O. Ownership remains held until settlement.

Logical retention charges include encoded evidence plus 1024 bytes and the
protocol's reserved completion payload: 512 bytes for MCP diagnostics, Git or HTTP
terminal facts. HTTP admission JSON is at most 65,536 bytes; its charge includes
that entire immutable payload. Git admission JSON is at most 8,192 bytes and
reserves the same 512-byte completion allowance. MCP retains its existing 16,384-byte charged-record
ceiling. Shared batching reserves for the largest accepted domain member. The logical allowance is one quarter of the
database partition, leaving index/fragmentation headroom. A separately configurable
1–1,000,000 retained-row ceiling bounds full semantic validation work; it is not a
promised history window. Each pruning scan is bounded by batch capacity plus 256;
insufficient space drops the whole observation batch, not live requests.
Oldest records are removed transactionally after checking incoming identities for
conflicting immutable facts. There are no execution pins: a self-contained terminal
can reconstruct a pruned or dropped initial observation. Late initial observations
cannot regress retained terminal fields. Sequence high-water and a cumulative
deleted-record count commit with rows and byte/count accounting. Generation plus
pruning metadata invalidates prior history snapshots; absent rows and incomplete
records never establish nonexecution.

The database/WAL budget excludes control storage's existing 1 GiB limit, the
bounded SHM coordination file (16 MiB maximum), memory, and creation/backup/restore
staging. Creation, paired backup and migration must preflight and reserve their
complete stage, backup and rollback costs before selection. A process-owned cooperative filesystem allowance is retained through settlement;
headroom checks account for full bounded stages, retained originals and concurrent
Gateway staging on the same filesystem. This is not physical preallocation or protection
against concurrent noncooperating filesystem consumers. Any later I/O uncertainty
fails closed. Deterministic tests are not power-loss qualification.

Commit acknowledgment—not row readability or marker cleanup—is the traffic
evidence boundary. Failures pause optional recording only, never request-local
authority. Callers enqueue sanitized observations without waiting for this boundary.
The owning boundary retains a closed cause, stage, SQLite code when known, and
settlement fact. Pre-transaction deadline/lock failure and statement failure with
confirmed rollback do not permanently latch recording. Deadline expiry after
preflight is classified at acquisition or statement settlement, not exempted as a
generic timeout. The batch is discarded once, never split, retried or replayed.

The single writer pins its actual connection through synchronous commit/rollback.
Its transaction lifetime is detached from batch cancellation solely to prevent
`database/sql` from installing an asynchronous rollback owner; statement work still
uses the finite batch deadline and SQLite retains its 50 ms busy bound. Commit or
rollback uncertainty never establishes a receipt, even if rows are readable. An
unresolved non-autocommit connection remains owned and unavailable, not pooled or
replaced. Recovery observes actual settlement without executing another rollback.
Shutdown joins the actual writer and reports unresolved settlement as unclean.
Successful checkpoint truncation or WAL absence is not a shutdown/restart correctness
prerequisite: acknowledged commits survive through WAL-aware validation. Process
absence, including service-wrapper stop confirmation, does not prove clean storage
settlement or a completed checkpoint.

The existing writer lifecycle schedules revalidation after one second, doubling
failed-attempt delay to a 30-second ceiling without an exhausted retry counter.
Each attempt has a cooperative 30-second deadline, preserves physical budgets and
performs exact file, schema/application/binding, integrity and every-row semantic
validation. It never deletes, recreates or repairs files. Known deadline, lock,
I/O and full conditions are eligible for this bounded validation; unsafe ownership,
permissions, missing files, integrity failure and unclassified settled errors need
operator action. A new failure during validation prevents that validation from
restoring authority. Time, readable status and absent records are never proof.
A successful validation permits only new independent observations; observed
`recovered` requires a new successful acknowledgment. Lost history is not rebuilt.

There is no traffic-only persistent manual-verification latch. Restart likewise
restores write authority only after exact schema/application/binding,
physical-budget, complete structural and every-row semantic validation, including
nullable groups, chronology, accounting and sequence/pruning consistency. Validation
is streaming and has a 30-second cooperative deadline; incomplete validation is
failure, never partial history readiness. Under exclusive installation and generation
ownership, `OpenTraffic` verifies path safety before SQLite opening, then authenticates
application/schema/installation/generation and all retained evidence through WAL-aware
read-only connections before opening a writable handle. This preserves rejected DB/WAL
bytes even when the prior process died and no connection survives to prevent close-time
checkpointing. After authentication, writer connection settings are verified before
exposing readers or admitting writes. It never uses
immutable reads for serving startup. Validation is not subject to the online reader's
one-second deadline and does not gate security readiness. Reads may remain available while write authority
is faulted; readable history grants no authority and cannot resume execution.
History reports `opening`, `ready`, `unavailable`, `faulted`, or `disabled` independently
of security readiness. Its additional process-local health distinguishes `healthy`,
`degraded` (validated, awaiting acknowledgment), `recovering`,
`operator_action_required`, and `recovered`. One bounded incident retains the first
failure time/cause/stage/settlement, latest recovery cause/stage, first typed SQLite
code, affected/discarded submission counts and last successful acknowledgment time.
Subsequent already-faulted refusals increase loss counts without replacing the
initiating cause. A new failure after observed recovery starts a new incident;
process delivery totals remain cumulative. No incident is written to SQLite or a
new file. Default-level failure/recovery diagnostics use the existing best-effort
bounded adapter, limited to one of each transition per minute; sink loss never
blocks status, serving or recording recovery. Valid committed traffic WAL is recovered
by ordinary SQLite opening, without manual repair, sidecar deletion, selector changes
or history reset. Unsafe paths, verified corruption, foreign bindings and unresolved
rollback journals remain typed failures; application/security recovery markers retain
their separate fail-closed boundaries. Invalid artifacts are never deleted, replaced
or chmodded by application recovery. Closed immutable inspection and digest-bound
backup verification still require closed artifacts and retain their explicit checkpoints.
Valid older traffic schemas retain their bounded supported upgrade.

The facade owns one opener lifecycle. A settled transient opening/validation
failure uses the same one-to-30-second bounded backoff; persistent faults and
uncertain schema migration are not reopened automatically. Every failed attempt
closes its own handles before another attempt, and no attached writer is replaced.
Drain interrupts backoff and prevents late attachment; a fully validated late
result is closed instead. Close joins the
opener, actual writer and reader settlement before composition releases storage or
installation ownership. Readers and schema upgrades pin their SQL connection and
retain synchronous transaction settlement, including its actual result, rather
than installing a cancellation-driven rollback owner whose error is discarded.
Every reader callback receives the bounded read context for all statements; the
transaction lifetime alone is detached. `ErrTxDone` alone is not a settlement
receipt. Caller-cancelled reads, including SQLite interruption of an executing
statement, do not fault recording after successful settlement. Independent raw
storage/integrity errors and typed settlement failures take precedence over any
joined cancellation or benign domain result. The internal read-lifetime deadline
remains a recording fault even if the caller subsequently cancels. Schema-upgrade commit and
acknowledgment uncertainty retain their owning stage and settlement; a subsequent
validation failure retains the already-committed settlement. An observation deadline may report unconfirmed cleanup,
never cancellation of fsync or permission to mark clean or start a replacement.
Process exit releases the OS lock, but an unconfirmed drain never publishes a clean
marker. Optional history does not isolate shared-filesystem stalls, uninterruptible
I/O or physical disk exhaustion.

## Installation selection after migration retirement

`internal/paths` owns canonical root selection and process locks. Defaults use `agent-gateway`; legacy directories, files, links and tombstones are not inspected and never affect implicit selection. Selected-root ownership, permissions, integrity and locking remain mandatory. The current executable uses this selection and `gateway.lock` regardless of an accidental basename change. Explicit roots remain authoritative, including legacy/custom roots; there is no filesystem search, automatic relocation, merge or second owner.

The installation-migration CLI, stopped host inspection and atomic whole-directory exchange are retired after rollout-owner attestation that all installations migrated. Historical v1 tombstones retain the exact source, destination, device and inode record. Neither completed nor unknown, incomplete, oversized, unsafe or mismatched records participate in implicit selection. The source tombstone still blocks old binaries from recreating a root; it is not portable backup metadata.

Nothing deletes or rewrites tombstones, interrupted reservations, old plists, logs, backups or recovery markers. There is no resume or reverse-exchange command. Unexpected residual state or durability uncertainty requires operator investigation and a separately reviewed stopped plan. Binary/configuration rollback retains the same destination root. See [installation safety](../operators/installation-safety.md).

Retirement preserves installation ULID, SQLite application/schema identity, database/backup lineage and metadata, `gateway.db`/`gateway.lock`, bearer/verifier/fingerprint domains, native-keyring services/generation handles, policy/history/OAuth material and all MCP and client credential contracts. Deterministic source tests are not native service/keyring or live-host adoption evidence.

## Security mutation and stopped recovery

### Mutation intent and latch

Security mutations share one actual control-storage owner. Ordinary and recovery-bearing APIs attempt immediate acquisition and never queue. The obsolete invocation FIFO, waiter deadlines and receipt-dependent runtime path are removed. Active ownership lasts through transaction and marker settlement. Pre-mutation capacity, cancellation or latch refusal creates no intent or row. No waiting deadline substitutes for active settlement and no automatic callback retry is introduced.

Authorized self-service access-request create/cancel remains synchronous owner-scoped durable control work behind its admitted subject. Administrative grants, credentials, routing and approval/rejection retain mandatory atomic audit and honest external-effect uncertainty. Optional traffic loss does not relax these controls.

Before a transaction begins, Gateway writes an installation-bound owner-only intent through temp write, file sync, atomic rename, and directory sync. A known commit or rollback moves the intent through a synced tombstone deletion. Marker I/O failures, storage-class statement failures, busy begin, commit errors, and post-commit uncertainty latch mutations; elapsed time, restart, or successful reads cannot clear the latch. The at-most-512-byte marker has one closed recovery union: the existing keyring fence action or one agent credential candidate containing only principal ID, credential ID, and captured positive principal/credential revisions. Recovery-bearing tombstone deletion restores the tombstone if final directory sync fails.

### Diagnostic observations

The startup-bound storage observer reports actual mutation acquire/release/reject facts, hold durations and process-local mutation-attempt correlation. Occupancy is one actual writer with no waiting slots. Control work uses the coarse `foreign` writer class without tracing its resource or workflow; runtime traffic no longer enters this control mutation path. Facts are sampled under short owner locks where needed, but encoding and output run only on the separate diagnostic worker, outside storage/authority/catalog locks.

Durability failure and latch events classify size check, identity check, intent arm, transaction begin/body/commit/rollback, and intent cleanup. Local diagnostics must also preserve useful underlying and joined cleanup causes, affected paths and native codes under the [operator disclosure policy](administrative-control-plane.md#operator-diagnostic-disclosure-policy), without dumping secret-bearing SQL values or marker contents. The source owner snapshots local explanations after storage admission release; traffic writers also release their writer gate before formatting details. Public incidents retain their existing closed projection. The closed diagnostic queue is not an audit queue and cannot change intent settlement, latch/recovery, actual slot ownership, or mandatory audit-before-dispatch. Sink loss or flush expiry never marks otherwise settled storage unclean. See [serve diagnostics](administrative-control-plane.md#serve-diagnostics) for bounds and output failure semantics.

### Agent-candidate recovery

`maintenance verify-and-recover-storage` reacquires stopped-process ownership, requires the current schema, and applies a recognized recovery action before clearing marker artifacts. Agent candidate cleanup is the sole stopped-process agent-credential SQL exception in storage: it clears only an exact current ID/revision tuple and advances principal and credential revisions once, while absent, replaced, or stale candidates are no-ops and no prior credential is restored. Unknown, mixed, disagreeing, foreign-installation, oversized, or failed recovery remains latched. The command closes SQLite before durable marker removal, emits one safe machine JSON result, and does not make the service ready; normal startup must verify the generation again.

### Restore credential invalidation

Authorization separately owns general stopped-stage credential surgery on a supplied verified current-schema replacement store. One marker-armed transaction validates all synthetic, principal, grant, target, capacity, and revision semantics before writing; clears every complete current slot while advancing only each affected principal and credential revision once; then revalidates the complete authority and absence of current slots before commit. Zero credentials is an exact no-op. Principal metadata and timestamps, grants, synthetic identity, authorization revision, Gateway revision, server facts, and admin authority remain unchanged. Backup owns no principal, credential, or grant DML. For every accepted schema-3-through-current (currently 21) restore lineage, orchestration migrates and initially verifies the stage, invokes this authorization seam, rekeys admin authority, checkpoints and closes, then reruns closed SQLite verification plus complete authorization and grant-request semantics through the server-target and request supplied-transaction inspectors before installation. Every pre-install fault leaves the original generation authoritative.

### Operator command boundary

The canonical executable exposes `maintenance verify-and-recover-storage`, `maintenance reset-admin-credentials`, `maintenance restore-backup BACKUP_ID`, `maintenance migrate-traffic-storage`, `maintenance setup-secret-storage`, and `maintenance rotate-master-key`. Retired `storage`, `admin reset`, `backup restore`, top-level `restore`, and `restore --verify-current` spellings have no execution aliases. Verification is current-schema recovery, not reset, initialization, backup selection, or service startup. Restore requires one explicit valid backup ID and a fresh exclusive owner-only replacement bearer sink. Migration requires an exact installation ID; other operations accept an optional assertion.

Every maintenance command plans against an existing stopped owner, supports nonmutating `--dry-run`, and requires default-no confirmation or explicit `--confirm`. Immutable inspection refuses nonempty WAL/journal state rather than hiding committed content or opening writable recovery. Security-only verification and administrator reset hash the closed control database and marker slots; this also binds the control-owned selector without reading untouched optional history. Explicit migration and history-inclusive restore additionally inspect and bind their history targets. Security-only restore binds only control, security markers and independently verified artifact control; untouched optional history and unrelated retained artifacts do not participate in approval. A changed control-owned selector still invalidates consent. Execution revalidates the inspected plan under uninterrupted stopped ownership before any write. Unknown or inconsistent marker actions and preexisting restore-stage artifacts refuse. Dry runs never write audits, markers, SQLite sidecars, stages or bearer files. Domain owners remain responsible for mutation semantics and report known staging, changed-selection and uncertain effects without replay.

CLI success projections use `operation:"verify-and-recover-storage"` or `operation:"restore-backup"`, with no `mode` member. Both retain `ok`, `installation_id`, and decimal-string `revision`; only restore has `backup_id` and `history` (`omitted-not-verified` or `restored`). This projection is separate from durable backup metadata and audit vocabulary. Exact JSON, typed exits, stopped-service prerequisites, and rollback/uncertainty procedures are published in the [operator recovery guide](../operators/backup-and-recovery.md#structured-results-and-exits). No command rename alters data-root precedence, schema support, installed service argv, installation/credential/keyring identities, lineage, or recovery markers. No automatic replay or compensation is added.

## Backup and generation replacement

### Backup publication

**Encrypted recovery:** format 4 includes authenticated encrypted upstream generations and the CA signing identity in the control snapshot. Metadata binds the required SHA-256 `master_key_id`; the master key remains separately safeguarded and is never included. All retained generations must use the same encrypted key identity, authenticate successfully, and cover selected authorities; unresolved legacy dependencies, candidates and fences refuse creation. Snapshot verification never reads native storage. Raw administrator/agent tokens and all traffic remain excluded.

Stopped format-4 restore requires an existing installation with a structurally verified closed current database, matching installation identity and a safe current `master-key`. Same-key recovery requires matching backup/current key identity. After rotation, an explicit `--recovery-key` may instead select the backup's original safe key, solely for decryption; it never replaces the active key. It authenticates every retained generation before staging, validates domain configuration and authority, preserves upstream selections and the exact CA certificate, invalidates all restored agent slots and resets all administrator verifiers. Both backed-up and post-backup access tokens become invalid. No upstream calls establish remote validity; expiry, revocation or refresh-token rotation can require reauthentication.

The key lifetime encryption count is nonrefundable across restore: under uninterrupted stopped ownership, read the current high-water only after refusing nonempty WAL/journal state. Same-key restore carries `max(current, backup)` into the staged custody singleton, copies ciphertext and performs no encryption. Cross-key restore additionally requires unlatched current storage and the explicit matching historical key. After consent, reserve `N` encryptions in the current database before producing any staged ciphertext, where `N` is every retained backup generation, and refuse when `current + N > 2^32`. Checkpoint and close that reservation, then decrypt/re-encrypt the private stage under the still-current key with count `current + N`. The historical key never encrypts again; its old counter is not merged into a different key's budget. Every failed/interrupted stage keeps its reservation, even if stage cleanup succeeds. Unknown reservation effects stop before encryption, with no refund or replay. Verify the exact target key/count tuple after checkpoint and before installation. Backups themselves remain untouched.

Repeated restores and either side of atomic replacement preserve consumed publications/reservations. This is a single installation lineage, not clone/import/host relocation or manual database rollback; a lost or rolled-back current counter cannot be reconstructed from old artifacts. Rotation candidates are one-attempt keys, and cross-key staged exposures are conservatively charged before use.

Formats 0, 2 and 3 retain historical recovery support only when no legacy material dependency or unresolved cleanup provenance remains. They are not self-contained secret recovery and cannot restore into encrypted custody, including dry run and `--security-only`; an encrypted database copy in an old format also refuses. Old idempotency replay cannot bypass this boundary. `encrypted_backup_unsupported` names incompatible formats or unresolved legacy dependencies; unknown formats are invalid. Existing backup inventory and verified artifact reads/deletion remain available without weakening source checks. Legacy native credential reads are not supported.

Supported encrypted installations create **format 4**; historical unconfigured storage fixtures retain **format 3**, with `history:"omitted"` in metadata and
public backup representations. They contain only the control database and metadata,
not a traffic database. Creation pins one control-only SQLite snapshot with a
30-second cooperative lifetime, then removes embedded legacy invocation rows from
the private copy and compacts it before verification/digest/publication. It never
acquires the traffic writer, opens history, validates omitted history or reserves
traffic-sized staging. The control copy, compaction and WAL use a cooperative
four-times-control-limit reservation (4 GiB), independent of history occupancy or
budget. Configuration, authority and administrative audit remain intact. Inventory
continues to enforce safe metadata, formats and the fixed record bound.

Format 0 remains the original single-store artifact and format 2 remains a fully
verified pair. Neither is reinterpreted as security-only. Existing format 2 binds the control selector and installation to a verified traffic
generation, with independent sizes, SHA-256 digests and traffic budget metadata.
Under a one-second maximum admission/writer pause, Gateway pins exact coherent
SQLite read connections for both stores. Actual acquisition overruns fail even
when SQL succeeds; refusal never latches otherwise healthy traffic. Locks release
only after acquisition settles. Copying uses those stable snapshots outside the
pause, with a 30-second lifetime; checkpoint pressure refuses traffic work rather
than waiting for the backup. Already admitted work may complete later, leaving
unknown terminal evidence in the snapshot. Pins and execution are never backed up.

Stopped-process and backup procedures are canonical in [backup and recovery](../operators/backup-and-recovery.md). On-demand backup uses SQLite's online backup API under one nonblocking global work slot. Gateway stages an owner-only closed generation, verifies identity/schema/revision/full integrity and the 1 GiB bound, computes SHA-256, writes safe internal metadata, and atomically publishes it under a 26-character ID. The artifact-bound authority/key digest provides durable retry identity without storing a bearer or replaying a secret; 64 retained artifacts are the fixed record bound. Verification of a closed artifact reads its digest-bound database immutably: it neither creates sidecars under ambient permissions nor consults an unrelated WAL. Existing sidecars are not deleted by verification or installation naming cleanup.

### Status occupancy

System status obtains backup-record and retained backup-idempotency counts in one
metadata traversal, using one retention instant and the existing inclusive
idempotency window. Reads observe completed create/delete operations without a
cache; hidden staging entries are not published records. Directory enumeration
uses bounded batches and each metadata JSON read is limited to 8 KiB, with strict
closed fields, duplicate rejection, identity/format checks and valid timestamps.
An excess visible entry beyond the fixed 64-record maximum fails accounting before
opening that entry's metadata; status never truncates an over-limit count into a
successful saturated result.
Descriptor-relative no-follow opens validate owner-only directories and regular
metadata files before reading. Missing, unreadable, malformed or unsafe required
metadata fails the status request with `storage_unavailable`, never healthy zero
occupancy. A concurrent deletion may likewise cause a failed read; the next
request observes the completed deletion without retrying the mutation.

These counts do not establish artifact integrity: status neither opens nor reads
backup databases, hashes their contents, nor runs SQLite or traffic verification.
Startup and list inventory use the same bounded no-follow metadata discipline,
not historical payload verification. Enumeration additionally bounds hidden entries
to 4096; excess or malformed inventory is explicit backup-administration
unavailability, never healthy zero or automatic deletion. Backup-manager construction
does not fail security startup for an inventory error. Creation/publication,
selected get/delete, idempotent creation and stopped restore retain full verification.
A listed artifact establishes inventory presence, not restore integrity. Supported new creation uses history-independent format 4 for encrypted custody; format 3 remains recognized for historical unconfigured storage. Metadata accounting is not
restore or mutation authorization; the successful status representation is unchanged.

### Generation replacement

Stopped migration stages the control copy without upgrading the original, validates
and publishes a generation-addressed traffic file first, then installs its matching
control selector by one atomic rename. The original control inode is checkpointed
and retained under a unique `.previous-*` name before replacement; old traffic and
interrupted stages remain. Two fixed-file renames are never described as atomic.
Initial setup creates the matching pair before publishing authority. An explicitly history-inclusive legacy restore uses
a fresh traffic generation, invalidating history cursors while preserving IDs,
high-water, pruning and unknown outcomes. Legacy single-store backups receive
bounded semantic evidence validation and stopped extraction; accepted schemas
before invocation history legitimately extract an empty store. No path restores
execution pins or falls back to empty history.

Format-3/4 restore and explicit `--security-only` imports of format 0 or 2 retain
stopped-exclusive ownership, current closed-control/marker checks, independently
verified control metadata, size, digest, schema and staged authority validation.
They publish a coherent control replacement with a NULL history selector, never an
unrelated existing history generation. Selector disabling is audited in the supplied
transaction. Legacy embedded rows are omitted only from the bounded private stage,
which is compacted before installation; originals are unchanged. Missing/corrupt
paired history is not read and is reported `omitted-not-verified`, never a valid
whole pair. Full `backup get`/delete and default legacy restore still verify every
claimed history payload. Unknown or conflicting control markers, control WAL/journals,
unsafe mutation targets and preexisting restore stages refuse. Unrelated retained
artifacts, including unsafe history paths, are neither followed nor removed.

Backup restore holds stopped-process ownership, validates the published artifact and current installation binding, and copies one complete control generation. Accepted schema-3-through-current (currently 24) artifacts are forward-migrated as necessary and fully verified while staged before restored agent credentials are invalidated, admin authority is rekeyed, and replacement is published. The staged database resets all restored admin verifiers only after publishing a replacement non-expiring bearer. Before checkpointing, the staged database atomically assigns a fresh audit history generation and appends an offline restore-installation attempt. This preserves the backup's retained history and pruning marker while explicitly breaking consumer continuity. A checkpointed staged database atomically replaces the active generation without prior WAL/SHM sidecars. Only after successful installation does Gateway reopen the installed database, append the correlated successful installation outcome, and checkpoint it. Pre-install failures leave current history authoritative; a crash after installation may expose the new generation with a pending attempt and no outcome. A successful audit outcome establishes installation, not completion of subsequent marker cleanup or readiness. Desired servers and safe server history reconstruct as stopped durable facts; runtime, process/session/route state, OAuth transients, events and raw access tokens are never restored. Only format 4 recovers authenticated encrypted upstream material; legacy-only restore does not recover native values. Marker clearing and readiness still require completed replacement verification and a fresh normal startup.

The default `admin-bearer` file is deliberately retained but invalid after restore. The operator must explicitly select the fresh protected `--secret-output` file; it is never silently promoted. Pre-install failure may leave an inactive fresh bearer file. Format-4 CA authority/certificate selection is atomic inside SQLite. Its derived `http-ca.pem` export is preflighted against the current certificate and published after installation, without replacing unrelated files. Failure after selection is reported as changed/uncertain, never rollback; export inspection and an explicitly qualified recovery plan reconcile a stale derived file. Client trust is not mutated.

### Independent history export

`GET /api/v2/history/export` and `history export` materialize one existing bounded
read-only traffic transaction. The administrator-only response identifies installation,
generation, capture time, high-water, pruning, retained count, requested/next sequence,
truncation and protocol-tagged full MCP/HTTP/Git records. At most 256 aggregate records
and 900 KiB are returned, within the existing one-second reader lifetime and immediate
capacity refusal. There is no control mutation, traffic writer pause, export job,
filesystem stage, execution queue or replay. Security backup and restore do not depend
on export success. Unavailable history is `history_unavailable`, never a successful
empty export; capacity/deadline failure is `history_busy`.

Coverage is only that response's rolling-history snapshot, not a complete traffic
audit. `after_sequence` requests a new transaction, not a continuation of a frozen
snapshot: completion, pruning and generation can change between calls. Optional
inclusive `through_sequence` limits records to an earlier high-water without pinning
storage or changing global coverage metadata. Consumers must compare
generation/coverage; absent rows never establish nonexecution and missing
completion remains unknown. Export neither repairs nor deletes original files,
unsafe paths, journals or interrupted stages.
