# Public Contract

## HTTP traffic history

Administrator bearer/session bodyless GET reads use `/api/v2/http/traffic` and
`/api/v2/http/traffic/{id}`, independently of MCP invocations. Responses are
no-store; there is no mutation, replay or traffic-to-grant operation. Collection
`HTTPTrafficQuery` accepts singleton nonempty `limit` (default 50, canonical 1–100), `cursor`,
`principal_id` (exact), `destination` (exact canonical hostname/IP), `type`
(`request`, `connect`, `invalid`), `decision` (`allow`, `block`, `intercept`,
`invalid`), and `outcome` (`not_dispatched`, `outcome_unknown`, `succeeded`,
`prestart_failure`, `upstream_failure`). Unknown, duplicate or malformed values
fail. All filters combine before descending shared sequence pagination; indexed
stored query facts avoid loading policy evidence to select a page.

`HTTPTrafficPage` is exactly `{items,next_cursor}`. Each summary is
`{id,admitted_at,principal_id,target,type,decision,outcome}`; target is null for
unparseable requests, `{host,port}` for CONNECT, or `{host,port,scheme,method}`
for a request. No observed path exists. Item `HTTPTrafficRecord` is exactly
`{admission,completion}` using the executable bounded evidence contract; only
items include historical matched selectors and material-generation references.
No current-resource lookup reconstructs their meaning. Missing completion is
unknown for an allow, including confirmation failure, not permission to replay.

HTTP cursors use a distinct authenticated version with the shared process-local
key, binding complete filters, generation, shared pruning, upper and next sequence.
They are at most 512 bytes. Later insertions are excluded; any shared pruning or
generation replacement invalidates continuation. Like MCP, completion predicates
are coherent per page, not a frozen terminal snapshot; counts describe loaded
matches, not totals. Traffic reader capacity/deadline and complete startup
validation remain shared. HTTP UI refresh uses the existing bounded/coalesced
System traffic invalidation, not per-record fetches or a new stream owner.

## HTTP grant resources

| Route                         | Allow                |
| ----------------------------- | -------------------- |
| `/api/v2/http/grants`         | `GET, POST`          |
| `/api/v2/http/grants/{id}`    | `DELETE, GET, PATCH` |
| `/api/v2/http/access-preview` | `POST`               |

All use administrator bearer/session authority, closed bounded JSON, no-store responses and no replay/idempotency. `HTTPGrantWrite` requires exactly `principal_id`, nullable `description`, `policy` and nullable `expires_at`. POST returns `HTTPGrant` / 201; PATCH atomically replaces that complete configuration / 200, retaining ID and principal. GET returns the same resource / 200. DELETE is bodyless and returns `Empty` / 204. PATCH and DELETE require exact `"http-grant-ID-REVISION"` ETags; GET/create/update return them. Missing and stale preconditions use `grant_precondition_required` and `stale_grant_revision`; invalid policy uses `invalid_grant`, incompatible references use `conflict`.

`HTTPGrant` is exactly `{id,principal_id,description,revision,policy,expires_at,state,created_at,updated_at}`. Revisions are positive decimal strings; policy version remains immutable integer 1. State is active/expired; response timestamps use fixed UTC nanoseconds (`2026-09-21T00:00:00.000000000Z`), with updated time no earlier than creation and non-null expiry later than creation. `HTTPGrantListQuery` accepts singleton nonempty `cursor`, canonical `limit` (default 50, maximum 100), `principal_id`, `identity`, `principal`, `target`, `type`, `state`, `sort`, `direction`. Text filters and ordering reuse MCP grant recognition rules; target means destination hostname. Type uses the four HTTP types. Sort is id/description/principal/target/effect/state, where effect sorts the explicit HTTP type. Default is description ascending, ID ascending ties. Direction requires sort. `QueryPage<HTTPGrantTableItem>` has the existing count/offset envelope; items are exactly `{grant,principal_display_name}`. HMAC snapshot cursors bind HTTP representation, query, metadata, principal labels and expiry state for five minutes, never policy bodies.

HTTP default is the required `http_default:"block"|"allow"` member of every canonical `Principal` representation. Read `/api/v2/principals/{id}` and PATCH any nonempty subset of `display_name`, `state`, `visibility`, and `http_default` with its exact `"principal-ID-REVISION"` ETag. Omitted members remain unchanged; null and unknown values reject. Missing/stale preconditions use `principal_precondition_required`/`stale_principal_revision`; invalid settings use `invalid_principal`. One authorization-owned transaction commits all settings, revisions and required audit or none. The dedicated `/api/v2/http/defaults/{id}` GET/PATCH resource and its ETag are removed, without compatibility aliases or redirects. Clients must upgrade their strict Principal decoders and use the unified principal precondition; never reuse an old default-resource ETag. HTTP policy evaluation remains separate from MCP authority.

`HTTPAccessInput` is exactly `{principal_id,url,method}` or `{principal_id,connect:{host,port}}`; members of the other branch, including null, reject. POST returns `HTTPAccessPreview`, exactly `{decision,default,policy_only,network_verified,tls_verified,material_verified,admission_authority}`. Only policy_only is true among the Boolean qualification fields. It shares production policy selection, uses known literal address classification without DNS, and returns only safe revision/reference/reason evidence. Request paths/query values are neither echoed nor retained in audit, events or diagnostics. No dispatch, secret resolution, permission lease or future admission authority is created.

## HTTP credential resources

| Route                                  | Allow                |
| -------------------------------------- | -------------------- |
| `/api/v2/http/credentials`             | `GET, POST`          |
| `/api/v2/http/credentials/{id}`        | `DELETE, GET, PATCH` |
| `/api/v2/http/credentials/{id}/rotate` | `POST`               |

All use existing administrator bearer/session authority, strict bounded JSON, no-store responses, and no idempotency or automatic replay. GET collection uses `HTTPCredentialListQuery`, accepting only `cursor` and `limit` (default 50, maximum 100), in creation-descending order, returning `QueryPage<HTTPCredential>` (including `total_count` and `offset`). Item GET is bodyless and queryless. `HTTPCredential` contains `id`, `name`, `boundary:{host,port,allow_wildcard}`, `recipe:{header,prefix}`, decimal-string `revision`, `available`, `referencing_grants:[{id}]`, `created_at`, and `updated_at`. Availability is coherent selected-generation metadata, not a guarantee of later keyring access. No secret or keyring handle is returned.

