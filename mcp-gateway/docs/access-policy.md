# Principals, grants, and requests

Audience: Gateway administrators managing agent access

Purpose: Manage principals, credentials, grants, and grant requests.

This guide owns operator workflows for principal lifecycle, one-time agent credentials, immutable grants, constraints, and grant-request adjudication. Generated help owns exact syntax:

- `mcp-gateway principal --help`
- `mcp-gateway grant --help`
- `mcp-gateway grant-request --help`

See [DESIGN](../DESIGN.md) for the system design index and [Identity and authorization](design/identity-and-authorization.md) for normative authorization, policy evaluation, and request-state semantics. See [CLI and local administration](cli-local-administration.md) for shared authentication, output, strict input, ETag, confirmation, and retry rules. These are online workflows: start `mcp-gateway serve` first; a proven refused selected address reports the exact startup command.

## Create and inspect principals

List or inspect permanent principals before changing policy:

```bash
mcp-gateway principal list
mcp-gateway principal get PRINCIPAL_ID
mcp-gateway principal create --display-name NAME --visibility VISIBILITY
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
mcp-gateway principal update PRINCIPAL_ID --display-name NAME
mcp-gateway principal update PRINCIPAL_ID --etag ETAG --state disabled --yes
```

Changing state requires consequence confirmation. Disabling a principal clears its current credential and sessions. Re-enabling does not restore authority, a prior credential, or deleted grants. Display-name and visibility-only updates do not prompt. The CLI never refreshes a stale precondition or replays a patch automatically.

## Issue, rotate, or revoke an agent credential

A principal has at most one current non-expiring `mgw_agent_` bearer. `issue` requires an empty slot; `rotate` requires an occupied slot and atomically replaces its authority:

```bash
mcp-gateway principal credential issue PRINCIPAL_ID \
  --secret-output /safe/new/agent-bearer \
  --yes
mcp-gateway principal credential rotate PRINCIPAL_ID \
  --secret-output /safe/new/rotated-agent-bearer \
  --yes
```

Both commands always read the principal once to enforce slot intent. An optional explicit `--etag ETAG` must match that observation; omitting it uses the observed current value. The bearer is published once to a prepared controlling terminal or a fresh owner-only file. Metadata cannot recover it. Rotation advances principal and credential revisions atomically, so the old bearer never overlaps current authority.

Revoke with an automatic or explicit current principal ETag:

```bash
mcp-gateway principal credential revoke PRINCIPAL_ID --yes
mcp-gateway principal credential revoke PRINCIPAL_ID --etag ETAG --yes
```

Issue, rotate, revoke, and disable never replay automatically. On an uncertain result, read the principal and review its credential revision before deciding what to do. A lost bearer cannot be recovered and is not evidence that rotation failed. After Gateway acknowledges issue, lost output may leave the singular slot occupied even though no bearer can be recovered from metadata. After acknowledged rotation, the replacement may be current and the prior bearer may already be invalid. In either case, explicitly rotate or revoke the observed current credential instead of replaying the original operation.

## Create and inspect immutable grants

```bash
mcp-gateway grant list --principal-id PRINCIPAL_ID --server-id SERVER_ID
mcp-gateway grant get GRANT_ID
mcp-gateway grant create --description TEXT --principal-id PRINCIPAL_ID --effect allow --server-id SERVER_ID
mcp-gateway grant create --file PATH
```

Every grant has a stable ID and may have a non-unique human-readable description. The description is display metadata: update or clear it with `grant update GRANT_ID --description TEXT` (an empty value clears it) and an automatic or explicit exact ETag. A description-only patch advances the grant's metadata revision without advancing policy revision or cancelling leases. The direct create form creates an ordinary unconstrained grant and may add `--description`, `--upstream-name`, or `--expires-at`. Use the mutually exclusive strict file form for a constraint; it supplies the complete closed shape, including explicit nullable `description`, `upstream_name`, `constraint`, and `expires_at` members. Grants are immutable for identity and policy; each remains an `ALLOW` or `DENY` row even when its optional description changes. A server-wide grant uses a null upstream name; an exact-tool grant names one upstream tool. Exact names do not require a currently active descriptor.

