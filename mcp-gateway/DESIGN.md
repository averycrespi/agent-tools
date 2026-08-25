# MCP Gateway Design

## Purpose

MCP Gateway is a single-user local service that provides a strict HTTP/MCP boundary and durable control foundation. S1 established the accepted service foundation. The current S2 foundation adds the closed contract, shared strict-JSON primitive, durable server authority repository, transaction-fenced server keyring publication/invalidation, and authenticated desired-server and operation APIs. It implements transport-neutral runtime reconstruction, retry scheduling, OAuth trust/registration, bounded foreground authorization-code flow plus initial token authority, and write-only static/OAuth-client replacement, a dormant bounded raw catalog traversal seam, and durable catalog/descriptor resources, but not production downstream activation/publication, routing, or product UI workflows.

This document is the source of truth for intended implemented behavior. Contract vocabulary and durable repository behavior described below are available to later packages, but an S2 resource or state declaration is not a claim that the executable performs that lifecycle behavior yet.

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
- `servers`: the sole owner of durable S2 server, authority-revision, operation, cursor-watermark, and idempotency SQL
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

## Executable S1 and S2 contract

The `internal/contract` package is the single source consumed by later implementations. Its returned tables are copies so callers cannot mutate the canonical contract. The default authority is `127.0.0.1:8210`, its canonical Origin is `http://127.0.0.1:8210`, the supported protocol versions are modern `2026-07-28` and legacy `2025-11-25`, and the media types are `application/json`, `application/problem+json`, and `text/event-stream`.

### Route ownership

Methods are lexicographically ordered and become the exact `Allow` value. `HEAD` is never inherited from `GET`.

| Pattern                                          | Exact `Allow`        | Authority               |
| ------------------------------------------------ | -------------------- | ----------------------- |
| `/`                                              | `GET`                | public                  |
| `/assets/*`                                      | `GET`                | public                  |
| `/livez`                                         | `GET`                | public                  |
| `/readyz`                                        | `GET`                | public                  |
| `/mcp`                                           | `DELETE, GET, POST`  | agent                   |
| `/oauth/callback`                                | `GET`                | one-time OAuth state    |
| `/api/v1/admin-sessions`                         | `POST`               | admin bearer            |
| `/api/v1/admin-sessions/current`                 | `DELETE`             | admin session           |
| `/api/v1/admin-credentials`                      | `GET, POST`          | admin bearer or session |
| `/api/v1/admin-credentials/{id}`                 | `DELETE, GET`        | admin bearer or session |
| `/api/v1/system-status`                          | `GET`                | admin bearer or session |
| `/api/v1/backups`                                | `GET, POST`          | admin bearer or session |
| `/api/v1/backups/{id}`                           | `DELETE, GET`        | admin bearer or session |
| `/api/v1/events`                                 | `GET`                | admin bearer or session |
| `/api/v1/servers`                                | `GET, POST`          | admin bearer or session |
| `/api/v1/servers/{id}`                           | `DELETE, GET, PATCH` | admin bearer or session |
| `/api/v1/servers/{id}/operations`                | `GET, POST`          | admin bearer or session |
| `/api/v1/servers/{id}/operations/{operation_id}` | `GET`                | admin bearer or session |
| `/api/v1/servers/{id}/credential-replacements`   | `POST`               | admin bearer or session |
| `/api/v1/servers/{id}/auth-flows`                | `GET, POST`          | admin bearer or session |
| `/api/v1/servers/{id}/auth-flows/{flow_id}`      | `DELETE, GET`        | admin bearer or session |
| `/api/v1/catalog`                                | `GET`                | admin bearer or session |
| `/api/v1/servers/{id}/descriptors`               | `GET`                | admin bearer or session |
| `/api/v1/servers/{id}/descriptors/{tool_id}`     | `GET`                | admin bearer or session |

`/assets/*` requires a nonempty path below `/assets/`. Item patterns require exactly one nonempty segment. All other paths are unowned and therefore `404`.

### Safe problems

Problems have exactly `status`, `code`, and `title`. The fixed table is exhaustive; dependency messages, paths, payloads, and other details are never added.

| Status | Code                           | Fixed title                                    |
| -----: | ------------------------------ | ---------------------------------------------- |
|    400 | `malformed_request`            | The request is invalid.                        |
|    400 | `invalid_json`                 | The JSON body is invalid.                      |
|    400 | `invalid_cursor`               | The cursor is invalid.                         |
|    400 | `invalid_idempotency_key`      | The idempotency key is invalid.                |
|    400 | `ambiguous_credentials`        | Multiple credential types were supplied.       |
|    400 | `invalid_oauth_state`          | The OAuth state is invalid or expired.         |
|    401 | `authentication_required`      | Authentication is required.                    |
|    403 | `credential_domain_mismatch`   | The credential is for a different authority.   |
|    403 | `forbidden_origin`             | The Origin is not accepted.                    |
|    403 | `csrf_failed`                  | CSRF validation failed.                        |
|    404 | `not_found`                    | The resource was not found.                    |
|    405 | `method_not_allowed`           | The method is not allowed.                     |
|    409 | `conflict`                     | The request conflicts with current state.      |
|    409 | `idempotency_conflict`         | The idempotency key conflicts with prior work. |
|    413 | `body_too_large`               | The request body is too large.                 |
|    415 | `unsupported_media_type`       | The media type is not supported.               |
|    421 | `misdirected_request`          | The Host is not accepted.                      |
|    429 | `resource_limit`               | The resource limit is reached.                 |
|    503 | `storage_unavailable`          | Storage is unavailable.                        |
|    503 | `keyring_unavailable`          | The credential provider is unavailable.        |
|    503 | `shutting_down`                | The service is shutting down.                  |
|    400 | `invalid_server_configuration` | The server configuration is invalid.           |
|    400 | `invalid_operation`            | The server operation is invalid.               |
|    409 | `namespace_unavailable`        | The server namespace is unavailable.           |
|    409 | `operation_conflict`           | The server has conflicting work.               |
|    409 | `oauth_flow_active`            | The OAuth flow is already exchanging.          |
|    409 | `stale_cursor`                 | The cursor snapshot is no longer available.    |
|    412 | `stale_revision`               | The server revision is stale.                  |
|    428 | `precondition_required`        | The current server revision is required.       |
|    503 | `downstream_unavailable`       | The downstream server is unavailable.          |

### Fixed numeric limits

Every maximum accepts N and rejects N+1. Values below zero are invalid. These are compiled boundaries, not configuration.

