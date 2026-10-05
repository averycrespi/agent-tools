# Invocation and Ingress

Audience: Maintainers and contributors changing governed invocation, retained evidence, and MCP ingress

Authority: Normative product design

This chapter owns the behavior and invariants described below. Operational procedures remain in the linked guides; exact executable contract values remain owned by `internal/contract` and must agree with this chapter.

## HTTP traffic evidence

HTTP admission uses the same registered agent lease, short authority gate and
selected traffic writer as MCP. Under one coherent control snapshot it captures
one decision time, principal and singular agent-credential revisions, default and
policy revisions, at most four deduplicated deciding grant references with their
canonical configured selectors, transport, and the selected injection credential
metadata/material generation. These facts never join current grants on reads.
Only canonical origin/method or CONNECT host/port is observed evidence: no observed
path, query, header, body, address list, bearer, secret, upstream error or response
content enters storage or public projections. Configured matched selectors are
historical policy, not observed request paths. An opaque tunnel exposes no inner
HTTP requests.

The gate and control read end before material acquisition. Sealed request-local
candidates are confirmed once against the exact active binding,
shared policy revision and selected material generation. HTTP credential edits,
rotation, fence activation and deletion share a nonqueueing material guard held
only for metadata revalidation and final detachment, never keyring I/O. Registry
drain, control health and original request cancellation fence detachment; optional
history health does not.
Failed admission or confirmation never dispatches, reevaluates, retries or falls
back to uninjected access. Unparseable authenticated requests retain binding,
identity/time and `invalid_request`, with no inner target or policy. New engine
rejections additionally carry a closed `rejection` stage/reason pair: `headers`
(`invalid_headers`, `trailers_unsupported`, `upgrade_unsupported`,
`inner_proxy_authorization`), `request_form` (`connect_body`, `nested_connect`,
`origin_form_required`, `absolute_http_required`), or `target`
(`invalid_target_syntax`, `target_too_long`, `forbidden_path`,
`authority_mismatch`, plus historical `invalid_request_target` and
`invalid_connect_target`). Syntax includes malformed escapes; forbidden paths
include controls, backslashes and dot segments. These categories describe
rules, never offending input or parser error strings; their encoded object is at
most 128 bytes. No partly parsed destination or unvalidated method is retained.

Inner requests carry optional `connect` context: the actual enclosing CONNECT
admission ID and its canonical host/port, captured in that connection's handler
closure after confirmed interception, when a capture identity is available. Every H1 request and concurrent H2 stream
reauthenticates and verifies the original principal/credential binding before
recording this context. It is inherited connection evidence, not validated inner
target evidence, authority, or a timestamp-based join. It remains meaningful if
retention later removes the parent row. Opaque tunnels expose no inner records;
older rows have no reconstructed correlation or rejection details. Unauthenticated or
unverifiable authority produces no durable HTTP row. Denials and interception
complete without upstream dispatch; interception is not permission for
an inner request. The public summary projects `interception_selected` from the
recorded interception decision, including historical rows whose stored outcome is
`not_dispatched`. This selection proves neither CONNECT acceptance, TLS establishment,
upstream dispatch, request completion nor connection closure; no new lifecycle
evidence is recorded. `Allowed=false` describes upstream-dispatch permission,
not general CONNECT failure. A denied CONNECT retains `block` / `not_dispatched`;
an allowed opaque tunnel retains `allow` and its recorded completion or unknown
outcome. A recorded allow that loses confirmation remains unknown,
not evidence of execution or a fabricated denial.

HTTP shares one bounded nonblocking observation queue, atomic batches, optional
history fault boundary and budget with MCP and Git. Execution owns material clearing,
stream settlement and one-use opaque-origin release independently of capture.
One optional self-contained completion observation records completion time,
closed outcome, optional 100–599 request
status, nonnegative byte counts and elapsed milliseconds. New request completions
also distinguish `response_source` (`gateway` or `upstream`); `status` remains an
upstream status and `gateway_status` is a separate Gateway-generated 400–599
response. This records response selection, not confirmed delivery. A selected
upstream status can survive an interrupted body with an unknown outcome. Rejection
admission identifies Gateway validation, not an upstream response or proof of
response delivery. Optional persistence failure cannot change the live status. Missing source on historical or incomplete evidence
remains unavailable; it is never inferred from outcome alone. Outcomes are `succeeded`,
`prestart_failure`, `upstream_failure`, or `outcome_unknown`. Missing terminal
remains unknown for an allow and never overrides a known live result. Completion
queue charges include the complete sanitized initial snapshot and bounded terminal
payload. No background terminal retry or replay is introduced.

New request completions optionally retain `termination`, a closed object of at
most 128 bytes within the unchanged 512-byte completion bound. `stage` names the
observed operation: `exchange` (before an upstream response is available),
`upstream_read`, `downstream_write`, `downstream_flush`, `deadline` (setting or
clearing a downstream write deadline), `response_headers` (HEAD), or `complete`
(clean body EOF and completed writes/flushes). `condition` is `clean`, `cancelled`,
`timeout`, or `failure`. Clean is valid only for `complete` or HEAD
`response_headers` with the existing `succeeded` outcome. All other conditions
retain `outcome_unknown`; `complete` cannot carry a failure. Exchange facts
accompany a Gateway-selected response; subsequent transfer facts accompany the
selected upstream status. Pre-dispatch preparation and opaque tunnels have no
response-transfer termination object.

On failure the operation error determines the condition: typed timeout/deadline
first, typed cancellation second, otherwise failure. A separate optional `context`
records the request-context snapshot (`cancelled` or `timeout`) when that error is
observed. It neither overrides the operation condition nor attributes initiation
or causal order; Gateway drain and peer closure can both cancel the context.
Deadline stage identifies deadline handling, not necessarily deadline expiry.
Clean records have no failure-context snapshot. EOF does not parse application
content or prove application success, and a terminal-looking SSE event followed by
cancellation is still incomplete. Error strings, content and cancellation origin
are never retained. Historical completions omit termination without backfill;
summary `completion_recorded` distinguishes their absence of details from missing
completion. Persistence failure retains the existing best-effort unknown boundary
without replay or a success claim.

## HTTP proxy engine

