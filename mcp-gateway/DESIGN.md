# MCP Gateway Design

## Purpose

MCP Gateway is a single-user local service that will provide a strict HTTP/MCP boundary and durable control foundation. S1 establishes the seams later slices consume without implementing server registration, OAuth, principals, grants, discovery, governed invocation, or product UI workflows.

This document is the source of truth for intended Gateway behavior. The S1 implementation is introduced incrementally; unimplemented behavior described here is not a claim that the current scaffold exposes it.

## Security model

- Bind one configured numeric IPv4 loopback authority, default `127.0.0.1:8210`; aliases, wildcard and non-loopback binds, alternate Host authorities, forwarding headers, trusted proxies, and CORS are rejected.
- Own every route and method explicitly. Unknown paths are `404`, known paths with unsupported methods are `405`, and production `/mcp` remains deny-all until a separately authenticated agent boundary is available.
- Keep admin and agent credentials, middleware, identifiers, and invalidation paths separate. Raw secrets may appear only at an approved one-time sink.
- Treat SQLite availability and integrity as security state. Security-critical writes fail closed, uncertain durability latches storage, and recovery is stopped-process only.
- Treat OS keyring support as an explicit typed capability. There is no plaintext fallback and no interactive prompt on service paths.
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

Credential and backup collection pages default to 50. Idempotency keys are 1–128 visible ASCII bytes. Credential expiry is five minutes through 365 days after creation. Fixed deadlines are: header read five seconds, API handler 30 seconds, SQLite busy two seconds, SSE keepalive and blocked write 15 seconds, legacy idle 30 minutes, legacy absolute eight hours, graceful shutdown 10 seconds, and idempotency retention 24 hours.

### Resource representations and mechanics

`AdminCredential` is exactly `{id,fingerprint,created_at,expires_at,non_expiring,status,revision}`; its creation form adds one-time `bearer`. Credential status is the closed set `active`, `revoked`, or `expired`. `Backup` is exactly `{id,created_at,installation_id,schema_version,source_revision,size_bytes,sha256}`. Collections are exactly `{items,next_cursor}`.

`SystemStatus` is exactly `{process,sqlite,keyring,limits,backup,protocols}`. Process state is `uninitialized`, `starting`, `ready`, `storage_failed`, or `draining`; SQLite state is `uninitialized`, `ready`, or `latched`; keyring capability is `ready`, `absent`, `locked`, `interaction_required`, `unavailable`, or `unsupported`; and backup state is `idle` or `creating`. The closed `limits` object contains `http_regular`, `http_control_auth`, `http_admin`, `http_health`, `mcp_work`, `mcp_streams`, `admin_sessions`, `legacy_sessions`, `event_streams`, `backup_work`, `backup_records`, `admin_credentials`, `idempotency_records`, `keyring_candidates`, and `database_bytes`; every entry is exactly `{in_use,limit,saturated}`. Protocol status is modern `2026-07-28`, legacy `2025-11-25`, and agent auth `deny_all`.

Cursor mechanics apply only to `GET /api/v1/admin-credentials` and `GET /api/v1/backups`. Durable idempotency applies only to `POST /api/v1/backups`. No S1 resource uses ETag, and the event stream has no replay mechanism. Invalidation kinds are the closed set `admin_credentials`, `system_status`, and `backups`.

Admin bearer values use prefix `mgw_admin_`, reserved agent bearer values use `mgw_agent_`, and the session cookie is `mcp_gateway_session`. The only approved raw-secret sinks are `controlling_terminal` and `owner_only_file`; the latter is a newly created, non-symlink-following `0600` file containing exactly the secret and one newline. Standard output and standard error are not secret sinks.

## Deterministic test foundation

Shared tests use mutex-safe fake time and finite deterministic entropy, real owner-only `0700` temporary data roots with symlink/type/owner/mode validation, and a streaming canary scanner that detects cross-buffer leaks without returning the canary in errors. The common real-binary runner requires a positive timeout and per-stream byte cap, captures stdout and stderr separately, reports truncation and exit status, and cancels its direct child when its context expires. Component-specific fault hooks, protocol fixtures, barriers, and process-group shutdown behavior remain with their owning packages.

## Initial executable state

The M1 scaffold contains no operational subcommands and starts no listener. This prevents partially composed security boundaries from becoming a usable service. Later S1 milestones add commands only with their owning storage, authority, and lifecycle checks.

## Non-goals

S1 does not implement downstream server lifecycle or transports, OAuth flows, principal/grant management, agent credential issuance, catalog discovery, tool routing or calls, invocation audit, grant-request tools, or product UI pages. It supports only the future modern `2026-07-28` and legacy `2025-11-25` Streamable HTTP adapters; pre-Streamable HTTP+SSE and MCP list-change notifications are excluded.
