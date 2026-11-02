# Storage and Recovery

Audience: Maintainers and contributors changing durable storage, artifact preservation, and stopped recovery

Authority: Normative product design

This chapter owns the behavior and invariants described below. Operational procedures remain in the linked guides; exact executable contract values remain owned by `internal/contract` and must agree with this chapter.

## Storage durability

An installation is one canonical owner-only `0700` directory guarded by a nonblocking exclusive process lock. A durable run marker distinguishes clean shutdown from an unclean stop; either startup path performs live identity, migration, pragma, and integrity verification before the store can be ready.

SQLite WAL and SHM files may have owner read/write plus optional group/other read permissions (`0600`, `0640`, `0604`, or `0644`) inside a verified owner-only `0700` directory. Validation requires the current owner, a regular single-link file, and no execute or shared-write bits; symlinks and foreign ownership refuse. The directory remains the confidentiality boundary. Validation never changes file permissions, and supported sidecar modes do not produce warnings. Databases, rollback journals, credentials and recovery markers retain their strict owner-only rules. Accepting WAL permissions never authorizes ignoring nonempty WAL data during closed-generation inspection.

SQLite uses application ID `MGW1`, an immutable installation ULID, decimal revision, and an ordered embedded migration history. Schema 4 adds the durable server tables without changing accepted earlier tables.

Schema 8 adds the separately owned `mcp_gateway` synthetic identity, authorization revision singleton, permanent principal/current-credential slots, and immutable grant rows; migration seeds revision zero with no principals or grants and stores no raw bearer, session, credential history, issuer, or reason. Schema 9 adds an empty bounded invocation-audit store with a database-generated monotonic sequence, coherent nullable route/authorization/terminal groups, no foreign keys to mutable authority or catalog rows, and one trigger that permits only the first paired terminal annotation while leaving FIFO deletion available.

It has no bearer, verifier, downstream request ID, successful result, raw error, dispatch-started, retry, or replay column. Schema 10 additively creates an empty grant-request store with permanent identity tombstones and never-reused insertion order, immutable owner/target/requested-policy and versioned canonical dedupe bytes, bounded submitted/approved evidence, a database-maintained global evidence-byte aggregate, partial pending uniqueness, terminal-only deletion, and one exact pending-to-terminal transition.

Historical grant IDs and target identities have no foreign keys to mutable rows. Schema 11 adds only closed OAuth diagnostic stage and bounded HTTP-status columns to retained auth-flow records; the existing flow ID is the correlation ID and the existing public reason is the diagnostic reason. Schema 12 added required bounded grant names and deterministically named existing rows from their stable IDs. Schema 13 replaces names with optional bounded descriptions, preserves every existing name as its row's description, and adds a positive metadata revision used only for conditional description updates. Schema 14 is an additive compatibility fence for version-2 matchers: it changes no grant or request table and rewrites no stored constraint or dedupe bytes, while ensuring binaries that know only the version-1 matcher reject the newer database generation.

Schema 15 adds the independent control-plane audit store and history-generation singleton. Events are bounded canonical JSON with indexed, generated filter columns and no foreign keys to mutable resources. Database triggers reject updates, deletion inside the retained window, sequence reuse/gaps, and decreasing timestamps; insertion atomically retains the newest 65,536 records and records pruning. Sequences remain monotonic after pruning. Storage verifies the exact embedded audit DDL and runs the audit owner's bounded semantic validation before accepting the generation; malformed event JSON, missing members, inconsistent sequence/retention state, or changed enforcement triggers fail closed. `internal/audit` owns online audit SQL and the supplied-transaction append seam. Producer and historical continuity qualification is tracked explicitly in the [control-plane audit coverage matrix](administrative-control-plane.md#control-plane-audit-coverage).

Schema 16 adds `grants.read_only`, `grant_requests.requested_read_only`, and `grant_requests.approved_read_only` as non-null integers restricted to 0 or 1, defaulting existing rows to 0 without changing legacy dedupe bytes. Checks limit true to server-wide ALLOW/request policy, preserve read-only approval, and require absent approved policies to remain unrestricted. Requested restrictions are immutable; grant restrictions cannot change through metadata updates. Storage verifies the embedded column/trigger definitions, while authorization and grantrequests validate durable policy semantics before readiness. Older binaries reject the newer schema rather than silently broadening restrictions.

Schema 17 adds nullable `invocations.failure_diagnostics`, a maximum 512-byte JSON object limited to failed terminal rows. Invocation validates its closed vocabulary at writes and startup. Existing rows retain NULL, with no reconstructed history. This historical single-store schema committed diagnostics with the terminal transition and included them in its backups. Current optional traffic observations retain the same closed diagnostic bound; raw errors and payloads remain forbidden.

