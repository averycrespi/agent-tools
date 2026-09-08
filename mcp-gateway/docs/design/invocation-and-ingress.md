# Invocation and MCP Ingress

Audience: Maintainers and contributors changing governed invocation, retained evidence, and MCP ingress

Authority: Normative product design

This chapter owns the behavior and invariants described below. Operational procedures remain in the linked guides; exact executable contract values remain owned by `internal/contract` and must agree with this chapter.

## Governed invocation and audit evidence

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

### Audit evidence and retention

Argument capture uses one fixed recursive key redactor owned by Gateway before compact encoding. Matching is case-insensitive over the normalized sensitive-key set; a matching value is replaced wholesale, including nested structures. Capture overflow falls back to the fixed `[TRUNCATED]` placeholder; redaction or encoding failure produces no capture, and neither path falls back to the original bytes. This is a least-disclosure control over recognized keys, not a claim that arbitrary secret material is detected. Operator-facing projections distinguish a retained capture, truncation, and absence without describing any capture as sanitized or safe. SQLite, backups, events, logs, process output, and test evidence must contain neither raw bearer values nor successful results, raw tool errors, or unredacted canaries.

The invocation repository is the sole online owner of schema-9 SQL. It serializes entropy while preparing one canonical admitted time and opaque ID before mutation, accepts final binding/policy evidence later, and validates all identifiers, revisions, fingerprints, names, compact redacted arguments, nullable groups, decisions, grants, and timestamp chronology again at insertion. The caller-owned admission transaction checks identity collision before deleting anything, evicts the lowest insertion sequences needed for a post-insert maximum of 65,536, and inserts the immutable row; any failure rolls both eviction and insertion back. Startup reads at most 65,537 rows through the latch-aware storage view and rejects malformed or over-capacity history before service. One synchronous best-effort terminal mutation writes only a canonical time/class pair when an unterminated ALLOW row still exists and completion is not before evaluation; eviction or a prior annotation is a benign miss. Acknowledged admission and effective terminal commits publish an ID-only `invocations` refresh hint after commit; rollback, uncertainty, and benign no-op annotation emit nothing.

### Admission and execution

Admission holds one authority gate around exactly one invocation-owned storage mutation. Binding-only branches prove the current credential but persist no policy fields. Resolved verification exposes a closed phase: a semantic evaluator failure may commit `authorization_unavailable` only after binding was explicitly verified, while binding, context, SQL, latch, or transaction failure rolls back without a row. A successful evaluation inserts exactly its current revision, time, decision, and smallest grant evidence. Only after the mutation returns acknowledged success, and while the gate remains active, may an ALLOW token detach the lease; rollback, uncertain commit, late drain, DENY, and BLOCK remain non-dispatchable.

The internal invocation service classifies the strict call params, defaults absent arguments to an empty object, resolves an external name once to a closed downstream/local target, and pins either the downstream validator/capability or one fixed local validator/handler. It validates and redacts the same token-preserving argument tree, performs the admission above, then releases authority and storage admission before one execution.

Downstream targets acquire their capability; local targets receive only the sealed admitted subject and acquire no downstream capacity. Capacity, route, cancellation, transport, and local storage evidence map only through the closed safe outcome boundary.

The service never resolves, evaluates, acquires, or executes a second time; it performs one synchronous best-effort terminal annotation after an ALLOW attempt, ignores annotation failure for the live response, and exposes an invocation ID only for errors backed by an acknowledged row. Successful projections contain no Gateway metadata.

Gateway supplies at most one automatic attempt, not exactly-once effects: an explicit caller retry after `outcome_unknown` may duplicate an effect, and no restart, cancellation, terminal-write failure, or lifecycle transition causes automatic replay. Local post-commit uncertainty returns `tool_unavailable`, omits terminal annotation/invalidation, and is recovered only through request reads or duplicate-first create retry.

### Capability acquisition

The current-runtime capability is an opaque, explicitly nonserializable process-local object. It captures server/tool/upstream/runtime identity and exact desired, static-credential, OAuth-client, OAuth-token, and catalog revisions, but exposes no identity enumeration or mutation.

Acquisition tries the global 32-slot channel before the four-slot server channel and never waits; server saturation immediately releases global. Only after both permits does an injected current-state seam revalidate route, runtime, every bound authority/catalog revision, and drain state.

A final capability-lock check closes the withdrawal race. Stale, withdrawn, draining, unavailable, canceled, and both saturation branches are typed pre-start rejections.

