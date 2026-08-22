# MCP Gateway Design

## Purpose

MCP Gateway is a single-user local service that will provide a strict HTTP/MCP boundary and durable control foundation. S1 establishes the seams later slices consume without implementing server registration, OAuth, principals, grants, discovery, governed invocation, or product UI workflows.

This document is the source of truth for intended Gateway behavior. The S1 implementation is introduced incrementally; unimplemented behavior described here is not a claim that the current scaffold exposes it.

## Security model

- Bind one configured numeric IPv4 loopback authority, default `127.0.0.1:8210`; aliases, wildcard and non-loopback binds, alternate Host authorities, forwarding headers, trusted proxies, and CORS are rejected.
- Own every route and method explicitly. Unknown paths are `404`, known paths with unsupported methods are `405`, and production `/mcp` remains deny-all until a separately authenticated agent boundary is available.
- Keep admin and agent credentials, middleware, identifiers, and invalidation paths separate. Raw secrets may appear only at an approved one-time sink.
- Treat SQLite availability and integrity as security state. Security-critical writes fail closed, uncertain durability latches storage, and recovery is stopped-process only.
- Treat OS keyring support as an explicit typed capability. Startup probing performs no secret operation or prompt presentation. Later `go-keyring` operations may invoke OS interaction or outlive cancellation as an accepted MVP limitation, but there is no plaintext fallback.
- Keep all registries and admission controls bounded and nonblocking. Restart discards sessions, streams, subscribers, and in-flight work.

## Process shape

The final S1 binary composes narrow internal boundaries in `cmd/mcp-gateway`:

- `paths`: installation path ownership and process exclusivity
- `storage`: SQLite identity, migrations, durability, and failure latching
- `admin`: bearer and browser-session authority
- `api`: strict resource and problem representations
- `httpboundary`: canonical listener and request validation
- `events`: best-effort invalidation delivery
- `limits`: fixed nonblocking admission limits
- `mcpingress`: authenticated modern/legacy protocol isolation
- `keyring`: typed capability and opaque secret generations
- `backup`: verified backup publication and stopped restore
- `lifecycle`: startup, readiness, draining, and shutdown coordination

Dependencies flow from the command composition root into these packages. Domain packages do not import `cmd`, do not share authority across credential domains, and do not introduce a cross-module internal library.

## Platform choices

- Go module: `github.com/averycrespi/agent-tools/mcp-gateway`
- MCP protocol adapter: `github.com/modelcontextprotocol/go-sdk` v1.7.0
- SQLite: `github.com/ncruces/go-sqlite3`, with settings verified from live connections
- Credential provider: `github.com/zalando/go-keyring`, behind an injectable typed adapter
- CLI: Cobra

The official MCP SDK does not own Gateway authentication, protocol downgrade decisions, limits, or lifecycle. SQLite defaults are not trusted in place of explicit per-connection setup and verification. Keyring errors are not collapsed into absence.

## Executable S1 contract

The `internal/contract` package is the single source consumed by later implementations. Its returned tables are copies so callers cannot mutate the canonical contract. The default authority is `127.0.0.1:8210`, its canonical Origin is `http://127.0.0.1:8210`, the supported protocol versions are modern `2026-07-28` and legacy `2025-11-25`, and the media types are `application/json`, `application/problem+json`, and `text/event-stream`.

### Route ownership

Methods are lexicographically ordered and become the exact `Allow` value. `HEAD` is never inherited from `GET`.

| Pattern                          | Exact `Allow`       | Authority               |
| -------------------------------- | ------------------- | ----------------------- |
| `/`                              | `GET`               | public                  |
| `/assets/*`                      | `GET`               | public                  |
| `/livez`                         | `GET`               | public                  |
| `/readyz`                        | `GET`               | public                  |
| `/mcp`                           | `DELETE, GET, POST` | agent                   |
| `/oauth/callback`                | `GET`               | one-time OAuth state    |
| `/api/v1/admin-sessions`         | `POST`              | admin bearer            |
| `/api/v1/admin-sessions/current` | `DELETE`            | admin session           |
| `/api/v1/admin-credentials`      | `GET, POST`         | admin bearer or session |
| `/api/v1/admin-credentials/{id}` | `DELETE, GET`       | admin bearer or session |
| `/api/v1/system-status`          | `GET`               | admin bearer or session |
| `/api/v1/backups`                | `GET, POST`         | admin bearer or session |
| `/api/v1/backups/{id}`           | `DELETE, GET`       | admin bearer or session |
| `/api/v1/events`                 | `GET`               | admin bearer or session |

