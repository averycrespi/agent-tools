# Agent-Tools Audit Remediation Plan

## Goal

Remediate the approved correctness, security-hardening, MCP-compatibility, lint-consistency, and documentation issues from the 2026-06-10 repo audit while explicitly deferring larger product/security/new-tool work.

The highest priorities are preventing indefinite hangs, fixing confirmed or low-risk correctness issues, making partial failures visible/recoverable, and aligning docs with actual behavior.

## Background / Repo Context

- This repo is a Go monorepo of independent tools under `go.work`: `worktree-manager/`, `mcp-broker/`, `broker-cli/`, `sandbox-manager/`, `pi-dispatch/`, `local-git-mcp/`, `local-gh-mcp/`, and `local-gomod-proxy/`.
- Root `AGENTS.md` says each tool should follow the same baseline structure and that new/shared conventions should mirror existing tool layout.
- The audit validated the overall architecture: host-side credential brokering, loopback-only binding, glob rules, approval, and SQLite audit are good patterns.
- The approved remediation scope intentionally excludes large future-facing work such as network egress allowlisting, CI, observability, and new tools.
- The approved scope keeps per-tool independence. Do not introduce a shared Go module for duplicated `exec.Runner` implementations; instead make timeout-aware runners the canonical copy-paste template and document that convention.

## Acceptance Criteria

- AC-1: `sandbox-manager` documentation no longer claims GID injection unless the implementation actually injects GID; docs accurately describe current UID/GID behavior and any group-permission limitation.
- AC-2: `local-git-mcp`, `local-gh-mcp`, `local-gomod-proxy`, `mcp-broker`, and `broker-cli` all have finite timeout behavior for the approved subprocess/HTTP/backend operations, with sane configurable defaults where appropriate.
- AC-3: Timeout behavior is covered by focused tests or fakes showing a blocked subprocess/backend/client path returns a timeout error instead of hanging.
- AC-4: `worktree-manager` and `pi-dispatch` handle the approved concurrency/partial-failure cases: same-branch concurrent worktree creation is recoverable/idempotent with setup-phase protection, copy/setup failures are surfaced as errors, `pi-dispatch` launch failures transition tasks to terminal `failed`, and stale control sockets are cleaned by reconciliation/removal paths.
- AC-5: `local-gomod-proxy` validates streamed file paths are contained under `GOMODCACHE` before opening them, with tests for accepted and rejected paths.
- AC-6: `local-gh-mcp` author formatting gives an explicit `is_bot` value precedence and only uses the `[bot]` suffix heuristic when the field is absent, with tests.
- AC-7: `broker-cli` writes its discovery cache atomically using a same-directory temporary file plus rename, with tests or implementation evidence covering concurrent/interrupted-write safety.
- AC-8: `mcp-broker` enforces a configurable `/mcp` body-size limit and bounds concurrent pending approvals, returning clear errors when limits are exceeded.
- AC-9: `mcp-broker` lints configured regex rules at startup and warns on suspicious unanchored regex matchers; no `rules test` subcommand is added in this scope.
- AC-10: `local-git-mcp` logs a prominent startup warning when `--allow-all-paths` is enabled and its README documents that the flag defeats the path-sandboxing intent.
- AC-11: `local-git-mcp` and `local-gh-mcp` adopt current MCP read-tool improvements approved in scope: structured output/`outputSchema` where appropriate, validation errors treated as tool execution errors, and metadata/annotation tests where missing.
- AC-12: GolangCI configuration is consistent across all eight tools, and the root contributor checklist documents the chosen convention.
- AC-13: Known doc drift is fixed: `worktree-manager` idempotency wording matches implemented semantics, `sandbox-manager` UID/GID wording matches AC-1, `pi-dispatch` uses “no central daemon” rather than implying no long-lived per-task supervisors, and `broker-cli` documents the `--no-cache` flag.
- AC-14: No deferred items are accidentally implemented as part of this plan.

