# pd Read-Only MCP Server Plan

## Goal

Add a new `pd mcp` CLI command to `pi-dispatcher` that starts a stdio MCP server exposing read-only Pi Dispatcher inspection tools. The server should let MCP clients inspect tasks, latest runs, response previews, and bounded logs without initiating task mutations, worktree changes, supervisor control, or status reconciliation.

## Background / Repo Context

- `pi-dispatcher` is the Go CLI module for `pd`; commands live under `pi-dispatcher/cmd/pd`, and command wrappers should delegate to internal packages per `pi-dispatcher/CLAUDE.md`.
- The root Cobra command is defined in `pi-dispatcher/cmd/pd/root.go` and currently registers `config`, `run`, `list/ps`, `status`, `wait`, `logs`, `stop`, `cleanup`, `rm`, `token`, `dashboard`, and hidden `supervisor`. `pd mcp` must be added to this registration.
- Current CLI inspection commands can write: `pd ps`, `pd status`, and `pd wait` share stale-supervisor reconciliation behavior that may mark tasks `unknown` in SQLite. Do not reuse those command paths for MCP read tools.
- `pd dashboard` is the closest existing read-only surface. `pi-dispatcher/DESIGN.md` states Dashboard APIs use read-only store queries and log reads, and intentionally do not perform stale-status reconciliation.
- Dashboard APIs currently expose:
  - `GET /dashboard/api/tasks` for task summaries.
  - `GET /dashboard/api/tasks/{id}` for task detail, latest run metadata, and latest assistant response preview from the Pi event stream.
  - `GET /dashboard/api/tasks/{id}/logs` for bounded stdout/stderr log windows.
- Dashboard view helpers and response structs live in `pi-dispatcher/internal/dashboard/dashboard.go` but are currently unexported and tied to HTTP handlers. MCP should share the same representation or move equivalent read-only view logic into a reusable internal package rather than duplicate divergent schemas.
- `internal/store` provides read APIs suitable for MCP: `ListTaskSummaries`, `GetTask`, and `LatestRun` in `pi-dispatcher/internal/store/dashboard.go` and `pi-dispatcher/internal/store/store.go`.
- `store.Open` currently creates the store directory, sets WAL/busy timeout, initializes schema, and applies migrations. This means `pd mcp` can be semantically read-only at the tool level while still using the existing store open path, matching dashboard behavior. A strictly SQLite read-only open mode is out of scope for this first feature unless the implementer can add it without destabilizing existing commands.
- Existing stdio MCP server pattern to copy is `local-git-mcp`: `local-git-mcp/cmd/local-git-mcp/root.go` creates `mcpserver.NewMCPServer`, registers tools from an internal handler, then calls `mcpserver.ServeStdio`; tool definitions and dispatch live in `local-git-mcp/internal/tools/tools.go`.
- Existing MCP modules use `github.com/mark3labs/mcp-go v0.54.1`; `pi-dispatcher/go.mod` does not yet depend on it.

## Acceptance Criteria

- AC-1: `pd mcp` is a registered Cobra subcommand with no positional args that starts a stdio MCP server and registers only read-only MCP tools.
- AC-2: The MCP server exposes a dashboard-equivalent read-only tool surface:
  - `list_tasks` returns task summaries with latest-run metadata.
  - `get_task` returns one task detail with latest-run metadata and the bounded latest assistant response preview from the Pi event stream.
  - `get_task_logs` returns a bounded stdout or stderr log window for the latest run, with offset/next_offset/size metadata.
- AC-3: MCP tool calls never invoke task mutation, stop/cleanup/rm, worktree-manager, sandbox-manager, control sockets, supervisor commands, or stale-status reconciliation.
- AC-4: MCP tool outputs intentionally omit sensitive persisted fields that Dashboard omits, including full prompt text, `pi_argv_json`, system prompt / append-system-prompt values, and environment variable values. Environment variable names may be exposed as Dashboard already does.
- AC-5: Tool definitions include MCP annotations marking them read-only and local/open-world false where appropriate.
- AC-6: Missing tasks, invalid streams, invalid offsets/limits, store/log read failures, and unknown tools return MCP tool error results rather than crashing the stdio server.
- AC-7: `pi-dispatcher` docs describe the new `pd mcp` command, its read-only tool surface, privacy model, and relationship to Dashboard.
- AC-8: Module metadata is updated cleanly and the `pi-dispatcher` test/lint/audit workflow passes, or any pre-existing unrelated failure is documented with evidence.

## Non-Goals / Out of Scope