`/assets/*` requires a nonempty path below `/assets/`. Item patterns require exactly one nonempty segment. All other paths are unowned and therefore `404`.

### Safe problems

Problems have exactly `status`, `code`, and `title`. The fixed table is exhaustive; dependency messages, paths, payloads, and other details are never added.

| Status | Code                         | Fixed title                                    |
| -----: | ---------------------------- | ---------------------------------------------- |
|    400 | `malformed_request`          | The request is invalid.                        |
|    400 | `invalid_json`               | The JSON body is invalid.                      |
|    400 | `invalid_cursor`             | The cursor is invalid.                         |
|    400 | `invalid_idempotency_key`    | The idempotency key is invalid.                |
|    400 | `ambiguous_credentials`      | Multiple credential types were supplied.       |
|    400 | `invalid_oauth_state`        | The OAuth state is invalid or expired.         |
|    401 | `authentication_required`    | Authentication is required.                    |
|    403 | `credential_domain_mismatch` | The credential is for a different authority.   |
|    403 | `forbidden_origin`           | The Origin is not accepted.                    |
|    403 | `csrf_failed`                | CSRF validation failed.                        |
|    404 | `not_found`                  | The resource was not found.                    |
|    405 | `method_not_allowed`         | The method is not allowed.                     |
|    409 | `conflict`                   | The request conflicts with current state.      |
|    409 | `idempotency_conflict`       | The idempotency key conflicts with prior work. |
|    413 | `body_too_large`             | The request body is too large.                 |
|    415 | `unsupported_media_type`     | The media type is not supported.               |
|    421 | `misdirected_request`        | The Host is not accepted.                      |
|    429 | `resource_limit`             | The resource limit is reached.                 |
|    503 | `storage_unavailable`        | Storage is unavailable.                        |
|    503 | `keyring_unavailable`        | The credential provider is unavailable.        |
|    503 | `shutting_down`              | The service is shutting down.                  |

### Fixed numeric limits

Every maximum accepts N and rejects N+1. Values below zero are invalid. These are compiled boundaries, not configuration.

| Contract name                  |    Maximum |
| ------------------------------ | ---------: |
| `request_target_bytes`         |       8192 |
| `request_header_bytes`         |      32768 |
| `request_header_count`         |        100 |
| `request_header_value_bytes`   |       8192 |
| `api_json_body_bytes`          |    1048576 |
| `mcp_body_bytes`               |    4194304 |
| `json_depth`                   |         64 |
| `http_regular`                 |        128 |
| `http_control_auth`            |         32 |
| `http_admin`                   |         16 |
| `http_health`                  |          8 |
| `mcp_work`                     |         32 |
| `mcp_streams`                  |         32 |
| `admin_sessions`               |        128 |
| `legacy_sessions`              |        128 |
| `event_streams`                |         16 |
| `event_buffered_invalidations` |         16 |
| `backup_work`                  |          1 |
| `backup_records`               |         64 |
| `admin_credentials`            |        128 |
| `admin_list_page`              |        100 |
| `backup_list_page`             |        100 |
| `database_bytes`               | 1073741824 |
| `idempotency_key_bytes`        |        128 |
| `idempotency_records`          |       1024 |
| `opaque_id_bytes`              |         26 |
| `cursor_bytes`                 |        512 |
| `sse_frame_bytes`              |        512 |
| `keyring_secret_bytes`         |     262144 |
| `keyring_chunk_bytes`          |       3000 |
| `keyring_candidates`           |         64 |
| `keyring_work`                 |          1 |

Credential and backup collection pages default to 50. Idempotency keys are 1–128 visible ASCII bytes. Credential expiry is five minutes through 365 days after creation. Fixed deadlines are: header read five seconds, API handler 30 seconds, SQLite busy two seconds, SSE keepalive and blocked write 15 seconds, legacy idle 30 minutes, legacy absolute eight hours, graceful shutdown 10 seconds, and idempotency retention 24 hours.

