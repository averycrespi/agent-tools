# Public Contract

Audience: Maintainers and contributors changing the public HTTP and data contract

Authority: Normative product design

This chapter owns the behavior and invariants described below. Operational procedures remain in the linked guides; exact executable contract values remain owned by `internal/contract` and must agree with this chapter.

## HTTP and data contract

The `internal/contract` package is the single executable source consumed by API, ingress, authorization, discovery, and composition implementations. Its returned tables are copies so callers cannot mutate the canonical contract. The tables and mechanics below document the corresponding normative closed vocabulary for principal-credential authentication, administration, discovery, and invocation. The default authority is `127.0.0.1:8210`, its canonical Origin is `http://127.0.0.1:8210`, the supported protocol versions are modern `2026-07-28` and legacy `2025-11-25`, and the media types are `application/json`, `application/problem+json`, and `text/event-stream`.

### Route ownership

Methods are lexicographically ordered and become the exact `Allow` value. `HEAD` is never inherited from `GET`.

| Pattern                                              | Exact `Allow`        | Authority                                         |
| ---------------------------------------------------- | -------------------- | ------------------------------------------------- |
| `/`                                                  | `GET`                | public                                            |
| `/assets/*`                                          | `GET`                | public                                            |
| `/livez`                                             | `GET`                | public                                            |
| `/readyz`                                            | `GET`                | public                                            |
| `/mcp`                                               | `DELETE, GET, POST`  | agent                                             |
| `/oauth/callback`                                    | `GET`                | one-time OAuth state                              |
| `/api/v1/admin-sessions`                             | `POST`               | admin bearer                                      |
| `/api/v1/admin-sessions/current`                     | `DELETE, POST`       | admin session                                     |
| `/api/v1/admin-credentials`                          | `GET, POST`          | admin bearer or session                           |
| `/api/v1/admin-credentials/{id}`                     | `DELETE, GET`        | admin bearer or session                           |
| `/api/v1/admin-authority`                            | `GET`                | admin bearer                                      |
| `/api/v1/admin-credentials/{id}/rotation-completion` | `POST`               | admin bearer                                      |
| `/api/v1/system-status`                              | `GET`                | admin bearer or session                           |
| `/api/v1/backups`                                    | `GET, POST`          | admin bearer or session                           |
| `/api/v1/backups/{id}`                               | `DELETE, GET`        | admin bearer or session                           |
| `/api/v1/events`                                     | `GET, POST`          | GET: admin bearer or session; POST: admin session |
| `/api/v1/servers`                                    | `GET, POST`          | admin bearer or session                           |
| `/api/v1/servers/{id}`                               | `DELETE, GET, PATCH` | admin bearer or session                           |
| `/api/v1/servers/{id}/operations`                    | `GET, POST`          | admin bearer or session                           |
| `/api/v1/servers/{id}/operations/{operation_id}`     | `GET`                | admin bearer or session                           |
| `/api/v1/servers/{id}/credential-replacements`       | `POST`               | admin bearer or session                           |
| `/api/v1/servers/{id}/auth-flows`                    | `GET, POST`          | admin bearer or session                           |
| `/api/v1/servers/{id}/auth-flows/{flow_id}`          | `DELETE, GET`        | admin bearer or session                           |
| `/api/v1/catalog`                                    | `GET`                | admin bearer or session                           |
| `/api/v1/servers/{id}/descriptors`                   | `GET`                | admin bearer or session                           |
| `/api/v1/servers/{id}/descriptors/{tool_id}`         | `GET`                | admin bearer or session                           |
| `/api/v1/principals`                                 | `GET, POST`          | admin bearer or session                           |
| `/api/v1/principals/{id}`                            | `GET, PATCH`         | admin bearer or session                           |
| `/api/v1/principals/{id}/credential`                 | `DELETE, POST`       | admin bearer or session                           |
| `/api/v1/grants`                                     | `GET, POST`          | admin bearer or session                           |
| `/api/v1/grants/{id}`                                | `DELETE, GET, PATCH` | admin bearer or session                           |
| `/api/v1/grant-constraints/validate`                 | `POST`               | admin bearer or session                           |
| `/api/v1/grant-requests`                             | `GET`                | admin bearer or session                           |
| `/api/v1/grant-requests/{id}`                        | `GET`                | admin bearer or session                           |
| `/api/v1/grant-requests/{id}/approve`                | `POST`               | admin bearer or session                           |
| `/api/v1/grant-requests/{id}/reject`                 | `POST`               | admin bearer or session                           |
| `/api/v1/invocations`                                | `GET`                | admin bearer or session                           |
| `/api/v1/invocations/{id}`                           | `GET`                | admin bearer or session                           |

