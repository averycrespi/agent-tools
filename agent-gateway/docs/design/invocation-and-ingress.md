# Invocation and MCP Ingress

Audience: Maintainers and contributors changing governed invocation, retained evidence, and MCP ingress

Authority: Normative product design

This chapter owns the behavior and invariants described below. Operational procedures remain in the linked guides; exact executable contract values remain owned by `internal/contract` and must agree with this chapter.

## Governed invocation and audit evidence

Administrative reads of MCP invocation evidence use only `GET /api/v2/mcp/invocations` and `GET /api/v2/mcp/invocations/{id}`, `agent-gateway mcp invocation list/get`, and `#/mcp/invocations` with supported detail/filter context. The former `/api/v2/invocations`, top-level `invocation` CLI, and `#/activity/invocations` locations are retired without aliases, redirects, fallback requests, or replay. See the [coordinated operator cutover](../operators/administration.md#mcp-invocation-namespace-cutover).

This is an API v2 administrative namespace change only. Public invocation JSON, filters, limits, cursors, generations, historical IDs/rows and redacted captures remain unchanged; no migration, evidence rewrite, protocol discriminator or activity store is introduced. The `invocations` event kind, admission-before-dispatch, one-shot outcomes, credential bytes, backup lineage, MCP ingress/self-service and callback identities remain unchanged. **Audit Log** remains shared administrator/system/offline-maintenance evidence at `#/audit-log`, `/api/v2/audit-events`, and `audit` CLI, produced separately from MCP invocations.

### Outcome projection

Operator interpretation of retained records is canonical in [invocation evidence](../operators/invocation-evidence.md). The invocation result boundary consumes only that typed completion evidence. Missing terminal evidence means the outcome is unknown; it never proves rollback, nonexecution, or safe retry. A validated success projects required `content`, optional `structuredContent`, and optional false `isError`; the five content unions are closed, bounded, and recursively stripped of protocol `_meta`. The modern `resultType` framing member is accepted only as `complete` and removed, while input-required work and every unsupported member or type fail closed. Tool errors, JSON-RPC errors, and invalid complete results expose only `downstream_failure`; pre-start failures expose `tool_unavailable`; uncertain handoff exposes only `outcome_unknown`. No raw error or unsuccessful content crosses the boundary, and no result is persisted.

The retained outcome vocabulary is closed:

| Outcome                     | Meaning                                                                      |
| --------------------------- | ---------------------------------------------------------------------------- |
| `invalid_params`            | The request could not be classified as a valid call.                         |
| `unknown_tool`              | No current target matched the requested external name.                       |
| `invalid_arguments`         | The resolved target rejected the unchanged arguments before execution.       |
| `authorization_unavailable` | Safe authorization or audit admission could not be established.              |
| `deny`                      | Current policy explicitly denied the call.                                   |
| `block`                     | No applicable allow authorized the call.                                     |
| `prestart_failure`          | The admitted call failed before transport or local execution handoff.        |
| `succeeded`                 | A complete successful result was observed for the live caller.               |
| `downstream_failure`        | A complete unsuccessful downstream result was observed and safely collapsed. |
| `outcome_unknown`           | Handoff may have occurred or required terminal evidence is missing.          |

The accompanying basis is `admission`, `policy`, `terminal`, or `missing_terminal`. Missing terminal evidence always projects as unknown.

### Live call rejection contract

Both modern and legacy governed `tools/call` errors retain JSON-RPC code `-32000` and the five existing `data.code` values. Only `call_rejected` carries a required closed `data.reason`. The reason comes from the acknowledged admission class or its evaluated decision, never a second policy evaluation. Messages are bounded Gateway-owned text; no grant IDs, matching constraints, argument values, or raw internal/downstream errors are interpolated.

| `data.reason`               | `error.message`                                                                                                                                                                                                                                             |
| --------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `invalid_params`            | Request rejected: invalid tools/call parameters. Check the request shape.                                                                                                                                                                                   |
| `unknown_tool`              | Request rejected: unknown tool. Refresh tools/list and check the tool name.                                                                                                                                                                                 |
| `invalid_arguments`         | Request rejected: invalid tool arguments. Check the tool’s input schema.                                                                                                                                                                                    |
| `deny`                      | DENIED: a matching DENY grant forbids this call. Additional ALLOW grants and self-service requests cannot override it.                                                                                                                                      |
| `block`                     | BLOCKED: no matching ALLOW grant authorizes this call. If available, you may use mcp_gateway.list_grants to inspect your access or mcp_gateway.create_grant_request to request access. Requesting access does not authorize the call; approval is required. |
| `authorization_unavailable` | Call rejected: authorization could not be established. This is not a DENY or BLOCK decision.                                                                                                                                                                |

For a blocked resolved local `mcp_gateway` target, the message is instead: “BLOCKED: no matching ALLOW grant authorizes this call. You may ask an administrator to review your access.” Self-service is optional and grant-controlled, not a workaround for DENY. A DENY may expire or be removed by an administrator; its precedence does not imply permanence. Guidance neither submits requests nor replays calls.

Malformed parameters, unknown targets, and invalid arguments remain binding-only admission classes even if policy would deny a valid call. A semantic authorization failure after verified binding uses `authorization_unavailable`; an acknowledged ALLOW that cannot detach (for example, because drain intervened) also uses that reason, never DENY/BLOCK. Rejections execute neither downstream nor local targets.

Without an acknowledged admission, the error is `audit_unavailable` with no invocation ID or reason, even if evaluation or an uncertain commit may have occurred. Acknowledged rejection errors retain `data.invocationId`. Successes have no Gateway metadata; other error codes omit `data.reason`. Existing `outcome_unknown` and `data.outcomeUnknown` semantics, downstream error projection, authentication/admission ordering, and audit fail-closed behavior are unchanged. Invalid internal response combinations fail closed to `audit_unavailable` rather than inventing a rejection reason.

### Internal evidence boundary

`internal/activity` owns only common evidence values: the prepared identity and admission time, principal and credential identity/revision/fingerprint, admission class and authorization evidence, and optional paired completion time/class. Its envelope composes these lifecycle facts without owning validation, authority, SQL, execution, or public representations. The existing closed contract vocabulary remains unchanged; MCP is the only supported domain.

`internal/invocation.MCPDetails` owns the requested tool name, fixed-redacted argument capture, and optional resolved route. A resolved route follows the [access-target boundary](identity-and-authorization.md#internal-access-target-boundary): it carries the canonical `accesstarget.MCP` exact target plus invocation-owned tool ID and pinned descriptor revision/fingerprint. Synthetic local and downstream targets are distinctions within MCP, not separate protocols. An absent route means unresolved evidence, never server-wide scope. Malformed calls may retain their existing independently available name/capture fields; resolved classes still require complete exact-target and descriptor evidence.

Invocation prepares identity before binding/policy evidence, snapshots all mutable evidence (including the exact-target name pointer) before insertion, and adapts the common envelope and MCP details to the unchanged schema-9 columns and public invocation projections. Stored nullable groups must be checked for completeness before constructing typed values; normalization cannot turn incomplete evidence into absence. A missing completion remains unknown for admitted ALLOWs. Administrative audit stays a distinct evidence domain owned by `internal/audit`; the common values introduce no additional writer, store, runtime, or audit path.

### Audit evidence and retention

Argument capture uses one fixed recursive key redactor owned by Gateway before compact encoding. Matching is case-insensitive over the normalized sensitive-key set; a matching value is replaced wholesale, including nested structures. Capture overflow falls back to the fixed `[TRUNCATED]` placeholder; redaction or encoding failure produces no capture, and neither path falls back to the original bytes. This is a least-disclosure control over recognized keys, not a claim that arbitrary secret material is detected. Operator-facing projections distinguish a retained capture, truncation, and absence without describing any capture as sanitized or safe. SQLite, backups, events, logs, process output, and test evidence must contain neither raw bearer values nor successful results, raw tool errors, or unredacted canaries.

Invocation owns all traffic SQL and validates identifiers, revisions, fingerprints,
names, compact redacted arguments, nullable groups, decisions, grants and chronology.
Traffic checks collisions before transactional retention and insertion. Its production
65,536-row ceiling and physical/logical budgets define rolling bounded history,
not a guaranteed retention window. Pins protect live dispositions and completion;
oldest eligible rows may be pruned around pinned holes. Generation and cumulative
pruning changes invalidate cursors rather than silently omitting records. Separate
bounded control snapshots resolve current principal names; their digest is cursor
state, never authorization. There is no cross-store SQL or name lookup during a
traffic write. Acknowledged changes emit coalesced invocation/System invalidations;
uncertain or failed writes do not invent durable evidence.

### Receipt-based traffic persistence

The [isolated traffic store](storage-and-recovery.md#isolated-traffic-store)
reuses `PreparedAdmission`, common activity values, exact MCP details, the existing
SQL shape, capture limits and complete semantic validators. Production selects it
for MCP persistence; the legacy repository remains only a migration/test seam.

The store queues evidence only. Defaults bound admission occupancy, including
active settlement, to 128 records and 2 MiB charged bytes; each transaction contains
at most 32 records/512 KiB, with 2 ms dwell, 250 ms queue lifetime and a two-second
cooperative write lifetime. Configuration validates positive finite limits (at most
1024 queued records/16 MiB, 10 ms dwell, one-second acquisition and five-second
write lifetime). A separate completion queue reserves the same record capacity at
128 bytes per completion. One completion transaction precedes each admission batch,
so neither class can starve the other under sustained arrivals. Completion transactions
batch only already-queued records under the same record/byte bounds, without added
dwell. An invalid member rolls back the entire active completion batch; no member is
split out or replayed. No queued member
contains an executable callback. No acquisition expiry extends into transaction
settlement, and accepted callers wait for settlement even after cancellation.

Successful atomic commit creates individual opaque process-local receipts containing
immutable evidence and the original request cancellation context. Canceled callers
receive no receipt; later cancellation prevents confirmation even with a fresh
context. IDs and history reads cannot create receipts. `Confirm` consumes one live
ALLOW receipt's dispatch disposition, but is only the **evidence half** of admission:
production reacquires and confirms current authority before execution.
The store never executes or reauthorizes a call.

`Release` settles a no-dispatch disposition. Confirmed calls remain pinned until
`Complete` settles exactly one synchronous best-effort paired completion attempt,
including refusal/failure. Completion never accepts or rewrites the live upstream
result. Capacity/deadline refusals do not fault healthy storage; storage/commit/
rollback uncertainty does. Missing terminal evidence remains unknown. Pins have a
separate bounded process-local cardinality (1024 by default, at most 4096); forgotten
live dispositions fail closed at that capacity rather than expiring a potentially
executing row. Closing/restarting never reconstructs pins, receipts, completion or
execution. There is no background completion backlog or retry.

### Admission and execution

Admission has three phases. Under a short authority gate and one coherent control
read snapshot, evaluate the authenticated immutable binding and pinned target once,
sealing the decision, revision and evaluation time. Release the gate and read
transaction before submitting immutable evidence to traffic persistence; no control
writer, marker or authority gate spans traffic commit. After an exact acknowledged
receipt, reacquire authority and confirm the same active binding and unchanged
global authorization revision. Under shared drain, control-health and traffic-fault
fences, consume that receipt once and detach the pending lease atomically. Never
wait for authority while holding the traffic writer. A sealed evaluation replaces
the old gate-scoped pending-detachment object across phases.

Revocation, replacement, principal/policy revision, cancellation, control latch,
traffic fault or drain winning before confirmation blocks dispatch without another
evaluation, alternate decision, retry or reroute. Grant expiry is evaluated at the
captured evaluation time, not reinterpreted during confirmation. Acknowledged
ALLOW losing confirmation returns `authorization_unavailable` with its invocation
ID and leaves unknown terminal evidence. Binding-only, DENY and BLOCK branches
return their acknowledged original evidence without dispatch confirmation and
settle their pins. A readable row alone is never acknowledgment. Completion is one
synchronous best-effort receipt-based traffic write, never a control mutation or
authority reacquisition; its failure cannot replace a known live result.

The internal invocation service classifies the strict call params, defaults absent arguments to an empty object, resolves an external name once to a closed downstream/local target, and pins either the downstream validator/capability or one fixed local validator/handler. It pins the published descriptor's normalized `readOnlyHint` alongside that target for read-only ALLOW evaluation, never accepting client-supplied annotations. It validates and redacts the same token-preserving argument tree, performs the admission above, then releases authority and storage admission before one execution. Catalog replacement fences the pinned capability before a later acquisition/dispatch, so old read-only authority cannot authorize a replacement descriptor. Fixed local tools use their compiled hints; admitted-call detachment, no retry, and no reroute remain unchanged.

Downstream targets acquire their capability; local targets receive only the sealed admitted subject and acquire no downstream capacity. Capacity, route, cancellation, transport, and local storage evidence map only through the closed safe outcome boundary.

The service never resolves, evaluates, acquires, or executes a second time; it performs one synchronous best-effort terminal annotation after an ALLOW attempt, ignores annotation failure for the live response, and exposes an invocation ID only for errors backed by an acknowledged row. Successful projections contain no Gateway metadata.

Gateway supplies at most one automatic attempt, not exactly-once effects: an explicit caller retry after `outcome_unknown` may duplicate an effect, and no restart, cancellation, terminal-write failure, or lifecycle transition causes automatic replay. Local post-commit uncertainty returns `tool_unavailable`, omits terminal annotation/invalidation, and is recovered only through request reads or duplicate-first create retry.

### Contention qualification

The disposable race-enabled composition workload exercises 32 real ingress calls
at concurrency four in warn, debug and stalled-diagnostic modes. A downstream HTTP
barrier proves overlapping executions; assertions inspect retained admissions and
terminals and the absence of control-store dual writes. A separate held-control-
writer scenario proves traffic persistence does not join its wait queue or retain
authority. Deterministic receipt interleavings prove revocation completes while the
traffic writer is held and prevents later confirmation. These are correctness and
isolation checks, not the dependent throughput qualification or latency promises.

### Capability acquisition

The current-runtime capability is an opaque, explicitly nonserializable process-local object. It captures server/tool/upstream/runtime identity and exact desired, static-credential, OAuth-client, OAuth-token, and catalog revisions, but exposes no identity enumeration or mutation.

Acquisition tries the global 32-slot channel before the four-slot server channel and never waits; server saturation immediately releases global. Only after both permits does an injected current-state seam revalidate route, runtime, every bound authority/catalog revision, and drain state.

A final capability-lock check closes the withdrawal race. Stale, withdrawn, draining, unavailable, canceled, and both saturation branches are typed pre-start rejections.

A lease may execute one call or cancel once; it releases server before global and preserves lower-level marker classification. Withdrawal synchronously marks the capability unavailable and cancels registered leases.

The internal invocation service is the only capability consumer. Production composition supplies it through one closed ingress adapter, while agent discovery continues to enumerate descriptor projections independently without acquiring capabilities; no root or ingress owner can resolve or acquire a route directly.

## MCP ingress and governed invocation

### Process diagnostics and correlation

Debug observations cover admission results, execution start/result, and terminal-annotation results separately. The service assigns a process-local diagnostic call counter before audit identity preparation (and the composition pipeline fence); it does not consume audit entropy or change dispatch availability. Rejected pre-ack attempts have a call ID but no invocation ID. An actual invocation ID is linked only after the audit mutation acknowledges. Execution evidence never implies a successful terminal annotation; missing annotation still means unknown durable outcome. Diagnostic counters saturate by dropping further records that require the exhausted correlation ID, never by rejecting a call or reusing a counter.

Authority observations distinguish wait, acquisition, release and rejection, with closed capacity, expiry, cancellation and drain causes. Occupancy samples come from the actual owners; they are point-in-time observations, not summed status placeholders. Authority samples the actual gate channel while its mutex freezes outstanding work, and retires gate ownership and outstanding work together under that mutex, so a departing owner cannot become a phantom waiter. Durations are monotonic elapsed milliseconds, not deadlines for active work. Diagnostic call IDs are unrelated to client JSON-RPC IDs, headers, tool names, arguments or credential fingerprints. Mutation counters are scoped to the storage or authority owner within a process instance. The [serve diagnostic contract](administrative-control-plane.md#serve-diagnostics) owns privacy, loss, and sink lifecycle.

### Agent authentication and leases

The internal agent authenticator accepts only the canonical `mgw_agent_` encoding, derives one agent-domain verifier, and scans every complete active current slot in one bounded coherent transaction with constant-time comparison and no verifier predicate or early match return. Success exposes only principal ID/revision/visibility and credential ID/revision/fingerprint. Admin-domain bearers are a domain mismatch; missing, malformed, unknown, replaced, revoked, disabled, or cleared authority is one non-enumerating authentication failure. Invalid loaded candidate state, capacity overflow, or a latch before, during, or after a match fails unavailable with no partial binding.

One repository-owned exclusive authority gate encloses authentication/lease registration and every principal, credential, grant, authorization-revision, or invocation admission mutation. Its process-wide `authority_work` bound admits 32 outstanding operations: one executing and up to 31 waiting. Excess arrivals reject immediately. Admitted callers wait at most one second for the gate, shortened by their context cancellation or deadline; capacity exhaustion and gate-wait expiry return `resource_limit`, including HTTP 429 during agent authentication rather than HTTP 401. The wait deadline does not bound work after gate acquisition. The fixed order remains authority admission, exclusive gate, storage mutation, transaction checks/write, targeted post-commit invalidation or detachment, then gate and admission release. Authority waiting never holds a storage transaction or mutation slot. Invocation evaluation and confirmation use separate short coherent control reads; traffic persistence waits outside authority. Control mutation admission remains nonqueueing. Gate exclusivity still orders credential reads and lease registration against revocation and targeted invalidation.

Principal PATCH and credential replace/revoke cancel only that principal's pending leases after an acknowledged commit and conservatively whenever their mutation latches storage. Principal creation cannot have an existing target lease, and grant create/delete never close credential channels: admission evaluates policy once and confirmation requires the exact captured global revision while holding the same gate.

Success returns an already-registered pending lease with immutable safe binding, latch/drain-aware currentness, cancellation completion, and idempotent release. Drain first atomically fences new authority admission and registration, wakes queued callers, detaches all pending leases under the registry lock, cancels them after unlocking, and only then boundedly waits for all outstanding gate holders and waiters to release their occupancy; a deadline can make quiescence unclean but cannot leave a pending lease open.

Waiting runs in the caller's goroutine with a bounded wait context; the registry creates no worker goroutines or alternate owner. Cancellation, wait expiry, drain, and every completed operation release their actual outstanding occupancy.

Evaluation and confirmation each acquire only the authority gate and a bounded
control read, never `Store.Mutate`. Evaluation requires the exact active binding,
captures one authority-clock UTC time and current revision, and seals the single
policy result against unchanged token-preserving arguments. The advisory discovery
revision does not substitute for the captured revision. Confirmation requires
exact captured-revision equality and the same process-local candidate, pending
lease and acknowledged traffic evidence. It atomically changes pending to admitted
under the registry drain fence and removes credential invalidation; subsequent
credential/policy changes neither cancel nor reauthorize admitted execution.

Drain fences all new gate entrants before waiting boundedly, then removes and cancels every pending lease outside the registry lock. A timed-out drain leaves the fence set for a later completion. Stopped candidate recovery runs under exclusive process ownership, where no live registry exists.

Ingress authentication runs before MCP body reads, era classification, and session lookup. Production consumes the coordinated composition-owned positive authenticator and discovery bundle. The authentication seam accepts the authorization repository's already-registered non-expiring lease with exact principal/credential revisions, fingerprint, and visibility. A shared idempotent request owner releases the lease on boundary abort or every ingress terminal path, and lease invalidation cancels an in-flight modern request; detached ingress bindings, credential expiry, and optional post-auth subscriptions no longer exist.

### Modern ingress

Gateway validates the modern `2026-07-28` header/body protocol mirror and dispatches only sessionless POST requests to the official SDK's stateless transport. Per-request client metadata replaces initialization state in this era.

Modern requests reject legacy session IDs, cannot fall through to legacy classification, and propagate request cancellation. Before SDK dispatch, one shared raw codec preserves the JSON-RPC ID, strictly accepts absent, empty, or cursor-only list parameters (beside required modern protocol metadata), owns the closed discovery success/error envelopes, and returns fixed method-not-found for every uncomposed non-lifecycle feature method.

Its call-only sibling recognizes only a non-null string/number ID, excludes batches and notifications, strips `_meta` and disallowed fields, and passes token-preserving name/arguments plus closed wire-validity evidence to an era-neutral service seam. It owns exact success projection and the five safe call error codes, with the closed rejection reasons and fixed messages above.

Modern injection supplies the registered request lease and cancellation context directly to that seam before stateless SDK dispatch. Legacy injection first matches the reauthenticated request's exact initialized session binding, then supplies only that request lease and cancellation context; it never treats the session ID or session-owned lease as call authority.

### Shared discovery and composition

The shared injected list service makes both eras advertise exactly `tools:{}`, passes each request-scoped authenticated lease/cursor and cancellation to the discovery pager, maps only closed errors, and writes the pager's verified final bytes without re-encoding; `listChanged` remains absent. Legacy initialization retains its separate session-owned lease, while every list first reauthenticates and matches the exact session binding before using its own request lease.

Production composition owns the authority, policy service, process-local cursor key, pager, one startup-validated invocation repository/service, its nonqueueing process-local pipeline fence/counter, and both ingress adapters before listener startup. Root consumes their one validated dependency bundle and reports `principal_credentials`, so no partial call/authenticator/status graph is constructible.

The adapter's nonqueueing pipeline counter encloses the complete synchronous service call through terminal annotation; composition fences it and closes its storage-wait stop signal before authority and route drain, then includes its quiescence in the deadline-bounded result without adding a public status field. This wakes queued invocations and rejects new terminal acquisition without globally fencing producer cleanup. Active storage owners retain ownership through settlement; drain does not replace their transaction context with a wait deadline. A known live result survives a rejected best-effort terminal annotation, leaving missing terminal evidence unknown. Authority still cancels pending leases, including drain between commit and detachment. Quiescence waits outside authority/storage locks, and unresolved cleanup remains unclean. SDK bootstrap and legacy session lifecycle methods remain SDK-owned.

### Legacy sessions

Legacy `2025-11-25` initialization reserves one of 128 slots before entropy or state publication, then uses a stateful official-SDK adapter with a Gateway-generated opaque session ID. Successful publication alone transfers the initial registered lease into the session; every later POST, GET, or DELETE uses its own request lease and requires the exact protocol header, session ID, principal ID, credential ID, and credential revision. The session watches lease cancellation, and idle/absolute lifetime, replacement, revocation, disablement, DELETE, initialization failure, shutdown, or restart closes the in-memory session and releases its lease once. There is no credential-expiry timer. Modern, legacy, MCP-work, and MCP-stream state remain separate; 32 work and 32 stream permits reject without queuing.