| Contract name                        |    Maximum |
| ------------------------------------ | ---------: |
| `request_target_bytes`               |       8192 |
| `request_header_bytes`               |      32768 |
| `request_header_count`               |        100 |
| `request_header_value_bytes`         |       8192 |
| `api_json_body_bytes`                |    1048576 |
| `mcp_body_bytes`                     |    4194304 |
| `json_depth`                         |         64 |
| `http_regular`                       |        128 |
| `http_control_auth`                  |         32 |
| `http_admin`                         |         16 |
| `http_health`                        |          8 |
| `mcp_work`                           |         32 |
| `mcp_streams`                        |         32 |
| `admin_sessions`                     |        128 |
| `legacy_sessions`                    |        128 |
| `event_streams`                      |         16 |
| `event_buffered_invalidations`       |         16 |
| `backup_work`                        |          1 |
| `backup_records`                     |         64 |
| `admin_credentials`                  |        128 |
| `admin_list_page`                    |        100 |
| `backup_list_page`                   |        100 |
| `database_bytes`                     | 1073741824 |
| `idempotency_key_bytes`              |        128 |
| `idempotency_records`                |       1024 |
| `opaque_id_bytes`                    |         26 |
| `cursor_bytes`                       |        512 |
| `sse_frame_bytes`                    |        512 |
| `keyring_secret_bytes`               |     262144 |
| `keyring_chunk_bytes`                |       3000 |
| `keyring_candidates`                 |         64 |
| `keyring_work`                       |          1 |
| `namespace_bytes`                    |         32 |
| `display_name_bytes`                 |        256 |
| `stdio_arguments`                    |         64 |
| `stdio_environment_entries`          |         32 |
| `stdio_secret_environment_entries`   |         16 |
| `stdio_path_bytes`                   |       4096 |
| `stdio_argument_bytes`               |       4096 |
| `stdio_arguments_bytes`              |      32768 |
| `stdio_environment_name_bytes`       |       4096 |
| `stdio_environment_value_bytes`      |       4096 |
| `secret_slot_name_bytes`             |         64 |
| `resource_url_bytes`                 |       8192 |
| `server_identities`                  |       1024 |
| `servers`                            |         64 |
| `enabled_servers`                    |         32 |
| `downstream_runtimes`                |         32 |
| `server_reconciliations`             |          4 |
| `per_server_reconciliation`          |          1 |
| `catalog_traversals`                 |          4 |
| `oauth_flows`                        |         16 |
| `per_server_oauth_flows`             |          1 |
| `oauth_callback_work`                |          8 |
| `terminal_operations`                |         64 |
| `terminal_auth_flows`                |         16 |
| `s2_list_page`                       |        100 |
| `active_tools_per_server`            |        256 |
| `active_tools`                       |       2048 |
| `durable_tool_identities_per_server` |        512 |
| `durable_tool_identities`            |       4096 |
| `tools_list_pages`                   |         32 |
| `tools_list_page_bytes`              |    4194304 |
| `tool_descriptor_bytes`              |     131072 |
| `tool_schema_bytes`                  |      98304 |
| `tool_name_bytes`                    |        128 |
| `external_tool_name_bytes`           |        128 |
| `tool_title_bytes`                   |       1024 |
| `tool_description_bytes`             |      16384 |
| `downstream_mcp_body_bytes`          |    4194304 |
| `downstream_sse_event_bytes`         |    4194304 |
| `downstream_legacy_session_id_bytes` |        512 |
| `oauth_metadata_body_bytes`          |    1048576 |
| `oauth_json_depth`                   |         64 |
| `oauth_response_body_bytes`          |     262144 |
| `oauth_url_bytes`                    |       8192 |
| `oauth_query_bytes`                  |       8192 |
| `oauth_client_id_bytes`              |       8192 |
| `oauth_client_secret_bytes`          |       8192 |
| `oauth_scope_count`                  |         64 |
| `oauth_scope_token_bytes`            |        256 |
| `oauth_scope_bytes`                  |       8192 |
| `stdio_protocol_frame_bytes`         |    4194304 |
| `stdio_stderr_bytes`                 |      65536 |
| `stdio_output_rate_bytes_per_second` |    8388608 |
| `stdio_output_burst_bytes`           |    8388608 |
| `downstream_dispatch`                |         32 |
| `per_server_downstream_dispatch`     |          4 |
| `s2_idempotency_records`             |       1024 |

Credential, backup, and S2 collection pages default to 50. Idempotency keys are 1–128 visible ASCII bytes. Credential expiry is five minutes through 365 days after creation. S2 downstream HTTP reuses the S1 `request_header_bytes`, `request_header_count`, and `request_header_value_bytes` bounds rather than declaring alternatives.

Fixed S1 deadlines are: header read five seconds, API handler 30 seconds, SQLite busy two seconds, SSE keepalive and blocked write 15 seconds, legacy idle 30 minutes, legacy absolute eight hours, graceful shutdown 10 seconds, and idempotency retention 24 hours. S2 adds a five-minute OAuth flow lifetime; connect/OAuth/initialization deadlines of 10/15/30 seconds; catalog page/traversal deadlines of 15/60 seconds; a maximum downstream call deadline of 60 seconds; stdio graceful/forced stop windows of 3/2 seconds; a five-minute catalog poll interval with at most 30 seconds jitter; and reconciliation retry delays of 1, 2, 4, 8, 16, 32, then 60 seconds.

### Resource representations and mechanics

`AdminCredential` is exactly `{id,fingerprint,created_at,expires_at,non_expiring,status,revision}`; its creation form adds one-time `bearer`. Credential status is the closed set `active`, `revoked`, or `expired`. `Backup` is exactly `{id,created_at,installation_id,schema_version,source_revision,size_bytes,sha256}`. Collections are exactly `{items,next_cursor}`.

`SystemStatus` is exactly `{process,sqlite,keyring,limits,backup,protocols}`. Process state is `uninitialized`, `starting`, `ready`, `storage_failed`, or `draining`; SQLite state is `uninitialized`, `ready`, or `latched`; keyring capability is `ready`, `absent`, `locked`, `interaction_required`, `unavailable`, or `unsupported`; and backup state is `idle` or `creating`. The closed `limits` object contains `http_regular`, `http_control_auth`, `http_admin`, `http_health`, `mcp_work`, `mcp_streams`, `admin_sessions`, `legacy_sessions`, `event_streams`, `backup_work`, `backup_records`, `admin_credentials`, `idempotency_records`, `keyring_candidates`, `keyring_work`, `database_bytes`, `server_identities`, `servers`, `downstream_runtimes`, `server_reconciliations`, `catalog_traversals`, `oauth_flows`, `oauth_callback_work`, `s2_idempotency_records`, `active_tools`, `durable_tool_identities`, and `downstream_dispatch`; every entry is exactly `{in_use,limit,saturated}`. Protocol status is modern `2026-07-28`, legacy `2025-11-25`, and agent auth `deny_all`.

S1 cursor mechanics remain limited to `GET /api/v1/admin-credentials` and `GET /api/v1/backups`; S1 durable idempotency remains limited to `POST /api/v1/backups`; and no S1 resource uses ETag. S2 declarations add targeted snapshot or watermark cursors, idempotency, exact preconditions, and strong Server ETags only where listed below. The event stream still has no replay mechanism. Invalidation kinds are the closed set `admin_credentials`, `system_status`, `backups`, `servers`, `server_operations`, `server_auth_flows`, and `catalog`.