`/assets/*` requires a nonempty path below `/assets/`. Item patterns require exactly one nonempty segment. All other paths are unowned and therefore `404`.

The invocation-read mechanics are `InvocationListQuery` → `InvocationPage` for the collection and `None` → `Invocation` for an item. They use the sole invocation repository and introduce no mutation, replay, event, or mutable join.

The non-mutating grant matcher validation mechanic is `GrantConstraintValidation` → `GrantConstraintValidationResult`. The request is exactly `{constraint}`, where `constraint` must be a non-null object; missing, null, or non-object members are malformed requests. A well-formed request always returns `200`: valid constraints return an empty diagnostics array, while compiler-invalid constraint objects return one safe `GrantConstraintDiagnostic` with a JSON Pointer field and fixed message. The endpoint uses the production compiler, writes no durable state, publishes no invalidation, and grant creation and request approval still compile authoritatively in their own mutation paths.

### Safe problems

Problems normally have exactly `status`, `code`, and `title`. The `invalid_server_configuration` problem additionally has one required `context` object with exact `field` and `rule` members so every administrative client can identify the rejected configuration boundary. Both values come from closed vocabularies: fields are `configuration`, `namespace`, `display_name`, `enabled`, `transport`, `transport.kind`, `transport.executable`, `transport.arguments`, `transport.working_directory`, `transport.environment`, `transport.secret_environment`, `transport.url`, `transport.protocol_mode`, `transport.authentication`, `transport.authentication.mode`, `transport.authentication.trusted_origins`, `transport.authentication.request_offline_access`, `transport.authentication.registration`, `transport.authentication.registration.mode`, `transport.authentication.registration.issuer`, `transport.authentication.registration.client_id`, and `transport.authentication.registration.token_endpoint_auth_method`; rules are `invalid`, `required`, `maximum`, `unique`, `disjoint`, `canonical_absolute_path`, `canonical_url`, and `transport_policy`. Only one deterministic first violation is returned. Dependency messages, submitted values, paths, payloads, dynamic map keys, array positions, and other details are never added.