## Non-Goals / Out of Scope

- Do not implement mcp-broker tool-description pinning/rug-pull detection.
- Do not implement sandbox-manager network egress allowlisting.
- Do not add sandbox-manager runtime warnings for copied secret paths beyond any documentation explicitly required by AC-1 or AC-13.
- Do not create MCP 2026-07-28 tracking issues/plans as part of this remediation.
- Do not implement elicitation-based approvals.
- Do not add GitHub Actions CI, Dependabot, or Renovate.
- Do not add `wt ls`, `wt status`, or `wt add --force`; worktree-manager UX scope is limited to surfacing copy/setup failures under AC-4.
- Do not add sandbox `sb logs` or `sb status --json`.
- Do not add pi-dispatch UX/lifecycle items such as `pd cleanup --all`, max runtime, dashboard token rotation handling, or completion gates.
- Do not add observability/trace/cost/log-file work.
- Do not create or implement new tool candidates: local-registry-proxy, gate-runner, hook-policyd, agent-notify, doc-drift, or sandbox snapshot/clone.
- Do not add an `mcp-broker rules test` dry-run command.
- Do not introduce a shared Go module for copied runner code.

## Constraints

- Keep changes minimal and directly tied to acceptance criteria.
- Preserve each tool as an independent Go module.
- Follow each tool’s `CLAUDE.md` / `AGENTS.md` before editing within that tool.
- Update existing documentation when behavior, flags, configuration, or workflows change; do not create new docs unless needed by existing doc structure.
- Prefer tests using fakes for hangs/timeouts rather than slow real sleeps or real external network/process dependencies.
- Timeout defaults should be finite but avoid breaking legitimate long-running operations. `go mod download` should get a longer default than lightweight CLI/HTTP discovery calls.
- Approval timeout in `mcp-broker` must remain longer than any proxy/tool-call timeout path that would otherwise preempt a human approval flow.

## Chosen Approach

Implement the approved items in correctness-first order:

1. Fix small confirmed/low-risk correctness and doc drift items.
2. Add finite timeout behavior across subprocess/HTTP/backend paths using per-tool copies of a canonical timeout-aware runner/client pattern.
3. Harden workflow tools against partial failure and race conditions.
4. Add broker-specific request/approval safeguards and rules linting.
5. Add current MCP structured-output/read-tool improvements for git/GitHub MCP servers.
6. Standardize lint configuration and update docs to match actual behavior.

The plan intentionally avoids large product expansions and new tools so the remediation can be completed with bounded risk.

## Design Decisions

- D1: `sandbox-manager` GID remediation is documentation-only for now. Do not implement GID injection in this plan.
- D2: Timeout behavior should be implemented across all listed tools, not just the most critical tools.
- D3: `worktree-manager` should prefer idempotent/recoverable semantics plus a small setup-phase lock, not rollback-on-failure semantics.
- D4: `mcp-broker` request hardening includes body-size limits and bounded pending approvals, but no per-token rate limiting in this scope.
- D5: `mcp-broker` rules ergonomics scope is startup lint warnings only; no dry-run command.
- D6: Keep copied runner implementations rather than adding a shared module.
- D7: Worktree-manager UX scope is limited to making copy/setup failures visible as part of correctness work.

## Implementation Notes

### Documentation-only GID correction

- Inspect `sandbox-manager/internal/sandbox/files/lima.yaml`, `sandbox-manager/internal/sandbox/template.go`, `sandbox-manager/DESIGN.md`, and `sandbox-manager/CLAUDE.md`.
- Update docs to accurately state that UID is injected and GID is not currently injected, unless current code proves otherwise.
- If docs mention group-writable mount behavior, clarify any limitation without changing implementation.

### Timeouts

Apply a consistent timeout-aware pattern in each relevant tool without creating a shared module.