Schema 19 adds secret-free HTTP credential metadata and permanent tombstones, and expands the keyring fence kind to `http_credential` while preserving existing MCP fences. Structural verification checks the replacement fence table and credential DDL; the HTTP credential owner validates canonical metadata and current material bindings before readiness. Unresolved legacy HTTP material refuses without authority invalidation or native access.

Schema 20 adds HTTP default and grant tables without changing MCP rows. Defaults backfill to block and an exact verified principal-insert trigger seeds each new default atomically. Grant rows contain only canonical policy configuration, bounded descriptions, immutable principal/ID, revisions and timestamps. The authorization owner validates all defaults, policies and credential references during startup. Unavailable credential material remains unavailable without deleting its valid grant references.

Schema 21 adds the installation CA's safe singleton revision, opaque handle and
public certificate, and expands the closed keyring fence kind to `http_ca`.
The CA owner validates metadata/bindings; signing bytes remain only in protected
credential generations (encrypted control records for new writes). Unresolved legacy CA references refuse without dropping authority or replacing keys. See [CA lifecycle](downstream-servers.md#installation-interception-ca).

Schema 23 adds `secret_custody` (the installation key identity/version and nonrefundable encryption count) and bounded `secret_generations` (explicit legacy selection or authenticated ciphertext). Migration labels only existing keyring handles as legacy and never reads native material. Secret selection still commits atomically with domain metadata through the existing coordinator. Admin and agent tokens remain hash-only; the protected administrator-bearer file is unchanged.

Schema 25 adds configuration-import input/result digests, independent server first-activation fences and exact expired-grant receipts. Existing installations migrate with empty import facts and unchanged authority. The stopped importer owns one atomic definitions/receipt transaction; authorization owns expired-policy validation and servers own explicit activation. The result digest binds the resulting portable graph, activation and expiry facts, while domain checks prove unset imported authority. Storage verifies the exact embedded DDL. Current-schema validation never falls back on missing or damaged import evidence. These are new-installation facts, never restored source history or secret material; see [initialization and import](administrative-control-plane.md#installation-and-stopped-commands).

The fixed `master-key` file is a 32-byte random key, not a password. Creation uses exclusive no-follow creation relative to a verified 0700 owner directory, a regular single-link 0600 owner file, file sync and directory sync before recording key identity. Setup retains partial/uncertain files rather than overwriting or replaying creation. A complete unbound key from interrupted initial setup can be adopted explicitly; an established database key identity or encrypted material never permits generating a replacement for a missing/wrong key. Reads check ownership, mode, file type, link count and exact size. Initialization provisions custody before administrator/CA authority. Existing installations use explicit stopped `maintenance setup-secret-storage`, without reinitialization or native migration. Established missing/wrong keys fail startup closed. Explicit initialization resume additionally retains a custody-publication intent before key creation; an interrupted attempt with no remaining key refuses instead of generating a new identity. The bounded initialization protocol in [administrative control](administrative-control-plane.md#installation-and-stopped-commands) is the sole initialization resume protocol and never clears historical SQL markers.

### Legacy custody refusal

Native Keychain/Secret Service adapters, probes, fallback and the temporary `migrate-secrets`, `verify-secrets` and `cleanup-native-secrets` commands are removed. Storage open, migration-stage opening and immutable maintenance inspection reject any explicit legacy generation or unresolved cleanup item (`retained`, `uncertain` or `reappeared`). Custody setup, binding and deletion independently enforce that refusal. No provider scan, reference invalidation, key replacement or native cleanup occurs.

Schema 24 adds permanent, secret-free `native_cleanup` provenance and per-item disposition. Only successfully copied authoritative generations acquire cleanup ownership. This historical inventory remains recognition evidence; the current binary cannot enumerate or delete native entries. Cleanup inventory is not foreign-keyed to mutable credential authority and survives later credential deletion and replacement. It is not an automatic cleanup queue.

Offline rotation retains complete encrypted-custody authentication; normal startup retains domain-dependency validation. Missing required handles, malformed payloads, candidates, fences and unresolved legacy generations fail completeness. Expired credentials and certificates remain custody material; remote validity, token refresh, interception and client trust require separate operational checks.

Preserve refused installations and their keys, references, provenance and native objects. Complete the separately authorized pre-removal upgrade procedure with a compatible predecessor before adopting this binary; do not delete evidence or regenerate secrets to bypass refusal. Completed cleanup provenance remains compatible. No startup, migration, shutdown or support-removal path invokes native cleanup. Source tests do not authorize installed-resource mutations or promise binary downgrade safety.

### Offline master-key rotation

`maintenance rotate-master-key --retain-recovery-keys` is an explicitly confirmed stopped operation, never startup repair. It requires exclusive installation ownership, a valid existing key, complete authenticated encrypted custody and no pending candidates, authority fences, legacy generations or unrecognized historical security evidence. It preserves every generation handle, protected value, domain selection, administrator/agent verifier and exact CA identity. It never consults or migrates native custody. The `keyring/rotate` audit event and revision advance commit with the ciphertext transaction.

The crash protocol has one authority decision: the key identity in the control database. Both keys are retained before that decision:

1. Create a unique private preparation directory. Exclusively write and sync the installation ID, old key and fresh random candidate key; sync that directory. Partial preparations remain unselected and are never adopted or reused.
2. Atomically rename the complete directory to `master-key.rotation`, then sync the installation directory. Any object at that reserved path fences normal storage opening and migration. No encryption starts before successful durable arming.
3. In one ordinary storage transaction, authenticate every retained generation, re-encrypt it with fresh random nonces and new key-bound AAD, and replace the custody identity/count. The new key's count equals the number of published generations. A failed or uncertain transaction never retries encryption with this candidate. Checkpoint and close the database before key publication.
4. Exclusively write and sync a private replacement key file, then atomically rename it over the verified old `master-key` and sync the installation directory. The recovery bundle still holds both complete keys. Verify all committed ciphertext with the selected file.
5. Rename the recovery bundle to `master-key-retained-<new-key-id>` and sync the installation directory. This removes the startup fence without deleting either key. Explicit retention acknowledgement is required; retained keys are not backup artifacts.

`--recover` is separately confirmed reconciliation, not replay. It verifies the complete bundle, installation identity, closed database and every ciphertext. If the database still selects the old key and the active file matches it, abandon the candidate permanently and archive the bundle. If it selects the new key, the active file must match the old or new key; finish/seal only key-file publication, then archive. A third identity, missing/wrong key, incomplete evidence, unsafe sidecars, unresolved journal or unrecognized historical marker refuses. Reconciliation reads committed WAL under exclusive ownership, authenticates the selected key against every ciphertext and seals the database/WAL evidence for confirmation. It never checkpoints or opens a writer during inspection. Nonempty WAL without its safe shared-memory index requires a separately qualified stopped plan. Recognized historical SQL intents remain untouched; no generic marker clearance precedes rotation recovery. A lost completion response is inspected, not repeated. Atomic rename and successful file/directory sync provide the filesystem durability boundary; injected failures are not physical power-loss qualification.

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
Absent fields retain their original canonical encoding and accounting. Readers and startup validate the new vocabulary together; older
binaries cannot read newly recorded fields, so rollback against that history is
unsupported. Missing historical diagnostics, correlation and response source
remain absent. HTTP termination remains within the existing 512-byte completion
bound and fixed charge; nested stage/condition/context vocabulary is validated by
the same canonical encoder on writes, startup and reads. Missing
historical termination is never synthesized from status or outcome.

Traffic schema 3 adds a distinct `git_traffic` table with immutable admission and
one-terminal triggers to that same generation. Its bounded admission contains
identities/revisions, configured policy facts, operation, command count, allowed
disposition, selected material generation and an optional bounded requested-ref
prefix. The [public contract](public-contract.md#git-configuration-resources)
owns the deliberate ref-name retention exception and strict limits; observed
URLs, OIDs, wire prefixes, hashes, packs, arbitrary messages and secrets remain
excluded. Completion contains closed transport facts and, when actually known,
upstream aggregate and aligned per-ref claims. Legacy field absence remains
unavailable; no DDL migration or history reconstruction is needed. Missing terminal stays unknown;
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
reject traffic schema 3. Traffic creation checkpoints and closes an
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
adds traffic selection after schema 17 diagnostics; stopped migration preserves diagnostics, while older schema-9-through-16 history has none. Unsupported experimental traffic schemas are rejected, not silently recreated.

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
`serve` and operator-authored supervisor arguments accept 1 MiB–16 GiB through
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
reserves the same 512-byte completion allowance. Ref evidence uses at most 1,536
bytes and eight entries within admission (shortened further to fit its envelope),
not an additional charge. Completion uses at most eight `ok`/`ng` entries, never
repeated names or raw messages, within its existing allowance. Count/byte overflow
is explicitly `truncated`, including a possible empty prefix. MCP retains its existing 16,384-byte charged-record
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
bounded SHM coordination file (16 MiB maximum), memory, and creation/migration
staging. Traffic-stage creation and migration must preflight and reserve their
complete stage, retained-original and rollback costs before selection. A process-owned cooperative filesystem allowance is retained through settlement;
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
absence, including supervisor stop confirmation, does not prove clean storage
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
or chmodded by application recovery. Closed immutable inspection still requires closed artifacts and retains its explicit checkpoints.
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

### Committed state and process-local latch

Security mutations share one actual control-storage owner. Ordinary and recovery-bearing APIs attempt immediate acquisition and never queue. The obsolete invocation FIFO, waiter deadlines and receipt-dependent runtime path are removed. Active ownership lasts through actual driver transaction and connection settlement. Pre-mutation capacity, cancellation or latch refusal creates no row or filesystem intent. No waiting deadline substitutes for active settlement and no automatic callback retry is introduced.

Authorized self-service access-request create/cancel remains synchronous owner-scoped durable control work behind its admitted subject. Administrative grants, credentials, routing and approval/rejection retain mandatory atomic audit and honest external-effect uncertainty. Optional traffic loss does not relax these controls.

Database-only mutations create no filesystem intent or tombstone. Domain changes and mandatory audit commit atomically. After a lost response or crash, committed validated SQLite state remains authority; missing acknowledgment never proves rollback or permits replay. Normal exclusive startup verifies WAL-aware identity/schema/migrations, durability settings, integrity and bounds, followed by current domain, policy and authenticated-custody validation before readiness. Corrupt, foreign, unsafe storage, wrong keys and invalid authority remain blockers.

Each mutation pins its actual SQL connection. The transaction lifetime alone is detached from caller cancellation so database/sql cannot install an asynchronous rollback owner; statement work still uses its finite caller deadline. Commit/rollback runs synchronously. A connection returns to the pool only after the actual driver is observed in autocommit; unresolved ownership is quarantined and storage remains process-locally latched. Commit/rollback errors, storage-class body failures and lost local acknowledgment still fence that process. Readable rows, elapsed time, cancellation and sql.ErrTxDone alone are not settlement proof. Restart never clears a live owner's fence; a new exclusive process accepts only the fully validated committed state.

### Diagnostic observations

The startup-bound storage observer reports actual mutation acquire/release/reject facts, hold durations and process-local mutation-attempt correlation. Occupancy is one actual writer with no waiting slots. Control work uses the coarse `foreign` writer class without tracing its resource or workflow; runtime traffic no longer enters this control mutation path. Facts are sampled under short owner locks where needed, but encoding and output run only on the separate diagnostic worker, outside storage/authority/catalog locks.

Durability failure and latch events classify size check, identity check and transaction begin/body/commit/rollback. Local diagnostics must also preserve useful underlying and joined cleanup causes, affected paths and native codes under the [operator disclosure policy](administrative-control-plane.md#operator-diagnostic-disclosure-policy), without dumping secret-bearing SQL values or marker contents. The source owner snapshots local explanations after storage admission release; traffic writers also release their writer gate before formatting details. Public incidents retain their existing closed projection. The closed diagnostic queue is not an audit queue and cannot change transaction settlement, process-local latching, actual slot ownership, mandatory atomic security-administration audit, or request-local authorization/confirmation. Optional traffic history remains independent of execution. Sink loss or flush expiry never marks otherwise settled storage unclean. See [serve diagnostics](administrative-control-plane.md#serve-diagnostics) for bounds and output failure semantics.

### Credential commits and historical evidence

Agent issuance and keyring activation use the same committed-state policy. Committed issuance is not invalidated just because bearer delivery was lost. Operators inspect metadata, revoke and reissue deliberately; no bearer recovery or prior-authority restoration exists. Activation is authoritative only with its actual transactional prerequisites, exact published revision and current policy/ciphertext validation. A genuinely pending fence still denies reads; superseded generations never become fallback authority.

Historical generic and recovery-bearing SQL marker slots are recognized only under exclusive installation ownership and validated database identity. Recognition uses the bounded closed document and exact known action union, rejecting unknown members, duplicates, malformed, oversized, mixed, conflicting, unsafe or foreign evidence. Recognized files remain byte-for-byte unchanged, with no compensating candidate invalidation, activation refencing, replay or manual clearance. Unknown evidence remains a blocker requiring a qualified stopped plan. Retained backup/restore artifacts are never traversed or mutated; recognition of security-authority evidence is not restored backup support.

### Operator command boundary

The canonical executable exposes `maintenance reset-admin-credentials`, `maintenance migrate-traffic-storage`, `maintenance setup-secret-storage`, and `maintenance rotate-master-key`. Generic storage verification/recovery and retired recovery spellings have no execution aliases or replacement catch-all repair/unlock command. Initialization retains its existing explicit phase-bound resume protocol. Migration requires an exact installation ID; other operations accept an optional assertion. Administrator reset requires a fresh exclusive owner-only replacement bearer sink.

Every maintenance command plans against an existing stopped owner, supports nonmutating `--dry-run`, and requires default-no confirmation or explicit `--confirm`. Ordinary immutable inspection refuses nonempty WAL/journal state rather than hiding committed content or opening writable recovery; dedicated rotation reconciliation uses its exclusive WAL-aware inspection instead. Administrator reset hashes closed control and historical marker slots, binding the selector without reading untouched optional history. Explicit migration additionally inspects and binds its history target. A changed selector invalidates consent. Execution revalidates the plan under uninterrupted stopped ownership before writing. Unknown or inconsistent marker actions refuse. Dry runs never write audits, markers, sidecars, stages or bearer files. Domain owners report known staging, changed-selection and uncertain effects without replay.

Exact JSON, typed exits, stopped-service prerequisites and uncertainty procedures are published in the [operator recovery guide](../operators/backup-and-recovery.md#structured-results-and-exits). Removal does not alter root precedence, schema identity, installed service argv or credentials. Administrator replacement requires file and parent-directory sync before activation/revocation, followed by secure reopen, authentication and verification for online rollover. An uncertain output remains retained. No automatic replay or compensation is added.

## Backup and restore retirement

Installation-snapshot create/list/get/delete and stopped restore support is deliberately removed, including format verification, staging, encrypted-restore reservation/rewrap orchestration, APIs, UI, status occupancy and live manifests. No execution alias, compatibility endpoint, transition procedure or legacy restore path remains. Portable definitions use `export config` plus fresh `init --from`, never source authority, custody, CA identity or history.

Existing artifacts are untouched. Startup, upgrade, init/resume and normal operation never traverse, validate, rewrite, relocate, chmod or delete historical backup inventory or restore-specific residue. Initialization may classify root names to preserve its closed setup boundary, but skips only the exact retired `backups`, `gateway.db.restore` and restore sidecar/marker names without opening them. Malformed inventory cannot gate readiness or status. Unknown markers still refuse; generic mutation intents, initialization receipts, rotation bundles and native cleanup provenance retain their actual owners. Keyring authority fences retain genuinely pending credential cutovers and external OAuth handoffs, not installation snapshots. Validated committed activation is never refenced from a historical SQL intent.

Historical audit category/action and reason vocabulary remains readable for persisted evidence; it does not advertise live capabilities. No schema or historical file is rewritten as feature-removal cleanup. See [operator recovery](../operators/backup-and-recovery.md#backup-and-restore-removal).

## Migration generation replacement

Stopped traffic migration retains its shared consistent SQLite copy, immutable inspection, filesystem headroom, checkpoint and atomic replacement safeguards. It stages control without upgrading the original, validates and publishes generation-addressed traffic first, then installs the matching selector by one atomic rename. The original control inode is checkpointed and retained under a unique `.previous-*` name; old traffic and interrupted stages remain. Two fixed-file renames are never described as atomic. Initial setup creates its matching pair before publishing authority. No execution pins are persisted or replayed and no invalid generation falls back to empty history.

### Independent history export

`GET /api/v2/history/export` and `history export` materialize one existing bounded
read-only traffic transaction. The administrator-only response identifies installation,
generation, capture time, high-water, pruning, retained count, requested/next sequence,
truncation and protocol-tagged full MCP/HTTP/Git records. At most 256 aggregate records
and 900 KiB are returned, within the existing one-second reader lifetime and immediate
capacity refusal. There is no control mutation, traffic writer pause, export job,
filesystem stage, execution queue or replay. Configuration export and forwarding do not depend on history export success. Unavailable history is `history_unavailable`, never a successful
empty export; capacity/deadline failure is `history_busy`.

Coverage is only that response's rolling-history snapshot, not a complete traffic
audit. `after_sequence` requests a new transaction, not a continuation of a frozen
snapshot: completion, pruning and generation can change between calls. Optional
inclusive `through_sequence` limits records to an earlier high-water without pinning
storage or changing global coverage metadata. Consumers must compare
generation/coverage; absent rows never establish nonexecution and missing
completion remains unknown. Export neither repairs nor deletes original files,
unsafe paths, journals or interrupted stages.
