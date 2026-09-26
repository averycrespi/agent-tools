<a id="access-control-principals-grants-and-requests"></a>

# Access control: agents, grants, and requests

Agents and credentials are shared administration. Grants and **MCP → Requests** administer MCP permissions, not network traffic or queued calls. Use `mcp grant` and `mcp grant-request`; their API resources also belong to MCP. Upgrade clients and service together and reload browsers using the [exact namespace mappings and rejected-link recovery](upgrade-compatibility.md#mcp-permission-namespace-cutover). Old spellings are rejected, never redirected or replayed.

Audience: Gateway administrators managing agent access

Purpose: Manage agents, credentials, grants, and grant requests.

An **agent** is a persistent Gateway identity with its own access policies and at most one current credential. It does not represent a running process or session.

CLI commands use **agent** (`agent list`, `agent credential issue`); the old `principal` namespace has no alias. Technical interfaces continue to call the durable identity a **principal**: retained flags (`--principal-id`), [API routes and fields](../design/public-contract.md) (including `principal_id`), JSON representations, ETags, error codes, audit identifiers, MCP contracts, and the browser route `#/principals` are unchanged. Raw technical values and user-authored names are shown verbatim; retained documentation anchors keep existing links working. Administrator identities and credentials remain separate.

This guide owns Agent Gateway operator workflows for agent lifecycle, one-time agent credentials, immutable grants, constraints, and grant-request adjudication. Use the current `agent-gateway` executable. Executable retirement does not change credentials or the fixed `mcp_gateway.*` self-service tools. Standalone administrative clients must upgrade for the [operator v2 cutover](upgrade-compatibility.md#operator-v2-cutover). HTTP grants are administered separately from MCP permissions. Generated help owns exact syntax:

- `agent-gateway agent --help`
- `agent-gateway mcp grant --help`
- `agent-gateway mcp grant-request --help`

See [DESIGN](../../DESIGN.md) for the system design index and [Identity and authorization](../design/identity-and-authorization.md) for normative authorization, policy evaluation, and request-state semantics. See [Administrator CLI and local administration](administration.md) for shared authentication, output, strict input, ETag, confirmation, and retry rules. These are online workflows: start `agent-gateway serve` first; a proven refused selected address reports the exact startup command.

<a id="create-and-inspect-principals"></a>

## Create and inspect agents

List or inspect permanent agents before changing policy:

```bash
agent-gateway agent list
agent-gateway agent get PRINCIPAL_ID
agent-gateway agent create --display-name NAME --visibility VISIBILITY
```

Creation requires a display name and one MCP discovery visibility mode. The browser labels the create/edit control, detail, column, and filter **MCP discovery visibility**; the API and CLI retain `visibility` and `--visibility`:

- `all` discovers every current MCP tool;
- `requestable` hides tools covered by an applicable unconstrained `DENY`;
- `allowed-only` requires an applicable `ALLOW` and no applicable unconstrained `DENY`.

Discovery visibility grants no access; MCP grants remain authoritative for calls. A constrained `ALLOW` may make a tool discoverable even when a particular argument object will not match, while a constrained `DENY` does not hide it.

Agent creation also creates an ordinary permanent grant described as **Default Gateway access** for the six fixed `mcp_gateway` self-service tools. It grants no downstream access or authority for future protocols. This is the design's synthetic default grant: the description stays unchanged for existing and new records. That grant counts toward capacity, can be deleted or overridden by `DENY`, and only an administrator can restore equivalent access. Creation is atomic: capacity or required audit failure leaves neither a new agent nor its grant. Human creation output is agent metadata; JSON retains `{principal,default_grant}`. Issue the credential separately and configure downstream MCP grants separately. Agents are permanent and cannot be deleted.

The agent ID/state and single credential slot remain shared identity, not per-protocol settings. Generic compatibility names such as `visibility`, `default_grant`, and `AgentCredential` do not make MCP grants protocol-general. Existing credentials and backups remain usable without reinitialization, conversion, or rotation for this clarification. HTTP shares this same agent credential, with separate HTTP permissions; MCP grants do not authorize HTTP.

<a id="update-principal-state-or-visibility"></a>

## Update agent state or visibility

Submit a nonempty direct patch; omit `--etag` for one validated current-item preflight, or supply it to pin an already observed exact value and skip that convenience read:

```bash
agent-gateway agent update PRINCIPAL_ID --display-name NAME
agent-gateway agent update PRINCIPAL_ID --etag ETAG --state disabled --yes
```

Changing state requires consequence confirmation. Disabling an agent clears its current credential and sessions. Re-enabling does not restore authority, a prior credential, or deleted grants. Display-name and visibility-only updates do not prompt. The CLI never refreshes a stale precondition or replays a patch automatically.

## Issue, rotate, or revoke an agent credential

An agent has at most one current non-expiring `mgw_agent_` bearer. `issue` requires an empty slot; `rotate` requires an occupied slot and atomically replaces its authority:

```bash
agent-gateway agent credential issue PRINCIPAL_ID \
  --secret-output /safe/new/agent-bearer \
  --yes
agent-gateway agent credential rotate PRINCIPAL_ID \
  --secret-output /safe/new/rotated-agent-bearer \
  --yes
```

Both commands always read the agent once to enforce slot intent. An optional explicit `--etag ETAG` must match that observation; omitting it uses the observed current value. The bearer is published once to a prepared controlling terminal or a fresh owner-only file. Metadata cannot recover it. Rotation advances agent and credential revisions atomically, so the old bearer never overlaps current authority.

Revoke with an automatic or explicit current agent ETag:

```bash
agent-gateway agent credential revoke PRINCIPAL_ID --yes
agent-gateway agent credential revoke PRINCIPAL_ID --etag ETAG --yes
```

Issue, rotate, revoke, and disable never replay automatically. On an uncertain result, read the agent and review its credential revision before deciding what to do. A lost bearer cannot be recovered and is not evidence that rotation failed. After Gateway acknowledges issue, lost output may leave the singular slot occupied even though no bearer can be recovered from metadata. After acknowledged rotation, the replacement may be current and the prior bearer may already be invalid. In either case, explicitly rotate or revoke the observed current credential instead of replaying the original operation.

## Configure an agent client manually

Repository-owned guest provisioning is retired. Choose and manage your own client environment; Gateway does not require Lima or a particular agent harness. This source change does not modify installed clients, shell profiles, credentials or services, and does not qualify live adoption.

For trusted local Lima forwarding, start the initialized host Gateway with:

```bash
agent-gateway serve --allowed-host host.lima.internal
```

The listener remains numeric loopback at `127.0.0.1:8210`. The environment must supply trusted forwarding separately. Plain HTTP supplies neither confidentiality nor server authentication; do not expose this as arbitrary remote access. See [forwarding trust boundaries](administration.md#trusted-local-forwarding-and-sandbox-administration). Agent clients need only an agent credential, never an administrator bearer or Gateway data directory.

Create a dedicated agent and issue its credential using the procedure above; configure grants separately. For existing agents, retain the current credential rather than issuing or rotating merely for a path/name change. Transfer only that agent credential through an authenticated, confidential channel to a chosen owner-private client file, conventionally `~/.config/agent-gateway/agent-token`. Before transfer, verify ownership and reject symlinked parents/files, nonprivate destinations and unexpected existing content. Use a `0700` token directory and a regular `0600` (or `0400`) token file; keep `.config` owned and not group/world-writable. Reconcile conflicting copies privately with their owner; never overwrite an unexplained credential.

Configure the client's supported secret-file mechanism, or load the validated file at client startup into its supported agent-token environment variable. Do not embed tokens in profiles/settings JSON, command arguments, URLs, tickets or logs. Disable tracing before reading credentials; never dump the environment. Environment delivery exposes the bearer to the client and inherited children, not an OS credential boundary. Refuse missing/invalid files rather than falling back to stale inherited values.

### Existing sandbox migration and conflicts

The retired scripts no longer enforce token validation, marker replacement, alias agreement or permission checks. Operators now own those checks; this guide is not an automated replacement provisioning system.

For consumers using the Gateway environment contract, retain **both** `AGENT_GATEWAY_ENDPOINT` / `AGENT_GATEWAY_AGENT_TOKEN` and `MCP_GATEWAY_ENDPOINT` / `MCP_GATEWAY_AGENT_TOKEN`. Derive both pairs from one deliberately selected trusted endpoint (for Lima, `http://host.lima.internal:8210/mcp`) and the same current agent file. Do not let inherited aliases independently select endpoint or authority. Retiring provisioning does not authorize removing legacy exports or changing external consumers.

Historical profiles may contain `mcp-gateway` or `agent-gateway` managed marker blocks, or a `~/.config/mcp-gateway/agent-token` copy. With clients stopped and files quiescent, inspect these and independent launcher settings before editing. Keep private backups and unrelated profile content. Reconcile duplicate/partial blocks and conflicting credentials explicitly; do not source multiple competing loaders. If keeping an old-path copy for rollback, ensure it holds the same current agent credential, not an administrator or invalidated bearer. No old-path fallback is implied.

After an authorized rotation, securely refresh every retained client copy with the replacement, then restart clients and launchers that retain old environments. Rotation has no authority overlap; do not restore an invalidated bearer as rollback or replay an uncertain rotation. File transfer alone cannot update an already-running process.

### Consumer compatibility, qualification and rollback

Historical inspection of [agent-config `fadc9ac8d681f27bf9c8e5f59fc56011a0d512b1`](https://github.com/averycrespi/agent-config/commit/fadc9ac8d681f27bf9c8e5f59fc56011a0d512b1) found a consumer accepting only the legacy `MCP_GATEWAY_*` pair: endpoint environment overrides trusted global settings, bearer comes only from the environment, and project settings cannot redirect global authority. That revision validates exact `/mcp`, transport/hostname rules and agent-token shape, rejecting administrator tokens and embedded credentials. This is historical compatibility evidence, not qualification of the currently installed external consumer.

Before adopting a different consumer, test pairing, precedence, redaction, administrator rejection and global/project authority with synthetic material in disposable client environments. Canonical-only exports remain outside this retirement. Record exact consumer/configuration revisions and results; loader tests do not prove connectivity or deployment. Rollback may restore a known-compatible client configuration, but must retain the current credential and trusted endpoint, remove conflicting exports, and restart the client. Do not restore deleted provisioning scripts as the current procedure.

### Nonsecret adoption evidence

For each client environment and launcher, the rollout owner records permission/ownership checks, selected transfer path, consumer version, endpoint/alias agreement, current-token agreement as Boolean results, absence of stale loaders and fresh-process confirmation—never token values or environment dumps. Record separately authorized connectivity evidence, unresolved conflicts, rollback choice and next action. Uninspected clients remain unqualified. This repository's tests and CI do not inventory live profiles, transfer real credentials or establish rollout completion.

## Create and inspect immutable grants

```bash
agent-gateway mcp grant list --principal-id PRINCIPAL_ID --server-id SERVER_ID
agent-gateway mcp grant get GRANT_ID
agent-gateway mcp grant create --description TEXT --principal-id PRINCIPAL_ID --effect allow --server-id SERVER_ID
agent-gateway mcp grant create --file PATH
```

Every grant has a stable ID and may have a non-unique human-readable description. The description is display metadata: update or clear it with `mcp grant update GRANT_ID --description TEXT` (an empty value clears it) and an automatic or explicit exact ETag. A description-only patch advances the grant's metadata revision without advancing policy revision or cancelling leases. The direct create form creates an ordinary unconstrained grant and may add `--description`, `--upstream-name`, or `--expires-at`. Use the mutually exclusive strict file form for a constraint; it supplies the complete closed shape, including explicit nullable `description`, `upstream_name`, `constraint`, and `expires_at` members. Grants are immutable for identity and policy; each remains an `ALLOW` or `DENY` row even when its optional description changes. A server-wide grant uses a null upstream name; an exact-tool grant names one upstream tool. Exact names do not require a currently active descriptor.

### Read-only server access

Add `--read-only` to direct server ALLOW creation, or select **Only tools marked read-only** in the browser's **Allowed tools** dropdown for a server ALLOW:

```bash
agent-gateway mcp grant create --principal-id PRINCIPAL_ID --effect allow --server-id SERVER_ID --read-only
```

The strict grant file accepts optional Boolean `"read_only":true` alongside the existing required members, with `upstream_name:null` and `constraint:null`. Omission or false preserves unrestricted behavior. True is invalid for DENY, exact-tool, or argument-constrained grants; direct flags (including `--read-only=false`) cannot be combined with `--file`.

Only tools explicitly declaring `annotations.readOnlyHint=true` qualify, including future qualifying tools. Missing, null, or false hints do not qualify. Annotations are trusted server declarations, not side-effect isolation. This restricts one ALLOW, not the whole agent: other ALLOW grants may authorize writes, and matching DENY still wins. CLI and browser grant/request reads distinguish read-only from unrestricted server access. Browser replacement ALLOWs retain the restriction; replacement DENYs cover all tools on the server.

### Author argument constraints

Untagged constraints use the permanent v1 equality form `{"equals":{"/object/path":value}}`. V2 uses the closed `{"version":2,"equals":{...},"regex":{...}}` form with at least one and at most 16 total atoms. For example, a strict exact-tool grant file can combine equality and regex while retaining lexical tokens:

```json
{
  "description": "Bounded report access",
  "principal_id": "PRINCIPAL_ID",
  "effect": "allow",
  "server_id": "SERVER_ID",
  "upstream_name": "reports.read",
  "constraint": {
    "version": 2,
    "equals": { "/attempt": 1, "/filters/region": "us" },
    "regex": { "/resource": "item-\\d+" }
  },
  "expires_at": null
}
```

Equality retains exact scalar and lexical-number tokens; regex uses bounded full-string Go RE2 matching against strings only. Grant and request JSON responses plus CLI mutation bodies preserve literal matcher bytes, including `<`, `>`, and `&`, rather than HTML-escaping and expanding them. Regex evaluation shares a fail-closed work budget weighted by nonzero input bytes and compiled program size, including empty-string matches. Both versions use object-only RFC 6901 traversal. Constraints apply only to exact-tool grants. Gateway does not coerce values, traverse arrays, interpret schemas as policy, or treat overlapping paths as equivalent. The browser shows the selected server's namespace followed by a dot and an editable tool autocomplete; it stores the server ID and literal upstream name separately. Tool suggestions show only the upstream name because the namespace is already displayed. **Known** (green) means found in the available catalog/schema, not authorized; **Unknown** (amber) is not a validation error. Empty inputs remain neutral. Loading and unavailable data are distinct, and manual future names and pointers remain usable.

Use **Add constraint** for compact rows of JSON pointer, recognition status, EQUALS/MATCHES, type, value, and Remove. All constraints must match. New pointers start empty. Tool and pointer suggestions open on focus or with the dropdown button; type to filter, use arrow keys and Enter to select, or click an option. Escape dismisses suggestions; Tab keeps the typed value without selecting a suggestion. Nested scalar suggestions include escaped RFC 6901 pointers, descriptions, types, and optional enum values. Unsupported schema portions are disclosed. A known path defaults the equality type when the value is empty and no type was explicitly selected; types remain selectable, and enum suggestions never restrict manual scalar values. Boolean uses a true/false selector and Null a fixed null value. Enter numbers as exact JSON tokens, including any significant lexical spelling.

MATCHES visibly locks the type to String and uses full-string Go RE2 without coercion, even when schema guidance suggests another type. Switching operators clears the value; returning to EQUALS restores its type. Tool/server changes, late schema responses, and validation failures never rewrite entered constraint values or explicit types. Schema/type differences appear only when relevant beneath the row. Set optional Expiry after constraints using the browser's local date/time control; Gateway receives the corresponding UTC instant. Leave it blank for permanent access, then review the exact target and complete read-only serialized policy in the final confirmation. Request approval uses the same editor while keeping submitted rules locked.

New non-null web constraints always use v2, including equality-only grants and request approvals without added rules. Approval keeps the submitted rules locked and retains their exact atoms while allowing only additive narrowing. Unconstrained policies remain null. Existing v1 grants/requests remain readable and unchanged, and CLI/API v1 compatibility remains supported. Matcher version is separate from the `/api/v2` route version.

Expired grants remain readable and count toward capacity until deleted. The **Default Gateway access** grant is also an ordinary capacity-owning row. Discovery is deliberately broader than execution for constrained policy; only exact call admission evaluates the unchanged argument object.

## Replace or remove a grant

Because grant policy is immutable, choose the replacement order deliberately. Create before delete produces temporary overlap; delete before create produces temporary loss. Each step is an independent confirmed mutation, and no later step runs automatically. If the first result is uncertain, stop and read current grants before submitting the second step. Re-read effective policy before changing the old row: `DENY` takes precedence over `ALLOW`, and an unconstrained denial can hide a tool from discovery.

Delete by stable grant ID:

```bash
agent-gateway mcp grant delete GRANT_ID --yes
```

Deletion has no ETag or idempotency surface. An uncertain create or delete requires narrow agent/grant reads rather than replay. Visibility by itself never authorizes calls, and deleting an expired or default grant can still change capacity or self-service behavior.

## Interpret MCP call rejections

Before MCP dispatch, Gateway admits up to 32 outstanding authority operations across all agents, with one executing and up to 31 waiting for at most one second. Authentication capacity exhaustion or gate-wait expiry returns HTTP 429 (`resource_limit`), not HTTP 401. Reduce concurrent requests if these persist; rotating a valid credential does not resolve overload. These limits are compiled, not configurable. See [authority admission](../design/invocation-and-ingress.md#agent-authentication-and-leases) for ordering and cancellation behavior.

Modern and legacy `tools/call` rejections retain JSON-RPC `-32000` and `data.code: "call_rejected"`. Read the closed `data.reason` rather than parsing message text:

- `invalid_params`: check the `tools/call` request shape.
- `unknown_tool`: refresh `tools/list` and check the name; discovery is not authorization.
- `invalid_arguments`: check the tool's input schema.
- `deny`: a matching DENY takes precedence. Additional ALLOW grants and self-service requests cannot override it. An administrator may review/remove the DENY, or it may expire; the response does not mean denial is permanent.
- `block`: no matching ALLOW authorizes the call. If available, the caller may use `mcp_gateway.list_grants` to inspect access or `mcp_gateway.create_grant_request` to request it. Requesting access does not authorize the call; approval is required and is not guaranteed.
- `authorization_unavailable`: authorization could not be established; this is not a DENY or BLOCK decision. Review Gateway health and retained evidence rather than assuming another grant will fix it.

Self-service tools require their own grants. A blocked `mcp_gateway` call therefore offers optional administrator review instead of circular advice to call blocked self-service tools. Guidance never submits a request or retries a call automatically. Rejected calls do not execute locally or downstream.

An acknowledged rejection includes `data.invocationId` for administrator investigation, but no grant IDs, constraints, argument values, or raw errors. Without acknowledged admission, Gateway returns `audit_unavailable` without an invocation ID or rejection reason; do not infer a policy decision. Other error codes omit `data.reason`, and uncertain-outcome handling is unchanged. See the [normative response contract](../design/invocation-and-ingress.md#live-call-rejection-contract) for exact messages and [invocation evidence](invocation-evidence.md) before deciding whether to retry an uncertain call.

## Review grant requests

Agents create and cancel requests only through the six fixed self-service tools. Administrators inspect the queue through the CLI:

```bash
agent-gateway mcp grant-request list --principal-id PRINCIPAL_ID --state pending
agent-gateway mcp grant-request get REQUEST_ID
```

The browser opens **Pending**, oldest first; **All requests** starts newest first. Search by exact request ID, agent, or target, and filter scope or (in All requests) state. Every filter and sort applies before pagination across the complete collection. **Previous** and **Next** replace the bounded page and show its exact matching range. Request rows show the requested server/tool, duration, and conditions even after a narrower approval. The first **Action** column offers **Review** for pending rows or **View decision** for closed rows. The full searchable Request ID is also a link to the same page.

The item starts with requested authority, duration, conditions, and catalog posture without repeated general access warnings. Approval confirmation puts Agent, Approved target, and Tools before Duration and Conditions; empty optional descriptions are omitted. Completed pages are titled by outcome and place the **Created grant** link directly under **Approved decision**. This records the result of approval, not the grant's current status. **Technical identifiers and immutable evidence** retains exact IDs, policy JSON, and submitted/current descriptors as item-only evidence; collections do not include descriptors. Descriptions and schemas are untrusted inert evidence, not approval instructions or proof of callable authority. Missing evidence means comparison is unavailable, not unchanged; deliberate server-to-tool narrowing has no like-for-like submitted descriptor to compare. Its confirmation instead reports the selected tool's current descriptor posture.

Requests move once from `pending` to `approved`, `rejected`, or `cancelled`. They never expire, reopen, or revoke a later grant. Ownership survives credential rotation, restart, and stopped restore. Semantically identical pending submissions may return the existing request.

## Approve or reject a request

Approval may only narrow scope, exact constraint tokens, and duration. **Approve as requested** opens final confirmation directly; an optional grant description does not alter authority. **Customize approval** opens Tools, Conditions, and Duration with an **Approval preview** showing requested and proposed values. **Hide customization** preserves edits; a collapsed edited draft is labeled **Custom approval edited**. **Approve as requested** always uses the original request, even with hidden edits. The green **Approve as narrowed** action opens final confirmation for a changed draft; an unchanged draft instead says **Approve as requested**. Submitted conditions are locked, with exact source inspectable; only additive conjunctive conditions, a tool on the same requested server, and shorter duration are permitted. **Reject request** opens its own reason dialog without validating the approval draft. Rejection creates no grant, does not revoke access, and does not create a DENY. For approval, enter a whole-number duration and select minutes, hours, days, or seconds; the selected duration cannot exceed the submitted request, and a temporary request cannot become permanent. Approval always reads the submitted policy before mutation to prevent removing read-only restrictions. Omit `--etag` to use that read's exact ETag, or supply an explicit exact value; a mismatch stops without refreshing the supplied ETag or submitting the approval. Use direct flags for an unconstrained approval or the mutually exclusive strict file form for a constraint:

```bash
agent-gateway mcp grant-request approve REQUEST_ID \
  --description TEXT \
  --scope tool \
  --target SERVER.TOOL \
  --yes
agent-gateway mcp grant-request approve REQUEST_ID --etag ETAG --file PATH --yes
```

For read-only server requests, retain server scope, `--read-only`, and future-tool acknowledgement. An unrestricted server request can also narrow to read-only:

```bash
agent-gateway mcp grant-request approve REQUEST_ID \
  --scope server --target SERVER_NAMESPACE --read-only \
  --acknowledge-future-tools --duration-seconds 600 --yes
```

In the browser, **Approve as requested** preserves the submitted restriction. **Customize approval** can change **Allowed tools** from **All tools** to **Only tools marked read-only** for a server request; a requested read-only restriction locks the dropdown and disables exact-tool narrowing. Both requested and approved restrictions remain visible after approval. Duration may only shorten, and future-tool acknowledgement remains required. A read-only server request cannot narrow to an unrestricted exact-tool grant, even if that tool currently declares itself read-only. Conservative active-DENY conflicts remain safe errors; neither client replays rejected, stale, or uncertain mutations.

The strict approval file accepts optional Boolean `read_only` inside `approved_policy` with the same server-only semantics. It contains the complete closed approval body; an additive v2 narrowing looks like:

```json
{
  "description": "Approved report access",
  "approved_policy": {
    "scope": "tool",
    "target": "reports.read",
    "constraint": {
      "version": 2,
      "equals": { "/attempt": 1, "/filters/region": "us" },
      "regex": { "/resource": "item-\\d+" }
    },
    "duration_seconds": "900",
    "future_tools_acknowledged": false
  }
}
```

Copy every submitted atom byte-for-byte into the approval constraint before adding atoms; changing a numeric spelling or regex pattern is not retention. Approval rechecks current denial and target facts, then atomically commits one ordinary `ALLOW` and the approved transition. The approved grant ID is historical evidence only after commit; later grant deletion does not rewrite request history.

Reject with one closed reason—`not_approved`, `existing_access`, `scope_too_broad`, or `policy_conflict`:

```bash
agent-gateway mcp grant-request reject REQUEST_ID \
  --reason scope_too_broad \
  --yes
```

An acknowledged decision stays visible. Approved decisions show a read-only **Requested versus Approved** comparison of scope, tools, duration, and the actual condition sets, including unchanged dimensions. Return using **Back to pending requests** (or the originating All requests view); filters, sorting, and page context survive that return in the current session and are revalidated. **Review next** is offered when another pending request is available, but never opens automatically; its current state is read again on entry. Deep links fall back to the pending queue. Stale cursors restart once with a notice, and concurrent settlement requires fresh review. Unknown outcomes remain visible after refresh with investigation guidance, not a success claim or automatic retry.

Adjudication never executes, resumes, or replays the motivating call. It does not hold a call while approval is pending. After approval, the agent must make an explicit fresh `tools/call`. If an adjudication result is uncertain, use request and grant reads as bounded evidence; do not replay automatically.

## Self-service boundary

The fixed tools are `mcp_gateway.get_identity`, `mcp_gateway.list_grants`, `mcp_gateway.create_grant_request`, `mcp_gateway.get_grant_request`, `mcp_gateway.list_grant_requests`, and `mcp_gateway.cancel_grant_request`. They let an admitted agent inspect its own identity and grants, create/get/list/cancel its own requests, and nothing more. They cannot select another agent, mutate grants directly, adjudicate, read invocation evidence, acquire a downstream capability, or perform filesystem, process, network, keyring, credential, or administrator work. Synthetic catalog revision 4 advertises both matcher versions and read-only server requests before an agent submits a request.

A request may target one exact external tool or one server namespace, but never the reserved `mcp_gateway` server or its tools. Their ordinary grants and local calls still work; only an administrator can restore or change that access. Exact-tool requests may include either bounded matcher version. Server-wide requests require explicit future-tools acknowledgement and cannot include a constraint. Duration is permanent when null or a canonical decimal from 60 through 2,592,000 seconds. Approval must retain every submitted operator atom exactly, may add conjunctive atoms, may narrow v1 to v2, and never narrows v2 to v1 or infers regex implication.

See [Invocation evidence and unknown outcomes](invocation-evidence.md) for interpreting policy decisions and call outcomes. Return to the [documentation map](../README.md) or [Gateway README](../../README.md) for common workflows.

## HTTP grants and Test access

Use **HTTP → Grants** for HTTP policy, and **Edit agent** for its HTTP default. New agents default to block; existing agents retain their stored allow/block value. MCP grants, visibility, credentials and self-service are unchanged. [Proxy activation](http-proxy.md) is separately opt-in. [Scoped HTTP credentials](administration.md#scoped-http-credentials) are optional dependencies, not permission by themselves.

```sh
agent-gateway http grant list
agent-gateway http grant get ID
agent-gateway http grant create --file /private/grant.json --yes
agent-gateway http grant update ID --file /private/grant.json --yes
agent-gateway http grant delete ID --yes
agent-gateway http default get PRINCIPAL_ID
agent-gateway http default update PRINCIPAL_ID --file /private/default.json --yes
agent-gateway http test-access --file /private/preview.json
```

Grant files contain `principal_id`, nullable `description`, `policy` and nullable `expires_at`. Update replaces the complete configuration in place, preserving ID and agent; it is not delete/recreate. Default files are exactly `{"http_default":"block"}` or `{"http_default":"allow"}`. These convenience commands read/write the canonical identity endpoint and return `Principal` JSON with its unified ETag, not a separate default resource. For a combined change, use `agent-gateway agent update PRINCIPAL_ID --display-name NAME --http-default allow --yes`. Omitted ETags get one validated read; use `--etag` to pin a reviewed agent revision. A stale or uncertain result must be inspected, never automatically replayed.

Example grant policy: `{"version":1,"type":"allow_requests","request":{"origin":{"scheme":"https","host":"api.example.com","port":443},"methods":{"values":["GET"]},"path":{"kind":"segment_prefix","value":"/v1"}}}`. An optional `credential_id` must contain the entire HTTPS origin scope. Referenced credentials cannot be deleted or have their recipe changed; incompatible scope edits fail atomically. Expired grants remain visible and reference-bearing until deleted.

Test access takes exactly `{"principal_id":"ID","url":"https://api.example.com/v1?x=1","method":"GET"}` or `{"principal_id":"ID","connect":{"host":"api.example.com","port":443}}`. It sends no upstream request and resolves neither DNS nor secrets. Interpret results as policy-only: network, TLS and material are unverified, and the result is not future admission authority. Submitted paths/query values are not retained in audit/events/logs. Use the same Test access form from the Grants page for readable deciding grant/default and credential-conflict explanations. Results show the policy, HTTP-default and agent revisions and revision-qualified deciding grant/credential references. They are labeled policy snapshots; policy changes require another explicit test.

The vocabulary is **Block destination**, **Allow tunnel**, **Block requests** and
**Allow requests**. A destination block wins; a matching tunnel allow makes
CONNECT opaque and bypasses request restrictions and injection. Otherwise each
request is intercepted and checked: request block wins, then request allow, then
the agent's HTTP default. A plain/default allow does not override a matching
credential requirement or permit private/loopback access. Only an applicable
allow grant can permit private/loopback access; metadata and Gateway listeners
remain forbidden. Do not treat GET as read-only or the cooperative proxy as
network-enforced containment.

See the [normative HTTP v1 contract](../design/identity-and-authorization.md#http-policy-version-1)
for the deliberately restricted path grammar, explicit wildcard hosts, exact
ports, credential containment and bounded explanations. This policy surface does not migrate Broker rules or qualify capacity or native
credentials.

<a id="browse-principal-and-grant-tables"></a>

## Browse agent and grant tables

The browser shows up to 50 records per page. Use **Previous** and **Next** above the table to replace the displayed page. Filters and Reset sit above navigation, with the displayed range and exact total on the right, for example **Showing 51–100 of 128 grants**. Column filters and sorting search the whole collection, not only the displayed rows; with filters active, the total counts only matching records. Zero results say **No grants/agents** or **No matching grants/agents**. Loading and failure do not present an old count as current. Grant rows include agent and target names without loading every reference record.

Filters and sorting are included in the URL. Browser Back/Forward restores those settings; changing them, reloading, or opening a shared link starts at the first matching page. Page cursors remain in the current browser session only. When a cursor expires or its snapshot changes, the table returns to the first page with a notice. If that read fails, use **Refresh** explicitly; there is no retry loop. Empty inventories, no matches, loading, and failures have distinct messages, and filters remain available with no rows.