| Status | Code                                    | Fixed title                                                         |
| -----: | --------------------------------------- | ------------------------------------------------------------------- |
|    400 | `malformed_request`                     | The request is invalid.                                             |
|    400 | `invalid_json`                          | The JSON body is invalid.                                           |
|    400 | `invalid_cursor`                        | The cursor is invalid.                                              |
|    400 | `invalid_idempotency_key`               | The idempotency key is invalid.                                     |
|    400 | `ambiguous_credentials`                 | Multiple credential types were supplied.                            |
|    400 | `invalid_oauth_state`                   | The OAuth state is invalid or expired.                              |
|    401 | `authentication_required`               | Authentication is required.                                         |
|    403 | `credential_domain_mismatch`            | The credential is for a different authority.                        |
|    403 | `forbidden_origin`                      | The Origin is not accepted.                                         |
|    403 | `csrf_failed`                           | CSRF validation failed.                                             |
|    404 | `not_found`                             | The resource was not found.                                         |
|    405 | `method_not_allowed`                    | The method is not allowed.                                          |
|    409 | `conflict`                              | The request conflicts with current state.                           |
|    409 | `idempotency_conflict`                  | The idempotency key conflicts with prior work.                      |
|    409 | `admin_rotation_conflict`               | The administrator credential rotation conflicts with current state. |
|    412 | `stale_admin_authority`                 | The administrator authority revision is stale.                      |
|    428 | `admin_authority_precondition_required` | The administrator authority revision is required.                   |
|    413 | `body_too_large`                        | The request body is too large.                                      |
|    415 | `unsupported_media_type`                | The media type is not supported.                                    |
|    421 | `misdirected_request`                   | The Host is not accepted.                                           |
|    429 | `resource_limit`                        | The resource limit is reached.                                      |
|    503 | `storage_unavailable`                   | Storage is unavailable.                                             |
|    503 | `keyring_unavailable`                   | The credential provider is unavailable.                             |
|    503 | `shutting_down`                         | The service is shutting down.                                       |
|    400 | `invalid_server_configuration`          | The server configuration is invalid.                                |
|    400 | `invalid_operation`                     | The server operation is invalid.                                    |
|    409 | `namespace_unavailable`                 | The server namespace is unavailable.                                |
|    409 | `operation_conflict`                    | The server has conflicting work.                                    |
|    409 | `oauth_flow_active`                     | The OAuth flow is already exchanging.                               |
|    409 | `stale_cursor`                          | The cursor snapshot is no longer available.                         |
|    412 | `stale_revision`                        | The server revision is stale.                                       |
|    428 | `precondition_required`                 | The current server revision is required.                            |
|    503 | `downstream_unavailable`                | The downstream server is unavailable.                               |
|    400 | `invalid_principal`                     | The principal is invalid.                                           |
|    400 | `invalid_grant`                         | The grant is invalid.                                               |
|    412 | `stale_grant_revision`                  | The grant revision is stale.                                        |
|    428 | `grant_precondition_required`           | The current grant revision is required.                             |
|    412 | `stale_principal_revision`              | The principal revision is stale.                                    |
|    428 | `principal_precondition_required`       | The current principal revision is required.                         |
|    503 | `authorization_unavailable`             | Authorization is unavailable.                                       |
|    400 | `invalid_grant_request`                 | The grant request is invalid.                                       |
|    409 | `grant_request_conflict`                | The grant request conflicts with current state.                     |
|    412 | `stale_grant_request_revision`          | The grant request revision is stale.                                |
|    428 | `grant_request_precondition_required`   | The current grant request revision is required.                     |

### Fixed numeric limits

Every maximum accepts N and rejects N+1. Values below zero are invalid. These are compiled boundaries, not configuration.