A lease may execute one call or cancel once; it releases server before global and preserves lower-level marker classification. Withdrawal synchronously marks the capability unavailable and cancels registered leases.

The internal invocation service is the only capability consumer. Production composition supplies it through one closed ingress adapter, while agent discovery continues to enumerate descriptor projections independently without acquiring capabilities; no root or ingress owner can resolve or acquire a route directly.

## MCP ingress and governed invocation

### Agent authentication and leases

The internal agent authenticator accepts only the canonical `mgw_agent_` encoding, derives one agent-domain verifier, and scans every complete active current slot in one bounded coherent transaction with constant-time comparison and no verifier predicate or early match return. Success exposes only principal ID/revision/visibility and credential ID/revision/fingerprint. Admin-domain bearers are a domain mismatch; missing, malformed, unknown, replaced, revoked, disabled, or cleared authority is one non-enumerating authentication failure. Invalid loaded candidate state, capacity overflow, or a latch before, during, or after a match fails unavailable with no partial binding.

One repository-owned exclusive authority gate encloses authentication/lease registration and every principal, credential, grant, authorization-revision, or invocation admission mutation. Its process-wide `authority_work` bound admits 32 outstanding operations: one executing and up to 31 waiting. Excess arrivals reject immediately. Admitted callers wait at most one second for the gate, shortened by their context cancellation or deadline; capacity exhaustion and gate-wait expiry return `resource_limit`, including HTTP 429 during agent authentication rather than HTTP 401. The wait deadline does not bound work after gate acquisition. The fixed order remains authority admission, exclusive gate, storage mutation, transaction checks/write, targeted post-commit invalidation or detachment, then gate and admission release. Waiting never holds a storage transaction or mutation slot; SQLite mutation admission remains nonblocking. Gate exclusivity still orders credential reads and lease registration against revocation and targeted invalidation.

Principal PATCH and credential replace/revoke cancel only that principal's pending leases after an acknowledged commit and conservatively whenever their mutation latches storage. Principal creation cannot have an existing target lease, and grant create/delete never close credential channels: admission re-evaluates policy while holding the same gate.

Success returns an already-registered pending lease with immutable safe binding, latch/drain-aware currentness, cancellation completion, and idempotent release. Drain first atomically fences new authority admission and registration, wakes queued callers, detaches all pending leases under the registry lock, cancels them after unlocking, and only then boundedly waits for all outstanding gate holders and waiters to release their occupancy; a deadline can make quiescence unclean but cannot leave a pending lease open.

Waiting runs in the caller's goroutine with a bounded wait context; the registry creates no worker goroutines or alternate owner. Cancellation, wait expiry, drain, and every completed operation release their actual outstanding occupancy.

The admission scope acquires only that gate and never opens `Store.Mutate`; its caller owns the existing security transaction. Both supplied-transaction verifiers require the exact current active credential binding and capture one authority-clock UTC evaluation timestamp inside the transaction. Resolved verification ignores advisory revision mismatch, evaluates the current transaction's complete policy against the unchanged token-preserving argument tree, and returns current bounded evidence plus a pending detachment only for ALLOW. Binding-only verification returns only current authorization revision and timestamp. The caller may mark detachment successful only after its mutation returns an acknowledged commit while the scope still holds the gate; rollback, uncertainty, cancellation, late confirmation, or released authority leaves the lease pending or unavailable. Confirmation atomically changes pending to admitted and removes it from credential invalidation, so later credential or policy changes neither cancel nor reauthorize the admitted invocation.

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

The adapter's nonqueueing pipeline counter encloses the complete synchronous service call through terminal annotation; composition fences it before authority and route drain, then includes its quiescence in the deadline-bounded result without adding a public status field. SDK bootstrap and legacy session lifecycle methods remain SDK-owned.

### Legacy sessions

Legacy `2025-11-25` initialization reserves one of 128 slots before entropy or state publication, then uses a stateful official-SDK adapter with a Gateway-generated opaque session ID. Successful publication alone transfers the initial registered lease into the session; every later POST, GET, or DELETE uses its own request lease and requires the exact protocol header, session ID, principal ID, credential ID, and credential revision. The session watches lease cancellation, and idle/absolute lifetime, replacement, revocation, disablement, DELETE, initialization failure, shutdown, or restart closes the in-memory session and releases its lease once. There is no credential-expiry timer. Modern, legacy, MCP-work, and MCP-stream state remain separate; 32 work and 32 stream permits reject without queuing.