### Resource representations and mechanics

`AdminCredential` is exactly `{id,fingerprint,created_at,expires_at,non_expiring,status,revision}`; its creation form adds one-time `bearer`. Credential status is the closed set `active`, `revoked`, or `expired`. `Backup` is exactly `{id,created_at,installation_id,schema_version,source_revision,size_bytes,sha256}`. Collections are exactly `{items,next_cursor}`.

`SystemStatus` is exactly `{process,sqlite,keyring,limits,backup,protocols}`. Process state is `uninitialized`, `starting`, `ready`, `storage_failed`, or `draining`; SQLite state is `uninitialized`, `ready`, or `latched`; keyring capability is `ready`, `absent`, `locked`, `interaction_required`, `unavailable`, or `unsupported`; and backup state is `idle` or `creating`. The closed `limits` object contains `http_regular`, `http_control_auth`, `http_admin`, `http_health`, `mcp_work`, `mcp_streams`, `admin_sessions`, `legacy_sessions`, `event_streams`, `backup_work`, `backup_records`, `admin_credentials`, `idempotency_records`, `keyring_candidates`, `keyring_work`, and `database_bytes`; every entry is exactly `{in_use,limit,saturated}`. Protocol status is modern `2026-07-28`, legacy `2025-11-25`, and agent auth `deny_all`.

Cursor mechanics apply only to `GET /api/v1/admin-credentials` and `GET /api/v1/backups`. Durable idempotency applies only to `POST /api/v1/backups`. No S1 resource uses ETag, and the event stream has no replay mechanism. Invalidation kinds are the closed set `admin_credentials`, `system_status`, and `backups`.

Admin bearer values use prefix `mgw_admin_`, reserved agent bearer values use `mgw_agent_`, and the session cookie is `mcp_gateway_session`. The only approved raw-secret sinks are `controlling_terminal` and `owner_only_file`; the latter is a newly created, non-symlink-following `0600` file containing exactly the secret and one newline. Standard output and standard error are not secret sinks.

## Storage generation and recovery

An installation is one canonical owner-only `0700` directory guarded by a nonblocking exclusive process lock. A durable run marker distinguishes clean shutdown from an unclean stop; either startup path performs live identity, migration, pragma, and integrity verification before the store can be ready.

SQLite uses application ID `MGW1`, an immutable installation ULID, decimal revision, and an ordered embedded migration history. Every connection installs a two-second busy policy, enables foreign keys, verifies WAL and `synchronous=FULL`, and derives `max_page_count` from the compiled 1 GiB database limit and that connection's actual page size. Foreign, newer, partial, corrupt, unsafe-permission, and over-limit generations fail closed.

Security mutations are admitted through one nonblocking slot. Before a transaction begins, Gateway writes an installation-bound owner-only intent through temp write, file sync, atomic rename, and directory sync. A known commit or rollback moves the intent through a synced tombstone deletion. Marker I/O failures, storage-class statement failures, busy begin, commit errors, and post-commit uncertainty latch mutations; elapsed time, restart, or successful reads cannot clear the latch.

`restore --verify-current` reacquires stopped-process ownership, requires the current schema, runs full verification, closes SQLite, and only then durably removes marker artifacts. It emits one safe machine JSON result and does not make the service ready; normal startup must verify the generation again.

On-demand backup uses SQLite's online backup API under one nonblocking global work slot. Gateway stages an owner-only closed generation, verifies identity/schema/revision/full integrity and the 1 GiB bound, computes SHA-256, writes safe internal metadata, and atomically publishes it under a 26-character ID. The artifact-bound authority/key digest provides durable retry identity without storing a bearer or replaying a secret; 64 retained artifacts are the fixed record bound.

Backup restore holds stopped-process ownership, validates the published artifact and current installation binding, copies one complete generation, and resets all restored admin verifiers on the staged database only after publishing a replacement non-expiring bearer. A checkpointed staged database atomically replaces the active generation without prior WAL/SHM sidecars. Sessions, protocol state, events, and keyring values are never restored. Marker clearing and readiness still require completed replacement verification and a fresh normal startup.