- `local-git-mcp`: thread request context with a finite deadline into the command runner.
- `local-gh-mcp`: use the existing `CommandContext` runner with a finite deadline.
- `local-gomod-proxy`: wrap `go mod download` in a per-request timeout with a long finite default.
- `mcp-broker`: use an HTTP client with timeout for streamable HTTP backends and ensure proxy/tool-call timeout behavior does not conflict with human approval timeout.
- `broker-cli`: add timeout behavior around tool-discovery HTTP calls.
- Add flags/config/environment only where consistent with the tool’s existing configuration style.
- Add tests with blocking fake runners/servers where possible. Avoid real long sleeps.

### Workflow-tool concurrency and partial failure

- `worktree-manager`: re-check current files before editing; the audit cited `workspace.go` around worktree creation/copy/setup.
- Treat “worktree already exists” from `git worktree add` as recoverable/idempotent where safe.
- Add a small lock for copy/setup phase under the worktree base dir or equivalent safe location.
- Surface copy/setup failures as command errors, not debug-only logs.
- Update design/docs to match the final semantics.
- `pi-dispatch`: inspect `cmd/pd/run_impl.go` and task state handling. If supervisor launch fails after row creation, transition the task to terminal `failed` and perform cleanup according to the approved semantics.
- Ensure stale sockets under the task socket directory are removed by reconciliation and `pd rm` paths when supervisors are dead.

### Small correctness/hardening fixes

- `local-gomod-proxy`: before streaming any path reported by `go mod download -json`, verify it is under `GOMODCACHE` using `filepath.Rel` or equivalent robust containment logic. Reject paths escaping the cache.
- `local-gh-mcp`: adjust `FormatAuthor()` so explicit `is_bot` wins. Only fall back to `[bot]` suffix stripping when the field is absent.
- `broker-cli`: change cache writes to same-directory temp-file write, close/sync as appropriate for the existing style, then `os.Rename`.

### Broker security hardening

- `mcp-broker`: add configurable body-size limit on `/mcp`, e.g. via `http.MaxBytesReader` or equivalent middleware.
- Add a bounded semaphore/queue for pending approvals. When full, return a clear tool error rather than enqueueing indefinitely.
- Add startup lint warnings for regex matchers that appear unanchored. Keep this as a warning; do not silently rewrite user rules.

### local-git-mcp warning

- Log a prominent warning at startup when `--allow-all-paths` is enabled.
- Update README flag/security documentation to say this disables the path sandboxing guarantee and should be used only in trusted contexts.

### MCP structured/read-tool improvements

- In `local-git-mcp` and `local-gh-mcp`, identify read/list/view tools that currently return text only but have natural structured results.
- Add `structuredContent` and `outputSchema` while preserving text/markdown fallback for compatibility.
- Audit validation error paths and ensure user/input validation failures are returned as tool execution errors, not protocol-level errors, where the MCP library permits that distinction.
- Add or extend tests that every tool has expected annotations/open-world hints, especially in `local-git-mcp` if the pattern is missing there.

### Lint config consistency

- Inspect existing `.golangci.yml` files in `mcp-broker`, `sandbox-manager`, `pi-dispatch`, and `worktree-manager`.
- Choose the least surprising convention: either copy the existing standard config into missing tools or move to a root-level config if all existing Makefiles and tooling support it cleanly.
- Apply the chosen convention to all eight tools.
- Update the root `AGENTS.md`/`CLAUDE.md` new-tool checklist or development notes so future tools include the lint configuration convention.

### Doc drift fixes

- `worktree-manager/DESIGN.md` and `worktree-manager/CLAUDE.md`: align idempotency claims with AC-4 semantics.
- `sandbox-manager/DESIGN.md` and `sandbox-manager/CLAUDE.md`: align UID/GID claims with AC-1.
- `pi-dispatch/DESIGN.md`: replace misleading “daemonless” wording with “no central daemon” or equivalent, while acknowledging long-lived per-task supervisors.
- `broker-cli/README.md`: add the missing `--no-cache` flag to the relevant flags table/usage docs.

## Documentation Impact

