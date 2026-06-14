# Pi Orchestrator MCP Server Plan

## Goal

Add a read-only stdio MCP server to Pi Orchestrator, launched as `po mcp`, so trusted local MCP clients can inspect workflow runs without starting the HTTP dashboard or using mutating CLI commands.

## Background / Repo Context

- `pi-dispatcher` already has the pattern to copy: `pi-dispatcher/cmd/pd/mcp.go` opens the store, creates an `mcpserver.NewMCPServer`, registers handler tools, and serves stdio; `pi-dispatcher/internal/pdmcp/tools.go` defines read-only/local annotations and JSON tool results.
- `pd mcp` exposes `list_tasks`, `get_task`, and `get_task_logs`; tests in `pi-dispatcher/internal/pdmcp/tools_test.go` cover read-only annotations, redaction/omission, bounded log windows, validation errors, unknown IDs, and store failures.
- `po` currently has no `mcp` command. Commands are registered in `pi-orchestrator/cmd/po/root.go`, and command wrappers should stay thin per `pi-orchestrator/CLAUDE.md`.
- Pi Orchestrator state is in SQLite via `pi-orchestrator/internal/store/store.go`: workflow runs, step runs, artifacts, and run requests. Dashboard-oriented read projections already exist in `pi-orchestrator/internal/store/dashboard.go` and `pi-orchestrator/internal/dashboard/dashboard.go`.
- Existing CLI inspection helpers are not safe to reuse for MCP: `po status` / `po logs` paths reconcile stale supervisors through `cmd/po/reconcile.go`, which can write `unknown` to SQLite. The MCP server must display persisted state as-is.
- Sensitive persisted fields include full workflow definitions (`DefinitionYAML`) and validated inputs (`InputsJSON`). Do not marshal raw `store.WorkflowRunDetail` or `store.WorkflowRunSummary` directly to MCP output without an explicit view type and field review.
- `pi-orchestrator/go.mod` does not yet depend on `github.com/mark3labs/mcp-go`; `pi-dispatcher/go.mod` currently uses `v0.54.1`.

## Acceptance Criteria

- AC-1: `po mcp` exists, accepts no positional arguments, starts a stdio MCP server named for Pi Orchestrator, and is registered with the root `po` command.
- AC-2: The MCP tool list is read-only/local and includes exactly these v1 tools:
  - `list_workflows`: returns configured workflow-definition summaries from `cfg.WorkflowDir`, including name, description, repo, input schema summaries, agent names/models, step IDs/dependencies, artifact declarations, source path, and validation status.
  - `get_workflow`: returns one validated workflow definition from `cfg.WorkflowDir`, including `source_path`, `raw_yaml`, parsed metadata, and validation status because workflow prompts are the definition content being explicitly requested.
  - `list_workflow_runs`: returns workflow-run summaries with persisted state and step-count/progress metadata.
  - `get_workflow_run`: returns one workflow-run detail, including steps, artifacts, cleanup status, backing `pd` task/run IDs, and local path metadata that is already exposed by dashboard/CLI inspection.
  - `get_workflow_run_logs`: returns a bounded supervisor log window for one workflow run with offset/limit/size/next-offset/truncated metadata.
  - `get_step_logs`: returns a bounded stdout or stderr log window for one workflow step's backing `pd` run with offset/limit/size/next-offset/truncated metadata.
- AC-3: Every MCP tool has `ReadOnlyHint=true` and `OpenWorldHint=false`; no tool mutates SQLite, launches/contacts supervisors, starts/stops workflows, reconciles stale process state, deletes resources, or calls external services.
- AC-4: MCP responses use explicit view structs. Run-oriented tools omit full workflow YAML, full inputs JSON, raw prompt text, environment variable values, and any unbounded log/event content. `get_workflow` is the only v1 tool that may expose raw workflow prompt text, and only for a named workflow definition under `cfg.WorkflowDir`; prompt-injection-capable text from logs is returned only through explicit bounded log tools.
- AC-5: Log tools validate required IDs, stream names, non-negative integer offsets, and maximum limits; oversized or malformed inputs return MCP tool errors rather than process errors. Missing workflow runs or steps return tool errors.
- AC-6: Tests cover tool definitions, summaries, detail redaction/omission, bounded supervisor logs, bounded step logs, validation errors, missing IDs, unknown tools, and store/read failures.
- AC-7: User-facing docs and design docs mention `po mcp`, its trust model, tool surface, read-only guarantees, sensitive-field omissions, and bounded-log behavior.