- No MCP tools that mutate tasks, stop runs, cleanup worktrees, remove metadata, launch new runs, reconcile stale statuses, rotate tokens, or alter config.
- No network MCP transport, HTTP listener, authentication layer, daemon mode, or browser UI changes.
- No artifact table, event timeline API, session-file reader, or full Pi session export.
- No guarantee that opening the store performs zero filesystem/SQLite writes; matching Dashboard’s existing `store.Open` behavior is acceptable for this feature.

## Constraints

- Preserve repo CLI conventions: commands are thin Cobra wrappers in `cmd/pd` and delegate to internal packages.
- Avoid generic Cobra arg errors; use `cobra.NoArgs` for `pd mcp`.
- Do not emit logging or banners to stdout from `pd mcp`; stdout belongs to the MCP JSON-RPC stdio stream. Diagnostic logs, if any, must go to stderr.
- Keep the server local to stdio. MCP annotations should communicate read-only behavior but are not a substitute for implementation-level safeguards.
- Bounded file reads are required for logs and Pi events to avoid unbounded memory use.
- Maintain Dashboard’s sensitivity posture: expose useful metadata and previews, but do not expose full prompts or launch argv/system prompts.

## Chosen Approach

Implement `pd mcp` as an in-process stdio MCP server inside the existing `pi-dispatcher` binary, using `mark3labs/mcp-go` and the same high-level pattern as `local-git-mcp`. Add a new internal MCP package for tool definitions and handlers, backed by a small read-only service that reuses or extracts Dashboard-equivalent view/log/response-preview logic.

Prefer extracting shared read-only presentation helpers from `internal/dashboard` into a reusable internal package, such as `internal/inspector` or `internal/views`, so Dashboard and MCP stay consistent. The HTTP Dashboard can then call the shared package, while MCP formats the same data as JSON text results. If extraction becomes too invasive, duplicating a minimal adapter is acceptable only when tests prove field parity for sensitive omissions.

## Design Decisions

- D1: Initial MCP scope is dashboard-equivalent read operations (`list_tasks`, `get_task`, `get_task_logs`). This is enough to satisfy read-only inspection without opening mutation or supervisor-control questions.
- D2: Do not reuse CLI `ps/status/wait` implementation paths because those paths can reconcile stale tasks by writing `unknown` statuses. Use store read queries directly, mirroring Dashboard.
- D3: Return JSON as MCP text content for successful tool results, matching `local-git-mcp`’s current pattern. Keep schemas explicit in tool descriptions/input schemas.
- D4: Use read-only MCP annotations (`ReadOnlyHint: true`, `OpenWorldHint: false`) for all tools because they read local pd state/log files and do not contact external systems.
- D5: Keep authentication out of scope. `pd mcp` is launched as a local stdio process by a user or broker with that user’s filesystem permissions, unlike Dashboard’s loopback HTTP endpoint.
- D6: Use Dashboard’s existing redaction/omission choices as the public data contract: prompt previews are okay; full prompts and Pi argv/system prompts are not.

## Implementation Notes

- Add `github.com/mark3labs/mcp-go v0.54.1` to `pi-dispatcher/go.mod` via `go get` or normal implementation imports followed by `go mod tidy`.
- Add a new Cobra command file under `pi-dispatcher/cmd/pd`, for example `mcp.go`, with `Use: "mcp"`, `Short: "Start read-only Pi Dispatcher MCP server"`, `Args: cobra.NoArgs`, and `RunE` that:
  - opens the configured store path from `cfg.DBPath()`;
  - constructs the internal MCP handler/service;
  - creates `mcpserver.NewMCPServer("pd", <version string>)` or a similarly stable server name;
  - registers all tools from the handler;
  - calls `mcpserver.ServeStdio`.
- Register `mcpCmd` in `rootCmd.AddCommand(...)` in `pi-dispatcher/cmd/pd/root.go`.
- Add an internal package such as `pi-dispatcher/internal/mcp` for MCP tool definitions and dispatch. Keep it independent from Cobra globals for testability.
- Add or extract a read-only inspection package, if useful, that can provide:
  - list task summaries from `store.ListTaskSummaries`;
  - task detail from `store.GetTask` + `store.LatestRun`;
  - latest assistant response preview by bounded tail read of `Run.PiEventsPath` using Dashboard’s current event parsing behavior;
  - log windows by bounded read of `Run.StdoutLogPath` or `Run.StderrLogPath`.