`HTTPCredentialCreate` requires `name`, `boundary`, `recipe`, and write-only `secret`; POST returns 201 plus the safe resource. PATCH `HTTPCredentialUpdate` is a complete secret-free metadata replacement requiring `name`, `boundary`, and `recipe`; it returns 200. POST rotate accepts only `HTTPCredentialRotate` with `{secret}` and returns 200. DELETE accepts `EmptyObject` and returns 204. Every mutation except create requires exact strong `If-Match: "http-credential-ID-REVISION"`; missing/stale preconditions use `precondition_required`/`stale_revision`. Resource reads and successful create/update/rotate return that ETag. Incompatible references use `conflict`; invalid recipes use `invalid_operation`, never reflected input. Secret ingress is bounded by the recipe and standard JSON body bounds; the browser validates those bounds before confirmation and clears rejected write-only input. Keyring capability/material failures use `keyring_unavailable`, not a storage latch; a joined actual storage latch retains `storage_unavailable` precedence. The [credential owner](downstream-servers.md#scoped-http-credentials) defines validation, lifecycle, containment, and failure behavior.

Audience: Maintainers and contributors changing the public HTTP and data contract

Authority: Normative product design

This chapter owns the behavior and invariants described below. Operational procedures remain in the linked guides; exact executable contract values remain owned by `internal/contract` and must agree with this chapter.

## HTTP and data contract

The `internal/contract` package is the single executable source consumed by API, ingress, authorization, discovery, and composition implementations. Its returned tables are copies so callers cannot mutate the canonical contract. The tables and mechanics below document the corresponding normative closed vocabulary for principal-credential authentication, administration, discovery, and invocation. The default authority is `127.0.0.1:8210`, its canonical Origin is `http://127.0.0.1:8210`, the supported protocol versions are modern `2026-07-28` and legacy `2025-11-25`, and the media types are `application/json`, `application/problem+json`, and `text/event-stream`.

`NormalizeHostname` owns the ASCII DNS hostname grammar shared by startup Host configuration and explicit CLI destinations. Early Host classification admits only the canonical numeric listener authority or an explicitly configured `--allowed-host` hostname; the latter ignores an absent or valid nonzero decimal request port. Matching folds ASCII case only and never consults DNS. This does not alter route/method authority, the exact numeric browser Origin, or OAuth callback construction. See [HTTP administration](administrative-control-plane.md#http-administration) for validation and security semantics.

### HTTP policy dialect

`HTTPPolicy`, `HTTPDecision` and the `HTTPPolicy*`, `HTTPMethodBytes`,
`HTTPHostBytes`, `HTTPPathBytes`, `HTTPTargetBytes` and `HTTPAddressFacts`
constants in `internal/contract/http_policy.go` define the immutable HTTP v1
contract. The [identity chapter](identity-and-authorization.md#http-policy-version-1)
owns its closed shapes, bounds, canonicalization, precedence and safe evidence.
HTTP grant/default resources above persist this dialect without adding System limit occupancy or a second authentication domain. Policy dialect version is distinct from resource revision. Existing principal and MCP contracts are unchanged.

### Diagnostic event contract

`DiagnosticEvents`, `DiagnosticLevels`, and the `DiagnosticQueueRecords`, `DiagnosticRecordBytes`, and `DiagnosticFlushDeadline` constants own the version-one serve diagnostic inventory and bounds. Each JSON diagnostic has `schema_version`, UTC `time`, `level`, fixed `event`, and a generated `process_id`; only its event-specific typed subset may accompany these. Each definition separates required and optional fields and lists exact causes, durability stages, and conditional requirements. Execution start/result and terminal annotation require both call and acknowledged invocation IDs; successful admission requires an invocation ID, whereas unavailable/stopped pre-ack admission forbids it. Authority/storage observations require mutation IDs and the exact owner limit; non-foreign storage writers additionally require call correlation. Wait events have no cause or duration, and durability/latch events require a closed nonempty stage. Invalid and unknown data is omitted by dropping the record, never generic serialization or regex-only redaction. The [administrative control-plane chapter](administrative-control-plane.md#serve-diagnostics) owns delivery and lifecycle. These lossy stderr observations do not add HTTP/MCP fields, change public problem representations, or extend durable audit schemas.

### Route ownership

All administrative routes use v2. No v1 route or alias is owned; retired paths return 404 before authentication or mutation work. `/mcp` and OAuth callback identities are unchanged.

Methods are lexicographically ordered and become the exact `Allow` value. `HEAD` is never inherited from `GET`.

The main callback remains unchanged. A configured per-flow callback-only numeric-loopback listener admits only its exact configured path, `GET`, and exact URI authority; it has no other public route. Main hostname allowlisting and browser Origin authority are independent. Collisions fail before URL publication; temporary listeners are composition-owned and released on terminal/cancellation/expiry/supersession/shutdown paths.

| Pattern                                              | Exact `Allow`        | Authority                                         |
| ---------------------------------------------------- | -------------------- | ------------------------------------------------- |
| `/`                                                  | `GET`                | public                                            |
| `/assets/*`                                          | `GET`                | public                                            |
| `/livez`                                             | `GET`                | public                                            |
| `/readyz`                                            | `GET`                | public                                            |
| `/mcp`                                               | `DELETE, GET, POST`  | agent                                             |
| `/oauth/callback`                                    | `GET`                | one-time OAuth state                              |
| `/api/v2/admin-sessions`                             | `POST`               | admin bearer                                      |
| `/api/v2/admin-sessions/current`                     | `DELETE, POST`       | admin session                                     |
| `/api/v2/admin-credentials`                          | `GET, POST`          | admin bearer or session                           |
| `/api/v2/admin-credentials/{id}`                     | `DELETE, GET`        | admin bearer or session                           |
| `/api/v2/admin-authority`                            | `GET`                | admin bearer                                      |
| `/api/v2/admin-credentials/{id}/rotation-completion` | `POST`               | admin bearer                                      |
| `/api/v2/system-status`                              | `GET`                | admin bearer or session                           |
| `/api/v2/backups`                                    | `GET, POST`          | admin bearer or session                           |
| `/api/v2/backups/{id}`                               | `DELETE, GET`        | admin bearer or session                           |
| `/api/v2/events`                                     | `GET, POST`          | GET: admin bearer or session; POST: admin session |
| `/api/v2/mcp/servers`                                | `GET, POST`          | admin bearer or session                           |
| `/api/v2/mcp/servers/{id}`                           | `DELETE, GET, PATCH` | admin bearer or session                           |
| `/api/v2/mcp/servers/{id}/operations`                | `GET, POST`          | admin bearer or session                           |
| `/api/v2/mcp/servers/{id}/operations/{operation_id}` | `GET`                | admin bearer or session                           |
| `/api/v2/mcp/servers/{id}/credential-replacements`   | `POST`               | admin bearer or session                           |
| `/api/v2/mcp/servers/{id}/oauth-flows`               | `GET, POST`          | admin bearer or session                           |
| `/api/v2/mcp/servers/{id}/oauth-flows/{flow_id}`     | `DELETE, GET`        | admin bearer or session                           |
| `/api/v2/mcp/catalog`                                | `GET`                | admin bearer or session                           |
| `/api/v2/mcp/servers/{id}/descriptors`               | `GET`                | admin bearer or session                           |
| `/api/v2/mcp/servers/{id}/descriptors/{tool_id}`     | `GET`                | admin bearer or session                           |
| `/api/v2/principals`                                 | `GET, POST`          | admin bearer or session                           |
| `/api/v2/principals/{id}`                            | `GET, PATCH`         | admin bearer or session                           |
| `/api/v2/principals/{id}/credential`                 | `DELETE, POST`       | admin bearer or session                           |
| `/api/v2/mcp/grants`                                 | `GET, POST`          | admin bearer or session                           |
| `/api/v2/mcp/grants/{id}`                            | `DELETE, GET, PATCH` | admin bearer or session                           |
| `/api/v2/mcp/grant-constraints/validate`             | `POST`               | admin bearer or session                           |
| `/api/v2/mcp/grant-requests`                         | `GET`                | admin bearer or session                           |
| `/api/v2/mcp/grant-requests/{id}`                    | `GET`                | admin bearer or session                           |
| `/api/v2/mcp/grant-requests/{id}/approve`            | `POST`               | admin bearer or session                           |
| `/api/v2/mcp/grant-requests/{id}/reject`             | `POST`               | admin bearer or session                           |
| `/api/v2/mcp/invocations`                            | `GET`                | admin bearer or session                           |
| `/api/v2/mcp/invocations/{id}`                       | `GET`                | admin bearer or session                           |
| `/api/v2/audit-events`                               | `GET`                | admin bearer or session                           |
| `/api/v2/audit-events/{id}`                          | `GET`                | admin bearer or session                           |

`/assets/*` requires a nonempty path below `/assets/`. Item patterns require exactly one nonempty segment. All other paths are unowned and therefore `404`.

MCP invocation reads have moved from `/api/v2/invocations` and its item resource to `/api/v2/mcp/invocations`. Retired paths are unowned: no alias, redirect, fallback request or replay. The CLI uses `mcp invocation list/get`; the browser uses `#/mcp/invocations`. This API v2 namespace cutover preserves all public projections, filters, read mechanics and historical evidence without storage migration or rewriting IDs, credentials or backup lineage. Administrative audit remains shared at `/api/v2/audit-events`, `audit` CLI and `#/audit-log`, labeled **Audit Log** without changing attribution or filters. See the [operator cutover](../operators/administration.md#mcp-invocation-namespace-cutover) for coordinated upgrade/reload and safe recovery.

The invocation-read mechanics are `InvocationListQuery` → `InvocationPage` for the collection and `None` → `Invocation` for an item. The invocation read service composes the sole invocation repository with the authorization-owned separately snapshotted principal-name reader; it introduces no mutation, replay, event, or mutable join into retained evidence projections.

### Invocation history queries

Invocation collections accept the existing singleton `limit` (default 50, 1–100), `cursor`, exact `principal_id`, `server_id`, `requested_name`, `admission_class`, `decision`, and `outcome` fields, plus optional `tool`, `principal`, and `search_locale`. Unknown, repeated, empty, invalid, or oversized members fail. Text filters contain at most 256 UTF-8 bytes without control/format characters. `search_locale` is a canonical language tag of at most 64 bytes; omission uses locale-independent casing. The browser supplies its current default locale. `decision=not_evaluated` selects null authorization evidence without adding a stored authorization decision. Every existing outcome remains selectable, including both explicit and missing-terminal `outcome_unknown`.

Filters compose with AND and select from all retained history before pagination. Tool recognition uses recorded requested name, otherwise recorded upstream name (prefixed `mcp_gateway.` for local targets), otherwise `Not resolved`. It never depends on current catalog visibility. Principal recognition uses a case-sensitive literal substring of the recorded principal ID OR the current display name; renamed names are not admission-time evidence, missing current names do not remove recorded identities, and a failed current-name read fails the query rather than returning partial matches.

Name search preserves collection fuzzy rules: trim, NFKD normalize, remove Unicode marks, then lowercase for the selected locale. Every whitespace-delimited query token must match a substring or, for tokens of at least four UTF-16 units without ASCII digits, a Unicode letter/number word within one insertion, deletion, substitution, or adjacent transposition. Literal ID matching uses the whole trimmed query without normalization.

Exact predicates stay in invocation-owned SQL. Text selection streams compact recognition fields in descending insertion order within the captured watermark, bounded by the selected traffic retained capacity (at most 1,000,000) plus an overflow probe and a one-second reader lifetime. It stops after `limit + 1` matches and hydrates only those summaries in one bounded statement in the same storage view. Principal names are read once under their existing 128-principal bound. No argument captures, whole-history browser traversal, catalog enumeration, per-row resource lookup, participates in name recognition; invocation evidence is read only from selected traffic. Cancellation remains active throughout the read.

Invocation cursors are bounded to 512 bytes. Version-three cursors authenticate the query digest, current-name digest when used, traffic generation/pruning count, upper/next sequence, and repository-process epoch with a process-local HMAC key; query bytes and name inventories are not serialized into the cursor. Changed queries/names, generation/pruning changes (including non-prefix holes), a different process epoch, old cursor versions, or a retention floor beyond the continuation return `409 stale_cursor`; malformed or tampered current-process cursors return `400 invalid_cursor`. Fresh reads capture a new traversal. Later insertions never enter a continuation; terminal annotations retain the existing page-coherent entry/exit behavior for outcome predicates, not a frozen outcome snapshot. Counts are loaded matches, not totals. Item/list projections remain unchanged, immutable evidence stays distinct from current recognition names, and restart/restore cannot resume an old cursor.

The non-mutating grant matcher validation mechanic is `GrantConstraintValidation` → `GrantConstraintValidationResult`. The request is exactly `{constraint}`, where `constraint` must be a non-null object; missing, null, or non-object members are malformed requests. A well-formed request always returns `200`: valid constraints return an empty diagnostics array, while compiler-invalid constraint objects return one safe `GrantConstraintDiagnostic` with a JSON Pointer field and fixed message. The endpoint uses the production compiler, writes no durable state, publishes no invalidation, and grant creation and request approval still compile authoritatively in their own mutation paths.

`GrantCreate` additionally accepts optional Boolean `read_only`; request and approval `Policy` accept the same field. Omitted or false retains unrestricted behavior; null and non-Booleans are invalid. True is restricted to server-wide ALLOWs/server requests with null argument constraints, never DENY or exact-tool policy. `Grant` and agent `GrantPolicy` reads emit `read_only:true` only for restricted grants; requested/approved policies preserve it on administrator and agent reads. Legacy unrestricted representations are unchanged. The [read-only policy contract](identity-and-authorization.md#read-only-server-allows) owns descriptor trust, lifecycle, dedupe compatibility, and the approval matrix.

### Independent traffic status

The existing System/status representation adds optional `traffic` metadata:
`ready`, `faulted`, `pressure`, `budget_bytes`, `database_bytes`, `wal_bytes`,
`quota_refusals`, `pruned_records`, `generation`, `rolling_history` and
`unknown_completion_possible`. The last two are true: neither a missing row nor
missing completion proves nonexecution. This is storage posture, not dispatch
authority. Control readiness and latch remain independent; a traffic-only fault
does not globally disable healthy administrative mutations. History item/list
representations, routes, IDs, filters and one-shot CLI behavior remain unchanged.

### Optional HTTP proxy status

`http_proxy` is optional for older status producers and present in production.
Its closed fields are `enabled`, `ready`, `ca_ready`, `connections`, `work`,
`active_streams`, `active_tunnels`, and `authority` only when enabled. Occupancy
objects use `{in_use,limit,saturated}`; counters are nonnegative. Disabled HTTP
reports false readiness and zero occupancy. `ca_ready` attests loaded process-local
signing capability and certificate validity, not native persistence or client trust.
Readiness also requires healthy traffic/control and open lifecycle admission.
No secret, request destination, path or principal identity appears in this status.

### Control-plane audit reads

`GET /api/v2/audit-events` and `GET /api/v2/audit-events/{id}` accept administrator bearer or session authority, remain bodyless and `no-store`, and have exact `Allow` value `GET`. There are no audit mutation, replay, export, or ordinary read-access-log endpoints.

`AuditListQuery` accepts only `limit` (default 50, canonical decimal 1–100), `cursor`, `generation`, `actor_type`, `credential_id`, `category`, `action`, `target_type`, `target_id`, `outcome`, `correlation_id`, `from`, and `until`. `AuditItemQuery` accepts only optional `generation`. Unknown, repeated, empty, malformed, or out-of-range members fail. Filters are conjunctive and authoritative; `credential_id` matches either the performing operator credential or a known system initiator. `from` and `until` must occur together, use fixed UTC nanosecond timestamps (`2026-09-05T00:00:00.000000000Z`), and define a nonempty half-open range of at most 366 days.

`AuditPage` is exactly `{items,next_cursor,history}`. Items are `AuditSummary`: `{id,sequence,timestamp,category,action,phase,outcome,actor,initiator,correlation_id,target}`. The sequence is a positive decimal string, not a JSON number. `AuditItem` is exactly `{event,history}`; its event adds `detail`, exactly `{reason,problem}`, each null or a closed public reason/problem code. No member accepts free text, unrestricted snapshots, invocation arguments/results, raw credentials, or raw errors. An actor is exactly `{type,credential}`, with type `operator`, `system`, or `offline_maintenance`. Only an operator has a nonnull credential, exactly `{id,fingerprint}`. A nonnull initiator has that same credential shape and is permitted only on system events. Targets are exactly `{type,id}` with canonical durable IDs. No human identity is inferred. `phase` is `attempt` with `outcome=pending`, or `outcome` with one of `succeeded`, `rejected`, `failed`, and `unknown`; attempts never carry outcome detail.

History is exactly `{generation,oldest_retained,pruned}`. The opaque generation is 64 lowercase hexadecimal characters. The oldest boundary is null for empty history, otherwise exactly `{id,sequence,timestamp}`. Retention keeps the newest 65,536 events, changes the oldest boundary, and sets `pruned` without changing the generation. Consumers retain and compare the generation separately from that boundary and must not combine different generations. Explicit mismatches return `409 audit_history_replaced`: The audit history generation has changed.

Collection order is descending sequence. A cursor is an opaque, authenticated, filter-bound traversal with an upper sequence watermark; later appends do not enter that traversal. Any pruning since its first page makes it `409 stale_cursor`, even if the pruned records would not match the filters. A different process epoch also makes cursors stale; changed filters, invalid encoding, or failed authentication of a current-process cursor produces `400 invalid_cursor`. On a stale cursor, discard the traversal and fetch a fresh first page, then compare its generation before combining results. A changed generation requires discarding previous-history state; pruning within the same generation does not mean replacement. Item reads can pin `generation` to avoid ID reuse across incompatible histories. Cursor bytes are bounded to 2,048; canonical persisted event JSON is bounded to 2,048 bytes and is strictly revalidated before projection.

The read/store backend does not itself establish producer coverage or restore continuity. Their qualification is owned by the [administrative audit coverage matrix](administrative-control-plane.md#control-plane-audit-coverage).

### Safe problems

Problems normally have exactly `status`, `code`, and `title`. The `invalid_server_configuration` problem additionally has one required `context` object with exact `field` and `rule` members so every administrative client can identify the rejected configuration boundary. Both values come from closed vocabularies: fields are `configuration`, `namespace`, `display_name`, `enabled`, `transport`, `transport.kind`, `transport.executable`, `transport.arguments`, `transport.working_directory`, `transport.environment`, `transport.secret_environment`, `transport.url`, `transport.protocol_mode`, `transport.headers`, `transport.authentication`, `transport.authentication.mode`, `transport.authentication.trusted_origins`, `transport.authentication.request_offline_access`, `transport.authentication.registration`, `transport.authentication.registration.mode`, `transport.authentication.registration.issuer`, `transport.authentication.registration.client_id`, and `transport.authentication.registration.token_endpoint_auth_method`; rules are `invalid`, `required`, `maximum`, `unique`, `disjoint`, `canonical_absolute_path`, `canonical_url`, and `transport_policy`. Only one deterministic first violation is returned. Dependency messages, submitted values, paths, payloads, dynamic map keys, array positions, and other details are never added.

| Status | Code                                    | Fixed title                                                                                 |
| -----: | --------------------------------------- | ------------------------------------------------------------------------------------------- |
|    400 | `malformed_request`                     | The request is invalid.                                                                     |
|    400 | `invalid_json`                          | The JSON body is invalid.                                                                   |
|    400 | `invalid_cursor`                        | The cursor is invalid.                                                                      |
|    400 | `invalid_idempotency_key`               | The idempotency key is invalid.                                                             |
|    400 | `ambiguous_credentials`                 | Multiple credential types were supplied.                                                    |
|    400 | `invalid_oauth_state`                   | The OAuth state is invalid or expired.                                                      |
|    401 | `authentication_required`               | Authentication is required.                                                                 |
|    403 | `credential_domain_mismatch`            | The credential is for a different authority.                                                |
|    403 | `forbidden_origin`                      | The Origin is not accepted.                                                                 |
|    403 | `csrf_failed`                           | CSRF validation failed.                                                                     |
|    404 | `not_found`                             | The resource was not found.                                                                 |
|    405 | `method_not_allowed`                    | The method is not allowed.                                                                  |
|    409 | `conflict`                              | The request conflicts with current state.                                                   |
|    409 | `idempotency_conflict`                  | The idempotency key conflicts with prior work.                                              |
|    409 | `admin_rotation_conflict`               | The administrator credential rotation conflicts with current state.                         |
|    412 | `stale_admin_authority`                 | The administrator authority revision is stale.                                              |
|    428 | `admin_authority_precondition_required` | The administrator authority revision is required.                                           |
|    413 | `body_too_large`                        | The request body is too large.                                                              |
|    415 | `unsupported_media_type`                | The media type is not supported.                                                            |
|    421 | `misdirected_request`                   | The Host is not accepted.                                                                   |
|    429 | `resource_limit`                        | The resource limit is reached.                                                              |
|    503 | `storage_unavailable`                   | Storage is unavailable.                                                                     |
|    503 | `keyring_unavailable`                   | The credential provider is unavailable.                                                     |
|    503 | `shutting_down`                         | The service is shutting down.                                                               |
|    400 | `invalid_server_configuration`          | The server configuration is invalid.                                                        |
|    400 | `invalid_operation`                     | The server operation is invalid.                                                            |
|    409 | `namespace_unavailable`                 | The server namespace is unavailable.                                                        |
|    409 | `operation_conflict`                    | The server has conflicting work.                                                            |
|    409 | `oauth_flow_active`                     | The OAuth flow is already exchanging.                                                       |
|    409 | `oauth_callback_unavailable`            | The OAuth callback port is unavailable. Stop the conflicting listener and start a new flow. |
|    409 | `stale_cursor`                          | The cursor snapshot is no longer available.                                                 |
|    409 | `audit_history_replaced`                | The audit history generation has changed.                                                   |
|    412 | `stale_revision`                        | The server revision is stale.                                                               |
|    428 | `precondition_required`                 | The current server revision is required.                                                    |
|    503 | `downstream_unavailable`                | The downstream server is unavailable.                                                       |
|    400 | `invalid_principal`                     | The principal is invalid.                                                                   |
|    400 | `invalid_grant`                         | The grant is invalid.                                                                       |
|    412 | `stale_grant_revision`                  | The grant revision is stale.                                                                |
|    428 | `grant_precondition_required`           | The current grant revision is required.                                                     |
|    412 | `stale_principal_revision`              | The principal revision is stale.                                                            |
|    428 | `principal_precondition_required`       | The current principal revision is required.                                                 |
|    503 | `authorization_unavailable`             | Authorization is unavailable.                                                               |
|    400 | `invalid_grant_request`                 | The grant request is invalid.                                                               |
|    409 | `grant_request_conflict`                | The grant request conflicts with current state.                                             |
|    412 | `stale_grant_request_revision`          | The grant request revision is stale.                                                        |
|    428 | `grant_request_precondition_required`   | The current grant request revision is required.                                             |

### Fixed numeric limits

Every maximum accepts N and rejects N+1. Values below zero are invalid. These are compiled boundaries, not configuration.

| Contract name                                 |    Maximum |
| --------------------------------------------- | ---------: |
| `request_target_bytes`                        |       8192 |
| `request_header_bytes`                        |      32768 |
| `request_header_count`                        |        100 |
| `request_header_value_bytes`                  |       8192 |
| `upstream_header_count`                       |         16 |
| `upstream_header_name_bytes`                  |        128 |
| `upstream_header_value_bytes`                 |       4096 |
| `upstream_header_bytes`                       |       8192 |
| `api_json_body_bytes`                         |    1048576 |
| `mcp_body_bytes`                              |    4194304 |
| `json_depth`                                  |         64 |
| `http_regular`                                |        128 |
| `http_control_auth`                           |         32 |
| `http_admin`                                  |         16 |
| `http_health`                                 |          8 |
| `authority_work`                              |         32 |
| `invocation_mutation_waiters`                 |         31 |
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
| `server_idempotency_records`                  |       1024 |
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
| `invocation_failure_diagnostic_bytes`         |        512 |
| `discoverable_tools`                          |       2054 |
| `grant_requests`                              |       4096 |
| `pending_grant_requests_per_principal`        |        128 |
| `grant_request_evidence_bytes`                |  268435456 |
| `grant_request_descriptor_bytes`              |     131072 |
| `grant_request_evidence_snapshot_bytes`       |     135168 |
| `grant_request_duration_seconds`              |    2592000 |
| `grant_request_target_bytes`                  |        128 |
| `agent_self_service_list_page`                |        100 |
| `control_audit_events`                        |      65536 |
| `control_audit_page`                          |        100 |
| `control_audit_event_bytes`                   |       2048 |
| `control_audit_cursor_bytes`                  |       2048 |

Credential, backup, server/catalog, and principal/grant collection pages default to 50. Idempotency keys are 1–128 visible ASCII bytes. Credential expiry is five minutes through 365 days after creation. Downstream HTTP reuses the public-boundary `request_header_bytes`, `request_header_count`, and `request_header_value_bytes` bounds rather than declaring alternatives.

#### Deadlines and defaults

Fixed service deadlines are: header read five seconds, API handler 30 seconds, SQLite busy two seconds, authority gate wait one second (within 32 outstanding authority operations, shortened by caller cancellation/deadline), invocation storage acquisition wait 250 ms (one active storage owner and up to 31 FIFO invocation waiters, shortened by caller cancellation/deadline or invocation drain), SSE keepalive and blocked write 15 seconds, legacy idle 30 minutes, legacy absolute eight hours, graceful shutdown 10 seconds, and idempotency retention 24 hours. Server coordination adds a five-minute OAuth flow lifetime; connect/OAuth/initialization deadlines of 10/15/30 seconds; catalog page/traversal deadlines of 15/60 seconds; a maximum downstream call deadline of 60 seconds; stdio graceful/forced stop windows of 3/2 seconds; a five-minute catalog poll interval with at most 30 seconds jitter; and reconciliation retry delays of 1, 2, 4, 8, 16, 32, then 60 seconds.

The invocation-storage deadline bounds acquisition only, before intent/SQL, not active mutation settlement. Full waiting capacity rejects immediately; foreign ordinary and recovery-bearing writers remain nonqueueing even during reserved FIFO handoff. Admission still waits after acquiring authority, so contention may delay authentication and policy changes. The internal capacity, expiry, cancellation/drain, and latch distinctions do not add public MCP errors: unacknowledged admission remains `audit_unavailable` without an invocation ID, and best-effort terminal failure cannot replace the live result. No fairness guarantee applies to nonqueueing foreign writers.

### Resource representations and mechanics

`AdminCredential` is exactly `{id,fingerprint,created_at,expires_at,non_expiring,status,revision}`; its creation form adds one-time `bearer`. Credential status is the closed set `active`, `revoked`, or `expired`. `Backup` is exactly `{id,created_at,installation_id,schema_version,source_revision,size_bytes,sha256}`. Collection envelopes and defaults are declared in the normalized collection contract below; filter presence never changes them.

`SystemStatus` is exactly `{process,sqlite,keyring,limits,backup,protocols}`. Process state is `uninitialized`, `starting`, `ready`, `storage_failed`, or `draining`; SQLite state is `uninitialized`, `ready`, or `latched`; keyring capability is `ready`, `absent`, `locked`, `interaction_required`, `unavailable`, or `unsupported`; and backup state is `idle` or `creating`.

The closed `limits` object contains `http_regular`, `http_control_auth`, `http_admin`, `http_health`, `mcp_work`, `mcp_streams`, `admin_sessions`, `legacy_sessions`, `event_streams`, `backup_work`, `backup_records`, `admin_credentials`, `idempotency_records`, `keyring_candidates`, `keyring_work`, `database_bytes`, `server_identities`, `servers`, `downstream_runtimes`, `server_reconciliations`, `catalog_traversals`, `oauth_flows`, `oauth_callback_work`, `server_idempotency_records`, `active_tools`, `durable_tool_identities`, `downstream_dispatch`, `principals`, `grants`, `grant_requests`, and `grant_request_evidence_bytes`; every entry is exactly `{in_use,limit,saturated}`. Protocol status is modern `2026-07-28`, legacy `2025-11-25`, and agent auth is closed to `deny_all` and `principal_credentials`; production reports `principal_credentials` from the same composed dependency bundle that supplies its authenticator and discovery service.

Administrative credential and backup cursor mechanics remain limited to `GET /api/v2/admin-credentials` and `GET /api/v2/backups`; backup durable idempotency remains limited to `POST /api/v2/backups`. Their request schemas are `AdminCredentialListQuery` and `BackupListQuery`, returning `Page<AdminCredential>` and `Page<Backup>`. Credential creation accepts `AdminCredentialCreate` and returns `CreatedAdminCredential`.

Ordinary administrator credential records use no item ETag. Rotation adds bearer-only `GET /api/v2/admin-authority` with strong `"admin-authority-<revision>"` ETag, optional authority `If-Match` on credential creation, and required exact authority `If-Match` on targeted rotation completion.

No command polls, refetches a precondition, or replays a mutation. Server and policy resources add targeted snapshot or watermark cursors, idempotency, exact preconditions, and strong ETags only where listed below.

The event stream still has no replay mechanism. Browser streaming uses session-only `POST /api/v2/events` with exact `EmptyObject` `{}`, Origin, and CSRF, returning the same `EventStream`; inherited bearer-or-session GET and POST share one hub, frame, keepalive, limit, overflow, deadline, and closure owner.

Invalidation kinds are the closed set `admin_credentials`, `system_status`, `backups`, `servers`, `server_operations`, `server_auth_flows`, `catalog`, `authorization`, `invocations`, and `grant_requests`.

Admin bearer values use prefix `mgw_admin_`, reserved agent bearer values use `mgw_agent_`, and the session cookie is `agent_gateway_session`. The legacy `mcp_gateway_session` name is expiry-only, never authority; see the [session cutover and exact cookie scope](administrative-control-plane.md#administrative-authority-and-sessions). Approved one-time output sinks begin with `controlling_terminal` and `owner_only_file`; the latter is a newly created, non-symlink-following `0600` file containing exactly the secret and one newline. Additional server-credential write-only secret ingress declarations are `admin_credential_replacement`, `dcr_client_secret`, `authorization_code_token_response`, `refresh_response`, and `authoritative_generation_refresh_copy`. Principal credential issuance adds only `agent_credential_creation` for the one-time credential creation body. Browser control adds `browser_one_time_display` and explicit `user_initiated_clipboard`; neither ordinary browser state nor automatic clipboard publication is a sink. `http_proxy_client_environment` permits only explicit client proxy-URL exports resolved at shell startup from the existing owner-private agent token file; never administrator tokens, persisted configuration or service environment. Standard output and standard error are not secret sinks.

### Server, catalog, and OAuth request mechanics

Administrator rotation uses `AdminAuthority` exactly `{revision}`; its revision is the maximum administrator credential revision and advances on every create, revoke, reset, or completed rotation. `AdminCredentialRotationCompletion` is exactly `{replacement_id}` and returns `AdminCredentialRotationResult` exactly `{old_credential,new_credential}` plus the resulting authority ETag. Conditional create compares the supplied authority revision before mutation. Completion rechecks that the named old credential is active, the replacement is active and non-expiring, and the authority revision is unchanged, then revokes only the named old credential in one transaction.

| Method and pattern                                       | Closed request schema                                       | Success schema/status                                     | Cursor | Idempotency | Exact `If-Match` | Response ETag |
| -------------------------------------------------------- | ----------------------------------------------------------- | --------------------------------------------------------- | ------ | ----------- | ---------------- | ------------- |
| `GET /api/v2/mcp/servers`                                | `ServerListQuery`                                           | `Page<Server>` / 200                                      | yes    | no          | no               | no            |
| `POST /api/v2/mcp/servers`                               | `ServerCreate`                                              | `ServerMutation` / 201 or replay 200                      | no     | yes         | no               | yes           |
| `GET /api/v2/mcp/servers/{id}`                           | none                                                        | `Server` / 200                                            | no     | no          | no               | yes           |
| `PATCH /api/v2/mcp/servers/{id}`                         | `ServerPatch`                                               | `ServerMutation` / 200                                    | no     | no          | yes              | yes           |
| `DELETE /api/v2/mcp/servers/{id}`                        | `EmptyObject`                                               | `ServerMutation` / 202 or replay 200                      | no     | no          | yes              | yes           |
| `GET /api/v2/mcp/servers/{id}/operations`                | `ServerOperationListQuery` or `ActiveServerOperationsQuery` | query page or active projection / 200                     | yes    | no          | no               | no            |
| `POST /api/v2/mcp/servers/{id}/operations`               | `ServerOperationCreate`                                     | `ServerOperationMutation` / 202 or replay 200             | no     | yes         | yes              | no            |
| `GET /api/v2/mcp/servers/{id}/operations/{operation_id}` | none                                                        | `ServerOperation` / 200                                   | no     | no          | no               | no            |
| `POST /api/v2/mcp/servers/{id}/credential-replacements`  | `CredentialReplacement`                                     | `CredentialReplacementResult` / 202                       | no     | no          | yes              | no            |
| `GET /api/v2/mcp/servers/{id}/oauth-flows`               | `ServerAuthFlowListQuery`                                   | `Page<ServerAuthFlow>` / 200                              | yes    | no          | no               | no            |
| `POST /api/v2/mcp/servers/{id}/oauth-flows`              | `EmptyObject`                                               | `AuthFlowCreation` / 201                                  | no     | no          | yes              | no            |
| `GET /api/v2/mcp/servers/{id}/oauth-flows/{flow_id}`     | none                                                        | `ServerAuthFlow` / 200                                    | no     | no          | no               | no            |
| `DELETE /api/v2/mcp/servers/{id}/oauth-flows/{flow_id}`  | `EmptyObject`                                               | empty / 204                                               | no     | no          | no               | no            |
| `GET /api/v2/mcp/catalog`                                | `CatalogListQuery`                                          | `CatalogPage` / 200                                       | yes    | no          | no               | no            |
| `GET /api/v2/mcp/servers/{id}/descriptors`               | `DescriptorListQuery`                                       | `Page<ToolDescriptor>\|Page<ToolDescriptorSummary>` / 200 | yes    | no          | no               | no            |
| `GET /api/v2/mcp/servers/{id}/descriptors/{tool_id}`     | none                                                        | `ToolDescriptor` / 200                                    | no     | no          | no               | no            |
| `GET /oauth/callback`                                    | `OAuthCallbackQuery`                                        | fixed `OAuthCallbackHTML` / 200, 400, or 503              | no     | no          | no               | no            |

### Normalized administrative collections

`CollectionContracts` enumerates every collection's query members, default order, page bounds, envelope, and explicit projections. Filter presence never selects another mode, order, maximum, or envelope. Every collection defaults to 50 rows. Unknown, repeated, empty, malformed, and incompatible query members fail; declared limits use canonical positive decimal spelling. Explicit sort defaults to ascending; direction requires sort. Omitted defaults and their explicit equivalents bind the same effective query. Primary-sort ties use immutable ID ascending; unique insertion sequence supplies history order.

| Collection under `/api/v2`     | Default order                           | Maximum | Ordinary success schema            |
| ------------------------------ | --------------------------------------- | ------: | ---------------------------------- |
| `admin-credentials`            | ID ascending                            |     100 | `Page<AdminCredential>`            |
| `backups`                      | ID ascending                            |     100 | `Page<Backup>`                     |
| `mcp/servers`                  | name ascending                          |      50 | `Page<Server>`                     |
| `mcp/catalog`                  | tool ascending                          |      50 | `CatalogPage`                      |
| `mcp/servers/{id}/descriptors` | last-seen descending                    |      50 | `Page<ToolDescriptor>`             |
| `mcp/servers/{id}/operations`  | created descending                      |      50 | `QueryPage<ServerOperation>`       |
| `mcp/servers/{id}/oauth-flows` | insertion sequence ascending            |     100 | `Page<ServerAuthFlow>`             |
| `principals`                   | name ascending                          |     100 | `QueryPage<Principal>`             |
| `mcp/grants`                   | description ascending                   |     100 | `QueryPage<GrantTableItem>`        |
| `mcp/grant-requests`           | submitted insertion sequence descending |     100 | `QueryPage<GrantRequestTableItem>` |
| `invocations`                  | insertion sequence descending           |     100 | `InvocationPage`                   |
| `audit-events`                 | insertion sequence descending           |     100 | `AuditPage`                        |

`Page<T>` is exactly `{items,next_cursor}`. `QueryPage<T>` adds exact matching `total_count` and zero-based `offset`, nonnegative integers from the same filtered snapshot as selected rows; empty results have zero count and offset. Counts remain limited to principals, grants, requests, and operation history. Catalog and audit retain their distinct metadata envelopes. No ordinary request selects `representation=table` or an implicit legacy insertion-order mode.

Principal lists support bounded name/state/visibility filters; grants additionally support exact principal/server IDs, identity/principal/target recognition, effect/state, and documented sorts. Grant collection items are always exactly `{grant,principal_display_name,server_display_name}`. Requests always use `{request,principal_display_name,server_display_name,resolved_server_id,resolved_upstream_name}`; the nested request remains its policy summary, never descriptor evidence. The [identity chapter](identity-and-authorization.md#principal-and-grant-contract) owns recognition rules and process-bound five-minute snapshot cursors. Member-resource and MCP self-service policy formats do not change.

The executable descriptor success schema is `Page<ToolDescriptor>|Page<ToolDescriptorSummary>`. Descriptor queries accept `tool`, `status`, `sort`, `direction`, `cursor`, `limit`, and optional `projection=full|summary` (omission equals full). Omitted status includes all; `available` means not retired and `retired` means retired evidence. Sort is tool/status/last-seen. The summary shape is exactly `{id,server_id,upstream_name,external_name,catalog_revision}` and uses narrow selected-page SQL without reading descriptor schemas. Both projections share filters, ordering, page bounds, and revision/watermark semantics; cursors also bind projection and handler process epoch. Neither `retired` nor `representation` is an API query member.

Server queries accept `name`, `namespace`, `status`, `sort`, `direction`, `cursor`, and `limit`. Name searches display name or ID, namespace searches namespace; sort is name/id/namespace/status/tools (active count). Synthesized status filters are exactly `ready`, `connecting`, `authorization_required`, `authentication_unavailable`, `capacity_saturated`, `disabled`, `deleted`, and `needs_attention`. Human presentation labels and browser fragment values remain unchanged; browser consumers translate the closed status values at the API boundary.

Catalog queries accept `tool`, `server`, `status`, `sort`, `direction`, `cursor`, and `limit`: text searches external tool name and server display name, status is available/issue, sort is tool/server. Server/catalog/descriptor recognition uses NFKC-normalized lowercase substring matching; both supplied and normalized text are bounded to 256 UTF-8 bytes without control/format characters. Filters are conjunctive before slicing. None of these collections returns totals or asserts callability.

Server query cursors bind the effective query, insertion upper watermark, handler epoch, and all observed identity/name/namespace/status/active-count/desired-revision tuples. Changed facts stale the traversal; later inserts remain excluded. Catalog cursors bind query, active generation, and upper watermark under the registry lock. Descriptor cursors bind query/projection, server identity, catalog revision, upper watermark, and handler epoch. Principal/grant/request snapshots retain authenticated metadata binding and expiry. Operation and history cursor rules remain below. Credential and backup ID continuations are not frozen snapshots. Discard stale traversals and start a fresh first page; never infer permission to replay a mutation.

`ServerCreate` is exactly `{namespace,display_name,enabled,transport}`; a nonempty `ServerPatch` permits only `display_name`, `enabled`, and complete `transport`. `ServerMutation` is exactly `{server,operation}`. `ServerOperationCreate` accepts only `reload`, `retry`, `refresh_catalog`, or `disconnect_credentials`; other operation kinds are internally generated.

Credential replacement input accepts only `static_credential` or `oauth_client`; `oauth_tokens` authority comes only from validated OAuth responses. The strong ETag is exactly `"server-<id>-<desired_revision>"`; weak, wildcard, malformed, multiple, absent where required, or noncurrent preconditions are not interchangeable.

Server idempotency is scoped to parent admin credential, method, stable persisted operation identity, key, canonical validated/defaulted request, and exact precondition, with a 24-hour lifetime and 1,024-record bound. The identity retains the pre-cutover `/api/v1/servers` creation key and corresponding operation-child key as durable data, not live HTTP aliases. Equivalent v2 requests find retained results/conflicts, including interrupted outcomes, without scheduling fresh work or automatically replaying effects. Operator projections do not rename backup metadata or agent-shared policy fields.

### Server and catalog vocabulary

`Server` is exactly `{id,namespace,display_name,desired_state,desired_revision,transport,credential_revisions,credential_state,runtime,catalog,created_at,updated_at,deleted_at}`. `credential_revisions` is exactly `{static_credential,oauth_client,oauth_tokens}`. Runtime is exactly `{state,reason,runtime_id,reconciliation,dispatch}` and catalog is exactly `{durable_state,active_state,durable_revision,active_revision,durable_tool_count,active_tool_count,last_success_at,traversal}`; each occupancy uses `LimitStatus`.

The sanitized transport union is closed to stdio `{kind,executable,arguments,working_directory,environment,secret_environment}` and Streamable HTTP `{kind,url,protocol_mode,authentication}`. HTTP authentication is exactly `{mode:none}`, `{mode:bearer}`, or OAuth `{mode,registration,trusted_origins,request_offline_access}` with optional `callback_uri`, `auth_server_metadata_url`, and `scopes`. URL overrides are nullable strings and scopes is a nullable string array on input; omitted/null overrides are absent on reads. `scopes: []` is retained as an explicit empty set. Existing omitted-field configurations keep their representation and behavior. Complete transport replacement clears omitted overrides; omitting PATCH transport preserves them. Canonical callback, metadata, and scope rules are owned by [Downstream servers](downstream-servers.md#foreground-authorization-flows). Registration is static `{mode,issuer,client_id,token_endpoint_auth_method}` or dynamic `{mode,issuer}`. Credential replacement is static `{kind,expected_revision,values}` or OAuth client `{kind,expected_revision,client_secret}`.

The executable operation collection request schema is `ServerOperationListQuery|ActiveServerOperationsQuery`; its success schema is `QueryPage<ServerOperation>|ActiveServerOperations`.

All ordinary operation collection requests return the declared query page in created-descending order by default, independent of filters. `projection=active` is a separate query accepting no other members: it returns `ActiveServerOperations`, exactly `{items,has_more}`, with at most two scheduled/running records in insertion-sequence/ID order and a truthful overflow flag (one bounded three-row SQL read). It is independent of history filters and traversal. An empty successful projection proves no active operation at that observation; unavailable/loading reads do not. Only a sole matching refresh record permits refresh-catalog attachment; this projection never replaces transactional admission.

`ServerOperationListQuery` accepts optional `action`, `status`, `sort`, and `direction`. Action and Status use the closed operation kind/state values; sorts are `action`, `status`, `created`, `started`, and `outcome`. Direction is `ascending` or `descending` and requires sort. `created` orders by the creation timestamp; `started` preserves started-or-created UTC timestamp ordering. The browser explicitly selects `created` descending. Omitted sort means created descending; explicit sorts default ascending. Action, Status, and Outcome order by their stored closed vocabulary (null outcome is empty); every primary sort uses immutable ID-ascending ties. All filters apply conjunctively in SQL before pagination. Queries accept canonical decimal limits 1–50 (default 50) and a cursor; unknown, repeated, empty, malformed, and incompatible options fail. Responses are `QueryPage<ServerOperation>` with exact snapshot-consistent matching `total_count` and zero-based `offset`, not loaded-row totals.

Operation table cursors are bounded to 512 bytes and bind server, exact query, handler process epoch, insertion upper watermark, pruning generation, and scheduled/running counts within that watermark. For unchanged records, deterministic ordering and offsets neither duplicate nor omit matching rows, including equal keys. Later inserts and their transitions are excluded. Within the captured watermark scheduled counts only decrease; while that count is unchanged, running counts only decrease. Every permitted mutable transition therefore changes this pair; terminal rows are immutable. Any such transition or any pruning invalidates continuation, even outside the filter. Queries execute counts and the bounded page read within one storage view, without caching rows or reading unbounded history into memory. Invalid cursor encoding/closed shape returns `400 invalid_cursor`; incompatible query, process, server, bounds, or snapshot returns `409 stale_cursor`. A stale traversal must discard prior pages and restart at page one. Old operator cursors are not a compatibility interface; no durable schema change or resumable process state is introduced.

`ServerOperation` is exactly `{id,server_id,kind,target_desired_revision,target_credential_revisions,state,reason,created_at,started_at,finished_at}`. `ServerAuthFlow` is exactly `{id,server_id,state,target_desired_revision,registration_revision,created_at,expires_at,finished_at,reason,diagnostic}`. Its diagnostic is null or exactly `{correlation_id,stage,reason,http_status}` with correlation ID equal to the flow ID, a closed diagnostic stage, the same stable public reason as the failed flow, and a null or bounded HTTP status. `ToolDescriptor` is exactly `{id,server_id,upstream_name,external_name,descriptor,fingerprint,catalog_revision,first_seen_at,last_seen_at,retired_at}`. Explicit descriptor `projection=summary` returns only identity/revision fields for searchable selectors, while omission or `projection=full` returns full `ToolDescriptor` resources under the same ordinary query policy. `CatalogToolDescriptor` contains those exact fields plus snapshot-consistent `server_display_name` and `server_catalog_state`. `CatalogPage` is exactly `{catalog,items,next_cursor}`, where catalog is exactly `{active_state,active_generation,changed_at,issue_count}` and items are `CatalogToolDescriptor` resources. `CredentialReplacementResult` is exactly `{server_id,kind,credential_revision,operation}` and `AuthFlowCreation` is exactly `{flow,authorization_url}`.

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