Admin bearer values use prefix `mgw_admin_`, reserved agent bearer values use `mgw_agent_`, and the session cookie is `mcp_gateway_session`. Approved one-time output sinks remain `controlling_terminal` and `owner_only_file`; the latter is a newly created, non-symlink-following `0600` file containing exactly the secret and one newline. S2's additional write-only secret ingress declarations are `admin_credential_replacement`, `dcr_client_secret`, `authorization_code_token_response`, `refresh_response`, and `authoritative_generation_refresh_copy`. Standard output and standard error are not secret sinks.

#### S2 request mechanics

| Method and pattern                                   | Closed request schema      | Success schema/status                         | Cursor | Idempotency | Exact `If-Match` | Response ETag |
| ---------------------------------------------------- | -------------------------- | --------------------------------------------- | ------ | ----------- | ---------------- | ------------- |
| `GET /api/v1/servers`                                | `ServerListQuery`          | `Page<Server>` / 200                          | yes    | no          | no               | no            |
| `POST /api/v1/servers`                               | `ServerCreate`             | `ServerMutation` / 201 or replay 200          | no     | yes         | no               | yes           |
| `GET /api/v1/servers/{id}`                           | none                       | `Server` / 200                                | no     | no          | no               | yes           |
| `PATCH /api/v1/servers/{id}`                         | `ServerPatch`              | `ServerMutation` / 200                        | no     | no          | yes              | yes           |
| `DELETE /api/v1/servers/{id}`                        | `EmptyObject`              | `ServerMutation` / 202 or replay 200          | no     | no          | yes              | yes           |
| `GET /api/v1/servers/{id}/operations`                | `ServerOperationListQuery` | `Page<ServerOperation>` / 200                 | yes    | no          | no               | no            |
| `POST /api/v1/servers/{id}/operations`               | `ServerOperationCreate`    | `ServerOperationMutation` / 202 or replay 200 | no     | yes         | yes              | no            |
| `GET /api/v1/servers/{id}/operations/{operation_id}` | none                       | `ServerOperation` / 200                       | no     | no          | no               | no            |
| `POST /api/v1/servers/{id}/credential-replacements`  | `CredentialReplacement`    | `CredentialReplacementResult` / 202           | no     | no          | yes              | no            |
| `GET /api/v1/servers/{id}/auth-flows`                | `ServerAuthFlowListQuery`  | `Page<ServerAuthFlow>` / 200                  | yes    | no          | no               | no            |
| `POST /api/v1/servers/{id}/auth-flows`               | `EmptyObject`              | `AuthFlowCreation` / 201                      | no     | no          | yes              | no            |
| `GET /api/v1/servers/{id}/auth-flows/{flow_id}`      | none                       | `ServerAuthFlow` / 200                        | no     | no          | no               | no            |
| `DELETE /api/v1/servers/{id}/auth-flows/{flow_id}`   | `EmptyObject`              | empty / 204                                   | no     | no          | no               | no            |
| `GET /api/v1/catalog`                                | `CatalogListQuery`         | `CatalogPage` / 200                           | yes    | no          | no               | no            |
| `GET /api/v1/servers/{id}/descriptors`               | `DescriptorListQuery`      | `Page<ToolDescriptor>` / 200                  | yes    | no          | no               | no            |
| `GET /api/v1/servers/{id}/descriptors/{tool_id}`     | none                       | `ToolDescriptor` / 200                        | no     | no          | no               | no            |
| `GET /oauth/callback`                                | `OAuthCallbackQuery`       | fixed `OAuthCallbackHTML` / 200, 400, or 503  | no     | no          | no               | no            |

In the executable mechanics table, `None` means no query or request body and `Empty` means an empty response body. A page is exactly `{items,next_cursor}`. List query schemas permit only their declared `cursor`, `limit`, and, for descriptors, `retired` members. `ServerCreate` is exactly `{namespace,display_name,enabled,transport}`; a nonempty `ServerPatch` permits only `display_name`, `enabled`, and complete `transport`. `ServerMutation` is exactly `{server,operation}`. `ServerOperationCreate` accepts only `reload`, `retry`, `refresh_catalog`, or `disconnect_credentials`; other operation kinds are internally generated. Credential replacement input accepts only `static_credential` or `oauth_client`; `oauth_tokens` authority comes only from validated OAuth responses. The strong ETag is exactly `"server-<id>-<desired_revision>"`; weak, wildcard, malformed, multiple, absent where required, or noncurrent preconditions are not interchangeable. S2 idempotency is scoped to parent admin credential, method, route, key, canonical validated/defaulted request, and exact precondition, with a 24-hour lifetime and 1,024-record bound.

#### S2 resource and union vocabulary

`Server` is exactly `{id,namespace,display_name,desired_state,desired_revision,transport,credential_revisions,credential_state,runtime,catalog,created_at,updated_at,deleted_at}`. `credential_revisions` is exactly `{static_credential,oauth_client,oauth_tokens}`. Runtime is exactly `{state,reason,runtime_id,reconciliation,dispatch}` and catalog is exactly `{durable_state,active_state,durable_revision,active_revision,durable_tool_count,active_tool_count,last_success_at,traversal}`; each occupancy uses `LimitStatus`.

The sanitized transport union is closed to stdio `{kind,executable,arguments,working_directory,environment,secret_environment}` and Streamable HTTP `{kind,url,protocol_mode,authentication}`. HTTP authentication is exactly `{mode:none}`, `{mode:bearer}`, or OAuth `{mode,registration,trusted_origins,request_offline_access}`. Registration is static `{mode,issuer,client_id,token_endpoint_auth_method}` or dynamic `{mode,issuer}`. Credential replacement is static `{kind,expected_revision,values}` or OAuth client `{kind,expected_revision,client_secret}`.

`ServerOperation` is exactly `{id,server_id,kind,target_desired_revision,target_credential_revisions,state,reason,created_at,started_at,finished_at}`. `ServerAuthFlow` is exactly `{id,server_id,flow_state,target_desired_revision,registration_revision,created_at,expires_at,finished_at,reason}`. `ToolDescriptor` is exactly `{id,server_id,upstream_name,external_name,descriptor,fingerprint,catalog_revision,first_seen_at,last_seen_at,retired_at}`. `CatalogPage` is exactly `{catalog,items,next_cursor}`, where catalog is exactly `{active_state,active_generation,changed_at,issue_count}`. `CredentialReplacementResult` is exactly `{server_id,kind,credential_revision,operation}` and `AuthFlowCreation` is exactly `{flow,authorization_url}`.