Constraints use the bounded equality form `{"equals":{"/object/path":value}}`. They support object-only RFC 6901 traversal, scalar values, exact lexical number tokens, and at most 16 atoms. Constraints apply only to exact-tool grants. Gateway does not coerce values, traverse arrays, interpret schemas, or treat overlapping paths as equivalent.

Expired grants remain readable and count toward capacity until deleted. The **Default Gateway access** grant is also an ordinary capacity-owning row. Discovery is deliberately broader than execution for constrained policy; only exact call admission evaluates the unchanged argument object.

## Replace or remove a grant

Because grant policy is immutable, choose the replacement order deliberately. Create before delete produces temporary overlap; delete before create produces temporary loss. Each step is an independent confirmed mutation, and no later step runs automatically. If the first result is uncertain, stop and read current grants before submitting the second step. Re-read effective policy before changing the old row: `DENY` takes precedence over `ALLOW`, and an unconstrained denial can hide a tool from discovery.

Delete by stable grant ID:

```bash
mcp-gateway grant delete GRANT_ID --yes
```

Deletion has no ETag or idempotency surface. An uncertain create or delete requires narrow principal/grant reads rather than replay. Visibility by itself never authorizes calls, and deleting an expired or default grant can still change capacity or self-service behavior.

## Review grant requests

Agents create and cancel requests only through the six fixed self-service tools. Administrators inspect the queue through the CLI:

```bash
mcp-gateway grant-request list --principal-id PRINCIPAL_ID --state pending
mcp-gateway grant-request get REQUEST_ID
```

Collections contain summaries. The item view adds item-only evidence, optional approved evidence, and a read-time comparison with the current target. Descriptor evidence explains what was requested; it is not current callability or authority.

Requests move once from `pending` to `approved`, `rejected`, or `cancelled`. They never expire, reopen, or revoke a later grant. Ownership survives credential rotation, restart, and stopped restore. Semantically identical pending submissions may return the existing request.

## Approve or reject a request

Approval may only narrow scope, exact constraint tokens, and duration. Omit `--etag` for one validated item preflight, or provide an explicit exact value. Use direct flags for an unconstrained approval or the mutually exclusive strict file form for a constraint:

```bash
mcp-gateway grant-request approve REQUEST_ID \
  --description TEXT \
  --scope tool \
  --target SERVER.TOOL \
  --yes
mcp-gateway grant-request approve REQUEST_ID --etag ETAG --file PATH --yes
```

Approval rechecks current denial and target facts, then atomically commits one ordinary `ALLOW` and the approved transition. The approved grant ID is historical evidence only after commit; later grant deletion does not rewrite request history.

Reject with one closed reason—`not_approved`, `existing_access`, `scope_too_broad`, or `policy_conflict`:

```bash
mcp-gateway grant-request reject REQUEST_ID \
  --reason scope_too_broad \
  --yes
```

Adjudication never executes, resumes, or replays the motivating call. It does not hold a call while approval is pending. After approval, the agent must make an explicit fresh `tools/call`. If an adjudication result is uncertain, use request and grant reads as bounded evidence; do not replay automatically.

## Self-service boundary

The fixed tools are `mcp_gateway.get_identity`, `mcp_gateway.list_grants`, `mcp_gateway.create_grant_request`, `mcp_gateway.get_grant_request`, `mcp_gateway.list_grant_requests`, and `mcp_gateway.cancel_grant_request`. They let an admitted principal inspect its own identity and grants, create/get/list/cancel its own requests, and nothing more. They cannot select another principal, mutate grants directly, adjudicate, read invocation evidence, acquire a downstream capability, or perform filesystem, process, network, keyring, credential, or administrator work.

A request may target one exact external tool or one server namespace. Exact-tool requests may include the same bounded equality constraint. Server-wide requests require explicit future-tools acknowledgement and cannot include a constraint. Duration is permanent when null or a canonical decimal from 60 through 2,592,000 seconds. Approval can narrow but never broaden those terms.

See [Invocation evidence and unknown outcomes](invocation-evidence.md) for interpreting policy decisions and call outcomes. Return to the [Gateway README](../README.md) for common workflows.