| Contract name                                 |    Maximum |
| --------------------------------------------- | ---------: |
| `request_target_bytes`                        |       8192 |
| `request_header_bytes`                        |      32768 |
| `request_header_count`                        |        100 |
| `request_header_value_bytes`                  |       8192 |
| `api_json_body_bytes`                         |    1048576 |
| `mcp_body_bytes`                              |    4194304 |
| `json_depth`                                  |         64 |
| `http_regular`                                |        128 |
| `http_control_auth`                           |         32 |
| `http_admin`                                  |         16 |
| `http_health`                                 |          8 |
| `mcp_work`                                    |         32 |
| `mcp_streams`                                 |         32 |
| `admin_sessions`                              |        128 |
| `legacy_sessions`                             |        128 |
| `event_streams`                               |         16 |
| `event_buffered_invalidations`                |         16 |
| `backup_work`                                 |          1 |
| `backup_records`                              |         64 |
| `admin_credentials`                           |        128 |
| `admin_list_page`                             |        100 |
| `backup_list_page`                            |        100 |
| `database_bytes`                              | 1073741824 |
| `idempotency_key_bytes`                       |        128 |
| `idempotency_records`                         |       1024 |
| `opaque_id_bytes`                             |         26 |
| `cursor_bytes`                                |        512 |
| `sse_frame_bytes`                             |        512 |
| `keyring_secret_bytes`                        |     262144 |
| `keyring_chunk_bytes`                         |       3000 |
| `keyring_candidates`                          |         64 |
| `keyring_work`                                |          1 |
| `namespace_bytes`                             |         32 |
| `display_name_bytes`                          |        256 |
| `stdio_arguments`                             |         64 |
| `stdio_environment_entries`                   |         32 |
| `stdio_secret_environment_entries`            |         16 |
| `stdio_path_bytes`                            |       4096 |
| `stdio_argument_bytes`                        |       4096 |
| `stdio_arguments_bytes`                       |      32768 |
| `stdio_environment_name_bytes`                |       4096 |
| `stdio_environment_value_bytes`               |       4096 |
| `secret_slot_name_bytes`                      |         64 |
| `resource_url_bytes`                          |       8192 |
| `server_identities`                           |       1024 |
| `servers`                                     |         64 |
| `enabled_servers`                             |         32 |
| `downstream_runtimes`                         |         32 |
| `server_reconciliations`                      |          4 |
| `per_server_reconciliation`                   |          1 |
| `catalog_traversals`                          |          4 |
| `oauth_flows`                                 |         16 |
| `per_server_oauth_flows`                      |          1 |
| `oauth_callback_work`                         |          8 |
| `terminal_operations`                         |         64 |
| `terminal_auth_flows`                         |         16 |
| `s2_list_page`                                |        100 |
| `active_tools_per_server`                     |        256 |
| `active_tools`                                |       2048 |
| `durable_tool_identities_per_server`          |        512 |
| `durable_tool_identities`                     |       4096 |
| `tools_list_pages`                            |         32 |
| `tools_list_page_bytes`                       |    4194304 |
| `tool_descriptor_bytes`                       |     131072 |
| `tool_schema_bytes`                           |      98304 |
| `tool_name_bytes`                             |        128 |
| `external_tool_name_bytes`                    |        128 |
| `tool_title_bytes`                            |       1024 |
| `tool_description_bytes`                      |      16384 |
| `downstream_mcp_body_bytes`                   |    4194304 |
| `downstream_sse_event_bytes`                  |    4194304 |
| `downstream_legacy_session_id_bytes`          |        512 |
| `oauth_metadata_body_bytes`                   |    1048576 |
| `oauth_json_depth`                            |         64 |
| `oauth_response_body_bytes`                   |     262144 |
| `oauth_url_bytes`                             |       8192 |
| `oauth_query_bytes`                           |       8192 |
| `oauth_client_id_bytes`                       |       8192 |
| `oauth_client_secret_bytes`                   |       8192 |
| `oauth_scope_count`                           |         64 |
| `oauth_scope_token_bytes`                     |        256 |
| `oauth_scope_bytes`                           |       8192 |
| `stdio_protocol_frame_bytes`                  |    4194304 |
| `stdio_stderr_bytes`                          |      65536 |
| `stdio_output_rate_bytes_per_second`          |    8388608 |
| `stdio_output_burst_bytes`                    |    8388608 |
| `downstream_dispatch`                         |         32 |
| `per_server_downstream_dispatch`              |          4 |
| `s2_idempotency_records`                      |       1024 |
| `principals`                                  |        128 |
| `grants`                                      |       4096 |
| `grant_description_bytes`                     |        256 |
| `constraint_atoms`                            |         16 |
| `constraint_bytes`                            |       8192 |
| `constraint_pointer_bytes`                    |        256 |
| `constraint_regex_pattern_bytes`              |       1024 |
| `constraint_regex_program_instructions`       |       4096 |
| `constraint_regex_total_program_instructions` |        256 |
| `constraint_regex_work_bytes`                 |    1048576 |
| `invocation_audit_rows`                       |      65536 |
| `invocation_argument_capture_bytes`           |       8192 |
| `discoverable_tools`                          |       2054 |
| `grant_requests`                              |       4096 |
| `pending_grant_requests_per_principal`        |        128 |
| `grant_request_evidence_bytes`                |  268435456 |
| `grant_request_descriptor_bytes`              |     131072 |
| `grant_request_evidence_snapshot_bytes`       |     135168 |
| `grant_request_duration_seconds`              |    2592000 |
| `grant_request_target_bytes`                  |        128 |
| `agent_self_service_list_page`                |        100 |

Credential, backup, server/catalog, and principal/grant collection pages default to 50. Idempotency keys are 1–128 visible ASCII bytes. Credential expiry is five minutes through 365 days after creation. Downstream HTTP reuses the public-boundary `request_header_bytes`, `request_header_count`, and `request_header_value_bytes` bounds rather than declaring alternatives.

#### Deadlines and defaults

Fixed service deadlines are: header read five seconds, API handler 30 seconds, SQLite busy two seconds, SSE keepalive and blocked write 15 seconds, legacy idle 30 minutes, legacy absolute eight hours, graceful shutdown 10 seconds, and idempotency retention 24 hours. Server coordination adds a five-minute OAuth flow lifetime; connect/OAuth/initialization deadlines of 10/15/30 seconds; catalog page/traversal deadlines of 15/60 seconds; a maximum downstream call deadline of 60 seconds; stdio graceful/forced stop windows of 3/2 seconds; a five-minute catalog poll interval with at most 30 seconds jitter; and reconciliation retry delays of 1, 2, 4, 8, 16, 32, then 60 seconds.