## Admin authority lifecycle

Admin bearer creation uses 32 bytes of entropy and the fixed `mgw_admin_` domain prefix. SQLite stores only a domain-separated SHA-256 verifier, a separately domain-separated 16-character fingerprint, metadata, status, and revision. Authentication distinguishes the reserved agent prefix before performing constant-time verifier comparisons; unknown and malformed admin values remain non-enumerating.

`initialize` and `admin-reset` require stopped-process ownership. They complete an approved one-time sink before entering the latched security mutation. Initialization activates only an empty authority set. Reset revokes every prior active verifier and inserts one replacement in the same revisioned transaction. Sink failure leaves all persisted authority unchanged, while an activation failure cannot make an unpublished bearer usable. Command JSON, standard error, argv, logs, and SQLite never contain the raw bearer.

Credential creation validates optional expiry against the compiled five-minute and one-year bounds before consuming entropy. Metadata reads derive expiry from the injected clock; authentication compares all active verifier candidates in constant time and rejects expired, revoked, unknown, malformed, and wrong-domain values safely. Revocation transactionally preserves at least one active non-expiring authority. At the 128-record cap, creation/reset prunes the oldest revoked or expired records by creation time and ID; it rejects without queuing when every record remains active.

Bearer exchange reserves one of 128 in-memory session slots before generating independent 32-byte session and CSRF values. Sessions use a host-only `mcp_gateway_session` cookie with `Path=/`, `HttpOnly`, and `SameSite=Strict`; no `Domain` is present, and `Secure` is omitted only for the fixed plain-loopback transport. Activity refreshes the 30-minute idle bound without extending the eight-hour absolute bound. Logout, idle/absolute or parent expiry, parent revocation, reset, and shutdown remove the slot and synchronously close its subscription channel. Ambiguous bearer-plus-cookie authority and incorrect session-bound CSRF fail without changing activity. No session state survives manager or process restart.

## Keyring capability and generation cutover

The `go-keyring` process-global functions sit behind instance-local adapters. Gateway's secret-free startup probe invokes no Get/Set/Delete or prompt presentation: Linux inspects the session D-Bus Secret Service, attempts only a nonpresenting unlock and dismisses any returned prompt object; macOS checks default-keychain metadata with bounded, output-discarding `security` commands. The snapshot is one of `ready`, `absent`, `locked`, `interaction_required`, `unavailable`, or `unsupported`, with a safe remediation code; it does not predict whether a later operation will interact. Missing items remain distinct from backend absence, and unknown native failures become unavailable without preserving native diagnostics.

Any Get/Set/Delete may invoke OS-managed interaction, fail, or outlive cancellation because `go-keyring` v0.2.7 is context-free. One process-global nonblocking `keyring_work` permit bounds outstanding operations; saturation rejects immediately and cancellation does not release the slot before the backend call returns. This accepted MVP limitation never permits file/configuration fallback. The MVP is unsuitable for unattended credential access; hardening is required before unattended deployment or after any unexpected dialog, cancellation-surviving call, or keyring-induced service blockage.

A keyring namespace binds the installation ULID, an immutable Gateway-derived resource-owner ULID, and one closed kind: `static_credential`, `oauth_client`, or `oauth_tokens`. Secret payloads are limited to 256 KiB, base64url encoded into stored values no larger than 3,000 bytes, and identified outside the provider only by random opaque handles. Chunks are written before a versioned owner/kind/handle/length/SHA-256 manifest, so no partial generation reads. Read verifies every binding, bound, decoded length, and digest; deletion handles complete or interrupted generations.

SQLite registers a non-authoritative candidate before the first keyring write, making crash leftovers discoverable without persisting secret bytes. After writing and reading back a complete generation, one latched transaction advances the Gateway revision, selects its opaque handle as authority, and moves the prior handle to bounded cleanup metadata. At most 64 candidates exist per owner/kind. Startup or replacement cleanup removes only non-authoritative candidates; interruption before commit preserves old authority, while interruption after commit exposes only the new complete generation.