| Vocabulary                 | Closed values                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| -------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| transport kind             | `stdio`, `streamable_http`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              |
| protocol mode              | `auto`, `modern`, `legacy`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              |
| authentication mode        | `none`, `bearer`, `oauth`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               |
| registration mode          | `static`, `dynamic`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| token endpoint auth method | `none`, `client_secret_basic`, `client_secret_post`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| desired state              | `enabled`, `disabled`, `deleted`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| runtime state              | `inactive`, `activating`, `active`, `stopping`, `retry_wait`, `degraded`, `authentication_required`, `deleted`                                                                                                                                                                                                                                                                                                                                                                                                                                                          |
| operation kind             | `activate`, `reload`, `retry`, `refresh_catalog`, `credential_replace`, `disable`, `delete`, `disconnect_credentials`                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| operation state            | `scheduled`, `running`, `succeeded`, `failed`, `cancelled`, `superseded`, `interrupted`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| credential state           | `not_required`, `ready`, `absent`, `locked`, `interaction_required`, `unavailable`, `unsupported`, `refreshing`, `reauthentication_required`, `disconnecting`, `cleanup_pending`                                                                                                                                                                                                                                                                                                                                                                                        |
| durable catalog state      | `empty`, `current`, `stale`, `unavailable`, `retired`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| active catalog state       | `absent`, `refreshing`, `current`, `stale`, `unavailable`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               |
| aggregate catalog state    | `empty`, `current`, `degraded`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          |
| auth-flow state            | `preparing`, `awaiting_callback`, `exchanging`, `succeeded`, `failed`, `expired`, `cancelled`, `superseded`, `interrupted`                                                                                                                                                                                                                                                                                                                                                                                                                                              |
| credential kind            | `static_credential`, `oauth_client`, `oauth_tokens`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| descriptor retired filter  | `include`, `exclude`, `only`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            |
| public reason              | `configuration_invalid`, `resource_limit`, `connectivity`, `tls_failed`, `protocol_unsupported`, `protocol_invalid`, `authentication_rejected`, `credential_absent`, `keyring_absent`, `keyring_locked`, `keyring_interaction_required`, `keyring_unavailable`, `keyring_unsupported`, `oauth_rejected`, `oauth_expired`, `registration_expired`, `process_exited`, `output_limit`, `stop_unconfirmed`, `catalog_invalid`, `catalog_limit`, `catalog_stale`, `superseded`, `cancelled`, `interrupted`, `revocation_failed`, `revocation_unsupported`, `cleanup_pending` |

#### Shared strict JSON

`internal/strictjson` is dependency-neutral and is now the parser used by `internal/api`. Callers supply positive byte and depth limits and decide whether their destination is closed. Parsing rejects invalid UTF-8, duplicate object members (including escape-equivalent names), excess size or depth, trailing values, and unknown members for closed destinations. Canonical equality ignores object-member order and equivalent JSON number spellings while preserving array order. This primitive performs no server, OAuth, protocol, or catalog traversal behavior.

## Storage generation and recovery

An installation is one canonical owner-only `0700` directory guarded by a nonblocking exclusive process lock. A durable run marker distinguishes clean shutdown from an unclean stop; either startup path performs live identity, migration, pragma, and integrity verification before the store can be ready.

SQLite uses application ID `MGW1`, an immutable installation ULID, decimal revision, and an ordered embedded migration history. Schema 4 adds the durable S2 server foundation without changing accepted S1 tables. Every connection installs a two-second busy policy, enables foreign keys, verifies WAL and `synchronous=FULL`, and derives `max_page_count` from the compiled 1 GiB database limit and that connection's actual page size. Foreign, newer, partial, corrupt, unsafe-permission, and over-limit generations fail closed.

Security mutations are admitted through one nonblocking slot. Before a transaction begins, Gateway writes an installation-bound owner-only intent through temp write, file sync, atomic rename, and directory sync. A known commit or rollback moves the intent through a synced tombstone deletion. Marker I/O failures, storage-class statement failures, busy begin, commit errors, and post-commit uncertainty latch mutations; elapsed time, restart, or successful reads cannot clear the latch.

`restore --verify-current` reacquires stopped-process ownership, requires the current schema, runs full verification, closes SQLite, and only then durably removes marker artifacts. It emits one safe machine JSON result and does not make the service ready; normal startup must verify the generation again.

On-demand backup uses SQLite's online backup API under one nonblocking global work slot. Gateway stages an owner-only closed generation, verifies identity/schema/revision/full integrity and the 1 GiB bound, computes SHA-256, writes safe internal metadata, and atomically publishes it under a 26-character ID. The artifact-bound authority/key digest provides durable retry identity without storing a bearer or replaying a secret; 64 retained artifacts are the fixed record bound.

Backup restore holds stopped-process ownership, validates the published artifact and current installation binding, and copies one complete generation. Accepted S1 schema-3 artifacts are forward-migrated and fully verified while staged, before admin rekey or replacement publication. The staged database resets all restored admin verifiers only after publishing a replacement non-expiring bearer. A checkpointed staged database atomically replaces the active generation without prior WAL/SHM sidecars. Desired servers and safe S2 history reconstruct as stopped durable facts; runtime, process/session/route state, OAuth transients, events, raw secrets, and keyring values are never restored. Marker clearing and readiness still require completed replacement verification and a fresh normal startup.

## Durable server authority foundation

`internal/servers` is the only owner of S2 SQL. It stores permanent S1-compatible uppercase ULID server identities and permanent namespace tombstones. A server keeps one canonical sanitized transport definition while nondeleted, an immutable namespace/ID, a monotonic desired revision, and UTC creation/update/deletion metadata. Creation enforces 1,024 lineage identities, 64 nondeleted servers, and 32 enabled servers atomically; deletion increments the desired revision once, clears the transport, and never frees identity or namespace.

Every server begins with OAuthRegistration fence revision `0` and independent `static_credential`, `oauth_client`, and `oauth_tokens` revisions at `0`. Credential metadata may hold only a validated opaque S1 keyring handle; no keyring generation bytes are represented. Publishing or invalidating one credential kind increments only that kind. The repository supplies a narrow callback that validates captured desired, target-kind credential, and optional registration/flow revisions and updates metadata plus initial-token flow activation directly on the keyring coordinator's existing `*sql.Tx`; it never opens a nested `Store.Mutate`.

Operations capture desired and all credential revisions, use closed kinds/states/reasons, and permit only the declared scheduled/running transitions into immutable terminal state. Startup interruption is one marker-armed mutation. The repository retains the newest 64 terminal operations per server, never prunes nonterminal work, and advances a durable retained-floor watermark whenever pruning occurs. Server and operation lists capture insertion-sequence upper watermarks; later inserts are excluded and a cursor whose unconsumed position crossed the retained floor fails stale.

