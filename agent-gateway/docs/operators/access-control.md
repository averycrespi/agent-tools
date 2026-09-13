# Access control: principals, grants, and requests

Audience: Gateway administrators managing agent access

Purpose: Manage principals, credentials, grants, and grant requests.

This guide owns Agent Gateway operator workflows for principal lifecycle, one-time agent credentials, immutable grants, constraints, and grant-request adjudication. Prefer `agent-gateway` for new commands; the `mcp-gateway` binary accepts the same commands and flags. Neither name changes credentials or the fixed `mcp_gateway.*` self-service tools. The shared internal access-target boundary also requires no database migration, grant/request rewrite, credential replacement, or MCP agent-client changes. Standalone administrative clients must upgrade for the [operator v2 cutover](administration.md#operator-v2-cutover). MCP remains the only supported target domain. Generated help owns exact syntax:

- `agent-gateway principal --help`
- `agent-gateway grant --help`
- `agent-gateway grant-request --help`

See [DESIGN](../../DESIGN.md) for the system design index and [Identity and authorization](../design/identity-and-authorization.md) for normative authorization, policy evaluation, and request-state semantics. See [Administrator CLI and local administration](administration.md) for shared authentication, output, strict input, ETag, confirmation, and retry rules. These are online workflows: start `agent-gateway serve` first; a proven refused selected address reports the exact startup command.

## Browse principal and grant tables

The browser shows up to 50 records per page. Use **Previous** and **Next** above the table to replace the displayed page. Filters and Reset sit above navigation, with the displayed range and exact total on the right, for example **Showing 51–100 of 128 grants**. Column filters and sorting search the whole collection, not only the displayed rows; with filters active, the total counts only matching records. Zero results say **No grants/principals** or **No matching grants/principals**. Loading and failure do not present an old count as current. Grant rows include principal and target names without loading every reference record.

Filters and sorting are included in the URL. Browser Back/Forward restores those settings; changing them, reloading, or opening a shared link starts at the first matching page. Page cursors remain in the current browser session only. When a cursor expires or its snapshot changes, the table returns to the first page with a notice. If that read fails, use **Refresh** explicitly; there is no retry loop. Empty inventories, no matches, loading, and failures have distinct messages, and filters remain available with no rows.

## Create and inspect principals

List or inspect permanent principals before changing policy:

```bash
agent-gateway principal list
agent-gateway principal get PRINCIPAL_ID
agent-gateway principal create --display-name NAME --visibility VISIBILITY
```

Creation requires a display name and one visibility mode:

- `all` discovers every current tool;
- `requestable` hides tools covered by an applicable unconstrained `DENY`;
- `allowed-only` requires an applicable `ALLOW` and no applicable unconstrained `DENY`.

Visibility controls discovery, not call authorization. A constrained `ALLOW` may make a tool discoverable even when a particular argument object will not match, while a constrained `DENY` does not hide it.

Principal creation also creates an ordinary permanent grant described as **Default Gateway access** for the six fixed `mcp_gateway` self-service tools. This is the design's synthetic default grant. That grant counts toward capacity, can be deleted or overridden by `DENY`, and only an administrator can restore equivalent access. Principals are permanent and cannot be deleted.

## Update principal state or visibility

Submit a nonempty direct patch; omit `--etag` for one validated current-item preflight, or supply it to pin an already observed exact value and skip that convenience read:

```bash
agent-gateway principal update PRINCIPAL_ID --display-name NAME
agent-gateway principal update PRINCIPAL_ID --etag ETAG --state disabled --yes
```

Changing state requires consequence confirmation. Disabling a principal clears its current credential and sessions. Re-enabling does not restore authority, a prior credential, or deleted grants. Display-name and visibility-only updates do not prompt. The CLI never refreshes a stale precondition or replays a patch automatically.

## Issue, rotate, or revoke an agent credential

A principal has at most one current non-expiring `mgw_agent_` bearer. `issue` requires an empty slot; `rotate` requires an occupied slot and atomically replaces its authority:

```bash
agent-gateway principal credential issue PRINCIPAL_ID \
  --secret-output /safe/new/agent-bearer \
  --yes
agent-gateway principal credential rotate PRINCIPAL_ID \
  --secret-output /safe/new/rotated-agent-bearer \
  --yes
```

Both commands always read the principal once to enforce slot intent. An optional explicit `--etag ETAG` must match that observation; omitting it uses the observed current value. The bearer is published once to a prepared controlling terminal or a fresh owner-only file. Metadata cannot recover it. Rotation advances principal and credential revisions atomically, so the old bearer never overlaps current authority.

Revoke with an automatic or explicit current principal ETag:

```bash
agent-gateway principal credential revoke PRINCIPAL_ID --yes
agent-gateway principal credential revoke PRINCIPAL_ID --etag ETAG --yes
```

Issue, rotate, revoke, and disable never replay automatically. On an uncertain result, read the principal and review its credential revision before deciding what to do. A lost bearer cannot be recovered and is not evidence that rotation failed. After Gateway acknowledges issue, lost output may leave the singular slot occupied even though no bearer can be recovered from metadata. After acknowledged rotation, the replacement may be current and the prior bearer may already be invalid. In either case, explicitly rotate or revoke the observed current credential instead of replaying the original operation.

## Provision a Pi agent in a Lima sandbox

Use [configure-agent-gateway.sh](../../examples/provision/configure-agent-gateway.sh) to configure the Pi MCP Gateway extension in a Linux guest. It emits **both** `AGENT_GATEWAY_ENDPOINT` / `AGENT_GATEWAY_AGENT_TOKEN` and the temporary `MCP_GATEWAY_ENDPOINT` / `MCP_GATEWAY_AGENT_TOKEN` compatibility pair. All four values derive from one selected endpoint and one current token file. [configure-mcp-gateway.sh](../../examples/provision/configure-mcp-gateway.sh) is a standalone, byte-identical compatibility entry point. This duplication is deliberate: sandbox-manager copies each script individually to a temporary filename, without siblings. The regression test guards equality; maintain the canonical file and refresh the compatibility copy together.

This is client configuration only: no installation, grant changes, credential issuance/rotation, or network connectivity test. There is no canonical-only mode. The external consumer gate below must pass before compatibility exports can be retired.

On the host, start the initialized Gateway with the trusted forwarding hostname allowed:

```bash
agent-gateway serve --allowed-host host.lima.internal
```

The listener remains `127.0.0.1:8210`. Lima must provide the trusted host-forwarding path separately. HTTP does not provide confidentiality or server authentication; use this only for trusted local forwarding. See [forwarding trust boundaries](administration.md#trusted-local-forwarding-and-sandbox-administration). Agent provisioning does **not** require the sandbox administrator credential described there.

Create a dedicated principal, record its ID, and issue its agent credential on the host:

```bash
agent-gateway principal create --display-name sandbox-pi --visibility allowed-only
mkdir -p "$HOME/.config/agent-gateway"
chmod 700 "$HOME/.config/agent-gateway"
agent-gateway principal credential issue PRINCIPAL_ID \
  --secret-output "$HOME/.config/agent-gateway/agent-token" \
  --yes
```

For a **new principal only**, the output file must be fresh; Gateway creates it owner-only. This is a chosen client-transfer path, not an automatically generated Gateway credential. Configure grants separately; issuance alone does not authorize upstream calls. For an existing principal, retain its current credential and follow the path migration below instead of issuing or rotating merely for a rename.

Add these entries to sandbox-manager configuration, preserving other paths and scripts:

```json
{
  "copy_paths": ["~/.config/agent-gateway/agent-token"],
  "scripts": [
    "/path/to/agent-tools/agent-gateway/examples/provision/configure-agent-gateway.sh"
  ]
}
```

Before the first transfer, open `sb shell` and create the guest's `~/.config/agent-gateway` directory with mode `0700` (and the legacy directory too if transferring that path). Verify ownership, absence of symlinks and any existing destination before copying. Sandbox-manager's parent `mkdir -p` does not enforce private permissions; provisioning scripts run too late to protect the initial transfer.

Then run `sb provision`. It refreshes `copy_paths` **before** scripts. Copy only the agent credential, never an administrator credential or the Gateway data directory. The guest token directory must be owned by the guest user with mode `0700`, the regular token file `0600` (or read-only `0400`), and `.config` owned and not group/world-writable. Symlinked `.config`, token directories/files, and `.bashrc` are refused. Ensure guest permissions before running the script; it never repairs or overwrites credentials.

The managed block reads and validates the current file at every Bash startup, then assigns:

```bash
export MCP_GATEWAY_ENDPOINT="$AGENT_GATEWAY_ENDPOINT"
export MCP_GATEWAY_AGENT_TOKEN="$AGENT_GATEWAY_AGENT_TOKEN"
```

The canonical endpoint is fixed in the script's `_agent_gateway_load` function to `http://host.lima.internal:8210/mcp`; inherited endpoint/token variables do not select authority. Both pairs are cleared first and replaced together. The current agent-config consumer accepts the bearer only through the legacy environment variable, not settings JSON or a credential-file setting. The exact `/mcp` suffix, trusted-host setup and consumer endpoint validation remain required. For a nondefault endpoint, deliberately adapt the single endpoint literal and qualify it against the consumer's validation; do not independently edit either alias. Read-only mode and timeouts remain at consumer defaults.

Tokens are never embedded in `.bashrc`, printed, or passed as arguments. The block disables shell xtrace before reading them and leaves it disabled; do not re-enable tracing around credential-consuming commands. Both pairs expose the same bearer to Pi and inherited child environments, as before; this is not an OS credential boundary. Never export an administrator bearer, dump the environment, or put credentials in settings JSON, tickets or logs.

### Existing sandbox migration and conflicts

| Previous interface                                | Transition interface                                                      |
| ------------------------------------------------- | ------------------------------------------------------------------------- |
| `configure-mcp-gateway.sh`                        | Standalone compatibility copy of `configure-agent-gateway.sh`             |
| `~/.config/mcp-gateway/agent-token`               | Legacy fallback; canonical `~/.config/agent-gateway/agent-token`          |
| `# >>> mcp-gateway >>>` / `# <<< mcp-gateway <<<` | One `agent-gateway` marker pair                                           |
| `MCP_GATEWAY_ENDPOINT`, `MCP_GATEWAY_AGENT_TOKEN` | Retained aliases of `AGENT_GATEWAY_ENDPOINT`, `AGENT_GATEWAY_AGENT_TOKEN` |

Token selection is evaluated at provisioning **and** shell startup:

- Only canonical present: use canonical. Only legacy present: use legacy; this supports already-provisioned profiles without moving a credential.
- Both present: validate both and require the same bearer (one optional final newline is insignificant); select canonical. A missing file is not an empty file: an existing empty, unreadable, invalid, administrator-shaped, nonprivate or symlinked file is an error, even when the other path is valid.
- Neither present or any conflict: provisioning fails without changing `.bashrc`; startup clears all four exports and emits a value-free diagnostic. Existing parent directories are validated even when their token file is absent; an empty or dangling symlink directory is not a missing-token fallback. There is no fallback to stale inherited credentials.
- Files are never moved, deleted or overwritten by either script. Existing legacy credentials remain until separately reconciled by their owner.

To migrate the transfer path without rotating authority:

1. Keep the old profile/script available as rollback evidence. Privately validate that the old host file is the current agent credential; do not print it. Create the canonical parent with mode `0700`. Copy the **same** credential to a fresh canonical file using an owner-private, no-clobber operation. If the destination exists, compare privately and stop on disagreement; never blindly overwrite it.
2. Inspect both guest paths **before** refreshing `copy_paths`, since the refresh itself can replace files before the script can reject a conflict. Reconcile any different credential with its owner. Update the repository checkout used by the profile, then update the profile's transfer path and script together. Keep refreshing both paths while both copies exist if future rotation must support rollback.
3. Reprovision. A single complete old or new managed block is replaced wholesale with one canonical block at the end of `.bashrc`; unrelated bytes stay in order. A missing final newline gets one separator. Nested, duplicate, mismatched, partial markers or simultaneous old/new blocks are refused before replacement; explicitly reconcile to one intended block rather than letting the script guess which authority was intended. A NUL-containing file is refused. The resulting `.bashrc` is owner-private.
4. Open a **fresh Bash shell**, then restart Pi and other launchers holding old environments. Merely updating files or a parent shell does not change an already-running process. Remove/reconcile independent Gateway exports outside the block and in launcher settings; otherwise they may reinstate stale values. Noninteractive launchers not sourcing `.bashrc` need equivalent trusted setup and qualification.

The block is intentionally moved to the end; it must not depend on a later line. Broker's separate managed block remains untouched, but do not load the old Broker extension alongside Gateway; removing that configuration is separate work. Provision only while this user's shell files and token paths are quiescent; these example scripts are not a lock or a boundary against another writer with control of the account.

For a real rotation, use the explicit `principal credential rotate` procedure above with a fresh output file, then securely refresh each retained transfer copy to the same replacement. Update guest copies together and start a fresh shell/Pi process. Two different old/new copies deliberately fail closed; do not select an old bearer as rollback after rotation invalidated it. Coordinate the interruption and never replay an uncertain rotation.

### Consumer compatibility, qualification and rollback

The inspected external consumer revision is [agent-config `fadc9ac8d681f27bf9c8e5f59fc56011a0d512b1`](https://github.com/averycrespi/agent-config/commit/fadc9ac8d681f27bf9c8e5f59fc56011a0d512b1). Its `pi/agent/extensions/mcp-gateway/config.ts` accepts only `MCP_GATEWAY_*`: endpoint environment overrides trusted global settings; the bearer comes only from the environment. Project settings are ignored, so they cannot redirect a global credential. It validates exact `/mcp`, transport/hostname rules and `mgw_agent_` shape; administrator-shaped tokens and endpoint-embedded credentials are rejected without reflecting input. `config.test.ts` covers these boundaries; `pi/agent/extensions/code-mode/tool.test.ts` still uses the legacy pair.

| Producer                                         | Consumer                                | Qualification boundary                                                                                                                                                                             |
| ------------------------------------------------ | --------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Old (`ce28fd6c447bf83a6058295be19e2b5c3d60f3b4`) | Pinned legacy revision above            | Baseline legacy pair; qualify in isolated configuration integration                                                                                                                                |
| Transition (this change), either entry point     | Pinned legacy revision above            | Legacy aliases preserve access; qualify resulting shell environment through the real loader                                                                                                        |
| Transition                                       | Separately delivered canonical consumer | **Blocked:** no compatible delivered revision identified; qualify canonical/legacy precedence, conflicts, atomic pairing, redaction, administrator rejection and global/project authority together |
| Canonical-only                                   | Any consumer                            | **Not enabled.** Requires compatible consumer qualification and separately authorized rollout/alias retirement                                                                                     |

Use synthetic agent material and disposable homes/project/global settings for configuration integration; never real tokens or native credentials. Record exact producer/consumer revisions, command, results and scope. Loader/source tests do not prove an MCP connection, installation or rollout. A merged consumer PR alone does not satisfy adoption. No external repository edits, Pi extension/settings namespace changes, MCP wire renames, keyring migrations or credential replacement are implied.

Rollback during transition normally means retaining the transition producer and selecting the known legacy consumer: it already receives the same legacy pair. If restoring the old producer is necessary, stop Pi/launchers, privately reconcile the old-path file to the **current** credential, restore the old `copy_paths`/script together, and explicitly remove the canonical managed block before running the old script (the old script cannot recognize it). Remove stale canonical exports from launcher environments, open a fresh shell and requalify. Keep unrelated content and private backups; do not delete credentials or revert to an invalidated bearer. Restoring an old shell without reconciling the marker/file mapping is unsafe.

### Nonsecret adoption evidence

For **each known sandbox profile and each client launch environment** (interactive Bash, noninteractive launcher and any independent settings), the rollout owner records:

- Profile/launcher identifier, exact producer and consumer revisions, transfer-path choice, script entry point, permission-check result and managed-block count—never bearer values or environment dumps.
- Fresh-shell/restarted-Pi confirmation; Boolean results for both endpoint values agreeing with the intended trusted endpoint, both tokens present/equal to the selected current file, and no stale/conflicting exports in startup files or launcher configuration.
- Public-safe results of configuration validation, administrator-token rejection, project-versus-global authority and redaction tests; separately authorized MCP connectivity/admission evidence, if obtained.
- Outcome, outstanding conflicts, operator, rollback choice and next action. Missing profiles/launchers stay explicitly unqualified; do not infer adoption from this repository's tests or CI.

This change does not inventory or mutate live profiles, start services, transfer real credentials, or claim deployment. The external canonical-consumer and live-adoption gaps remain open.

## Create and inspect immutable grants

```bash
agent-gateway grant list --principal-id PRINCIPAL_ID --server-id SERVER_ID
agent-gateway grant get GRANT_ID
agent-gateway grant create --description TEXT --principal-id PRINCIPAL_ID --effect allow --server-id SERVER_ID
agent-gateway grant create --file PATH
```

Every grant has a stable ID and may have a non-unique human-readable description. The description is display metadata: update or clear it with `grant update GRANT_ID --description TEXT` (an empty value clears it) and an automatic or explicit exact ETag. A description-only patch advances the grant's metadata revision without advancing policy revision or cancelling leases. The direct create form creates an ordinary unconstrained grant and may add `--description`, `--upstream-name`, or `--expires-at`. Use the mutually exclusive strict file form for a constraint; it supplies the complete closed shape, including explicit nullable `description`, `upstream_name`, `constraint`, and `expires_at` members. Grants are immutable for identity and policy; each remains an `ALLOW` or `DENY` row even when its optional description changes. A server-wide grant uses a null upstream name; an exact-tool grant names one upstream tool. Exact names do not require a currently active descriptor.

### Read-only server access

Add `--read-only` to direct server ALLOW creation, or select **Only tools marked read-only** in the browser's **Allowed tools** dropdown for a server ALLOW:

```bash
agent-gateway grant create --principal-id PRINCIPAL_ID --effect allow --server-id SERVER_ID --read-only
```

The strict grant file accepts optional Boolean `"read_only":true` alongside the existing required members, with `upstream_name:null` and `constraint:null`. Omission or false preserves unrestricted behavior. True is invalid for DENY, exact-tool, or argument-constrained grants; direct flags (including `--read-only=false`) cannot be combined with `--file`.

Only tools explicitly declaring `annotations.readOnlyHint=true` qualify, including future qualifying tools. Missing, null, or false hints do not qualify. Annotations are trusted server declarations, not side-effect isolation. This restricts one ALLOW, not the whole principal: other ALLOW grants may authorize writes, and matching DENY still wins. CLI and browser grant/request reads distinguish read-only from unrestricted server access. Browser replacement ALLOWs retain the restriction; replacement DENYs cover all tools on the server.

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
agent-gateway grant delete GRANT_ID --yes
```

Deletion has no ETag or idempotency surface. An uncertain create or delete requires narrow principal/grant reads rather than replay. Visibility by itself never authorizes calls, and deleting an expired or default grant can still change capacity or self-service behavior.

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
agent-gateway grant-request list --principal-id PRINCIPAL_ID --state pending
agent-gateway grant-request get REQUEST_ID
```

The browser opens **Pending**, oldest first; **All requests** starts newest first. Search by exact request ID, principal, or target, and filter scope or (in All requests) state. Every filter and sort applies before pagination across the complete collection. **Previous** and **Next** replace the bounded page and show its exact matching range. Request rows show the requested server/tool, duration, and conditions even after a narrower approval. The first **Action** column offers **Review** for pending rows or **View decision** for closed rows. The full searchable Request ID is also a link to the same page.

The item starts with requested authority, duration, conditions, and catalog posture without repeated general access warnings. Approval confirmation puts Principal, Approved target, and Tools before Duration and Conditions; empty optional descriptions are omitted. Completed pages are titled by outcome and place the **Created grant** link directly under **Approved decision**. This records the result of approval, not the grant's current status. **Technical identifiers and immutable evidence** retains exact IDs, policy JSON, and submitted/current descriptors as item-only evidence; collections do not include descriptors. Descriptions and schemas are untrusted inert evidence, not approval instructions or proof of callable authority. Missing evidence means comparison is unavailable, not unchanged; deliberate server-to-tool narrowing has no like-for-like submitted descriptor to compare. Its confirmation instead reports the selected tool's current descriptor posture.

Requests move once from `pending` to `approved`, `rejected`, or `cancelled`. They never expire, reopen, or revoke a later grant. Ownership survives credential rotation, restart, and stopped restore. Semantically identical pending submissions may return the existing request.

## Approve or reject a request

Approval may only narrow scope, exact constraint tokens, and duration. **Approve as requested** opens final confirmation directly; an optional grant description does not alter authority. **Customize approval** opens Tools, Conditions, and Duration with an **Approval preview** showing requested and proposed values. **Hide customization** preserves edits; a collapsed edited draft is labeled **Custom approval edited**. **Approve as requested** always uses the original request, even with hidden edits. The green **Approve as narrowed** action opens final confirmation for a changed draft; an unchanged draft instead says **Approve as requested**. Submitted conditions are locked, with exact source inspectable; only additive conjunctive conditions, a tool on the same requested server, and shorter duration are permitted. **Reject request** opens its own reason dialog without validating the approval draft. Rejection creates no grant, does not revoke access, and does not create a DENY. For approval, enter a whole-number duration and select minutes, hours, days, or seconds; the selected duration cannot exceed the submitted request, and a temporary request cannot become permanent. Approval always reads the submitted policy before mutation to prevent removing read-only restrictions. Omit `--etag` to use that read's exact ETag, or supply an explicit exact value; a mismatch stops without refreshing the supplied ETag or submitting the approval. Use direct flags for an unconstrained approval or the mutually exclusive strict file form for a constraint:

```bash
agent-gateway grant-request approve REQUEST_ID \
  --description TEXT \
  --scope tool \
  --target SERVER.TOOL \
  --yes
agent-gateway grant-request approve REQUEST_ID --etag ETAG --file PATH --yes
```

For read-only server requests, retain server scope, `--read-only`, and future-tool acknowledgement. An unrestricted server request can also narrow to read-only:

```bash
agent-gateway grant-request approve REQUEST_ID \
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
agent-gateway grant-request reject REQUEST_ID \
  --reason scope_too_broad \
  --yes
```

An acknowledged decision stays visible. Approved decisions show a read-only **Requested versus Approved** comparison of scope, tools, duration, and the actual condition sets, including unchanged dimensions. Return using **Back to pending requests** (or the originating All requests view); filters, sorting, and page context survive that return in the current session and are revalidated. **Review next** is offered when another pending request is available, but never opens automatically; its current state is read again on entry. Deep links fall back to the pending queue. Stale cursors restart once with a notice, and concurrent settlement requires fresh review. Unknown outcomes remain visible after refresh with investigation guidance, not a success claim or automatic retry.

Adjudication never executes, resumes, or replays the motivating call. It does not hold a call while approval is pending. After approval, the agent must make an explicit fresh `tools/call`. If an adjudication result is uncertain, use request and grant reads as bounded evidence; do not replay automatically.

## Self-service boundary

The fixed tools are `mcp_gateway.get_identity`, `mcp_gateway.list_grants`, `mcp_gateway.create_grant_request`, `mcp_gateway.get_grant_request`, `mcp_gateway.list_grant_requests`, and `mcp_gateway.cancel_grant_request`. They let an admitted principal inspect its own identity and grants, create/get/list/cancel its own requests, and nothing more. They cannot select another principal, mutate grants directly, adjudicate, read invocation evidence, acquire a downstream capability, or perform filesystem, process, network, keyring, credential, or administrator work. Synthetic catalog revision 4 advertises both matcher versions and read-only server requests before an agent submits a request.

A request may target one exact external tool or one server namespace, but never the reserved `mcp_gateway` server or its tools. Their ordinary grants and local calls still work; only an administrator can restore or change that access. Exact-tool requests may include either bounded matcher version. Server-wide requests require explicit future-tools acknowledgement and cannot include a constraint. Duration is permanent when null or a canonical decimal from 60 through 2,592,000 seconds. Approval must retain every submitted operator atom exactly, may add conjunctive atoms, may narrow v1 to v2, and never narrows v2 to v1 or infers regex implication.

See [Invocation evidence and unknown outcomes](invocation-evidence.md) for interpreting policy decisions and call outcomes. Return to the [documentation map](../README.md) or [Gateway README](../../README.md) for common workflows.
