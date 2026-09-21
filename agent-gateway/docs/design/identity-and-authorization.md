# Identity and Authorization

Audience: Maintainers and contributors changing principal identity, authorization, grants, and self-service requests

Authority: Normative product design

This chapter owns the behavior and invariants described below. Operational procedures remain in the linked guides; exact executable contract values remain owned by `internal/contract` and must agree with this chapter.

## Shared identity and MCP policy ownership

Principal IDs, display names, active/disabled state, the singular agent credential slot, authentication, and authority admission are shared identity/state. MCP targets and grants, tool-discovery visibility, access requests, and the six fixed synthetic self-service tools are currently MCP-only policy. Shared identity does not imply protocol-general grants or access.

`internal/composition` constructs and owns one authorization repository/authenticator and its authority gate/admission verifier. The distinction is semantic, not a split into identity and protocol authority owners. Sealed admitted subjects retain their existing identity/revision evidence; MCP policy uses the target boundary below. No generic protocol framework, protocol discriminator, second credential slot, or MCP-settings endpoint is introduced. HTTP policy uses this same singular agent credential, with separate HTTP permissions and default block for existing and new principals. Its control resources add no production HTTP ingress listener.

The public names `visibility` and `default_grant` and the `Principal`, `PrincipalCreation`, and `AgentCredential` representations remain compatibility contracts. `visibility` means MCP discovery visibility; it grants no access, and MCP grants remain authoritative for calls. `default_grant` identifies the ordinary MCP self-service grant created with a principal, not protocol-general or downstream authority. These clarifications require no reinitialization, migration, backup conversion, credential replacement, or data rewrite. Bearer/verifier/fingerprint framing, keyring identities, revisions, audit vocabulary, and self-service names/schemas remain unchanged.

## Traffic confirmation boundary