S2 idempotency stores no response body or secret-bearing request. It records only the parent admin credential ULID, method, route, visible-ASCII key, canonical request hash, exact precondition, safe server/operation references, desired revision, and fixed 24-hour timestamps. Lookup and conflict resolution occur before operation precondition evaluation. Matching retries replay the safe reference, differences conflict, records expire exactly at the retention boundary, expired records are deterministically removed, and 1,024 unexpired records reject new keys without eviction. Every durable mutation uses `storage.Store.Mutate`; storage admission is never held over external work, and a storage latch maps to the repository's fail-closed unavailable class.

The schema has no runtime ID, PID, session, route capability, request ID, OAuth state/verifier/code/authorization URL, token, exchange work, attempt, or raw secret column. Runtime and secret bytes therefore have no repository input or durable representation.

## Admin authority lifecycle

Admin bearer creation uses 32 bytes of entropy and the fixed `mgw_admin_` domain prefix. SQLite stores only a domain-separated SHA-256 verifier, a separately domain-separated 16-character fingerprint, metadata, status, and revision. Authentication distinguishes the reserved agent prefix before performing constant-time verifier comparisons; unknown and malformed admin values remain non-enumerating.

`initialize` and `admin-reset` require stopped-process ownership. They complete an approved one-time sink before entering the latched security mutation. Initialization activates only an empty authority set. Reset revokes every prior active verifier and inserts one replacement in the same revisioned transaction. Sink failure leaves all persisted authority unchanged, while an activation failure cannot make an unpublished bearer usable. Command JSON, standard error, argv, logs, and SQLite never contain the raw bearer.

Credential creation validates optional expiry against the compiled five-minute and one-year bounds before consuming entropy. Metadata reads derive expiry from the injected clock; authentication compares all active verifier candidates in constant time and rejects expired, revoked, unknown, malformed, and wrong-domain values safely. Revocation transactionally preserves at least one active non-expiring authority. At the 128-record cap, creation/reset prunes the oldest revoked or expired records by creation time and ID; it rejects without queuing when every record remains active.

Bearer exchange reserves one of 128 in-memory session slots before generating independent 32-byte session and CSRF values. Sessions use a host-only `mcp_gateway_session` cookie with `Path=/`, `HttpOnly`, and `SameSite=Strict`; no `Domain` is present, and `Secure` is omitted only for the fixed plain-loopback transport. Activity refreshes the 30-minute idle bound without extending the eight-hour absolute bound. Logout, idle/absolute or parent expiry, parent revocation, reset, and shutdown remove the slot and synchronously close its subscription channel. Ambiguous bearer-plus-cookie authority and incorrect session-bound CSRF fail without changing activity. No session state survives manager or process restart.

## Keyring capability and generation cutover

The `go-keyring` process-global functions sit behind instance-local adapters. Gateway's secret-free startup probe invokes no Get/Set/Delete or prompt presentation: Linux inspects the session D-Bus Secret Service, attempts only a nonpresenting unlock and dismisses any returned prompt object; macOS checks default-keychain metadata with bounded, output-discarding `security` commands. The snapshot is one of `ready`, `absent`, `locked`, `interaction_required`, `unavailable`, or `unsupported`, with a safe remediation code; it does not predict whether a later operation will interact. Missing items remain distinct from backend absence, and unknown native failures become unavailable without preserving native diagnostics.

Any Get/Set/Delete may invoke OS-managed interaction, fail, or outlive cancellation because `go-keyring` v0.2.7 is context-free. One process-global nonblocking `keyring_work` permit bounds outstanding operations; saturation rejects immediately and cancellation does not release the slot before the backend call returns. This accepted MVP limitation never permits file/configuration fallback. The MVP is unsuitable for unattended credential access; hardening is required before unattended deployment or after any unexpected dialog, cancellation-surviving call, or keyring-induced service blockage.

A keyring namespace binds the installation ULID, an immutable Gateway-derived resource-owner ULID, and one closed kind: `static_credential`, `oauth_client`, or `oauth_tokens`. Secret payloads are limited to 256 KiB, base64url encoded into stored values no larger than 3,000 bytes, and identified outside the provider only by random opaque handles. Chunks are written before a versioned owner/kind/handle/length/SHA-256 manifest, so no partial generation reads. Read verifies every binding, bound, decoded length, and digest; deletion handles complete or interrupted generations.

SQLite registers a non-authoritative candidate before the first keyring write, making crash leftovers discoverable without persisting secret bytes. After writing and reading back a complete generation, one latched transaction advances the Gateway revision, selects its opaque handle as authority, invokes any domain callback, and moves the prior handle to bounded cleanup metadata. Server callbacks make the candidate handle and one independent credential-kind revision current atomically under captured desired, authority, registration, and drain fences. Invalidation removes current keyring authority and nulls server authority metadata in the same transaction before best-effort deletion; failed deletion leaves only bounded non-authoritative cleanup metadata. At most 64 candidates exist per owner/kind. Startup or replacement cleanup removes only non-authoritative candidates; ordinary interruption before commit preserves old authority, while interruption after commit exposes only the new complete generation. The explicit installation seam used after authorization-server success invalidates both old and candidate authority on write, verification, publication, or acknowledged post-commit failure before cleanup, so that failure path serves neither generation.

Deterministic injected tests cover Darwin and Linux mappings, prompt dismissal, generation faults, N/N+1 candidates, and every cutover boundary. The native target uses an isolated Linux D-Bus/home/Secret Service environment when its prerequisites exist. macOS keychain search/default state is changed only in an explicitly confirmed disposable login context and is restored afterward; ordinary hosts receive a clear prerequisite skip.

## Direct stdio supervision foundation

`internal/runtimes` owns a bounded direct stdio supervisor for later lifecycle and protocol composition. It executes only the validated absolute executable with literal arguments and the exact working directory, creates a fresh process group, and supplies a clean environment containing only declared non-secret values plus runtime-resolved secret slots. It never invokes a shell, performs PATH lookup, or inherits ambient proxy or dynamic-loader variables. Process and group ownership remain memory-only.

Stdout is one bounded NDJSON frame stream: the 4 MiB ceiling excludes the newline delimiter, and an incomplete final frame is protocol-invalid. Stdout and concurrently drained stderr have independent token buckets with the compiled 8 MiB/s rate and 8 MiB burst; excess fails only that runtime with the safe `output_limit` class. At most 64 KiB of stderr is retained privately in memory and no raw process output, exit detail, or secret is exported to status, events, logs, or persistence. The supervisor admits 32 processes without waiters and exposes only safe process-exit classifications. Normal stop closes protocol input, signals only the captured and reverified process group, waits three seconds, then permits a forced group kill and two-second reap window. Ownership mismatch or an unverified reap fails closed and can be retried against the same process-local handle. The reconciliation manager owns a generation-fenced active publisher: every behavioral trigger synchronously fences and withdraws the old publication, retains exact process-local ownership, requires the injected driver to confirm stop before starting a replacement, conditionally publishes only the current candidate, and records operation success last. Unconfirmed stop retains only a cleanup handle, reports `stop_unconfirmed`, and gates replacement; a disabled/deleted retry performs cleanup without activation, while display-only revisions preserve the active generation. Process and group IDs are never persisted, so restart cannot kill a reused PID; an uncatchable Gateway crash may orphan a child for operator cleanup. First-signal drain increments the process-local epoch, fences and withdraws all routes before launching every at-most-32 runtime stop concurrently outside the global-four reconciliation path. Late work cannot publish, keyring consumers drain before storage closes, and the process still obeys the ten-second deadline; any unconfirmed runtime stop leaves the durable run marker unclean rather than claiming verified shutdown.