`internal/httpproxy` consumes the sole composition-owned authenticator, authority,
admission coordinator, HTTP material service, CA signer, remote factory and lifecycle.
It accepts a dedicated composition-selected listener. Bare `serve` defaults to
`127.0.0.1:8212`; `serve --http-proxy-listen` overrides the address and
`--clear-http-proxy-listen` explicitly disables it without loading CA signing
material. These selections are mutually exclusive. Canonical macOS managed
launches preserve omitted legacy HTTP configuration as disabled; new installs
persist the enabled address explicitly. The selected authority must be canonical
numeric IPv4 loopback, nonzero and distinct from administration/MCP. Both binds
and CA load must succeed before startup acknowledgement; default or explicit
enablement never silently falls back to MCP-only.
Administration, MCP routes and temporary OAuth callbacks are never proxy routes
or permitted upstream destinations, even with private-address permission.

Proxy authentication accepts one bounded `Proxy-Authorization` header: Bearer or
standard Basic with username `agent` and the existing agent credential as password.
Parsing is limited to 1024 bytes before decoding. All authentication-required
responses use a safe Basic 407 challenge, no-store and connection close. Administrator
credentials never authenticate, and proxy authorization never reaches an upstream.
Client proxy-URL environment exports are an explicit supported agent-secret sink,
read at shell startup from the private token file, never persisted in configuration.
No alternate credential slot is introduced.

Absolute-form plain HTTP uses request policy. CONNECT authenticates its original
bearer and selects either explicitly granted opaque TCP or local TLS interception.
Each intercepted H1 request/H2 stream freshly authenticates that bearer, pins its
original principal/credential identity and performs request-local admission/confirmation.
Origin requests neither supply nor receive proxy credentials. Later revocation
rejects new admissions without canceling admitted work. Interception itself grants
no upstream permission. Invalid authenticated coordinates retain only invalid-request
evidence, not the submitted URL. Parser-level malformed framing is rejected before
an authenticated request exists.