Control SQLite remains the sole authority for principal, credential and policy
state. Invocation evaluation uses one short gate/coherent control snapshot and
seals its exact revision, time and pending binding. No authority gate or control
transaction spans traffic persistence. ALLOW confirmation reacquires that gate,
requires the unchanged global authorization revision and exact active binding,
and atomically consumes matching process-local evidence and detaches under drain
and both storage-health fences. Unrelated policy changes may conservatively
reject; no failed confirmation reevaluates or retries. Evaluation-time expiry and
post-admission revocation semantics remain unchanged. Current display-name
snapshots used by traffic history are bounded recognition data, never authority.
The [invocation chapter](invocation-and-ingress.md#admission-and-execution) owns
the complete evaluation/receipt/confirmation and outcome protocol.

## Internal access target boundary

`internal/accesstarget.MCP` is the single internal target vocabulary for grant creation and evaluation, resolved invocation evidence and admission verification, structural discovery, conservative DENY checks, request dedupe/narrowing/approval, and self-grant projection. It carries an immutable server ID and a nullable exact upstream name: null means server-wide scope, not an unresolved call. MCP is the only supported domain. The value and its scope comparisons confer no authority, existence, validation, or read-only eligibility; callers do not mutate shared upstream-name pointers.

Public administrator resources retain `server_id` and nullable `upstream_name`; self-service policy retains namespace-based `scope` and `target`. Package-owned SQL adapters retain the existing columns, nullability, schema, and exact versioned dedupe framing. Namespace and catalog resolution stay with the MCP owners through the existing supplied-transaction seams, outside shared identity ownership. Authorization remains the sole principal/credential/grant repository, authenticator, authority gate, and admission verifier.

Validation remains purpose-specific: ordinary grants may be server-wide or name an uncatalogued tool, and synthetic grants and calls remain valid. Exact-call verification rejects server scope and malformed coordinates. Grant-request creation and approval still reject reserved synthetic targets; neither a target value nor a catalog hint makes them requestable. Pinned descriptor read-only facts remain separate from target coordinates. No protocol registry, additional target domain, migration, or credential-slot change is introduced.

## HTTP policy version 1

`internal/httppolicy` owns pure HTTP policy compilation, canonical coordinates,
origin-set containment and deterministic evaluation. `internal/contract/http_policy.go`
owns the closed dialect and explanation shapes. This pure component introduces no listener, authenticator, keyring operation, network access, dispatch or durable execution queue. The sole authorization owner supplies coherent principal/policy/credential revisions and active grants through schema-20 control resources; principal admission, credential validity and expiry filtering remain its work.
HTTP uses the existing agent credential and control database and the shared
bounded traffic store, not another identity or per-protocol store. Existing MCP behavior remains unchanged. Capacity remains unqualified.

### Closed policy shapes and precedence

Every policy requires `version:1` and one explicit `type`:

- `block_destination`: `destination:{host,port}` only.
- `allow_tunnel`: the same destination, optionally Boolean `allow_private`.
- `block_requests`: `request:{origin:{scheme,host,port},methods,path}` only.
- `allow_requests`: the same request, optionally `allow_private` and one
  `credential_id` (an opaque resource ID, never secret material).

These are not effect/scope combinations. Inapplicable options, even false,
unknown or case-aliased fields, nulls, duplicates, trailing data, unknown versions
and malformed combinations reject. A version is immutable semantics, not a
mutable resource revision. Future dialects need explicit dispatch; they cannot
reinterpret version 1 or silently add multiple credentials.

Destination block wins for every access. Otherwise CONNECT selects an opaque
tunnel if any tunnel allow matches; it bypasses **all** request grants and
credential injection. Without a tunnel allow, CONNECT selects local interception,
not upstream permission: each decrypted request must be evaluated before any
upstream connection. Requests use destination block, then request block, then
request allows, then the principal's separate HTTP default (`allow` or `block`).
Absolute-form HTTP always uses request policy. Default allow implies neither
tunnel access nor private/loopback permission. There is no specificity, priority,
creation-order or GET-as-read-only inference.

### Canonical selectors and forwarding

Destination/tunnel selectors contain only host and an explicit effective port
1–65535, never scheme/method/path. Request origins add exact `http` or `https`.
Ports never mean any port; URI omission resolves to 80/443. Textual ports must
use canonical decimal spelling (no leading zero, sign or empty port).

Hosts are exact by default. Only explicit `*.example.com` means any depth of
subdomains excluding the apex; no other glob or regex syntax exists. Wildcards
require a multi-label DNS suffix, not an IP. Host inputs and resulting ASCII
names are bounded to 253 bytes (excluding the wildcard operator). IDNA lookup
mapping produces lowercase A-labels, followed by strict ASCII DNS-label checks
(1–63 bytes, no edge hyphen); trailing dots, zones, numeric final DNS labels,
hexadecimal final labels and alternate IP forms reject. Standard IP literals use
`netip` normalization and IPv4-mapped addresses are unmapped. Host selectors,
credential boundaries and concrete targets use the same normalization.

Methods are either `{any:true}` or `{values:[...]}` with 1–32 unique methods.
Method tokens contain 1–32 uppercase ASCII letters, digits or HTTP token
punctuation; lowercase is rejected rather than case-folded. CONNECT is a
transport operation, never a request-method selector. Path is `{kind:"any"}`,
`{kind:"exact",value:"/path"}` or `{kind:"segment_prefix",value:"/path"}`.
Segment prefix includes the named path and slash-delimited descendants, not
lexical siblings; a trailing prefix slash is normalized away except at root.

The v1 path grammar deliberately admits only slash and ASCII unreserved bytes.
Unreserved percent escapes decode once; empty URI path becomes `/`. Encoded
separators, percent/double escaping, reserved delimiters, controls, non-ASCII
paths, repeated separators and literal/encoded dot segments reject rather than
being matched one way and forwarded another. This conservative subset is not a
claim to accept every legal URI. Fragments, userinfo and opaque URLs reject.
Query remains bounded, syntactically valid opaque forwarding data, never policy
or decision evidence; headers and bodies are not selectors.

`ParseConnect` requires explicit authority and matching Host. `ParseRequest`
requires an absolute HTTP/HTTPS URI and matching Host with effective ports; an
intercepted request also supplies the CONNECT destination, must be HTTPS and
must agree with that destination. SNI, when supplied, must normalize to the same
host. The ingress adapter must reject duplicate Host fields and construct an
absolute URI from origin-form only using its bound CONNECT coordinates, never
client forwarding headers. Canonical values have private fields and fresh URL
projections. Eventual forwarding must use their authority, method and path, not
reparse the original input; round-trip tests pin the intended Go HTTP coordinates.

### Credential and address authority

Only request allows may select one credential and only for HTTPS. Snapshot
construction proves the grant's **entire** origin scope is contained in the
credential's bounded union of HTTPS origins, including effective port and
wildcard/apex distinctions. Intersection is insufficient; a finite collection of
exact hosts cannot cover a wildcard subtree. Missing credentials or failed
containment invalidate the snapshot. Plain/default allows do not compete with
injection. Matching requirements for the same credential are compatible;
different credentials reject. Unavailable selected material rejects with no
uninjected fallback; later acquisition failure must do the same without retry.

`allow_private` permits RFC1918, IPv6 ULA and host loopback only within a matching
allow grant for the current principal. Request permission cannot come from a
tunnel grant or a different path; tunnel permission cannot come from a request
grant. The global default does not confer it. Link-local, metadata (including
`fd00:ec2::254`), unspecified, multicast, unsafe IPv4 reserved ranges and
Gateway-owned listener endpoints always reject. IPv6 transition/translation and
unallocated space are conservatively refused: outside ULA/loopback, only global
`2000::/3` excluding `2001::/23`, documentation and 6to4 ranges qualifies.

The trusted proxy owner supplies complete `AddressFacts`: all DNS answers and
all composition-owned listener IP/port pairs. Every candidate must pass; a mixed
public/forbidden answer rejects rather than falling back to a passing subset.
Literal targets must agree with supplied IPs. Missing/oversized facts reject an
otherwise allowed access. The evaluator does no DNS or connection work. The
proxy must pin checked IPs, prohibit rebinding, recheck every request on pooled
connections, and prevent redirects/retries from inheriting authority. Listener
facts must include aliases/resolved endpoints; their completeness is an adapter
obligation, not something this pure function can discover.

### Bounds and explanation evidence

V1 bounds are 16,384 policy JSON bytes, depth 8, 4,096 grants per snapshot, 256
credential facts with at most 64 origins each, 8,192 URI bytes, 4,096 path bytes,
32 methods of 32 bytes, 253 host bytes, and 64 resolved addresses/listener
endpoints each. Compilation validates all supplied grants, even nonmatching or
foreign-principal entries, then copies an immutable principal-specific snapshot.
Matching uses bounded linear scans and suffix/segment comparisons, no regex or
network-dependent work. Sorting grant IDs chooses stable evidence only, never
policy priority.

Typed decisions include fixed dialect version, principal ID/revision, coherent
policy revision, default revision, transport, allowed bit and a closed reason.
They retain at most four grant ID/revision references (decisive, private and two
credential sources) and two credential references, including conflicts. They
never include raw targets, query, headers, bodies, addresses, secrets or error
strings. Interception carries `allowed:false` because it is not upstream
permission. These are safe admission-time facts, not a lease or proof of durable
admission, current credential material or successful execution.

The proxy remains cooperative, not network-enforced egress containment. Broker
migration/retirement, HTTP self-service, intercepted WebSockets, HTTP/3,
query/header/body matching, traffic-to-grant shortcuts, retries/replay and
multiple-secret injection are outside this component.

## Persisted HTTP authority

Schema 20 adds one revisioned HTTP default per principal, backfilled to block and atomically seeded to block for every new principal. A dedicated resource preserves Principal/PrincipalCreation, MCP visibility/default_grant and the singular credential slot unchanged. HTTP defaults never supply credential, tunnel or private-network permission.

The sole authorization repository owns up to 4,096 retained HTTP grants, including expired rows. Create and complete in-place edits compile and persist canonical v1 selectors; stable IDs/principals, optional bounded descriptions, canonical expiry and creation/update timestamps are validated on reads and startup. Edits and deletion require exact current resource revisions. All mutations run under the existing authority-before-storage ordering, advance the shared authorization revision and commit required audit atomically. Consequently a pending confirmation at an older global revision fails without reevaluation. HTTP policy evidence represents the zero-based shared sequence plus one; it is not a second authority or policy dialect version. There is no evaluated-decision cache.

Grant writes validate whole-scope credential containment on the same SQL writer used by credential edits/deletion. The authorization-owned ReferenceInspector includes every retained reference, even expired grants. Referenced credentials reject deletion, recipe changes and scope edits that no longer contain every grant. The credential owner supplies safe metadata/availability facts on caller-owned snapshots, never nested views, DNS or secret resolution. Startup and staged backup validation check complete defaults, canonical policy, principal existence, capacity and credential references. Restore retains policy while invalidating material authority; unavailable material is valid retained configuration, not permission to fall back uninjected.

Preview and the ingress-facing authenticated-lease seam load the same coherent policy snapshot and call the same pure selector. Preview is never admission authority; the ingress-facing result is also evidence only, and this delivery exposes no HTTP execution/detachment path. A future proxy must seal and confirm its traffic receipt and exact credential material generation before dispatch. Pure policy eligibility is followed by complete address checks for ingress evaluation. Preview classifies literal addresses without lookup and explicitly leaves network/TLS/material unverified. No submitted target/path/query is retained in audit, diagnostics or events; persisted grant selectors are administrator configuration, not observed traffic.

## Principal and grant contract

Grant and access-request operator routes belong to MCP under `/api/v2/mcp/`; principals and credentials remain shared. This is a route-only cutover: persisted rows, IDs, descriptions, policy/dedupe bytes, ETags, audit vocabulary, `authorization`/`grant_requests` invalidations, backup lineage, and `mcp_gateway` self-service names and schemas retain their identities. No migration or protocol registry is introduced. The retired unnamespaced grant/request/validation paths reject before authentication or domain work, without redirects or inferred replacement operations.

| Method and pattern                          | Closed request schema | Success schema/status             | Cursor | Idempotency | Exact `If-Match` | Response ETag |
| ------------------------------------------- | --------------------- | --------------------------------- | ------ | ----------- | ---------------- | ------------- |
| `GET /api/v2/principals`                    | `PrincipalListQuery`  | `QueryPage<Principal>` / 200      | yes    | no          | no               | no            |
| `POST /api/v2/principals`                   | `PrincipalCreate`     | `PrincipalCreation` / 201         | no     | no          | no               | yes           |
| `GET /api/v2/principals/{id}`               | `None`                | `Principal` / 200                 | no     | no          | no               | yes           |
| `PATCH /api/v2/principals/{id}`             | `PrincipalPatch`      | `Principal` / 200                 | no     | no          | yes              | yes           |
| `POST /api/v2/principals/{id}/credential`   | `EmptyObject`         | `AgentCredentialCreation` / 201   | no     | no          | yes              | yes           |
| `DELETE /api/v2/principals/{id}/credential` | `EmptyObject`         | `Principal` / 200                 | no     | no          | yes              | yes           |
| `GET /api/v2/mcp/grants`                    | `GrantListQuery`      | `QueryPage<GrantTableItem>` / 200 | yes    | no          | no               | no            |
| `POST /api/v2/mcp/grants`                   | `GrantCreate`         | `Grant` / 201                     | no     | no          | no               | yes           |
| `GET /api/v2/mcp/grants/{id}`               | `None`                | `Grant` / 200                     | no     | no          | no               | yes           |
| `PATCH /api/v2/mcp/grants/{id}`             | `GrantPatch`          | `Grant` / 200                     | no     | no          | yes              | yes           |
| `DELETE /api/v2/mcp/grants/{id}`            | `None`                | `Empty` / 204                     | no     | no          | no               | no            |

Principal and grant collection cursors are authenticated with a fresh process-local repository key and expire five minutes after the first page. Continuations do not extend that lifetime. Restart, expiry, or alteration makes the cursor stale; clients must start a new traversal rather than persist cursors. Every ordinary request, including one without query settings, uses the same normalized query path and count envelope.

The principal collection accepts singleton nonempty `cursor`, `limit`, `name`, `state`, `visibility`, `sort`, and `direction` query members and returns no ETag. Omitted sort uses name ascending, with ID ascending ties. Principal sort keys are `name`, `id`, `state`, and `visibility`; direction is `ascending` or `descending`. The `name` filter matches display name or literal ID, and state and visibility use their closed resource values; create/read/PATCH use the sole composition-owned repository and exact strong principal ETags. Create accepts only required non-null display name and visibility and returns the principal plus its atomic ordinary default grant.

PATCH accepts a nonempty non-null subset of display name, state, and visibility; absent, weak, wildcard, malformed, multiple, wrong-principal, or stale preconditions fail safely, and exact no-ops conflict without invalidation. Successful mutations publish one ID-free authorization invalidation after commit.

The singular credential route accepts exact `{}` plus the current principal ETag: POST issues or replaces the slot and returns `AgentCredentialCreation` with the raw bearer exactly once, while DELETE revokes current authority and returns the safe principal. Both advance principal and credential revisions, expose the resulting ETag, and publish only the same ID-free invalidation after an acknowledged success; failures and retries never replay a bearer.

Grant list/create/read/PATCH/delete use the same sole authority and a servers-owned supplied-transaction target callback. Creation requires every legacy member, with optional Boolean `read_only` as described below, including nullable `description`, `upstream_name`, `constraint`, and `expires_at`; descriptions are valid UTF-8, 1–256 bytes when present, free of control characters and surrounding whitespace, and need not be unique. PATCH accepts only `description`, requires the exact strong grant ETag, and may clear the description with null. Listing retains singleton `principal_id` and `server_id` filters and additionally accepts `identity` (description or ID), `principal` (display name or ID), `target` (server display name, scope/tool name, or server ID), `effect`, `state`, `sort`, and `direction`. Grant sort keys are `id`, `description`, `principal`, `target`, `effect`, and `state`; target ordering uses server display name. Omitted sort uses description ascending, with ID ascending ties. Every collection item is exactly `{grant,principal_display_name,server_display_name}` and supplies recognition labels without browser collection traversal. The retired `representation` query member is rejected.

Text query values are valid UTF-8, at most 256 bytes, with no control or format characters. Text matching removes Unicode combining marks after NFKD normalization, lowercases, and requires every whitespace-separated token to match a substring or (for nondigit tokens of at least four characters) a word within one edit, including adjacent transposition. ID matching remains literal case-sensitive substring matching. Text sorts use normalized Unicode code-point order, with ID ascending as the deterministic tie-breaker. For both collections, `direction` requires an explicit `sort`; omitting `direction` selects ascending order. All filters are conjunctive; omitted or empty application controls impose no filter, while empty API query values are rejected.

The sole operator query path scans compact recognition metadata bounded by the fixed principal/grant capacities, not full credentials or policy bodies. The servers owner supplies bounded display-name facts in the same read transaction; authorization owns principal/grant metadata, matching, ordering, and selected-page resource reads. The query response is exactly `{items,next_cursor,total_count,offset}`. `total_count` is the exact number of matching records before page slicing, and `offset` is the zero-based position of the first returned row. Both are nonnegative JSON integers derived from the same filtered snapshot and read transaction as the selected page, including when `next_cursor` is null. An empty result has zero total and offset. Computing this metadata adds no count query or full-resource hydration. This envelope also applies without filters, including grant lists using only `principal_id` or `server_id`. Its authenticated cursor binds the complete query, representation, and metadata snapshot; relevant edits, insertions/deletions, renames, or derived expiry-state changes make continuation stale instead of silently moving rows between pages. Previous navigation can reuse a prior cursor while that snapshot remains current. There is no implicit legacy operator path.

Grant identity and policy are immutable and expose no idempotency surface. Create, read, and description-only PATCH return the grant ETag; PATCH requires that exact current value. Successful create, description update, and delete publish only the ID-free authorization invalidation.

`AgentCredential` is exactly `{id,fingerprint,revision,created_at}`. `Principal` is exactly `{id,display_name,state,visibility,revision,credential_revision,credential,created_at,updated_at}`; its credential is nullable. `PrincipalCreation` is exactly `{principal,default_grant}`, and `AgentCredentialCreation` is exactly `{principal,bearer}`. `Grant` is exactly `{id,description,revision,principal_id,effect,server_id,upstream_name,constraint,expires_at,state,created_at}`. The self-service `AgentGrant` projection is exactly `{id,description,effect,policy,expires_at,state,created_at}`. Authorization evidence is exactly `{decision,authorization_revision,evaluated_at,grant_id}`. Principal state is `active` or `disabled`; visibility is `requestable`, `allowed-only`, or `all`; grant effect is `allow` or `deny`; derived grant state is `active` or `expired`; and authorization decision is `allow`, `deny`, or `block`. The reserved synthetic identity is ULID `00000000000000000000000000` with namespace `mcp_gateway`. The principal ETag is exactly `"principal-<id>-<revision>"`; the grant ETag is exactly `"grant-<id>-<revision>"`.

## Principal credentials, grants, and policy

Principal creation is one marker-armed transaction that inserts revision-1 active authority with credential revision 0, inserts exactly one ordinary permanent server-wide synthetic MCP ALLOW described by the unchanged `DefaultGrantName` bytes (`Default Gateway access`), and advances the authorization revision exactly once. Principal or grant capacity rejection and either required audit failure roll back principal, default grant, and authorization revision together. The default grant permits Gateway's six fixed MCP self-service tools, not downstream tools or future protocols; it remains an ordinary deletable grant subject to matching DENY, not an authentication bypass or hidden entitlement. PATCH advances the principal revision once for any actual change; display-name and visibility changes do not alter authorization revision, while each active/disabled transition advances authorization revision once. Disablement clears the current credential slot and advances credential revision even when the slot is already absent; re-enablement restores neither credential nor deleted grant. Empty, stale, and exact no-op mutations fail without durable change, and principal identities are permanent.

Credential issue and replacement require the exact active principal revision, consume independent 32-byte entropy plus a fresh ULID, and return `mgw_agent_` plus raw unpadded base64url exactly once. SQLite retains only the ID, issuance time, `SHA-256("mcp-gateway/agent-verifier/v1\x00" || bearer)`, and the first eight lowercase-hex bytes of `SHA-256("mcp-gateway/agent-fingerprint/v1\x00" || verifier)`. Publication replaces the singular slot and advances principal and credential revisions once under the agent-candidate recovery marker; explicit revoke clears a present slot and advances both once under an ordinary marker. Neither changes authorization revision. Preparation or known rollback preserves prior authority. Any unacknowledged committed candidate returns no bearer, leaves online storage latched, and stopped recovery invalidates the exact candidate without restoring its predecessor.

Ordinary grants are immutable rows for identity and policy; optional descriptions are mutable display metadata, and rows are removed only by DELETE. Creation accepts a permanent or strictly future expiry, server-wide scope without a constraint, or exact scope with an optional compiled constraint; an exact upstream name needs no descriptor. It checks the fixed all-row capacity and the non-deleted server target through the servers-owned callback inside the same transaction, then advances authorization revision once. Description-only PATCH advances only the grant revision: it does not advance authorization revision, cancel admitted leases, or alter policy. A server deletion ordered later leaves the grant readable but inapplicable; one ordered first rejects creation. Expired rows remain counted, readable, and deletable, and no issuer, reason, history, revocation, or idempotency machinery exists.

Policy evaluation parses one bounded token-preserving argument object, captures one UTC timestamp, and reads the authorization revision and every grant for the principal from one coherent transaction. Every loaded row is validated before a result can escape. Matching DENY takes precedence over matching ALLOW, otherwise evaluation returns BLOCK; DENY and ALLOW evidence names only the lexicographically smallest matching grant, while BLOCK names none. Expiry is strict at the captured timestamp. Malformed arguments, invalid loaded policy, matcher failure, or unavailable storage fail closed without a partial decision or constraint data.

## Read-only server ALLOWs

`GrantCreate`, `Grant`, request `Policy`, and agent `GrantPolicy` accept or preserve the optional Boolean `read_only`. Omission or `false` means unrestricted legacy policy; reads omit the member for unrestricted policy and emit `"read_only":true` for restricted policy. Null, strings, numbers, and unknown members are invalid. This selector is separate from argument constraints. True is valid only for a server-wide ALLOW (`upstream_name:null`, `constraint:null`) or a server request (`scope:"server"`, `constraint:null`, `future_tools_acknowledged:true`). DENY and exact-tool grants cannot carry the restriction. Explicit false does not change those legacy combinations.

This ALLOW applies only when the current Gateway-published descriptor explicitly declares `annotations.readOnlyHint=true`. Missing, null, and false hints do not qualify; malformed catalog annotations still fail closed. No names or other hints imply eligibility. Hints are trusted server declarations, not side-effect isolation. Other ALLOWs may authorize writes, matching DENY still wins, and expiry and revocation are unchanged. Allowed-only discovery uses the same descriptor eligibility; requestable/all visibility is unchanged. Fixed synthetic tools use their compiled descriptor hints under the same grant semantics.

Invocation copies eligibility alongside the validator and capability from one published target. Authorization evaluates that pinned fact, never client metadata or approval-time evidence. Catalog refresh automatically includes future qualifying tools and changes eligibility for subsequent calls. Replacement between resolution and dispatch fences the old capability; it cannot reroute or reuse authority against a replacement descriptor. Existing admitted-call and one-shot execution rules remain unchanged. The standalone authorization evaluator has no descriptor authority and cannot apply read-only ALLOWs.

Approval must retain a submitted read-only restriction. The matrix is:

| Submitted           | Approved server unrestricted | Approved server read-only | Approved exact tool      |
| ------------------- | ---------------------------- | ------------------------- | ------------------------ |
| Server unrestricted | yes                          | yes                       | existing narrowing rules |
| Server read-only    | no                           | yes                       | no                       |
| Exact tool          | no                           | no                        | existing narrowing rules |

Read-only server-to-tool narrowing is forbidden even for a currently read-only tool: an unrestricted exact-tool grant could later authorize a changed descriptor. Existing duration narrowing and explicit future-tool acknowledgement remain required. Conservative active-DENY checks do not inspect annotations: any active DENY on the server conflicts, including an exact write-tool DENY. Approval atomically stores the approved restriction and resulting grant with the existing timestamp, revision, and invalidation semantics.

Read-only requests use dedupe version 3, the existing server-request frame with prefix `MGWGRQ3\x00`. This version is reserved for read-only server policy. Legacy unrestricted requests retain their exact version-1 or version-2 bytes; explicit false and omission share identity. Identical read-only requests dedupe under the existing duplicate-first rules, while unrestricted and read-only requests remain distinct. Schema 16 preserves the restriction across restart and rejects malformed durable combinations before readiness.

## Policy constraints

Constraints compile only the closed permanent versioned shapes described here. Untagged `{equals:{pointer:scalar}}` constraints are permanent version 1. Version 2 is the closed `{version:2,equals?:{pointer:scalar},regex?:{pointer:pattern}}` form and requires at least one atom across both operator maps. Equality compilation retains each valid number's original token; regex compilation accepts bounded Go RE2 patterns and anchors them to the full JSON string value. Both versions decode RFC 6901 `~0` and `~1` into immutable object-member segments and permit empty/numeric segments and pointer-prefix pairs. Evaluation traverses objects only: arrays are never indexed, regex never matches a non-string, all atoms are conjoined, and no coercion, numeric normalization, schema interpretation, regex implication, or overlap analysis occurs. Durable startup, grant reads, and evaluation reject unknown versions or any constraint that cannot be compiled under the fixed byte, aggregate atom, pointer, regex pattern, per-pattern and 256-instruction aggregate compiled-program, cumulative regex-work, and JSON-depth bounds. Combined with the fixed 4,096-grant and 4,096-request retention limits, the aggregate constraint limit caps retained regex programs at 2,097,152 instructions; invocation evaluation reads only grants structurally applicable to the requested server/tool and its read cache separately bounds retained programs across snapshots and projections. Each regex execution charges the nonzero input-byte count multiplied by compiled RE2 instruction count, so empty strings and complex programs cannot bypass the shared evaluation budget.

### Compiled read cache

The sole authorization repository shares one process-local exact-byte cache between evaluation, structural discovery/DENY validation, administrator grant projections, and admitted-subject grant projections. A key is the complete bounded source string, never a hash, grant ID, or revision. Successful entries survive revision changes: compilation is a pure function of immutable bytes and the process's fixed dialect/limits, not principal authority, time, or transaction state. Every caller still selects and validates its own rows in its coherent SQLite snapshot. No decision, grant metadata, expiry, subject, or snapshot is cached. Older snapshots can reuse the same bytes without seeing newer rows; changed or malformed bytes must compile independently. Returned projection JSON is copied, and no compiled program escapes a transaction-owned read.

The applicable deduplication lifetime starts when a source flight is installed and extends through that successful entry's residence. One source has one flight; unrelated cold sources compile outside the cache mutex. Identical readers join the flight without allocating another bookkeeping entry. Failed compilations are not retained, and successful entries are evicted in insertion order (hits do not extend residence). After failure or eviction a later call may recompile; readers already holding the completed flight still receive its immutable result. Cross-revision retention avoids whole-cache churn without retaining generations or snapshot owners.

Resource accounting is shared, not multiplied by read surface or revision:

- At most 4,096 resident entries and 128 MiB of charged weight. An entry charges its exact key bytes plus 256 bookkeeping units, 64 units per raw source byte, 512 per atom, 64 per RE2 instruction, and four per rune slot in the compiled syntax program. Source-scaled charging includes decoded strings, pointer segments and scalar representations; rune slots matter because Unicode classes are not bounded by instruction count alone. Weight is a conservative accounting metric, **not an exact Go heap/RSS measurement**. The map/list cannot retain more than 4,096 entries; flight maps cannot retain more than four keys.
- Every cache source is checked against the existing 8,192-byte limit before lookup, cloning, or flight creation. Successful programs retain at most 16 atoms and 256 aggregate instructions. Per-instruction rune lists are finite normalized Unicode ranges (at most `2 * (unicode.MaxRune + 1)` rune slots); this supplies a finite successful-program bound even for an entry too large for the weight budget. With `S=8192`, `A=16`, `I=256`, and `R=I*2*(unicode.MaxRune+1)`, one successful source has at most `P=S+256+64*S+512*A+64*I+4*R` accounting units. This deliberately loose ceiling is independent of eviction and history.
- All production cache callers execute within the single storage owner's four-connection pool, including supplied mutation transactions. Each walks rows serially and discards compiled state rather than appending it to result pages/policies. Allowing both a previous row and its replacement to remain live gives at most eight additional successful-program references, including old snapshots, completed flights and evicted entries. Thus reachable successful compiled state is bounded by the resident 128 MiB budget **plus at most eight `P` units**, not by the resident budget alone. Sharing a reference does not duplicate its program. This contract forbids retaining compiled values outside these transaction-owned reads or constructing a second production repository/cache.
- There are at most four in-flight compilations, explicitly enforced as well as bounded by storage occupancy. Their source keys total at most 32 KiB; completed/failed flights are removed immediately, and there is no retained waiter list or failure history. Compilation scratch is additional to successful-program weight: strict JSON uses the existing byte/depth limits; each RE2 source is at most 1,024 bytes, and Go 1.26's `regexp/syntax` parser/compiler additionally bounds instruction storage and parse-tree rune storage to 128 MiB each, tree height to 1,000, and repetition expansion to 1,000. These are per-expression library accounting limits, not a claim that total scratch heap equals 256 MiB. At most four such bounded compile operations run concurrently; malformed inputs do not bypass this limit. The public compiler's mutation/request/startup owners remain unchanged and do not acquire an extra read cache.

At resident capacity, evict the oldest entries until both bounds admit the new successful entry. An individual entry exceeding the weight budget is returned to current readers but not retained, so it can recompile after its flight ends. No capacity path bypasses validation or returns a partial authorization decision. A fifth distinct flight fails closed with the existing resource-limit error rather than spawning an unbounded uncached compile or adding a queue; ordinary production readers already occupy at most four storage connections. Compiler failures retain their existing caller-specific malformed/unavailable mapping. Public limits and policy decisions are unchanged.

#### Cache measurements and regression evidence

Baseline: `4b0ed12572a951d1be8a2b53d5fcc45c24dd4400`, with only a package-private compiler counter seam and test workload added before changing the cache algorithm. The seam forwards to the unchanged `CompileConstraint`; administrator/self scanners were receiver-bound to count their existing direct compiles. `TestMatcherCacheReadMeasurements` seeds 100 distinct `item/<n>/[0-9]+` regex grants in real SQLite and reads ten 100-row pages independently through `ListGrants` and `ListSelfGrants`. `TestMatcherCacheRevisionMeasurements` loads the same 100 sources across ten revisions. Run from the checkout with:

```sh
go -C agent-gateway test -race -count=1 -timeout=90s -v -run '^TestMatcherCache(Read|Revision)Measurements$' ./internal/authorization
```

| Workload                                     | Baseline compiles | Shared-cache compiles |
| -------------------------------------------- | ----------------: | --------------------: |
| Admin: 10 × 100-row pages                    |             1,000 |                   100 |
| Self-service: 10 × 100-row pages             |             1,000 |                   100 |
| Evaluation cache: 100 sources × 10 revisions |             1,000 |                   100 |

The baseline failed the retained 100-compilation assertions; the shared cache passes them (90% fewer compilations, zero recompilation on the nine repeated reads/revisions). This deterministic result, rather than a wall-clock speed claim, supports cross-revision retention. `TestMatcherCacheColdConcurrencyAndExhaustion` holds four unrelated compilers open simultaneously, verifies the fifth distinct source cannot allocate a flight, and verifies four identical older-revision readers add no compilation. The baseline's repository-wide compile mutex permits only one compiler at a time. Exact-byte identity, entry/weight eviction, old-reference safety, coherent older SQLite snapshots and malformed durable reads are covered by the race-enabled `TestMatcherCache*`, `TestCompiledConstraintCache*`, and `TestSelfProjection*` tests.

## Self-service grant requests

### Surface and public contract

Administrative and agent procedures for these contracts are canonical in [access control](../operators/access-control.md). The self-service contract implements exactly six self-only descriptors under `mcp_gateway`: `get_identity`, `list_grants`, `create_grant_request`, `get_grant_request`, `list_grant_requests`, and `cancel_grant_request`. Their compiled synthetic catalog extends discoverable cardinality to 2,054 through `discoverable_tools`=2054 without changing downstream `active_tools`=2048. The request vocabulary is closed over `pending`, `approved`, `rejected`, and `cancelled`; request policy scope is `tool` or `server`, duration is permanent or a canonical 60 through 2592000 seconds, and approval/cancellation never replays an original invocation.

Approval accepts an optional valid grant description alongside the approved policy. The administrator resources are `GET` `/api/v2/mcp/grant-requests`, `GET` `/api/v2/mcp/grant-requests/{id}`, `POST` `/api/v2/mcp/grant-requests/{id}/approve`, and `POST` `/api/v2/mcp/grant-requests/{id}/reject`. Their mechanics use `GrantRequestListQuery` → `QueryPage<GrantRequestTableItem>`, `None` → `GrantRequest`, `GrantRequestApproval` → `GrantRequest`, and `GrantRequestRejection` → `GrantRequest`; item/adjudication resources use exact ETags and adjudication requires a precondition. The [public contract](public-contract.md) owns the exact safe failures and fixed bounds. Authenticated status exposes only global request-row and request-evidence-byte occupancy.

### Request policy and storage

The ID-free `grant_requests` invalidation is snapshot-recovered and never replayed. These resources are composed into the production binary.

The ordinary permanent synthetic server-wide default ALLOW makes all six tools discoverable and callable under normal DENY-overrides semantics until an administrator deletes it; synthetic descriptors consume no durable or active downstream-catalog capacity or admin catalog state. Synthetic catalog revision 4 and the pinned `create_grant_request` descriptor advertise nullable constraints, permanent version-1 equality, and version-2 equality/full-string-RE2 operators plus the optional Boolean `read_only` server restriction so agents can detect policy support before mutation. Transaction ownership remains narrow: servers resolve extant or tombstoned namespaces on a caller-supplied transaction, catalog returns revalidated current/retired durable descriptor facts on that same transaction, and authorization returns only a conservative owner-principal active-DENY overlap bit.

The request-policy primitive compiles the versioned lexical constraint language. Untagged constraints retain their version-1 dedupe identity with decoded pointers and original number tokens; version-2 constraints use a distinct canonical identity that includes each operator, decoded pointer, exact scalar token, and exact regex pattern. Approval must retain every submitted atom exactly and may add conjunctive atoms; version 1 may narrow to version 2 when every equality is retained, version 2 may not narrow to version 1, and regex implication is never inferred. The primitive also bounds canonical descriptor evidence, derives expiry from the approval timestamp, and permits only the closed scope/constraint/duration narrowing matrix. None of these seams opens, commits, or rolls back a transaction.

The request repository is the sole online schema-10 DML owner; schema 10 stores no bearer, invocation link, successful result, raw error, free-form reason, or reviewer identity, and bounded descriptor evidence is admin-item-only: it resolves names and semantic duplicates before current-target, evidence, DENY, and capacity work; retains permanent ID tombstones; evicts only the oldest terminal prefix needed for row/evidence pressure; and publishes one ID-free request invalidation only after acknowledged commit. Existing duplicates and closed rejections roll back the read transaction without consuming identity, refreshing evidence, evicting history, or emitting events; uncertain post-commit completion latches storage and returns no success.

### Agent projections and cursors

An acknowledged ALLOW detachment also mints one sealed admitted subject containing only principal/credential IDs and revisions plus the evaluated authorization revision. Authorization-owned self projection reads accept no principal selector: they reverify that subject against the current credential while coherently projecting only safe identity fields and insertion-watermarked ordinary ALLOW/DENY grants, preserving exact constraints and derived active/expired state while resolving synthetic, extant, or deleted immutable target namespaces through a servers-owned supplied-transaction inspector.

Agent request get/list reads derive only the admitted subject's stable principal ID and select no internal target, dedupe, or evidence columns; missing and foreign IDs are the same result, pages pin the owner's insertion watermark, and later inserts cannot enter. Self-service owns one process-local HMAC cursor codec whose fixed canonical frames bind method, subject IDs and revisions, exact state filter, process generation, watermark, and position: malformed syntax is rejected before handler work, unauthenticatable frames are invalid, and authenticated cross-boundary frames are stale.

### Cancellation and adjudication

Request-owned cancellation and rejection are exact pending-to-terminal revision-2 transitions with one transaction timestamp; cancellation is owner-only and idempotent, while rejection checks revision before terminal conflict. Closed/no-op reads consume no clock or event, successful transitions publish one ID-free request invalidation only after acknowledged commit, and uncertainty reports no success.

### Startup validation and approval

Before readiness, the request repository coherently validates every retained public state plus permanent identity, owner existence, immutable extant/tombstoned target, versioned dedupe bytes, canonical submitted/approved evidence linkage and aggregate, and capacities through narrow supplied-transaction facts. Approved grant IDs remain shape-validated historical evidence without a live-grant dependency.

Atomic approval is authorization-owned: its exclusive authority gate, using the shared bounded authority admission and wait policy, encloses one storage mutation, current owner/DENY/grant-capacity checks, one timestamp-derived ordinary ALLOW insert with optional administrator description, one request-owned exact-row callback, and one authorization revision advance. The callback validates current ETag before terminal conflict, resolves immutable target and narrowing, captures server-to-tool evidence under aggregate capacity, and writes revision 2 with the same timestamp and grant ID.

Only acknowledged completion emits request then authorization invalidations; known failure rolls back both domains and post-commit uncertainty reports no success or event. The injected administrator API adds strict summary-list, evidence-bearing item, approve, and reject resources with a distinct filter/watermark cursor and exact strong request ETags.

The ordinary operator collection returns `{items,next_cursor,total_count,offset}`, with items exactly `{request,principal_display_name,server_display_name,resolved_server_id,resolved_upstream_name}` and no descriptor evidence. The nested request remains the unchanged summary shape; target identity always describes the submitted request, not a later narrowing. Missing labels fall back to qualified IDs. Agent reads and item-resource evidence remain unchanged. Operator default order is submitted insertion sequence descending; there is no `representation` mode switch.

The sole operator path accepts singleton bounded `request`, `principal`, `target`, `scope`, `sort`, and `direction` alongside `principal_id`, `state`, `limit`, and `cursor`. Scope is tool/server; sort is request/principal/target/state/submitted; direction is ascending/descending and requires sort. Text filters are valid UTF-8, at most 256 bytes without control/format characters. Request IDs use literal substring matching; principal and target match literal IDs or every case-insensitive whitespace-separated label token. Filters are conjunctive. Submitted order uses insertion sequence; label sorts use lowercase code-point order with ID-ascending ties.

Request-owned queries scan only compact metadata within the fixed request capacity. Authorization and servers supply bounded identity labels in the same transaction; only the selected page hydrates policy summaries, never descriptor evidence. Counts and offsets derive from the same filtered snapshot as rows. The HMAC cursor binds the complete query and recognition snapshot, expires after five minutes without renewal, and uses a fresh process-local key. Settlement, relevant identity edits, insertion/retention changes, query changes, expiry, or tampering reject continuation as stale rather than silently moving rows or counts. No cursor or draft is durably persisted.

Items compare the selected requested-or-approved target at read time across namespace tombstones, durable descriptors, and a narrow active-state fact. Adjudication responses retain pre-transition immutable item facts and transaction-returned approved evidence, avoiding a racy post-commit row lookup after terminal retention becomes eligible.