### Resource representations and mechanics

`AdminCredential` is exactly `{id,fingerprint,created_at,expires_at,non_expiring,status,revision}`; its creation form adds one-time `bearer`. Credential status is the closed set `active`, `revoked`, or `expired`. `Backup` is exactly `{id,created_at,installation_id,schema_version,source_revision,size_bytes,sha256}`. Collections are exactly `{items,next_cursor}`.

`SystemStatus` is exactly `{process,sqlite,keyring,limits,backup,protocols}`. Process state is `uninitialized`, `starting`, `ready`, `storage_failed`, or `draining`; SQLite state is `uninitialized`, `ready`, or `latched`; keyring capability is `ready`, `absent`, `locked`, `interaction_required`, `unavailable`, or `unsupported`; and backup state is `idle` or `creating`.

The closed `limits` object contains `http_regular`, `http_control_auth`, `http_admin`, `http_health`, `mcp_work`, `mcp_streams`, `admin_sessions`, `legacy_sessions`, `event_streams`, `backup_work`, `backup_records`, `admin_credentials`, `idempotency_records`, `keyring_candidates`, `keyring_work`, `database_bytes`, `server_identities`, `servers`, `downstream_runtimes`, `server_reconciliations`, `catalog_traversals`, `oauth_flows`, `oauth_callback_work`, `s2_idempotency_records`, `active_tools`, `durable_tool_identities`, `downstream_dispatch`, `principals`, `grants`, `grant_requests`, and `grant_request_evidence_bytes`; every entry is exactly `{in_use,limit,saturated}`. Protocol status is modern `2026-07-28`, legacy `2025-11-25`, and agent auth is closed to `deny_all` and `principal_credentials`; production reports `principal_credentials` from the same composed dependency bundle that supplies its authenticator and discovery service.

Administrative credential and backup cursor mechanics remain limited to `GET /api/v1/admin-credentials` and `GET /api/v1/backups`; backup durable idempotency remains limited to `POST /api/v1/backups`. Credential creation accepts `AdminCredentialCreate` and returns `CreatedAdminCredential`.

Ordinary administrator credential records use no item ETag. Rotation adds bearer-only `GET /api/v1/admin-authority` with strong `"admin-authority-<revision>"` ETag, optional authority `If-Match` on credential creation, and required exact authority `If-Match` on targeted rotation completion.

No command polls, refetches a precondition, or replays a mutation. Server and policy resources add targeted snapshot or watermark cursors, idempotency, exact preconditions, and strong ETags only where listed below.

The event stream still has no replay mechanism. Browser streaming uses session-only `POST /api/v1/events` with exact `EmptyObject` `{}`, Origin, and CSRF, returning the same `EventStream`; inherited bearer-or-session GET and POST share one hub, frame, keepalive, limit, overflow, deadline, and closure owner.

Invalidation kinds are the closed set `admin_credentials`, `system_status`, `backups`, `servers`, `server_operations`, `server_auth_flows`, `catalog`, `authorization`, `invocations`, and `grant_requests`.

Admin bearer values use prefix `mgw_admin_`, reserved agent bearer values use `mgw_agent_`, and the session cookie is `mcp_gateway_session`. Approved one-time output sinks begin with `controlling_terminal` and `owner_only_file`; the latter is a newly created, non-symlink-following `0600` file containing exactly the secret and one newline. Additional server-credential write-only secret ingress declarations are `admin_credential_replacement`, `dcr_client_secret`, `authorization_code_token_response`, `refresh_response`, and `authoritative_generation_refresh_copy`. Principal credential issuance adds only `agent_credential_creation` for the one-time credential creation body. Browser control adds `browser_one_time_display` and explicit `user_initiated_clipboard`; neither ordinary browser state nor automatic clipboard publication is a sink. Standard output and standard error are not secret sinks.

### Server, catalog, and OAuth request mechanics