## Non-Goals / Out of Scope

- No mutation tools for run, stop, cleanup, remove, token, dashboard, supervisor, or worktree operations.
- No stale-status reconciliation in MCP; persisted SQLite state is shown as-is.
- No artifact-content reader in v1. Artifact metadata may be exposed, but file contents should wait for a later design with root containment checks and byte bounds.
- No MCP resource or prompt templates; v1 is tool-only.
- No network listener or dashboard auth integration; stdio MCP inherits the launching user's local filesystem permissions.

## Constraints

- Keep the implementation local to `pi-orchestrator`; do not create a shared MCP package unless a concrete duplication problem appears.
- Match the existing `pd mcp` dependency and server style unless the current `mcp-go` API requires a small adjustment.
- Preserve stdout exclusively for MCP JSON-RPC. Diagnostics/startup errors should go to stderr through normal Cobra/server error handling.
- Use repo-relative imports and module-local packages; update `pi-orchestrator/go.mod` / `go.sum` with `go mod tidy`.
- Any subprocess execution or process-status reconciliation is disallowed in the MCP handler path.

## Chosen Approach

Create a new `pi-orchestrator/internal/pomcp` package that mirrors the shape of `pi-dispatcher/internal/pdmcp`: a small handler owns a read-only store interface, exposes tool definitions, dispatches tool calls, validates arguments, and returns JSON text results. Add `pi-orchestrator/cmd/po/mcp.go` as a thin Cobra command that opens `cfg.DBPath()`, constructs the handler, registers its tools with `mcp-go`, and serves stdio.

The v1 tool surface should cover both workflow definitions and workflow-run inspection. `list_workflows` and `get_workflow` mirror `po list` / `po show` for MCP clients that need to discover available workflows and inspect their required inputs before launching or discussing a run elsewhere. `list_workflow_runs`, `get_workflow_run`, and supervisor logs mirror existing dashboard capabilities. `get_step_logs` is included because workflow diagnosis usually needs the backing `pd` stdout/stderr paths already persisted on each step; bounding and validating it in `po mcp` avoids forcing every MCP client to configure both `po` and `pd` servers while still staying read-only.

## Design Decisions

- D1: Use explicit MCP view types instead of raw store structs. This prevents accidental exposure of `DefinitionYAML`, full `InputsJSON`, or future sensitive fields added to store structs.
- D2: Return persisted state only. Do not call `reconcileWorkflowRun`, `getWorkflowRun` from CLI helpers, supervisor control APIs, or process inspection from MCP.
- D3: Add workflow-definition tools, with a clear split between summary and full content. `list_workflows` should avoid prompt bodies for scanability; `get_workflow` may return raw YAML/prompt text because it is the explicit equivalent of `po show <workflow>`.
- D4: Include `get_step_logs` but not raw event-stream or artifact-content readers. Step stdout/stderr are the minimum useful diagnostic expansion beyond dashboard logs; event and artifact content can be larger and more prompt-injection/sensitive, so they remain out of scope.
- D5: Use offset/limit log windows, not dashboard's tail-only helper. Offset/limit matches `pd mcp`, supports pagination, and gives MCP clients predictable context bounds.
- D6: Make tool errors part of MCP results (`gomcp.NewToolResultError`) for user/data validation and expected missing-row cases; reserve returned Go errors for unexpected server failures during setup or MCP framework operation, matching `pdmcp` style.

## Implementation Notes