- Keep log window defaults and maximums aligned with Dashboard unless there is a clear reason to differ. Validate `stream` as `stdout` or `stderr`; validate `offset >= 0`; validate `limit >= 0`; consider preserving Dashboard’s default limit and adding a hard cap if the reusable helper does not already enforce one.
- Tool input schemas should be explicit:
  - `list_tasks`: no required inputs; optional future filters should not be added unless implemented and tested.
  - `get_task`: required `task_id` string.
  - `get_task_logs`: required `task_id` string; optional `stream` string default `stdout`; optional `offset` number default `0`; optional `limit` number default Dashboard default.
- Tests should cover tool names, annotations, argument validation, successful JSON output, not-found behavior, log validation, bounded output, context forwarding where practical, and unknown-tool handling. Mirror `local-git-mcp/internal/tools/tools_test.go` style.
- Add command-level tests in `pi-dispatcher/cmd/pd` proving `pd mcp` is registered and accepts no args. Avoid full stdio e2e tests unless they can be deterministic without hanging.
- Add shared-view tests or adapt existing dashboard tests to ensure sensitive fields remain omitted from both Dashboard and MCP outputs.

## Documentation Impact

- Update `pi-dispatcher/README.md`:
  - include `pd mcp` in the command list / Quick Start where appropriate;
  - document the three read-only tools and their high-level arguments;
  - state stdout is reserved for MCP stdio and diagnostics go to stderr;
  - state the server exposes local pd metadata/log previews to the launching MCP client and should only be configured for trusted local clients.
- Update `pi-dispatcher/DESIGN.md`:
  - add `pd mcp` to architecture/state surfaces alongside Dashboard;
  - state it shares Dashboard’s read-only inspection model and does not reconcile stale statuses or mutate task/worktree/control state;
  - note it uses stdio transport and local process permissions rather than Dashboard token auth.
- `pi-dispatcher/CLAUDE.md` likely needs no change unless new package conventions or development commands are introduced.

## Testing / Verification

- V1 for AC-1, AC-5: `go test ./cmd/pd -run 'Test.*MCP|TestRoot'` from `pi-dispatcher` should pass and demonstrate command registration/no-arg behavior.
- V2 for AC-2, AC-4, AC-6: `go test ./internal/...` from `pi-dispatcher` should pass, including MCP handler tests for `list_tasks`, `get_task`, `get_task_logs`, sensitive-field omissions, validation errors, and tool error results.
- V3 for AC-3: review tests and code paths to confirm MCP handlers call only store read APIs and bounded file readers, not reconciliation, mutation commands, control socket code, worktree-manager, or sandbox-manager. Include this evidence in the completion report.
- V4 for AC-7: inspect `pi-dispatcher/README.md` and `pi-dispatcher/DESIGN.md` diffs to confirm the command, tools, read-only/privacy model, and verification guidance are documented.
- V5 for AC-8: run `make test` and `make lint` from `pi-dispatcher`; run `make tidy` if dependencies changed; run `make audit` before final completion if practical.

## Risks and Mitigations

- Risk: Accidentally using existing CLI inspection code would mutate stale task statuses. Mitigation: MCP handlers should call Dashboard-style read APIs directly; tests/review should assert no reconciliation path is referenced.
- Risk: MCP output may expose sensitive full prompts, launch argv, system prompts, or env values. Mitigation: reuse Dashboard response structs/helpers or add explicit sensitive-field omission tests.
- Risk: `store.Open` is not strictly read-only. Mitigation: document the semantic/tool-level read-only boundary and match Dashboard behavior; defer a true read-only SQLite open mode unless needed later.
- Risk: stdio MCP servers can break if ordinary logs are written to stdout. Mitigation: send diagnostics only to stderr and avoid startup banners.
- Risk: Log/event files can be large or missing. Mitigation: bounded reads, clear tool error results, and tests for missing files and offset/limit validation.
- Risk: Reusing unexported Dashboard helpers may cause circular or awkward dependencies. Mitigation: extract neutral shared read-only view/file helpers into a package that both Dashboard and MCP can import.

## Assumptions

- The intended first version of “read-only operations” is Dashboard-equivalent inspection: list tasks, get task detail/response preview, and read bounded logs.
- MCP clients consuming `pd mcp` are trusted local clients launched under the user’s account or through a trusted broker.
- It is acceptable for MCP results to use JSON text content, consistent with existing repo MCP server patterns.

## Handoff Summary

Implement `.plans/2026-06-13-pd-mcp-readonly.md` by adding a `pd mcp` stdio MCP command to `pi-dispatcher`, backed by read-only Dashboard-equivalent inspection tools and tests. A suitable objective is: `/goal Implement .plans/2026-06-13-pd-mcp-readonly.md. Complete only after every acceptance criterion is satisfied with concrete evidence from tests, docs, and code review that no MCP tool mutates pd state.`
