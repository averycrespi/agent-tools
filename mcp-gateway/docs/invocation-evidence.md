# Invocation evidence and unknown outcomes

Audience: Operators investigating governed tool calls

Purpose: Interpret invocation evidence, redaction, and unknown outcomes.

This guide owns read-only invocation inspection, redacted evidence, terminal outcomes, transport certainty, and the operator response to unknown outcomes. Generated `mcp-gateway invocation --help` owns exact syntax.

See [DESIGN](../DESIGN.md) for the system design index and [Invocation and MCP ingress](design/invocation-and-ingress.md) for normative admission, execution, audit-retention, and failure semantics. See [Access policy](access-policy.md) for principals, grants, requests, and authorization decisions. See [CLI and local administration](cli-local-administration.md) for shared pagination and output behavior.

## List and inspect evidence

Invocation resources are read-only. They do not expose mutation, replay, result retrieval, or an event stream.

```bash
mcp-gateway invocation list --limit 50
mcp-gateway invocation get INVOCATION_ID
```

Lists are newest-first and support closed principal, server, requested-name, admission, decision, and outcome filters:

```bash
mcp-gateway invocation list \
  --principal-id PRINCIPAL_ID \
  --server-id SERVER_ID \
  --requested-name TOOL_NAME \
  --outcome outcome_unknown
```

Use `--admission-class`, `--decision`, and `--outcome` only with values shown by generated help. Filters bind the opaque cursor. A malformed cursor returns `invalid_cursor`; a cursor whose retention floor or bound state is no longer coherent returns `stale_cursor`. Start again without the cursor rather than trying to edit or reuse it under different filters.

Collections omit argument captures and return summary evidence only. `mcp-gateway invocation get INVOCATION_ID --output json` adds the one fixed-redacted argument capture when it was safely retained; default human item output remains summary-only. A missing item can mean the ID never existed or that bounded retention evicted it.

## Read the evidence shape

A summary identifies the admitted principal and credential revision, request time, admission class, requested name when classifiable, resolved target when present, authorization evidence when evaluated, and one outcome class plus basis.

A downstream target identifies the pinned server/tool/upstream descriptor evidence used for that attempt. Gateway-local targets identify one of the fixed self-service tools; they do not imply downstream handoff.

Authorization evidence records the evaluated `ALLOW`, `DENY`, or `BLOCK`, policy revision, evaluation time, and the smallest matching grant ID when applicable. It is evidence of the decision made for that invocation, not a live policy query. Read current grants separately before making a present-tense access decision.

## Interpret outcome classes

The closed projection distinguishes:

| Outcome                     | Operator interpretation                                                                                   |
| --------------------------- | --------------------------------------------------------------------------------------------------------- |
| `invalid_params`            | The request could not be classified as a valid call. No tool dispatch occurred.                           |
| `unknown_tool`              | No current target matched the requested external name.                                                    |
| `invalid_arguments`         | The resolved target rejected the unchanged argument object before execution.                              |
| `authorization_unavailable` | Safe authorization or audit admission could not be established; the call failed closed.                   |
| `deny`                      | Current policy explicitly denied the call.                                                                |
| `block`                     | No applicable allow authorized the call.                                                                  |
| `prestart_failure`          | The admitted call failed before transport or local execution handoff; no tool effect began.               |
| `succeeded`                 | A complete successful result was observed for the live caller. The result itself is not retained here.    |
| `downstream_failure`        | A complete unsuccessful downstream result was observed and collapsed to safe evidence.                    |
| `outcome_unknown`           | Handoff may have occurred, or required terminal evidence is missing; an effect may already have happened. |

The accompanying basis is `admission`, `policy`, `terminal`, or `missing_terminal`. Missing terminal evidence always projects as unknown rather than being treated as failure-before-start or success.

## Handle unknown outcomes

Missing terminal evidence is not proof that no effect occurred. Gateway provides at most one automatic attempt and never replays after uncertainty, cancellation, restart, route withdrawal, or terminal-write failure.

For a downstream `outcome_unknown`:

1. Record the invocation ID, target, admission time, and authorization evidence.
2. Inspect the authoritative external system through an independent safe read when one exists.
3. Decide whether duplication is acceptable for this operation.
4. Make any retry as a new explicit caller-owned request.

An explicit retry may duplicate an effect. Gateway has no exactly-once guarantee and no generic operation ledger that can prove remote side effects.

Gateway-local tools use a narrower result boundary. Known and post-commit-uncertain local storage failures return `tool_unavailable`; local storage uncertainty may leave one complete mutation but does not become a downstream-handoff claim. Recover through the matching get/list operation or duplicate-first request creation, never automatic replay.

## Understand redaction and retention

Gateway retains at most 65,536 invocation rows and evicts the oldest row in the same transaction as a new admission. Evidence is ordered by durable insertion sequence rather than client timestamps.

Argument capture uses fixed recursive sensitive-key redaction and an 8 KiB compact bound. Overflow stores a fixed placeholder. Redaction or encoding failure stores no capture rather than raw arguments. This is defense in depth, not guaranteed secret detection; callers must not submit secrets where the tool contract does not require them.

Successful content, `structuredContent`, unsuccessful content, raw errors, downstream request IDs, bearers, and unredacted arguments are never persisted. Backups contain only the same bounded safe evidence. Collections omit captures to reduce disclosure; inspect one item only when the operational need justifies it.

## Follow live updates safely

Polling never submits, completes, resumes, or replays a call. The browser can refresh the newest visible invocation snapshot from ID-only authenticated invalidation hints while live mode is enabled. Pausing live mode keeps the displayed snapshot stable for inspection. Neither event handling nor repeated operator reads submit, complete, resume, or replay a call, and reads do not alter audit rows, policy, routes, or request state.

When a cursor becomes stale, restart the read from the newest page. When a record is evicted, do not infer an outcome from absence. When current policy or catalog state differs from retained evidence, treat the row as historical evidence for that attempt and use current owner reads for present state.

Return to the [Gateway README](../README.md) for common workflows or [Server configuration](server-configuration.md) to inspect current runtime and catalog state.