Administrator rotation uses `AdminAuthority` exactly `{revision}`; its revision is the maximum administrator credential revision and advances on every create, revoke, reset, or completed rotation. `AdminCredentialRotationCompletion` is exactly `{replacement_id}` and returns `AdminCredentialRotationResult` exactly `{old_credential,new_credential}` plus the resulting authority ETag. Conditional create compares the supplied authority revision before mutation. Completion rechecks that the named old credential is active, the replacement is active and non-expiring, and the authority revision is unchanged, then revokes only the named old credential in one transaction.

| Method and pattern                                   | Closed request schema      | Success schema/status                                     | Cursor | Idempotency | Exact `If-Match` | Response ETag |
| ---------------------------------------------------- | -------------------------- | --------------------------------------------------------- | ------ | ----------- | ---------------- | ------------- |
| `GET /api/v1/servers`                                | `ServerListQuery`          | `Page<Server>` / 200                                      | yes    | no          | no               | no            |
| `POST /api/v1/servers`                               | `ServerCreate`             | `ServerMutation` / 201 or replay 200                      | no     | yes         | no               | yes           |
| `GET /api/v1/servers/{id}`                           | none                       | `Server` / 200                                            | no     | no          | no               | yes           |
| `PATCH /api/v1/servers/{id}`                         | `ServerPatch`              | `ServerMutation` / 200                                    | no     | no          | yes              | yes           |
| `DELETE /api/v1/servers/{id}`                        | `EmptyObject`              | `ServerMutation` / 202 or replay 200                      | no     | no          | yes              | yes           |
| `GET /api/v1/servers/{id}/operations`                | `ServerOperationListQuery` | `Page<ServerOperation>` / 200                             | yes    | no          | no               | no            |
| `POST /api/v1/servers/{id}/operations`               | `ServerOperationCreate`    | `ServerOperationMutation` / 202 or replay 200             | no     | yes         | yes              | no            |
| `GET /api/v1/servers/{id}/operations/{operation_id}` | none                       | `ServerOperation` / 200                                   | no     | no          | no               | no            |
| `POST /api/v1/servers/{id}/credential-replacements`  | `CredentialReplacement`    | `CredentialReplacementResult` / 202                       | no     | no          | yes              | no            |
| `GET /api/v1/servers/{id}/auth-flows`                | `ServerAuthFlowListQuery`  | `Page<ServerAuthFlow>` / 200                              | yes    | no          | no               | no            |
| `POST /api/v1/servers/{id}/auth-flows`               | `EmptyObject`              | `AuthFlowCreation` / 201                                  | no     | no          | yes              | no            |
| `GET /api/v1/servers/{id}/auth-flows/{flow_id}`      | none                       | `ServerAuthFlow` / 200                                    | no     | no          | no               | no            |
| `DELETE /api/v1/servers/{id}/auth-flows/{flow_id}`   | `EmptyObject`              | empty / 204                                               | no     | no          | no               | no            |
| `GET /api/v1/catalog`                                | `CatalogListQuery`         | `CatalogPage` / 200                                       | yes    | no          | no               | no            |
| `GET /api/v1/servers/{id}/descriptors`               | `DescriptorListQuery`      | `Page<ToolDescriptor>\|Page<ToolDescriptorSummary>` / 200 | yes    | no          | no               | no            |
| `GET /api/v1/servers/{id}/descriptors/{tool_id}`     | none                       | `ToolDescriptor` / 200                                    | no     | no          | no               | no            |
| `GET /oauth/callback`                                | `OAuthCallbackQuery`       | fixed `OAuthCallbackHTML` / 200, 400, or 503              | no     | no          | no               | no            |