## Raw downstream and remote transport foundation

`internal/downstream` owns monotonic runtime-local request IDs and strict closed JSON-RPC envelopes before protocol-era projection. Its stdio connection writes bounded NDJSON only through the supervised runtime and requires verified runtime stop on closure. Its Streamable HTTP connection emits only role-built JSON Content-Type, JSON/SSE Accept, protocol, method, optional name, validated parameter mirrors, runtime-bound legacy session, and server-scoped Authorization; bounded JSON or one bounded SSE event is returned without inheriting inbound authority, IDs, sessions, progress/task fields, headers, or metadata. Runtime-root closure cancels active HTTP exchanges and waits for each exchange to return before confirming transport stop; expiry of the stop context remains unconfirmed.

Each downstream runtime independently selects `modern`, `legacy`, or `auto`. Modern discovery and every later modern request carry exact `2026-07-28`, fixed `mcp-gateway/s2` client information, and explicit empty capabilities. Legacy initialization selects exact `2025-11-25`, omits modern metadata, and sends one `notifications/initialized`. Auto fallback destroys the probe and requires verified stdio reap before constructing a fresh legacy coordinator; only a strict matching `-32601` with absent/null data or either exact HTTP 400 text body qualifies. Valid DiscoverResult and `-32022` are modern evidence, with at most one strict mutually-supported modern retry; HTTP 404 and every malformed, authentication, timeout, network, redirect, 429, or 5xx case reject downgrade. Modern HTTP is sessionless. A legacy HTTP session is at most 512 bytes, memory-only, immutable, and runtime-bound; loss closes the runtime and never retries or reroutes a call.

A call object copies one pinned upstream name and validated argument object, constructs one exact `tools/call`, and is consumed after one execution attempt. Its monotonic state flips immediately before the first OS pipe write or HTTP RoundTripper handoff. Validation, closed-runtime, or cancellation failures before that point are `pre_start`; any partial write, connect/TLS/network error, response-read failure, session loss, or lifecycle cancellation after it is `start_uncertain`, without inference from a return value. Local cancellation is always applied. Modern stdio may send exactly one metadata-bearing `notifications/cancelled`, modern HTTP sends none, and legacy stdio/HTTP may send exactly one metadata-free notification on the bound session. The call registry lets runtime withdrawal cancel active attempts without replay or reroute; late cancellation after completion emits nothing.

The S4-facing current-runtime capability is an opaque, explicitly nonserializable process-local object. It captures server/tool/upstream/runtime identity and exact desired, static-credential, OAuth-client, OAuth-token, and catalog revisions, but exposes no identity enumeration or mutation. Acquisition tries the global 32-slot channel before the four-slot server channel and never waits; server saturation immediately releases global. Only after both permits does an injected current-state seam revalidate route, runtime, every bound authority/catalog revision, and drain state. A final capability-lock check closes the withdrawal race. Stale, withdrawn, draining, unavailable, canceled, and both saturation branches are typed pre-start rejections. A lease may execute one call or cancel once; it releases server before global and preserves lower-level marker classification. Withdrawal synchronously marks the capability unavailable and cancels registered leases. No package consumer exists in production composition, so S2 still cannot authorize, audit, persist, present, enumerate to agents, reroute, or replay calls.

`internal/remote` is the shared non-bypassable outbound substrate for downstream and OAuth roles. It canonicalizes scheme/host/default port, rejects userinfo and fragments, rejects queries unless the role explicitly permits a bounded OAuth query, supports only HTTPS plus the exact downstream IP-literal loopback HTTP exception, resolves every connection fresh, rejects any disallowed answer, and dials the first validated address directly while preserving the original TLS hostname and platform roots. Trusted roles may explicitly permit restricted addresses only after their own origin authority check. Each exchange uses a fresh no-proxy/no-cookie/no-redirect/no-compression/no-keepalive client, validates response header count/bytes/value bounds before a bounded body read, and owns body closure. Production downstream and OAuth source guards prohibit permissive HTTP convenience and SDK transport paths.

`internal/oauth` builds one process-local trust graph from the exact registered HTTPS MCP resource. One present challenge metadata value is authoritative or fails; otherwise endpoint-specific RFC 9728 metadata may fall back to root metadata only on absence. Resource strings are compared exactly against the branch-specific audience, at least one unique issuer is required, and `header` is mandatory when bearer methods are advertised. A sole same-resource-origin issuer may be selected automatically; every cross-origin or multiple-issuer choice requires the exact desired issuer. The selected issuer is fetched in RFC 8414 then OpenID well-known order, must match byte-for-byte, and must advertise code response/grant plus PKCE S256. Required and optional consumed members are exactly typed while extensions are discarded. Registered resources and issuer identifiers prohibit queries; standard metadata and delegated endpoint URLs allow only the compiled bounded query. Canonical explicit trusted origins are unique lowercase HTTPS DNS origins without default ports or paths. The registered-resource origin and explicitly trusted origins may resolve to restricted addresses; all other machine fetches retain the public-address policy.

A static registration is local and binds the exact graph issuer/resource, configured client ID, fixed numeric-loopback callback, and an explicitly metadata-supported token endpoint authentication method. Dynamic registration requires the advertised role endpoint and no reusable exact unexpired authority. It sends one unauthenticated bounded RFC 7591 native-client JSON request with the exact callback, code response, code/refresh grants, and one method selected Basic → Post → None; it never probes or retries. Publication accepts only bounded extensible JSON at `201` whose client ID, sole callback, response/grant support, returned method, and secret/expiry shape match. Confidential results require a nonempty secret plus expiry `0` or strictly future and atomically publish the registration row, `oauth_client` credential revision/handle, keyring authority, and verified candidate generation in one marker-armed transaction. Public None results publish only the registration revision. Static confidential registrations obtain client-secret authority only from the write-only replacement route. Stale desired/registration/client/flow or drain results are orphan cleanup only; registration-management artifacts and unknown extensions are discarded.