Outer absolute-form HTTP and CONNECT use request-target authority, ignoring a
conflicting raw Host as defined in the [authority contract](identity-and-authorization.md#canonical-selectors-and-forwarding).
That raw Host never controls policy, credentials, private permission or forwarding.
CONNECT fixes HTTPS authority; SNI and inner Host/H2 authority must agree through the policy
canonicalizer. Forwarding uses validated authority and the preserved escaped
path/opaque query, not the separate policy comparison path, forwarding headers
or an unvalidated raw URL. The [request compatibility contract](identity-and-authorization.md#canonical-selectors-and-forwarding)
owns syntax, conversions and byte bounds. Duplicate Host and malformed framing
are refused by the HTTP parser; accepted framing is reserialized on a fresh hop.
Intercepted upgrades/WebSockets and trailers reject; explicit tunnels are opaque
and never run inner request policy or credential injection. No HTTP/3 or TLS-error
fallback exists.

The remote owner resolves and pins at most 64 addresses once for complete-set
policy validation, and dials only after confirmed authority. A forbidden answer rejects the whole set;
it is never filtered into a more permissive subset. TCP connection establishment
tries these numeric IPv4/IPv6 addresses in resolution order, at most once per
candidate, within one nonrenewable ten-second total dial budget shortened by the
original request deadline. Each candidate receives at most an equal share of the
remaining time divided by the remaining candidate count. Fast refusal leaves its
unused time for later candidates; a stalled first address cannot consume every
candidate's share. This is bounded sequential connection establishment, not an
application retry, family race, fresh DNS lookup, reroute or additional authority.
Before each actual dial, the complete pinned set is rechecked against unconditional
exclusions, private permission and current composition-owned listener reservations.
Callback endpoints are reserved before binding. Private permission belongs only
to the selected request/tunnel grant.

There is one synchronous dial owner and no parallel candidate goroutine. Cancellation
stops further candidates, and failed, expired or cancellation-losing connections
close before return or the next candidate. HTTP/Git establish the connection in
the original request owner before handing it to the HTTP transport, whose detached
dial context must not prolong candidate work after request cancellation. Dial
implementations must honor context deadlines; a timer cannot kill an uncooperative
native call, and ownership is retained until it actually returns.

Only the winning connection is handed off once for TLS/application traffic. TLS
verifies the original hostname, not the numeric candidate; TLS failure, disconnect,
request-write failure or response timeout never selects another address. Each
request uses a fresh HTTP/1 upstream transport with a single-use connection handoff,
explicit H1-only protocol, no keepalive, proxy, compression, redirect client or body
replay. Thus H2 client streams cannot coalesce upstream connections or transfer
another principal/path's permission. Selected HTTPS credential material replaces
its single header only after admission and travels only on the approved winning
connection; missing material never falls back. An authorized upstream can itself
disclose any secret it receives; the proxy cannot prevent that disclosure.

Bounds are executable in `contract/http_engine.go`: 256 accepted connections, 128
active work owners, 96 per principal, 32 H2 streams per connection, 32 KiB streaming
buffers and header ceiling, 10-second dial/TLS handshake, 15-second headers,
60-second I/O inactivity, and 10-second caller drain. The H2 plaintext frame adapter
bounds incomplete frame/header phases without interpreting HPACK; HEADERS and
CONTINUATION share a nonrenewable header deadline. Between complete frames no
header timer caps a progressing stream. Existing H2 preface/SETTINGS and idle/ping
bounds remain. Rejections, uploads, response writes and cleanup are bounded;
request-upload teardown joins before completion evidence and handler return.
Occupancy permits 32 streams plus 32 tunnels with headroom, but is **not** capacity
qualification. Prior failed capacity evidence remains applicable.

Opaque tunnels expire one hour from admission, never renewed by traffic. Expiry
closes both sides and never reconnects; in-flight effects may be unknown. Intercepted
downloads/SSE have no blanket hard lifetime. Shutdown fences new work, closes owned
connections, and retains actual owner accounting until cleanup settles. CA material
is closed only after HTTP owners and their completion attempts settle, before the
shared traffic store closes. Timeout
reports unconfirmed cleanup, not permission to close storage underneath live work.
One completion attempt carries only safe status, byte counts, outcome and bounded
observed termination facts; interrupted
or uncertain dispatch remains unknown and is never replayed. Completion timestamps
use canonical UTC with exactly nine fractional digits, including on lower-precision
clocks; trimming trailing zeros violates the traffic store's evidence contract.

Bodyless responses retain status and permitted end-to-end metadata without synthetic
body writes. Initial header flushing is distinct from streaming body writes: even
an empty write is invalid for 204/304 on the H2 server. HEAD retains representation
metadata, including Content-Length, and leaves finalization to the HTTP server under
a checked finite write deadline. Its `succeeded` completion records a complete
upstream response and prepared downstream headers, not acknowledged client delivery;
later server-owned finalization errors cannot revise that completion. Observed
cancellation or transport errors remain failures/unknown outcomes, never ignored
normal-closure error strings or replay triggers. H1 retains Go's existing suppression
of 304 Content-Length (and Content-Type); 304 status, ETag and body absence remain
preserved. H2 can retain legal 304 representation length, which describes the selected
representation rather than bytes to be sent. This does not promise that every client
library interprets that metadata correctly.

### Live HTTP failures

Gateway-generated responses distinguish invalid authenticated requests (400),
proxy authentication (407), policy/address denial (403), authority admission
capacity and per-principal work capacity (429), global work capacity (503),
authority admission or credential/CA material unavailability (503),
upstream DNS/connection/TLS/protocol failure (502), and typed upstream timeout
(504). The accepted-socket limit closes before HTTP parsing, without attempting
an HTTP response. Parser-level framing failures remain owned by `net/http` and
may lack Gateway correlation. These classifications do not change authentication
ordering, policy, material fencing or one-shot dispatch. If all eligible TCP
candidates fail, any typed candidate timeout selects 504; otherwise connection
failure selects 502. Original request cancellation stops establishment rather than
starting another candidate. These safe categories expose no addresses, secrets or
raw errors. Connection attempts are not logical dispatches and do not increment
execution populations or create extra terminal observations. A timeout at the
exchange boundary still does not prove application nonexecution. History loss
does not select a live failure response.

Before response start, errors carry bounded status text, no-store, and the RFC 9209
`Proxy-Status` member `AgentGateway` with a closed `error` token. The executable
mapping is `contract.HTTPProxyFailureReason`; explicit work capacity uses
`connection_limit_reached` even with 503. Authentication and policy both use
`http_request_denied`, distinguished by 407/403. No details, destination, raw error
or retry instruction is included. A Gateway-generated `Gateway-Request-ID`, when
available, is 128 independent random bits encoded as 32 lowercase hexadecimal
characters, unrelated to client headers or audit identity. Successful CONNECT
instead exposes `Gateway-Connection-ID` for the enclosing connection. Neither ID
is authority, proof of dispatch, or proof of a stored record. Inner requests have
independent request IDs; they do not inherit CONNECT authority or correlation.

Client and upstream `Proxy-Status`, `Gateway-Request-ID`, and
`Gateway-Connection-ID` fields are stripped at the forwarding boundary, including
Connection-nominated fields. Upstream application statuses and bodies are preserved;
Gateway never annotates an upstream 403 as its own policy refusal. This deliberately
omits upstream Proxy-Status claims rather than allowing them to impersonate Gateway.
After response start or CONNECT establishment, observed failures terminate the
stream/connection without a second response. Internal panic values are discarded;
a pre-response panic selects 503, while a started response aborts. Typed lossy
process diagnostics observe failure stages separately from durable completion.
No universal client receipt, sink delivery, or durable logging is promised.

## Git routing and dispatch

On enabled HTTPS profile origins, validated method/URL coordinates classify
upload-pack and receive-pack discovery/service endpoints before HTTP policy.
Only configured canonical repositories and explicit `.git` aliases match.
Content-Type validates consistency, never routing authority. Unknown, ambiguous,
escaped, malformed or unsupported Git-shaped traffic rejects without HTTP fallback;
pages, archives, releases and downloads remain ordinary HTTP. Endpoint detection
uses coordinates relative to configured roots (including retained tombstones), or
the owner/repository coordinates of unknown GitHub repositories; arbitrary asset
names and query text are not service endpoints. HTTP admission rechecks Git
exclusion inside its own authority/profile snapshot, so activation between initial
classification and evaluation cannot create an HTTP-authorized Git dispatch.
Destination/TLS and listener checks remain shared and unconditional.

The request-local `gitwire` owner parses receive-pack commands before admission.
Limits are 256 KiB control prefix, 128 commands, 4 KiB capabilities, 32 capability
tokens, 1,024-byte refs, and one nonrenewable ten-second client-read deadline.
Unsigned SHA-1 branch/tag create/update/delete commands validate OIDs, pkt-line
framing and duplicate destinations. Every command must be authorized; no command
removal or partial forwarding is permitted. Negotiated atomic pushes retain their
exact wire controls without downgrade. Signed certificates, push options, shallow
controls, SHA-256, HTTP content encoding, dumb HTTP and alternate object transfers
are unsupported. PACK compression is opaque and permitted.

Discovery bodies and flush-only `0000` probes require exact EOF. Delete-only bodies
also require EOF before admission; create/update requests require the PACK signature
then stream the opaque remainder with bounded buffers/backpressure. The exact
validated prefix, source, actions and configuration revisions remain in one private
owner; candidates bind its identity as well as durable evidence. Retained summary
equality cannot substitute a different owner. Nothing invokes host Git or stores
objects in production. Native Git and backend processes exist only in fixtures.

After request-local admission and unchanged authority/material confirmation, the
engine injects only the repository-selected Git credential and makes one exchange.
There is no redirect, refresh, replay or uncertain-push retry. Each new client
request needs independent admission. Cancellation, rejection, early responses and
drain settle upload readers, streams and material before offering an optional
completion snapshot; a timeout does not assert settlement. Missing terminal evidence remains
unknown, and even clean HTTP 200 never asserts a successful Git ref mutation.
A complete transfer requires both the upload and response to finish; an early
response to an unfinished upload is not a complete transfer.

### Git report observation and history

The request-local Git wire owner observes at most 256 KiB of response controls,
without affecting live forwarding when observation is unsupported or exceeds the
bound. Negotiated report-status/report-status-v2 basic status and side-band-64k
channel-1 framing are supported; progress is discarded and channel-3 fatal or
unsupported v2 option records leave the report unknown. Every requested command
must appear exactly once, with no foreign refs, and both inner/outer flushes and
exact terminal framing must be complete. Ref/OID/message data never leaves the
request-local owner. Complete upstream reports distinguish reported success,
failure and partial success, never independently verified effects.

Observed pushes explicitly request `Accept-Encoding: identity`; any unexpected
encoding, automatic decompression, unsupported content type, non-200 status,
interrupted upload/response or incomplete report prevents a retained result.
Response bytes and headers still relay live with the ordinary safe transport
handling. Ordinary HTTP compression is unchanged. Completion-persistence failure
never replaces the known live response and does not invent terminal evidence.

Git traffic stays in the shared traffic store with one record per classified
exchange and no duplicate ordinary HTTP request. Safely classified rejections
retain only closed categories and authenticated identity/revision facts; unknown
repository coordinates and unsupported request content are not retained.
Optional admission-time policy facts hold configured repository name/canonical
URL, at most four applicable grant references and their total, and create/update/
delete counts. These are immutable historical configuration, not current labels
or observed refs. Public API, CLI and browser history expose admission, transport
and upstream-report evidence separately; absent historical additions remain
unavailable/unknown. No guessed CONNECT link, raw capture, traffic-to-grant action
or automatic retry is introduced.

## Governed invocation and audit evidence

Administrative reads of MCP invocation evidence use only `GET /api/v2/mcp/invocations` and `GET /api/v2/mcp/invocations/{id}`, `agent-gateway mcp invocation list/get`, and `#/mcp/invocations` with supported detail/filter context. The former `/api/v2/invocations`, top-level `invocation` CLI, and `#/activity/invocations` locations are retired without aliases, redirects, fallback requests, or replay. See the [coordinated operator cutover](../operators/upgrade-compatibility.md#mcp-invocation-namespace-cutover).

This is an API v2 administrative namespace change only. Public invocation JSON, filters, limits, cursors, generations, historical IDs/rows and redacted captures remain unchanged; no migration, evidence rewrite, protocol discriminator or activity store is introduced. The `invocations` event kind, admission-before-dispatch, one-shot outcomes, credential bytes, backup lineage, MCP ingress/self-service and callback identities remain unchanged. **Audit Log** remains shared administrator/system/offline-maintenance evidence at `#/audit-log`, `/api/v2/audit-events`, and `audit` CLI, produced separately from MCP invocations.

### Outcome projection

Operator interpretation of retained records is canonical in [invocation evidence](../operators/invocation-evidence.md). The invocation result boundary consumes only that typed completion evidence. Missing terminal evidence means the outcome is unknown; it never proves rollback, nonexecution, or safe retry. A validated success projects required `content`, optional `structuredContent`, and optional false `isError`; the five content unions are closed, bounded, and recursively stripped of protocol `_meta`. The modern `resultType` framing member is accepted only as `complete` and removed, while input-required work and every unsupported member or type fail closed. Tool errors, JSON-RPC errors, and invalid complete results expose only `downstream_failure`; pre-start failures expose `tool_unavailable`; uncertain handoff exposes only `outcome_unknown`. No raw error or unsuccessful content crosses the boundary, and no result is persisted.

The retained outcome vocabulary is closed:

| Outcome                     | Meaning                                                                      |
| --------------------------- | ---------------------------------------------------------------------------- |
| `invalid_params`            | The request could not be classified as a valid call.                         |
| `unknown_tool`              | No current target matched the requested external name.                       |
| `invalid_arguments`         | The resolved target rejected the unchanged arguments before execution.       |
| `authorization_unavailable` | Safe authorization could not be established.                                 |
| `deny`                      | Current policy explicitly denied the call.                                   |
| `block`                     | No applicable allow authorized the call.                                     |
| `prestart_failure`          | The admitted call failed before transport or local execution handoff.        |
| `succeeded`                 | A complete successful result was observed for the live caller.               |
| `downstream_failure`        | A complete unsuccessful downstream result was observed and safely collapsed. |
| `outcome_unknown`           | Handoff may have occurred or required terminal evidence is missing.          |

The accompanying basis is `admission`, `policy`, `terminal`, or `missing_terminal`. Missing terminal evidence always projects as unknown.

### Safe failure diagnostics

Failed downstream attempts additionally expose optional `diagnostics` in agent error data and invocation item API/CLI/browser reads (not collections). `gateway_observed` contains only closed `source`/`reason` pairs: `transport` with `prestart` or `handoff_uncertain`, `protocol` with `invalid_response` or `rpc_error`, `tool` with `reported_error`, and `result_validation` with `result_shape`. Validation names a rule, never an offending value. Existing error codes and terminal certainty remain authoritative.

Only a valid complete tool-error result can supply `server_reported`, from the MCP result `_meta["io.github.averycrespi.agent-tools/failure"]`. This is an unverified server claim, never a Gateway observation. The version-1 closed object requires integer `version: 1`, `category`, and `phase`; optional integer `http_status` is 100–599 and `retry_after_seconds` is 0–86400. Categories are `authentication`, `rate_limit`, `timeout`, `canceled`, `transport`, `json_decode`, `response_contract`, `response_limit`, `validation`, `capacity`, `overload`, `redirect_rejected`, and `upstream`. Phases are `admission`, `exchange`, `response_status`, `response_decode`, and `response_validation`. Unknown members, nulls, wrong types, unsupported versions, and oversized metadata are discarded, leaving Gateway-owned diagnostics and the original outcome unchanged. Each diagnostic object is bounded to 512 encoded bytes. All other metadata remains stripped, including on successes.

Version 2 is restricted to `response_contract` / `response_validation`, without status/retry fields, and requires a closed `validation` object: `schema` (`models_result` or `evaluate_result`), integer schema `version: 1`, `violations`, and Boolean `truncated`. Each violation has `code`, `path`, `rule`, and, only for type mismatches, `expected`/`observed`. Codes distinguish `missing`/`required`, `type`/`type`, `invalid_date`/`date`, `correspondence`/`correspondence`, and `constraint` with a closed schema-rule vocabulary. The executable contract owns the finite schema-specific path inventory: `$` is the root, `.[]` masks array positions, and `.*` masks dynamic map keys. Gateway rejects arbitrary paths, types, rules, schema identities and free text, even if the server calls them schema-derived. The `invalid_date` / `date` diagnostic covers malformed or impossible model release dates, accepting `YYYY-MM-DD` or RFC 3339 timestamps. Release-date failures and answer correspondence never expose the offending date, answer ID, question or value.

Version-2 canonical server metadata is at most 400 encoded bytes, reserving space within the unchanged 512-byte complete provenance envelope. Parsing allows at most six nesting levels. There are one to three violations, deduplicated after masking and sorted lexicographically by their canonical JSON encoding (field order: code, path, rule, expected, observed). TypeSafe selects the longest prefix fitting both the count and encoded-byte bounds; `truncated: true` means additional distinct masked violations were omitted, not their number. Repeated identical masked violations collapse without implying truncation. Readers enforce strict order, uniqueness, shape, vocabulary and bounds. The byte bound can yield fewer than three entries. Unknown answer variants report a safe union-rule failure rather than invented requirements from another variant.

Version-1 metadata and historical rows with absent diagnostics remain readable. No storage migration or bound increase is required; older readers discard unsupported v2 live metadata and cannot qualify reading newly stored v2 rows. Upgrade the bundled readers together; rollback to a v1-only Gateway against new history is not supported. Schema 17 retains only this validated subset atomically with the best-effort terminal annotation. Missing diagnostics remain valid for historical records, local failures, and lost terminal annotations; discarded historical errors cannot be recovered. The live caller can receive diagnostics even if terminal persistence fails. Server claims never prove nonexecution, change authorization, trigger retries, or override uncertain handoff. No raw bodies, headers, transport errors, free-text messages, provider identifiers, or submitted values enter this diagnostic contract. Logging retains its existing independent closed inventory and does not log server metadata.

### Live call rejection contract

Both modern and legacy governed `tools/call` errors retain JSON-RPC code `-32000` and the five existing `data.code` values. Only `call_rejected` carries a required closed `data.reason`. The reason comes from the request-local admission class or its evaluated decision, never a second policy evaluation. Messages are bounded Gateway-owned text; no grant IDs, matching constraints, argument values, or raw internal/downstream errors are interpolated.

| `data.reason`               | `error.message`                                                                                                                                                                                                                                             |
| --------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `invalid_params`            | Request rejected: invalid tools/call parameters. Check the request shape.                                                                                                                                                                                   |
| `unknown_tool`              | Request rejected: unknown tool. Refresh tools/list and check the tool name.                                                                                                                                                                                 |
| `invalid_arguments`         | Request rejected: invalid tool arguments. Check the tool’s input schema.                                                                                                                                                                                    |
| `deny`                      | DENIED: a matching DENY grant forbids this call. Additional ALLOW grants and self-service requests cannot override it.                                                                                                                                      |
| `block`                     | BLOCKED: no matching ALLOW grant authorizes this call. If available, you may use mcp_gateway.list_grants to inspect your access or mcp_gateway.create_grant_request to request access. Requesting access does not authorize the call; approval is required. |
| `authorization_unavailable` | Call rejected: authorization could not be established. This is not a DENY or BLOCK decision.                                                                                                                                                                |

For a blocked resolved local `mcp_gateway` target, the message is instead: “BLOCKED: no matching ALLOW grant authorizes this call. You may ask an administrator to review your access.” Self-service is optional and grant-controlled, not a workaround for DENY. A DENY may expire or be removed by an administrator; its precedence does not imply permanence. Guidance neither submits requests nor replays calls.

Malformed parameters, unknown targets, and invalid arguments remain binding-only admission classes even if policy would deny a valid call. A semantic authorization failure after verified binding uses `authorization_unavailable`; an evaluated ALLOW that cannot detach (for example, because drain intervened) also uses that reason, never DENY/BLOCK. Rejections execute neither downstream nor local targets.

Authority unavailability returns `call_rejected` / `authorization_unavailable`, not a history error. Error responses may include `data.invocationId` when capture identity exists; this is correlation only, not acknowledgment or proof of a retained row. Capture loss omits the ID without changing the outcome, reason or `data.outcomeUnknown`. Successes have no Gateway metadata; other error codes omit `data.reason`. Existing uncertain-handoff semantics and downstream projection remain. The legacy `audit_unavailable` code remains the fail-closed codec fallback for invalid internal response combinations, not a runtime history dependency. Mandatory control-plane audit remains fail closed.

### Internal evidence boundary

`internal/activity` owns only common evidence values: the prepared identity and admission time, principal and credential identity/revision/fingerprint, admission class and authorization evidence, and optional paired completion time/class. Its envelope composes these lifecycle facts without owning validation, authority, SQL, execution, or public representations. MCP common values retain their existing vocabulary; HTTP uses the separate closed `HTTPTrafficAdmission` and completion projection in `internal/contract`.

`internal/invocation.MCPDetails` owns the requested tool name, fixed-redacted argument capture, and optional resolved route. A resolved route follows the [access-target boundary](identity-and-authorization.md#internal-access-target-boundary): it carries the canonical `accesstarget.MCP` exact target plus invocation-owned tool ID and pinned descriptor revision/fingerprint. Synthetic local and downstream targets are distinctions within MCP, not separate protocols. An absent route means unresolved evidence, never server-wide scope. Malformed calls may retain their existing independently available name/capture fields; resolved classes still require complete exact-target and descriptor evidence.

Invocation prepares identity before binding/policy evidence, snapshots all mutable evidence (including the exact-target name pointer) before insertion, and adapts the common envelope and MCP details to the schema-9 admission columns and public invocation projections, with schema-17 optional safe failure diagnostics. Stored nullable groups must be checked for completeness before constructing typed values; normalization cannot turn incomplete evidence into absence. A missing completion remains unknown for admitted ALLOWs. Administrative audit stays a distinct evidence domain owned by `internal/audit`; the common values introduce no additional writer, store, runtime, or audit path.

### Audit evidence and retention

Argument capture uses one fixed recursive key redactor owned by Gateway before compact encoding. Matching is case-insensitive over the normalized sensitive-key set; a matching value is replaced wholesale, including nested structures. Capture overflow falls back to the fixed `[TRUNCATED]` placeholder; redaction or encoding failure produces no capture, and neither path falls back to the original bytes. This is a least-disclosure control over recognized keys, not a claim that arbitrary secret material is detected. Operator-facing projections distinguish a retained capture, truncation, and absence without describing any capture as sanitized or safe. SQLite, backups, events, logs, process output, and test evidence must contain neither raw bearer values nor successful results, raw tool errors, or unredacted canaries.

Invocation owns all traffic SQL and validates identifiers, revisions, fingerprints,
names, compact redacted arguments, nullable groups, decisions, grants and chronology.
Traffic checks collisions before transactional retention and insertion. Its production
1,000,000-row ceiling and physical/logical budgets define rolling bounded history,
not a guaranteed retention window. No history row pins live execution;
oldest rows may be pruned while their execution is still active. Generation and cumulative
pruning changes invalidate cursors rather than silently omitting records. Separate
bounded control snapshots resolve current principal names; their digest is cursor
state, never authorization. There is no cross-store SQL or name lookup during a
traffic write. Acknowledged changes emit coalesced invocation/System invalidations;
uncertain or failed writes do not invent durable evidence. One asynchronous
self-contained completion observation writes the canonical time/class pair and optional
validated, bounded failure diagnostics atomically. The queued representation is
owned encoded data, never raw tool errors or mutable caller metadata. Migration,
paired backup and restore preserve this same validated diagnostic evidence.

### Optional traffic persistence

The [isolated traffic store](storage-and-recovery.md#isolated-traffic-store)
retains the existing SQLite shapes, historical readers, capture limits and complete
semantic validators. Legacy control-store invocation write builders exist only in
test fixtures; there is no receipt-dependent runtime compatibility path.

One writer consumes one bounded nonblocking queue of immutable, pre-sanitized
observations. Defaults bound total occupancy, including active settlement, to 128
records and 2 MiB charged bytes. Transactions contain at most 32 records/512 KiB,
with 2 ms dwell, 250 ms queue lifetime and a two-second cooperative write lifetime.
Configuration permits at most 1,024 queued records/16 MiB, 10 ms dwell, one-second
queue lifetime and five-second write lifetime. Queue fullness, expiry, drain,
closed/faulted recording, invalid capture, or identity/redaction/encoding failure
drops capture without refusing valid execution. Raw data is never queued for later
redaction. No observation contains request contexts, callbacks, authority, material
handles, stream owners or cleanup obligations.

Initial and terminal observations use the same queue. Each terminal contains its
complete sanitized initial snapshot, charged in full, and can insert useful history
when the initial observation was lost or pruned. Matching identities preserve
immutable initial facts; terminal fields are first-write-only. Late initial records
cannot erase a terminal, and conflicting facts cannot overwrite another record.
History IDs are correlation, never permission or proof of exactly-once effects.

Callers never wait for persistence, including during response completion or cleanup.
Capacity refusal does not fault healthy storage; storage/commit/rollback uncertainty
faults only optional recording. No replacement writer, uncertain-write replay,
automatic retry or execution reconstruction is permitted. Retention has no live-work
pins. Execution retains its own finite bounds and releases material/opaque origins
only after actual settlement, independently of every capture outcome.

### Process-local recorded activity

The composition-selected `TrafficStore` constructs one memory-only observer before its writer starts. Writer settlement records successful committed MCP/HTTP insertions and first terminal updates, not live attempts or enqueue acceptance. A terminal that reconstructs a lost initial row contributes both committed facts. Duplicate/late observations do not recount unchanged facts. Failed or uncertain persistence contributes nothing even if SQL is readable. No ingress, response or authorization hook adds another count. Dedicated Git writes remain outside this MCP/HTTP-only summary. None of these counts proves exactly-once downstream effects.

Only immutable fixed protocol/outcome enum values accompany queued evidence to the observer. The ring has at most 60 one-minute buckets with fixed dimensions; update and snapshot use one bounded memory-only lock, with no downstream call, SQL, second writer, callback, persistent cache or per-identity deduplication state. `internal/activity` stays value-only. The administration dependency exposes only a snapshot function. [The public summary contract](public-contract.md#recorded-activity-summary) fixes its closed fields and 15 completed-minute window; the current partial minute is deliberately excluded rather than approximating a sub-minute rolling cutoff from whole buckets.

Collection starts when the selected store opens; retained admissions are never reconstructed after restart. Bucket timestamps come from acknowledgment settlement time, not the earlier evidence timestamp. Startup coverage is partial until the window is wholly observed. Compare wall elapsed time against the process monotonic clock on updates and reads: any backward time or a discrepancy over one second resets the epoch and clears the ring. Counter overflow also resets rather than wrapping. Reset resumes at the next complete-minute boundary, leaving the interrupted interval unavailable; unobserved bucket counts stay null. Snapshots carry epoch reason/start/as-of/window/bucket bounds so zeros apply only to explicitly observed coverage. Minute values and snapshots cannot mutate the ring. Sequence exhaustion leaves observation unavailable rather than reusing an epoch.

MCP binding-only refusal classes, evaluated allow/deny/block, and prestart/downstream/explicit-unknown terminals retain their existing distinctions. HTTP request, CONNECT and invalid/unclassified admission evidence stay separate. Interception selection is not general failure or CONNECT/TLS completion; opaque CONNECT is not an inner request. Successful HTTP transfer evidence does not interpret application content or status class. Missing terminals are never inferred as explicit unknown completions or in-flight work, and admissions minus completions is not a valid population. There are no 24-hour/cross-restart trends, latency histograms, administrative-mutation success metrics, ingress-attempt counts or success percentages.

### Process-local execution observations

One composition-owned fixed-size memory collector counts at live owners, independently
of the diagnostic level, stderr queue and history facade/writer. MCP counts recognizable
calls entering `Service.Call`, not HTTP envelopes, authentication refusals or list/ping
messages. HTTP counts entries to the proxy request handler; CONNECT is a separate
population, and intercepted inner requests enter the HTTP owner independently. Git
parser entries form a labeled subset of HTTP requests, never an additional HTTP
execution. Do not sum HTTP and Git request counts. Pre-routing failures cannot invent
a Git classification. Parser/socket failures before these owners remain outside
coverage.

Execution counts mean confirmed execution-pipeline entry (including subsequent
prestart failure), not proof of downstream handoff. MCP local/downstream outcomes
are counted by their single finish owner. HTTP/Git and opaque CONNECT count at the
single completion owner after transfer bookkeeping and before optional capture.
Interception selection does not count as an opaque tunnel execution. Git HTTP 200
and even complete transfer retain an unknown mutation outcome; separately labeled
upstream success/failure/partial reports are untrusted facts, never Gateway-observed
successful pushes. Request completion latency includes cleanup; execution latency
uses monotonic elapsed time at the corresponding owner. Admission latency covers
handling through admission, not an independently timed SQL transaction.

Each protocol has fixed admission/execution/request latency vectors with disjoint
buckets <=1, <=10, <=100, <=1000, <=10000 ms and greater. No dynamic label, identity,
URL, hostname, argument, ref, raw error, tracing registry or retained callback exists.
Counters saturate at 2^53-1 and set overflow; they never wrap or reset on read or sink
failure. A fresh random epoch and collection start identify each process graph. Entropy
failure leaves the epoch empty (cross-snapshot correlation unavailable), without
gating counting or falling back to clock/PID identity.
Wall-clock changes neither reset cumulative counts nor alter monotonic durations.
Crash losses, unobserved boundaries and missing terminals remain unknown; no completeness,
rate, denominator, cross-restart continuity or zero-activity claim follows. These
counters never derive occupancy by subtraction: MCP work/dispatch and HTTP work,
connections, streams and tunnels still come from their existing actual owners.
Recorded activity above and all historical query populations remain unchanged.

### Admission and execution

Under a short authority gate and one coherent control read snapshot, evaluate the
authenticated immutable binding and pinned target once, sealing the decision,
revision, valid authority-clock evaluation time and original request context. Release
the gate/read before material acquisition or optional capture. Reacquire authority
to confirm the same active binding and unchanged global authorization revision.
Under the existing drain and control-health fences, consume the sealed candidate
once and atomically change its pending lease to admitted using the existing CAS.
There is no permission registry, history receipt, stored-row lookup or capture
serialization prerequisite. HTTP/Git executable facts are distinct from capture DTOs.

Revocation, replacement, principal/policy revision, original cancellation, control
latch or drain winning before confirmation blocks dispatch without reevaluation,
alternate decision, retry or reroute. A fresh confirmation context cannot resurrect
the original canceled request. Grant expiry remains evaluated at captured time.
ALLOW losing confirmation returns `authorization_unavailable`, never DENY/BLOCK;
any recorded allow remains incomplete rather than fabricating execution. Binding-only,
DENY and BLOCK branches return their original classification without dispatch.
History preparation, identity, enqueue or persistence failures affect recording only.
Completion offers one nonblocking self-contained observation without control mutation
or authority reacquisition; its loss cannot replace a known live result.

The internal invocation service classifies the strict call params, defaults absent arguments to an empty object, resolves an external name once to a closed downstream/local target, and pins either the downstream validator/capability or one fixed local validator/handler. It pins the published descriptor's normalized `readOnlyHint` alongside that target for read-only ALLOW evaluation, never accepting client-supplied annotations. It validates and redacts the same token-preserving argument tree, performs the admission above, then releases authority and storage admission before one execution. Catalog replacement fences the pinned capability before a later acquisition/dispatch, so old read-only authority cannot authorize a replacement descriptor. Fixed local tools use their compiled hints; admitted-call detachment, no retry, and no reroute remain unchanged.

Downstream targets acquire their capability; local targets receive only the sealed admitted subject and acquire no downstream capacity. Capacity, route, cancellation, transport, and local storage evidence map only through the closed safe outcome boundary.

The service never resolves, evaluates, acquires, or executes a second time. It offers one nonblocking optional terminal observation after an ALLOW attempt; capture refusal or failure cannot replace the live response. An error's optional invocation ID is correlation only, not proof of persistence. Successful projections contain no Gateway metadata.

Gateway supplies at most one automatic attempt, not exactly-once effects: an explicit caller retry after `outcome_unknown` may duplicate an effect, and no restart, cancellation, terminal-write failure, or lifecycle transition causes automatic replay. Local post-commit uncertainty returns `tool_unavailable`, omits terminal annotation/invalidation, and is recovered only through request reads or duplicate-first create retry.

### Contention qualification

[Implementation evidence](../maintainers/implementation-evidence.md#contention-qualification) records correctness and isolation workloads; these are not throughput or latency qualification.

### Capability acquisition

The current-runtime capability is an opaque, explicitly nonserializable process-local object. It captures server/tool/upstream/runtime identity and exact desired, static-credential, OAuth-client, OAuth-token, and catalog revisions, but exposes no identity enumeration or mutation.

Acquisition tries the global 32-slot channel before the four-slot server channel and never waits; server saturation immediately releases global. Only after both permits does an injected current-state seam revalidate route, runtime, every bound authority/catalog revision, and drain state.

A final capability-lock check closes the withdrawal race. Stale, withdrawn, draining, unavailable, canceled, and both saturation branches are typed pre-start rejections.

A lease may execute one call or cancel once; it releases server before global and preserves lower-level marker classification. Withdrawal synchronously marks the capability unavailable and cancels registered leases.

The internal invocation service is the only capability consumer. Production composition supplies it through one closed ingress adapter, while agent discovery continues to enumerate descriptor projections independently without acquiring capabilities; no root or ingress owner can resolve or acquire a route directly.

## MCP ingress and governed invocation

### Process diagnostics and correlation

Debug observations separate request-local admission, execution start/result and terminal enqueue results. Terminal enqueue success is not persistence acknowledgment. The service assigns a process-local call counter before optional capture identity preparation; it does not consume capture entropy or change dispatch availability. An invocation ID, when available, is correlation only and may have no retained row. Diagnostic events requiring unavailable correlation may be dropped. Execution evidence never proves a retained terminal; missing history remains unknown. Diagnostic counters saturate by dropping further records needing that ID, never rejecting a call or reusing a counter.

Authority observations distinguish wait, acquisition, release and rejection, with closed capacity, expiry, cancellation and drain causes. Occupancy samples come from the actual owners; they are point-in-time observations, not summed status placeholders. Authority samples the actual gate channel while its mutex freezes outstanding work, and retires gate ownership and outstanding work together under that mutex, so a departing owner cannot become a phantom waiter. Durations are monotonic elapsed milliseconds, not deadlines for active work. Diagnostic call IDs are unrelated to client JSON-RPC IDs, headers, tool names, arguments or credential fingerprints. Mutation counters are scoped to the storage or authority owner within a process instance. The [serve diagnostic contract](administrative-control-plane.md#serve-diagnostics) owns privacy, loss, and sink lifecycle.

### Agent authentication and leases

The internal agent authenticator accepts only the canonical `mgw_agent_` encoding, derives one agent-domain verifier, and scans every complete active current slot in one bounded coherent transaction with constant-time comparison and no verifier predicate or early match return. Success exposes only principal ID/revision/visibility and credential ID/revision/fingerprint. Admin-domain bearers are a domain mismatch; missing, malformed, unknown, replaced, revoked, disabled, or cleared authority is one non-enumerating authentication failure. Invalid loaded candidate state, capacity overflow, or a latch before, during, or after a match fails unavailable with no partial binding.

One repository-owned exclusive authority gate encloses authentication/lease registration and every principal, credential, grant, authorization-revision, or invocation admission mutation. Its process-wide `authority_work` bound admits 32 outstanding operations: one executing and up to 31 waiting. Excess arrivals reject immediately. Admitted callers wait at most one second for the gate, shortened by their context cancellation or deadline; capacity exhaustion and gate-wait expiry return `resource_limit`, including HTTP 429 during agent authentication rather than HTTP 401. The wait deadline does not bound work after gate acquisition. The fixed order remains authority admission, exclusive gate, storage mutation, transaction checks/write, targeted post-commit invalidation or detachment, then gate and admission release. Authority waiting never holds a storage transaction or mutation slot. Invocation evaluation and confirmation use separate short coherent control reads; optional traffic persistence never blocks the caller. Control mutation admission remains nonqueueing. Gate exclusivity still orders credential reads and lease registration against revocation and targeted invalidation.

Principal PATCH and credential replace/revoke cancel only that principal's pending leases after an acknowledged commit and conservatively whenever their mutation latches storage. Principal creation cannot have an existing target lease, and grant create/delete never close credential channels: admission evaluates policy once and confirmation requires the exact captured global revision while holding the same gate.

Success returns an already-registered pending lease with immutable safe binding, latch/drain-aware currentness, cancellation completion, and idempotent release. Drain first atomically fences new authority admission and registration, wakes queued callers, detaches all pending leases under the registry lock, cancels them after unlocking, and only then boundedly waits for all outstanding gate holders and waiters to release their occupancy; a deadline can make quiescence unclean but cannot leave a pending lease open.

Waiting runs in the caller's goroutine with a bounded wait context; the registry creates no worker goroutines or alternate owner. Cancellation, wait expiry, drain, and every completed operation release their actual outstanding occupancy.

Evaluation and confirmation each acquire only the authority gate and a bounded
control read, never `Store.Mutate`. Evaluation requires the exact active binding,
captures one authority-clock UTC time and current revision, and seals the single
policy result against unchanged token-preserving arguments. The advisory discovery
revision does not substitute for the captured revision. Confirmation requires
exact captured-revision equality and the same process-local candidate, pending
lease, without any traffic receipt or stored-history lookup. It atomically changes pending to admitted
under the registry drain fence and removes credential invalidation; subsequent
credential/policy changes neither cancel nor reauthorize admitted execution.

Drain fences all new gate entrants before waiting boundedly, then removes and cancels every pending lease outside the registry lock. A timed-out drain leaves the fence set for a later completion. Stopped candidate recovery runs under exclusive process ownership, where no live registry exists.

Ingress authentication runs before MCP body reads, era classification, and session lookup. Production consumes the coordinated composition-owned positive authenticator and discovery bundle. The authentication seam accepts the authorization repository's already-registered non-expiring lease with exact principal/credential revisions, fingerprint, and visibility. A shared idempotent request owner releases the lease on boundary abort or every ingress terminal path, and lease invalidation cancels an in-flight modern request; detached ingress bindings, credential expiry, and optional post-auth subscriptions no longer exist.

### SDK transport trust boundary

Both embedded SDK transports run behind Gateway's early HTTP boundary and agent authentication. Gateway owns Host and Origin validation: only the canonical numeric IPv4 loopback authority or an explicitly allowed forwarding hostname is admitted, before authentication or body processing. Forwarding headers remain forbidden, and hostname allowlisting grants neither browser Origin trust nor credentials or grants. The original request Host is preserved.

The modern and legacy SDK handlers disable only their redundant localhost-protection guard, which otherwise rejects allowed forwarding hostnames when the HTTP listener context is loopback. Production ingress must remain behind `httpboundary.Boundary`; the SDK is not an independently exposed HTTP server. Gateway's listener validation, authentication, protocol classification, limits, and authorization remain authoritative.

### Modern ingress

Gateway validates the modern `2026-07-28` header/body protocol mirror and dispatches only sessionless POST requests to the official SDK's stateless transport. Per-request client metadata replaces initialization state in this era.

Modern requests reject legacy session IDs, cannot fall through to legacy classification, and propagate request cancellation. Before SDK dispatch, one shared raw codec preserves the JSON-RPC ID, strictly accepts absent, empty, or cursor-only list parameters (beside required modern protocol metadata), owns the closed discovery success/error envelopes, and returns fixed method-not-found for every uncomposed non-lifecycle feature method.

Its call-only sibling recognizes only a non-null string/number ID, excludes batches and notifications, strips `_meta` and disallowed fields, and passes token-preserving name/arguments plus closed wire-validity evidence to an era-neutral service seam. It owns exact success projection and the five safe call error codes, with the closed rejection reasons and fixed messages above.

Modern injection supplies the registered request lease and cancellation context directly to that seam before stateless SDK dispatch. Legacy injection first matches the reauthenticated request's exact initialized session binding, then supplies only that request lease and cancellation context; it never treats the session ID or session-owned lease as call authority.

### Shared discovery and composition

The shared injected list service makes both eras advertise exactly `tools:{}`, passes each request-scoped authenticated lease/cursor and cancellation to the discovery pager, maps only closed errors, and writes the pager's verified final bytes without re-encoding; `listChanged` remains absent. Legacy initialization retains its separate session-owned lease, while every list first reauthenticates and matches the exact session binding before using its own request lease.

Production composition owns the authority, policy service, process-local cursor key, pager, one startup-validated invocation repository/service, its nonqueueing process-local pipeline fence/counter, and both ingress adapters before listener startup. Root consumes their one validated dependency bundle and reports `principal_credentials`, so no partial call/authenticator/status graph is constructible.

The adapter's nonqueueing pipeline counter bounds active MCP service calls to 1,024, independently of history. Composition fences it before authority and route drain, then includes actual execution quiescence in the deadline-bounded result without adding a public status field. Capture enqueue is nonblocking; no history pin or persistence acknowledgment owns cleanup. Authority cancels pending leases, including drain between evaluation and detachment. Quiescence waits outside authority/storage locks; timers never prove I/O settlement and unresolved cleanup remains unclean. SDK bootstrap and legacy session lifecycle methods remain SDK-owned.

### Legacy sessions

Legacy `2025-11-25` initialization reserves one of 128 slots before entropy or state publication, then uses a stateful official-SDK adapter with a Gateway-generated opaque session ID. Successful publication alone transfers the initial registered lease into the session; every later POST, GET, or DELETE uses its own request lease and requires the exact protocol header, session ID, principal ID, credential ID, and credential revision. The session watches lease cancellation, and idle/absolute lifetime, replacement, revocation, disablement, DELETE, initialization failure, shutdown, or restart closes the in-memory session and releases its lease once. There is no credential-expiry timer. Modern, legacy, MCP-work, and MCP-stream state remain separate; 32 work and 32 stream permits reject without queuing.