The executable descriptor-collection success schema is `Page<ToolDescriptor>|Page<ToolDescriptorSummary>`; the selected representation determines which closed item shape is returned. In the executable mechanics tables, `None` means no query or request body and `Empty` means an empty response body. A page is exactly `{items,next_cursor}`. List query schemas permit only their declared query members. Principal lists additionally support bounded `name`, `state`, `visibility`, `sort`, and `direction`; grant lists support `principal_id`, `server_id`, `identity`, `principal`, `target`, `effect`, `state`, `sort`, `direction`, and optional `representation=table`. The executable grant-collection schema is `Page<Grant>|Page<GrantTableItem>`; the table representation selects `Page<GrantTableItem>` with items exactly `{grant,principal_display_name,server_display_name}`; omission retains `Page<Grant>`. Both preserve their default ordering and page sizes when query options are omitted. See [Identity and Authorization](identity-and-authorization.md#principal-and-grant-contract) for exact matching, ordering, and process-bound five-minute cursor semantics. Descriptor lists permit `retired` plus the sole optional alternate representation `representation=summary`.

`ServerCreate` is exactly `{namespace,display_name,enabled,transport}`; a nonempty `ServerPatch` permits only `display_name`, `enabled`, and complete `transport`. `ServerMutation` is exactly `{server,operation}`. `ServerOperationCreate` accepts only `reload`, `retry`, `refresh_catalog`, or `disconnect_credentials`; other operation kinds are internally generated.

Credential replacement input accepts only `static_credential` or `oauth_client`; `oauth_tokens` authority comes only from validated OAuth responses. The strong ETag is exactly `"server-<id>-<desired_revision>"`; weak, wildcard, malformed, multiple, absent where required, or noncurrent preconditions are not interchangeable.

Server idempotency is scoped to parent admin credential, method, route, key, canonical validated/defaulted request, and exact precondition, with a 24-hour lifetime and 1,024-record bound.

### Server and catalog vocabulary

`Server` is exactly `{id,namespace,display_name,desired_state,desired_revision,transport,credential_revisions,credential_state,runtime,catalog,created_at,updated_at,deleted_at}`. `credential_revisions` is exactly `{static_credential,oauth_client,oauth_tokens}`. Runtime is exactly `{state,reason,runtime_id,reconciliation,dispatch}` and catalog is exactly `{durable_state,active_state,durable_revision,active_revision,durable_tool_count,active_tool_count,last_success_at,traversal}`; each occupancy uses `LimitStatus`.

The sanitized transport union is closed to stdio `{kind,executable,arguments,working_directory,environment,secret_environment}` and Streamable HTTP `{kind,url,protocol_mode,authentication}`. HTTP authentication is exactly `{mode:none}`, `{mode:bearer}`, or OAuth `{mode,registration,trusted_origins,request_offline_access}`. Registration is static `{mode,issuer,client_id,token_endpoint_auth_method}` or dynamic `{mode,issuer}`. Credential replacement is static `{kind,expected_revision,values}` or OAuth client `{kind,expected_revision,client_secret}`.

`ServerOperation` is exactly `{id,server_id,kind,target_desired_revision,target_credential_revisions,state,reason,created_at,started_at,finished_at}`. `ServerAuthFlow` is exactly `{id,server_id,flow_state,target_desired_revision,registration_revision,created_at,expires_at,finished_at,reason,diagnostic}`. Its diagnostic is null or exactly `{correlation_id,stage,reason,http_status}` with correlation ID equal to the flow ID, a closed diagnostic stage, the same stable public reason as the failed flow, and a null or bounded HTTP status. `ToolDescriptor` is exactly `{id,server_id,upstream_name,external_name,descriptor,fingerprint,catalog_revision,first_seen_at,last_seen_at,retired_at}`. Descriptor collection queries accept only the ordinary pagination and retired-state fields plus optional `representation=summary`; that representation returns items exactly `{id,server_id,upstream_name,external_name,catalog_revision}` so searchable selectors do not transfer descriptor schemas before selection, while omission returns full `ToolDescriptor` resources. `CatalogToolDescriptor` contains those exact fields plus snapshot-consistent `server_display_name` and `server_catalog_state`. `CatalogPage` is exactly `{catalog,items,next_cursor}`, where catalog is exactly `{active_state,active_generation,changed_at,issue_count}` and items are `CatalogToolDescriptor` resources. `CredentialReplacementResult` is exactly `{server_id,kind,credential_revision,operation}` and `AuthFlowCreation` is exactly `{flow,authorization_url}`.

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

## Strict JSON

`internal/strictjson` is dependency-neutral and is the parser used by `internal/api`. Callers supply positive byte and depth limits and decide whether their destination is closed. Parsing rejects invalid UTF-8, duplicate object members (including escape-equivalent names), excess size or depth, trailing values, and unknown members for closed destinations. Canonical equality ignores object-member order and equivalent JSON number spellings while preserving array order. This primitive performs no server, OAuth, protocol, or catalog traversal behavior.
