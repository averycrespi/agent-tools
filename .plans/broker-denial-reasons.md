# MCP Broker Denial Reasons Plan

## Goal

Return clear denial reasons in MCP tool error results so agents can distinguish user denials, rule denials, and approval timeouts, with optional free-text reasons from the dashboard and rule config.

## Constraints

- Keep the MCP call itself successful and return denial details via `CallToolResult.IsError`, matching existing broker behavior.
- Preserve backward compatibility for existing configs: rules without `reason` remain valid.
- Dashboard supports optional explicit denial text; Telegram denial remains binary and does not collect explicit reasons.
- Avoid changing backend MCP server contracts or auth behavior.
- Keep implementation small and consistent with current `Approver` and audit plumbing.

## Acceptance Criteria

- AC-1: A dashboard denial with a nonblank reason returns an MCP tool error containing `denied by user: <reason>` and records the same denial reason in audit.
- AC-2: A dashboard denial with a blank or whitespace-only reason returns an MCP tool error containing `denied by user` and records a user denial in audit.
- AC-3: A deny rule may include optional `reason`; when matched, the MCP tool error contains `denied by rule: <reason>` and the audit record captures that denial reason.
- AC-4: A deny rule without `reason` still rejects the call and returns a generic rule-denial message without requiring config changes.
- AC-5: Approval timeout returns an MCP tool error containing `denied by timeout` and records timeout as the denial reason.
- AC-6: Telegram denials continue to work as binary denials and produce `denied by user`, with no explicit denial-reason UI or protocol added for Telegram.

## Chosen Approach

Use a small typed denial-reason convention across the existing pipeline rather than introducing a new response schema. Extend rule config with optional `reason`, update broker error formatting to include the normalized denial reason, and update the dashboard decision API/UI to pass optional free text. This keeps agent-facing behavior simple because the agent already receives broker errors as MCP error tool results.

Denial reason formatting:

- Dashboard deny with explicit reason: `denied by user: <reason>`
- Dashboard deny without explicit reason: `denied by user`
- Telegram deny: `denied by user`
- Rule deny with configured reason: `denied by rule: <reason>`
- Rule deny without configured reason: `denied by rule`
- Approval timeout: `denied by timeout`

Internally, use canonical reason strings in audit/approver flow:

- `user` for human denial without explicit text.
- `user: <reason>` for dashboard human denial with explicit text.
- `rule` or `rule: <reason>` for rule denial.
- `timeout` for timeout.

If implementation finds a cleaner internal representation while preserving the external strings above, prefer the smaller/clearer code.

## Documentation Impact

Update:

- `mcp-broker/README.md`
  - Document optional dashboard denial reasons.
  - Document optional rule `reason` field, especially for deny rules.
  - Mention timeout/user/rule denial text returned to agents.
- `mcp-broker/DESIGN.md`
  - Update pipeline/audit/rules/dashboard sections to include denial reasons.
- `mcp-broker/CLAUDE.md` only if implementation adds a new convention or gotcha useful for future coding sessions.

No changelog exists in this tool.

## Assumptions / Open Questions

- Q1: Telegram explicit denial reasons are intentionally out of scope; Telegram deny should map to `denied by user`.
- Q2: Rule `reason` should be accepted in config for any rule for compatibility simplicity, but only deny-rule matches need to surface it.
- Q3: Free-text dashboard reasons should be trimmed; whitespace-only input counts as no explicit reason.
- Q4: No strict maximum length is planned unless implementation discovers an existing dashboard/API payload convention to mirror.

## Ordered Tasks

### T1: Extend config and rule evaluation for rule reasons

Covers: AC-3, AC-4

- Add optional `Reason string \`json:"reason,omitempty"\``to`internal/config.RuleConfig`.
- Preserve config load/save/default behavior.
- Update `internal/rules` so broker code can retrieve the matched rule metadata, likely by using existing `EvaluateWithRule` and `Rules()` or by adding a small helper if that is cleaner.
- Add/update unit tests showing deny rules with and without `reason` remain valid and match correctly.

### T2: Normalize broker denial formatting

Covers: AC-1, AC-2, AC-3, AC-4, AC-5, AC-6

- Update `internal/broker/broker.go` to produce agent-facing errors exactly matching the chosen denial strings.
- For rule denials, set `audit.Record.DenialReason` to `rule` or `rule: <reason>` and `audit.Record.Error` to the agent-facing message.
- For approver denials, map existing approver reasons (`user`, `user: ...`, `timeout`) to agent-facing messages.
- Keep `ErrDenied` wrapping where current tests rely on `errors.Is`, unless the implementation discovers no callers depend on it.
- Update broker unit tests for all denial variants.

### T3: Add dashboard optional denial reason UI/API

Covers: AC-1, AC-2

- Update `internal/dashboard/dashboard.go` decision handling to accept optional JSON field `reason` on `/api/decide`.
- Change pending request decision channel from a raw string to a small decision struct if needed.
- Trim dashboard-supplied reason; blank means `user`, nonblank means `user: <reason>`.
- Update `internal/dashboard/index.html` to render an inline optional reason input for each pending request and include it only/primarily when denying.
- Update dashboard tests for deny with explicit reason and deny with blank reason.

### T4: Preserve Telegram binary denial behavior

Covers: AC-6

- Confirm `internal/telegram` still returns `user` for explicit deny and `timeout` on context deadline/cancellation.
- Do not add Telegram reason collection.
- Update Telegram tests only if broker-level formatting changes require expected strings elsewhere.

### T5: Update docs

Covers: AC-1, AC-2, AC-3, AC-4, AC-5, AC-6

- Update `mcp-broker/README.md` rules and approval sections with examples:
  - dashboard reason field behavior
  - `reason` on deny rules
  - agent-facing denial messages
- Update `mcp-broker/DESIGN.md` to describe denial reasons in pipeline, rules, audit, dashboard, broker, and MultiApprover/Telegram boundaries where relevant.
- Update `mcp-broker/CLAUDE.md` only if there is a new implementation convention future agents should know.

### T6: Add/adjust end-to-end coverage

Covers: AC-1, AC-3, AC-5

- Update `test/e2e/teststack_test.go` helpers so deny can optionally send a reason.
- Add or extend E2E tests to assert MCP error content for:
  - dashboard denial with explicit reason
  - rule denial with configured reason
  - timeout if an existing timeout-friendly test harness can cover it without making tests slow/flaky
- Keep E2E timeout bounded with a short configured approval timeout if implemented.

## Verification Checklist

- [ ] V1: `make -C /Users/avery/work/agent-tools/mcp-broker fmt`
- [ ] V2: `make -C /Users/avery/work/agent-tools/mcp-broker test`
- [ ] V3: `make -C /Users/avery/work/agent-tools/mcp-broker test-e2e`
- [ ] V4: `make -C /Users/avery/work/agent-tools/mcp-broker lint`
- [ ] V5: Manually inspect or test the dashboard pending request card to confirm the inline reason field is visible and optional.
- [ ] V6: Confirm Documentation Impact was followed: `README.md` and `DESIGN.md` updated, and `CLAUDE.md` updated only if needed.

## Known Issues / Follow-ups

- Telegram explicit denial reasons are not supported by design.
- No configurable maximum denial-reason length is planned in this scope.