Documentation updates are required and in scope:

- `sandbox-manager/DESIGN.md`
- `sandbox-manager/CLAUDE.md`
- `worktree-manager/DESIGN.md`
- `worktree-manager/CLAUDE.md`
- `pi-dispatch/DESIGN.md`
- `broker-cli/README.md`
- `local-git-mcp/README.md`
- Root `AGENTS.md`/`CLAUDE.md` for lint and runner conventions, if that is where the repo checklist is maintained.

Do not add a doc-drift checker or new documentation site/page as part of this plan.

## Testing / Verification

Run focused tests as each tool is changed, then run broader checks as practical.

- V1 for AC-1/AC-13: review updated docs and confirm no stale UID/GID, idempotency, daemonless, or `--no-cache` claims remain.
- V2 for AC-2/AC-3: run each affected tool’s unit tests covering blocked fake subprocess/backend/client timeout paths.
- V3 for AC-4: run worktree-manager tests for concurrent same-branch add/idempotent recovery and setup failure surfacing; run pi-dispatch tests for launch-failure terminal state and stale socket cleanup.
- V4 for AC-5: run local-gomod-proxy tests for allowed cache paths and rejected escaping paths.
- V5 for AC-6: run local-gh-mcp author-formatting tests covering explicit `is_bot` true/false/absent cases.
- V6 for AC-7: run broker-cli cache tests or a focused concurrent write test.
- V7 for AC-8/AC-9: run mcp-broker tests for body-size rejection, approval queue overflow, and regex lint warnings.
- V8 for AC-10: run local-git-mcp tests and manually inspect/log-test startup warning path if no existing logger test pattern exists.
- V9 for AC-11: run local-git-mcp and local-gh-mcp tool schema/output tests, including text fallback compatibility.
- V10 for AC-12: run lint targets for all eight tools after config standardization.
- V11 final repo check: run `make test` from the repo root if feasible. If full root checks are too slow or require unavailable prerequisites, record the exact per-tool commands run and any skipped prerequisites.

## Risks and Mitigations

- Risk: Timeout defaults could be too aggressive for legitimate slow operations. Mitigation: use longer defaults for `go mod download` and backend/tool-call paths than for lightweight discovery calls; make defaults configurable where the tool already has config/flag patterns.
- Risk: `mcp-broker` tool-call timeout could conflict with approval waits. Mitigation: keep approval wait semantics separate or ensure proxy deadlines exceed approval timeout for require-approval tools.
- Risk: Worktree idempotency can hide real corruption if implemented too broadly. Mitigation: only treat known safe “already exists” cases as recoverable and still surface copy/setup errors clearly.
- Risk: Regex lint could create noisy warnings. Mitigation: warn only for regex matchers where anchoring is plausibly expected, and do not fail startup.
- Risk: Structured MCP output could break clients if text fallback is removed. Mitigation: preserve existing text/markdown content while adding structured output/schema.
- Risk: Lint config standardization could introduce widespread lint failures unrelated to remediation. Mitigation: choose the existing repo-standard config and make only necessary small fixes, or document any pre-existing lint failures explicitly.

## Assumptions

- Current code line numbers from the audit may have drifted; implementers should re-locate behavior by symbol/file before editing.
- Existing Makefile targets remain the source of truth for per-tool test/lint commands.
- The repo intentionally values independent tool modules over shared internal libraries.
- Runtime GID injection is not required in this remediation; documentation accuracy is sufficient for the approved 1.1 scope.

## Handoff Summary

Suggested autonomous objective:

```text
/goal Implement .plans/2026-06-10-audit-remediation-plan.md. Complete only after every acceptance criterion is satisfied with concrete evidence from tests, docs, and targeted review. Do not implement any item listed under Non-Goals / Out of Scope.
```

Completion evidence should map every AC-1 through AC-14 to changed files and verification output. If any test or lint command cannot run because of missing local prerequisites, report the exact blocker and the narrower checks that did run.