- Add `github.com/mark3labs/mcp-go v0.54.1` to `pi-orchestrator/go.mod` unless `go mod tidy` resolves a newer compatible version through the workspace; prefer matching `pi-dispatcher` for consistency.
- Add `pi-orchestrator/cmd/po/mcp.go` analogous to `pi-dispatcher/cmd/pd/mcp.go`, importing `pi-orchestrator/internal/pomcp` and `pi-orchestrator/internal/store`.
- Register `mcpCmd` in `pi-orchestrator/cmd/po/root.go` with the other commands.
- Add `pi-orchestrator/internal/pomcp/tools.go` with:
  - `Store` interface containing only read methods needed by the handler, likely `ListWorkflowRunSummaries`, `GetWorkflowRunDetail`, and `GetWorkflowRun` if log tools need only the run row.
  - A workflow-definition reader abstraction over `cfg.WorkflowDir`, adapted from `cmd/po/workflow_commands.go` and `internal/workflow`, so it can list `.yaml` / `.yml` files, validate definitions, and reject workflow names containing path separators or invalid stems.
  - Tool definitions and shared read-only/local annotation.
  - JSON result helper and numeric/string argument parsers, adapted from `pdmcp`.
  - Log-window helper that reads a file path with explicit `offset` and `limit`, returns stream/path/content/offset/next_offset/size/truncated metadata, treats empty paths and missing files deliberately, and caps `limit` at a documented maximum (use the same 1 MiB max as `pd mcp` unless there is a compelling reason not to).
- View shape guidance:
  - `list_workflows` should return `{ "workflows": [...] }` with name, description, repo, source path, validation status/error, input schemas, agent summaries, step IDs/agents/needs, and artifact declarations. It should not include step prompt bodies.
  - `get_workflow` input: `workflow` name. It should reject names with path separators or invalid stems, resolve only under `cfg.WorkflowDir`, validate with `workflow.LoadFile`, and return `source_path`, `raw_yaml`, parsed metadata, and validation status. It may include step prompt bodies because the caller explicitly requested a definition.
  - `list_workflow_runs` should return `{ "runs": [...] }` with ID, workflow, state, repo, branch, worktree/artifact paths, outcome, cleanup fields if available, created/updated/ended timestamps, and step counts/progress. Do not include raw `inputs_json`; if inputs are needed, include at most an explicit bounded preview and truncation flag, but omission is safer for v1.
  - `get_workflow_run` should return `{ "run": ..., "steps": [...], "artifacts": [...] }` with run and step metadata, `pd_task_id`, `pd_run_id`, `pd_stdout_path`, `pd_stderr_path`, `pd_events_path`, artifact names/relative paths/existence/required flags, and cleanup fields. Omit `definition_yaml` and raw/full `inputs_json`.
  - `get_workflow_run_logs` input: `run_id`, optional `offset`, optional `limit`.
  - `get_step_logs` input: `run_id`, `step_id`, optional `stream` (`stdout` default, `stderr` allowed), optional `offset`, optional `limit`.
- Avoid importing or calling `cmd/po` helpers from `pomcp`; keep handler code in internal packages for testability.
- Add tests under `pi-orchestrator/internal/pomcp/`, modeled on `pi-dispatcher/internal/pdmcp/tools_test.go`. Use temporary workflow directories for workflow-definition tools, a fake store for handler error paths, and temporary files for log-window tests where practical. Use real `store.Store` setup where it meaningfully verifies current projections. Cover `get_workflow` path separator / traversal rejection and ensure `list_workflows` handles invalid workflow files as validation-status entries rather than aborting the entire listing.
- Consider adding a small read-only store method if existing store APIs make it awkward to list cleanup fields or to fetch one step by ID. Keep it read-only and covered by store tests if added.

## Documentation Impact

Update existing docs rather than adding new docs:

- `pi-orchestrator/README.md`: add `po mcp` to command examples and explain the tools, trust model, read-only behavior, workflow-definition prompt exposure through `get_workflow`, run-detail omissions, and bounded logs near the dashboard/security sections.
- `pi-orchestrator/DESIGN.md`: add a Pi Orchestrator MCP section analogous to `pi-dispatcher/DESIGN.md`, explicitly stating that MCP is stdio-only, trusted-local, read-only, does not reconcile stale state, exposes workflow definitions on request, and exposes bounded supervisor/step logs.
- No changelog update is required unless the repo has a release/changelog convention not found during implementation.

## Testing / Verification

- V1 for AC-1/AC-2/AC-3: `cd pi-orchestrator && go test -race ./...` passes, including tests that assert tool names and annotations.
- V2 for AC-4/AC-5/AC-6: targeted `pomcp` tests pass and assert sensitive substrings such as full workflow YAML, full inputs JSON, and oversized log content are absent from run detail/list responses; `list_workflows` omits prompt bodies and reports invalid workflow files without aborting the whole listing; `get_workflow` returns prompt bodies only for an explicitly named workflow and rejects path separator / traversal attempts; validation failures return MCP tool errors.
- V3 for AC-1: `cd pi-orchestrator && go run ./cmd/po mcp --help` shows the new command with no positional args and a read-only description.
- V4 for AC-7: `rg -n "po mcp|MCP|list_workflows|get_workflow|list_workflow_runs|get_workflow_run|get_workflow_run_logs|get_step_logs" pi-orchestrator/README.md pi-orchestrator/DESIGN.md` finds the documented surface and trust model.
- V5 final hygiene: run `cd pi-orchestrator && make audit` before reporting completion. If environment constraints prevent `make audit` (for example, missing govulncheck network/cache access), report the blocker and run the strongest available subset: `make tidy`, `make fmt`, `make lint`, and `make test`.

## Risks and Mitigations

- Risk: Accidentally exposing persisted run prompts or sensitive inputs by reusing raw store/dashboard structs. Mitigation: MCP-specific view structs and tests that assert sensitive fixture strings are absent from run/list outputs; allow prompt text only in explicit `get_workflow` responses.
- Risk: Violating read-only semantics by reusing CLI inspection functions that reconcile stale process state. Mitigation: handler depends only on read-only store methods and file reads; tests can use a fake store interface, and code review should reject imports from `cmd/po` helpers.
- Risk: Huge logs exhaust MCP context or client memory. Mitigation: require offset/limit parsing, default to a bounded limit, cap maximum limit, and return size/next-offset metadata.
- Risk: Local paths and logs may still be sensitive. Mitigation: document the trusted-local stdio model and bounded exposure; do not add auth because stdio has no network listener and runs with user permissions.
- Risk: Step log paths are persisted absolute paths and could be stale or missing. Mitigation: read only paths from the selected persisted step, return clear tool errors or empty windows for expected missing files per chosen helper behavior, and never accept arbitrary paths as input.

## Assumptions

- `get_step_logs` is in scope for v1 because it is read-only, bounded, and materially improves orchestrator troubleshooting without requiring a separate `pd` MCP server.
- It is acceptable for `get_workflow` to expose raw workflow YAML, including step prompts, because it mirrors `po show <workflow>` for a named file under the configured workflow directory.
- It is acceptable for MCP run detail responses to expose the same local path metadata and backing `pd` identifiers already available through dashboard/CLI inspection, but not raw persisted workflow snapshots or full inputs.
- Showing raw persisted state rather than reconciled state is the intended behavior for read-only inspection surfaces, consistent with `pd mcp`.

## Handoff Summary

Implement `.plans/2026-06-14-po-mcp-server.md` by adding a `po mcp` stdio server and a new `internal/pomcp` read-only handler. Complete only after the MCP tools are present, annotated read-only/local, tested for redaction and bounded logs, documented in README/DESIGN, and verified with the Pi Orchestrator audit command or the strongest available documented subset.

Suggested `/goal` objective:

```text
Implement .plans/2026-06-14-po-mcp-server.md. Complete only after every acceptance criterion is satisfied with concrete evidence from tests, docs, and command output.
```
