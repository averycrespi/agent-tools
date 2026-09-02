# Storage and Recovery

Audience: Maintainers and contributors changing durable storage, backup, restore, and stopped recovery

Authority: Normative product design

This chapter owns the behavior and invariants described below. Operational procedures remain in the linked guides; exact executable contract values remain owned by `internal/contract` and must agree with this chapter.

## Storage durability

An installation is one canonical owner-only `0700` directory guarded by a nonblocking exclusive process lock. A durable run marker distinguishes clean shutdown from an unclean stop; either startup path performs live identity, migration, pragma, and integrity verification before the store can be ready.

SQLite uses application ID `MGW1`, an immutable installation ULID, decimal revision, and an ordered embedded migration history. Schema 4 adds the durable server tables without changing accepted earlier tables.

Schema 8 adds the separately owned `mcp_gateway` synthetic identity, authorization revision singleton, permanent principal/current-credential slots, and immutable grant rows; migration seeds revision zero with no principals or grants and stores no raw bearer, session, credential history, issuer, or reason. Schema 9 adds an empty bounded invocation-audit store with a database-generated monotonic sequence, coherent nullable route/authorization/terminal groups, no foreign keys to mutable authority or catalog rows, and one trigger that permits only the first paired terminal annotation while leaving FIFO deletion available.

It has no bearer, verifier, downstream request ID, successful result, raw error, dispatch-started, retry, or replay column. Schema 10 additively creates an empty grant-request store with permanent identity tombstones and never-reused insertion order, immutable owner/target/requested-policy and versioned canonical dedupe bytes, bounded submitted/approved evidence, a database-maintained global evidence-byte aggregate, partial pending uniqueness, terminal-only deletion, and one exact pending-to-terminal transition.

Historical grant IDs and target identities have no foreign keys to mutable rows. Schema 11 adds only closed OAuth diagnostic stage and bounded HTTP-status columns to retained auth-flow records; the existing flow ID is the correlation ID and the existing public reason is the diagnostic reason. Schema 12 added required bounded grant names and deterministically named existing rows from their stable IDs. Schema 13 replaces names with optional bounded descriptions, preserves every existing name as its row's description, and adds a positive metadata revision used only for conditional description updates.

Storage owns only this DDL, seeding, and structural migration boundary. Authorization owns online SQL and validates every bounded principal-authority singleton and row coherently before the rest of the production graph is constructed; server target existence and synthetic collision checks remain delegated to the servers package on that same transaction.

Every connection installs a two-second busy policy, enables foreign keys, verifies WAL and `synchronous=FULL`, and derives `max_page_count` from the compiled 1 GiB database limit and that connection's actual page size. Foreign, newer, partial, corrupt, unsafe-permission, and over-limit generations fail closed.

## Security mutation and stopped recovery

### Mutation intent and latch

Security mutations are admitted through one nonblocking slot. Before a transaction begins, Gateway writes an installation-bound owner-only intent through temp write, file sync, atomic rename, and directory sync. A known commit or rollback moves the intent through a synced tombstone deletion. Marker I/O failures, storage-class statement failures, busy begin, commit errors, and post-commit uncertainty latch mutations; elapsed time, restart, or successful reads cannot clear the latch. The at-most-512-byte marker has one closed recovery union: the existing keyring fence action or one agent credential candidate containing only principal ID, credential ID, and captured positive principal/credential revisions. Recovery-bearing tombstone deletion restores the tombstone if final directory sync fails.

### Agent-candidate recovery

`restore --verify-current` reacquires stopped-process ownership, requires the current schema, and applies a recognized recovery action before clearing marker artifacts. Agent candidate cleanup is the sole stopped-process agent-credential SQL exception in storage: it clears only an exact current ID/revision tuple and advances principal and credential revisions once, while absent, replaced, or stale candidates are no-ops and no prior credential is restored. Unknown, mixed, disagreeing, foreign-installation, oversized, or failed recovery remains latched. The command closes SQLite before durable marker removal, emits one safe machine JSON result, and does not make the service ready; normal startup must verify the generation again.

### Restore credential invalidation

Authorization separately owns general stopped-stage credential surgery on a supplied verified current-schema replacement store. One marker-armed transaction validates all synthetic, principal, grant, target, capacity, and revision semantics before writing; clears every complete current slot while advancing only each affected principal and credential revision once; then revalidates the complete authority and absence of current slots before commit. Zero credentials is an exact no-op. Principal metadata and timestamps, grants, synthetic identity, authorization revision, Gateway revision, server facts, and admin authority remain unchanged. Backup owns no principal, credential, or grant DML. For every accepted schema-3-through-12 restore lineage, orchestration migrates and initially verifies the stage, invokes this authorization seam, rekeys admin authority, checkpoints and closes, then reruns closed SQLite verification plus complete authorization and grant-request semantics through the server-target and request supplied-transaction inspectors before installation. Every pre-install fault leaves the original generation authoritative.

## Backup and generation replacement

### Backup publication

Stopped-process and backup procedures are canonical in [recovery](../recovery.md). On-demand backup uses SQLite's online backup API under one nonblocking global work slot. Gateway stages an owner-only closed generation, verifies identity/schema/revision/full integrity and the 1 GiB bound, computes SHA-256, writes safe internal metadata, and atomically publishes it under a 26-character ID. The artifact-bound authority/key digest provides durable retry identity without storing a bearer or replaying a secret; 64 retained artifacts are the fixed record bound.

### Generation replacement

Backup restore holds stopped-process ownership, validates the published artifact and current installation binding, and copies one complete generation. Accepted schema-3-through-12 artifacts are forward-migrated as necessary and fully verified while staged before restored agent credentials are invalidated, admin authority is rekeyed, and replacement is published. The staged database resets all restored admin verifiers only after publishing a replacement non-expiring bearer. A checkpointed staged database atomically replaces the active generation without prior WAL/SHM sidecars. Desired servers and safe server history reconstruct as stopped durable facts; runtime, process/session/route state, OAuth transients, events, raw secrets, and keyring values are never restored. Marker clearing and readiness still require completed replacement verification and a fresh normal startup.