Foreground flow creation first commits one safe `preparing` record under the global-16/per-server-one bound, then performs discovery/registration outside SQLite admission. Independent 32-byte state and verifier material, S256 challenge, exact graph/registration/revision binding, endpoint snapshot, and sorted requested scopes remain in one process-local registry. Only the creation response receives the exact canonical authorization URL; durable rows, later reads, events, logs, backups, and browser assets cannot represent it or its state/verifier. Publication to `awaiting_callback` rechecks desired, registration, and flow identity; superseded or late DCR cannot publish. New preparation supersedes `preparing`/`awaiting_callback`, `exchanging` conflicts, cancellation is terminally idempotent, exact five-minute expiry excludes exchanging work, behavioral server mutation fences flows, and startup interrupts every nonterminal record. The callback validates one nonempty state before immediate global-eight admission, then consumes it before inspecting code/error/issuer. The captured RFC 9207 support bit controls exact `iss` presence and byte equality for both code and error branches. A valid code commits `exchanging`, rechecks every captured fence, loads confidential client authority only from keyring when required, and sends one exact form to the bound token endpoint through the hardened factory. Basic percent-encodes credentials before the sole Authorization header; Post places client ID/secret only in form; None sends client ID only. A strict bounded `200 application/json` Bearer response may narrow but never expand requested scopes; omission preserves requested or unspecified/default semantics. The versioned complete token generation binds server, issuer, registration revision, resource, scopes, issuance, optional expiry, access token, and optional refresh token. Post-authorization cutover fences prior tokens, and activation atomically clears the keyring fence and marks the unchanged exchanging flow succeeded; any candidate, storage, staleness, drain, or acknowledged post-commit failure invalidates token authority and fails the flow. Callback bodies are fixed nonreflecting HTML. Write-only replacement validates the exact stdio/bearer slot set or static Basic/Post OAuth mode before decoding secret values, then uses ordinary complete-generation cutover. The credential metadata increment, safe `credential_replace` operation, and foreground-flow supersession share the coordinator-owned transaction; desired revision and ETag do not change. A process-local fence withdraws publication before commit, and failed commit recovery reconstructs old authority. Refresh is per-server single-flight and holds global keyring admission across exact authority reads, rediscovery, one hardened request, and the shared post-AS-success installation protocol. Eligibility uses the bounded lifetime lead. Hardened downstream HTTP projects only bounded recognized Bearer challenge fields; modern discovery, legacy initialization, and the first raw catalog page terminate with a typed credential-replacement disposition and no same-client refresh or replay. Reconciliation consumes at most one recognized invalid-token disposition for the exact generation. The refresh uses the challenged candidate's desired, registration, client, and token fence without generic status or trigger callbacks; successful cutover withdraws and verifies stop before exact current-authority reacquisition and a fresh runtime replays only the challenged stage once. Staleness, uncertainty, drain, stop-unconfirmed, or a second challenge cannot replay. Confidential omission copies the old refresh token, public clients require distinct rotation, and effective scope cannot expand. Pre-handoff failure preserves authority; uncertain post-handoff, invalid client/grant, bad public rotation, or any post-success persistence/staleness/drain fault synchronously withdraws and invalidates authority for foreground reauthorization. Insufficient scope is projected for a process-local sorted-union step-up and performs no refresh or replay. The concrete raw catalog path preserves that disposition only on its first page; insufficient scope is projected for foreground step-up, while later-page challenges become terminal authentication failures. Traversal uses the Gateway runtime request seam from empty cursor with global-four immediate admission, exact 15-second page/60-second total deadlines, 32-page/4-MiB bounds, cursor cycle and name/collision/count rejection, and isolated bounded raw descriptor issues. Isolated entries project to a closed SDK-independent descriptor and stable `(server_id,upstream_name)` key. Input/output schemas must compile as object-root JSON Schema 2020-12 with local-fragment references only through an external-rejecting loader; unknown descriptor/annotation fields are removed, effective annotation defaults materialized, and 96-KiB combined schema/128-KiB canonical bounds enforced. RFC 8785 canonical bytes produce lowercase SHA-256 fingerprints. Only modern HTTP input properties may retain unique typed nested `x-mcp-header` bindings; present nonnull validated scalar arguments mirror through canonical string/boolean/safe-integer values and SEP-2243 base64 wrapping. A normalized candidate commits one complete durable revision under current desired/registration/credential/catalog fences. Projected per-server/global identity capacity is checked before immutable ID allocation; success alone retires omissions, reappearance reuses identity, safe issues aggregate by closed class, and failure changes no snapshot facts. Descriptor reads use revision/filter/insertion-watermark cursors and include retired evidence. A process-local registry serializes every capacity reservation, durable commit, immutable active replacement, stale retention, and withdrawal under one global lock; active generation cursors and per-server status change atomically, while a post-commit stale runtime publishes nothing. Backups include only SQLite facts, and a new process starts a new empty active generation. Polling uses an injected strict-after-now five-minute epoch grid with the installation/server SHA-256 offset, one traversal per server, and the existing global-four immediate admission. Explicit refresh attaches its one durable operation to exact in-flight poll work without lifecycle restart; waiter cancellation does not cancel owner work. A challenge completion is delivered only after singleflight removal, and the manager admits one exact poll/explicit handoff with no operation for an unattached poll and no queue or duplicate consumer. The attached operation remains running across refresh, withdrawal, verified stop, and fresh first-page traversal; independent desired, authority, lifecycle, or drain changes supersede it. Fresh completion alone schedules one next strict-grid poll from the then-current clock. Healthy nonchallenge failures retain one stale safe issue and the old immutable snapshot, while withdrawal/drain cancels work and timers. The traverser has no SDK list cache/subscription path. A successful active commit with an exact runtime atomically swaps opaque per-tool capabilities pinned to every runtime/authority/catalog revision. Immediate global-then-server admission revalidates after permits and again at execution; normalized modern-HTTP bindings alone derive outbound headers from validated arguments. Stale state rejects new work, withdrawal fences and cancels leases, and no production ingress consumes or enumerates these capabilities. Disconnect/delete reconciliation uses the same stop-before-cleanup path: current local authority is invalidated before remote work, OAuth disconnect preserves registration/client authority, and delete clears every present credential plus registration authority without changing its tombstone. A validated revocation endpoint receives one refresh-token request followed by one distinct access-token request using one metadata-supported Basic/Post/None method; absence/failure is observed safely and never restores authority. Local generation deletion failure is `cleanup_pending`; explicit retry performs candidate deletion only, with no remote replay or authority restoration.

## Deterministic test foundation

Shared tests use mutex-safe fake time and finite deterministic entropy, real owner-only `0700` temporary data roots with symlink/type/owner/mode validation, and a streaming canary scanner that detects cross-buffer leaks without returning the canary in errors. The common real-binary runner requires a positive timeout and per-stream byte cap, captures stdout and stderr separately, reports truncation and exit status, cancels its direct child when its context expires, and can signal a bounded started process for lifecycle tests. Component-specific fault hooks, protocol fixtures, and barriers remain with their owning packages.