Deterministic injected tests cover Darwin and Linux mappings, prompt dismissal, generation faults, N/N+1 candidates, and every cutover boundary. The native target uses an isolated Linux D-Bus/home/Secret Service environment when its prerequisites exist. macOS keychain search/default state is changed only in an explicitly confirmed disposable login context and is restored afterward; ordinary hosts receive a clear prerequisite skip.

## Deterministic test foundation

Shared tests use mutex-safe fake time and finite deterministic entropy, real owner-only `0700` temporary data roots with symlink/type/owner/mode validation, and a streaming canary scanner that detects cross-buffer leaks without returning the canary in errors. The common real-binary runner requires a positive timeout and per-stream byte cap, captures stdout and stderr separately, reports truncation and exit status, and cancels its direct child when its context expires. Component-specific fault hooks, protocol fixtures, barriers, and process-group shutdown behavior remain with their owning packages.

## Implemented HTTP and control boundary

`serve` verifies stopped-process ownership and storage, takes one secret-free keyring capability snapshot, and only then opens the exact configured numeric IPv4 loopback listener. The boundary validates request target, header, forwarding, Host, Origin, route, and method constraints before authentication or body work. Health, ordinary, control-auth, and authenticated-admin permits are independent and nonblocking; authenticated control work transfers permits without allowing ordinary traffic to consume recovery capacity.

The minimum control API implements bearer-to-session exchange, session logout, bounded credential create/list/read/revoke, and the closed system-status representation. Bearer and cookie authority cannot be combined. Cookie requests require the exact Origin, and unsafe cookie requests require the session CSRF value and JSON media type. JSON parsing bounds body size and nesting and rejects invalid UTF-8, duplicate members, unknown members, and trailing input. Credential collection cursors are bounded and collection-bound; no implemented resource uses ETag or durable idempotency. API responses are `no-store`, problems retain the fixed safe envelope, and no response enables CORS.

The public shell and local stylesheet are embedded, contain no external active content, and use the fixed restrictive CSP. The injected OAuth-state seam is mounted at `/oauth/callback`; production wiring always returns the safe invalid/expired-state problem.

## Implemented MCP ingress

Agent authentication runs before MCP body reads, era classification, and session lookup. Production uses a deny-all authenticator; positive fixtures inject opaque principal/credential/expiry bindings. Admin bearer prefixes are rejected as the wrong credential domain, while missing, malformed, unknown, and expired agent credentials remain non-enumerating.

Gateway validates the modern `2026-07-28` header/body protocol mirror and dispatches only sessionless POST requests to the official SDK's stateless transport. Per-request client metadata replaces initialization state in this era. Modern requests reject legacy session IDs, cannot fall through to legacy classification, propagate request cancellation, and advertise neither tools nor list-change capability.

Legacy `2025-11-25` initialization reserves one of 128 slots before entropy or state publication, then uses a stateful official-SDK adapter with a Gateway-generated opaque session ID. Every later POST, GET, or DELETE requires the exact legacy protocol header, the session ID, and a reauthenticated binding matching both principal and credential. Idle/absolute expiry, credential expiry or invalidation, DELETE, shutdown, and restart close or discard the in-memory session and release capacity. Modern, legacy, MCP-work, and MCP-stream state remain separate; 32 work and 32 stream permits reject without queuing.

## Current executable state

The executable exposes stopped-process `initialize`, `admin-reset`, `restore --verify-current`, and verified backup replacement, plus `serve` for the verified HTTP/control foundation. Initialization/reset/restore publish raw authority only to the controlling terminal or a new owner-only file; normal output remains safe machine JSON. Public health, authenticated admin credential/session/status/backup resources, the minimal static shell, the reserved rejecting OAuth callback, and dual-era authenticated MCP adapters are implemented. Production `/mcp` remains deny-all. Event streaming and coordinated two-stage shutdown remain unavailable until their owning S1 milestones are complete.

## Non-goals

S1 does not implement downstream server lifecycle or transports, OAuth flows, principal/grant management, agent credential issuance, catalog discovery, tool routing or calls, invocation audit, grant-request tools, or product UI pages. It supports only the future modern `2026-07-28` and legacy `2025-11-25` Streamable HTTP adapters; pre-Streamable HTTP+SSE and MCP list-change notifications are excluded.