## Implemented HTTP and control boundary

`serve` verifies stopped-process ownership and storage, takes one secret-free keyring capability snapshot, and only then opens the exact configured numeric IPv4 loopback listener. The boundary validates request target, header, forwarding, Host, Origin, route, and method constraints before authentication or body work. Health, ordinary, control-auth, and authenticated-admin permits are independent and nonblocking; authenticated control work transfers permits without allowing ordinary traffic to consume recovery capacity.

The control API implements bearer-to-session exchange, session logout, bounded credential and backup resources, invalidation events, system status, desired-server and operation create/list/read APIs, and auth-flow create/list/read/delete APIs. Bearer and cookie authority cannot be combined. Cookie requests require the exact configured Origin, and unsafe cookie requests require the session CSRF value and JSON media type. JSON parsing bounds body size and nesting and rejects invalid UTF-8, duplicate members, unknown members, and trailing input. Server creation and explicit operations use parent-authority-scoped durable idempotency; server and operation lists use snapshot cursors; server reads and mutations carry exact strong Server ETags, with required exact `If-Match` on PATCH, DELETE, and operation creation. API responses are `no-store`, problems retain the fixed safe envelope, and no response enables CORS.

The public shell and local stylesheet are embedded, contain no external active content, and use the fixed restrictive CSP. `/oauth/callback` is production-wired to the one-time flow registry and returns only fixed success/failure/transient HTML with no-store, deny-all CSP, no-referrer, and nosniff headers; it never reflects query or dependency data.

## Events, composed limits, and lifecycle

The authenticated event hub admits 16 streams with 16 buffered invalidations each. Publication is nonblocking and carries only the closed safe `admin_credentials`, `system_status`, `backups`, `servers`, `server_operations`, `server_auth_flows`, and `catalog` representations; current production composition emits S1 invalidations plus implemented server, server-operation, and auth-flow invalidations. Frames have no ID, cursor, replay, or storage authority. Connect and reconnect begin with no history; a full buffer disconnects the slow stream so its client recovers by reloading authenticated snapshots. A 15-second comment keepalive and per-write deadline bound dead peers. Request cancellation, session terminal state, parent-bearer invalidation, and shutdown close the stream and release its permit.

Event streams release authenticated-admin admission after authentication because their own registry supplies the lifetime bound; saturation therefore cannot consume the status/recovery pool. Live status composes the independent HTTP, session, MCP, event, backup, keyring-work, candidate, credential, idempotency, backup-record, database, durable server identity, nondeleted server, process-local runtime, reconciliation, and foreground OAuth-flow occupancies. Server reads compose process-local runtime state and safe reason/runtime identifiers over durable desired authority; restore never turns durable rows into active facts before fresh reconstruction. Every admission and retained-record cap is compiled, rejects before expensive allocation without queuing, and releases on every terminal path.

The first `SIGINT` or `SIGTERM` atomically enters drain, makes readiness false, rejects new work other than health and authenticated status, invalidates the keyring consumer epoch, and closes events, MCP compatibility state, and admin sessions. HTTP shutdown then has the fixed ten-second bound. A second signal invokes immediate forced exit rather than waiting for context-free work. Graceful completion removes the durable run marker; forced/unclean termination leaves it for the next startup's full storage verification. Runtime registries are never serialized, so no session or in-flight work resumes.

## Implemented MCP ingress

Agent authentication runs before MCP body reads, era classification, and session lookup. Production uses a deny-all authenticator; positive fixtures inject opaque principal/credential/expiry bindings. Admin bearer prefixes are rejected as the wrong credential domain, while missing, malformed, unknown, and expired agent credentials remain non-enumerating.

Gateway validates the modern `2026-07-28` header/body protocol mirror and dispatches only sessionless POST requests to the official SDK's stateless transport. Per-request client metadata replaces initialization state in this era. Modern requests reject legacy session IDs, cannot fall through to legacy classification, propagate request cancellation, and advertise neither tools nor list-change capability.

Legacy `2025-11-25` initialization reserves one of 128 slots before entropy or state publication, then uses a stateful official-SDK adapter with a Gateway-generated opaque session ID. Every later POST, GET, or DELETE requires the exact legacy protocol header, the session ID, and a reauthenticated binding matching both principal and credential. Idle/absolute expiry, credential expiry or invalidation, DELETE, shutdown, and restart close or discard the in-memory session and release capacity. Modern, legacy, MCP-work, and MCP-stream state remain separate; 32 work and 32 stream permits reject without queuing.

## Current executable state

The executable exposes stopped-process `initialize`, `admin-reset`, `restore --verify-current`, and verified backup replacement, plus `serve` for the verified HTTP/control foundation. Public health, authenticated admin credential/session/status/backup/event, desired-server, operation, and foreground auth-flow resources, the minimal static shell, the bounded OAuth callback and initial token installer, isolated dual-era MCP adapters, composed fixed limits, coordinated two-stage shutdown, and internal server-scoped keyring publication/invalidation seams are implemented. Desired mutations and explicit operation triggers commit safe scheduled records with closed admission, attachment, conflict, supersession, and replay semantics, then notify the process-local reconciliation manager. The manager captures desired/authority/drain fences, serializes each server, admits four globally without waiter goroutines, and uses one coalesced timer with the fixed retry sequence. A direct bounded stdio supervisor with verified graceful/forced process-group cleanup, concurrent all-runtime shutdown drain, and injected stop-before-start/publication lifecycle seams is implemented behind the runtime boundary, but the production driver remains fail-closed until downstream activation is composed. Descriptor and catalog routes remain unserved contract declarations. Production `/mcp` remains deny-all.

## Non-goals

The current checkpoint wires durable servers to transport-neutral reconstruction, per-server serialization, global-four immediate admission, stale fences, fixed retries, a process-local bounded direct stdio supervisor, and generation-fenced withdraw → verified stop → replacement start → conditional publication ordering. The injected production driver still performs no process or network activation and reports a safe unsupported state until downstream composition lands. Before it can run, asynchronous reconstruction validates exactly the authority required by the current transport: static slot equality, bearer, or current OAuth registration/client/token binding and expiry. Credential-free transports skip keyring access. Provider absent/locked/interaction-required/unavailable/unsupported and work saturation remain distinct fail-closed outcomes isolated to the dependent server. Shutdown rejects late credential-status mutation, epoch-fences keyring returns, and drains runtime then keyring authority before storage closure without waiting beyond the process deadline for context-free native work. It does not yet serve descriptor or catalog resources, and does not implement production downstream activation, token refresh/revocation/disconnect, catalog traversal/publication, principal/grant management, agent credential issuance, tool routing or calls, invocation audit, grant-request tools, or product UI pages. It supports only the future modern `2026-07-28` and legacy `2025-11-25` Streamable HTTP adapters; pre-Streamable HTTP+SSE and MCP list-change notifications are excluded.
