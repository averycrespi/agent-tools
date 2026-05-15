# Dispatch Board Plan

## Goal

Add Dispatch Board: a read-only, pi-dispatch-native web dashboard for exploring `pd` tasks, runs, events, and logs from a local browser.

## Constraints

- Build inside `pi-dispatch`, not as a separate monorepo tool.
- Preserve `pd run`'s daemonless model; the dashboard starts only when the user runs the dashboard command.
- Keep v1 strictly read-only: no steer, followup, stop, rm, worktree mutation, control-socket actions, or read-triggered stale-status reconciliation from the web UI.
- Follow pi-dispatch conventions: Cobra commands stay thin and delegate to internal packages; external process execution goes through existing runner abstractions when needed.
- Mirror proven mcp-broker dashboard patterns where useful: embedded single-page UI, loopback HTTP server, token/cookie browser auth, SSE, and `httptest` coverage.
- Do not import another module's `internal` packages; copy/adapt small patterns such as loopback validation and browser opening as local pi-dispatch code.
- Default server behavior: bind `127.0.0.1:8300`, print an authenticated URL, auto-open the browser, and support `--host`, `--port`, and `--no-open` flags.
- Auth is required even though the dashboard is read-only, because task prompts, repo paths, logs, and session file paths may be sensitive.
- Public dashboard routes live under `/dashboard/`; internal handler paths may be stripped to `/api/*` and `/events`, but docs and verification should use `/dashboard/api/*` and `/dashboard/events`.
- Dispatch Board shows raw persisted task status in v1. It must not perform CLI-style stale supervisor reconciliation, because that writes to SQLite. Users can run `pd ps` or `pd status` when they want explicit reconciliation.

## Acceptance Criteria

- AC-1: Running `pd dashboard` starts a loopback-only HTTP server on `127.0.0.1:8300` by default, prints an authenticated Dispatch Board URL, and auto-opens the browser unless `--no-open` is passed.
- AC-2: Requests without valid pd auth cannot access the dashboard UI, `/dashboard/api/*`, or `/dashboard/events`; visiting the printed token URL establishes a browser cookie session.
- AC-3: The dashboard exposes read-only JSON APIs under `/dashboard/api/*` for task summaries, task detail/latest run metadata, task events, and stdout/stderr log reads without exposing mutation endpoints or performing read-triggered reconciliation writes.
- AC-4: The Dispatch Board UI shows an Explorer MVP: task list with status/repo/branch/time metadata, filtering/search, selected task details, run metadata, event timeline, and stdout/stderr log viewer.
- AC-5: Live updates work via SSE backed by server-side polling of SQLite/log files; task status/event/log changes become visible without manual page reload.
- AC-6: Existing CLI behavior for `pd run`, `pd ps`, `pd status`, `pd events`, `pd logs`, `pd attach`, and mutation commands remains unchanged, including existing CLI inspection reconciliation behavior.
- AC-7: README and DESIGN documentation describe the dashboard command, safety/auth model, API/UI scope, and v1 read-only boundary.

## Chosen Approach

Implement Dispatch Board as a pi-dispatch-native embedded dashboard served by a new `pd dashboard` command. This keeps the dashboard close to the SQLite state model and existing `internal/store` types, avoids schema duplication in a separate tool, and still provides a clean product/UI identity. Use SSE backed by polling rather than changing supervisor write paths; this gives a live local UX while preserving the existing daemonless/supervisor architecture.

## Documentation Impact

- Update `pi-dispatch/README.md`:
  - Add `pd dashboard` to Quick Start.
  - Document default URL/port, `--host`, `--port`, `--no-open`, token URL, cookie auth, auth token path, `pd token rotate`, and read-only scope.
  - Mention dashboard exploration of tasks/runs/events/logs in the inspection section.
- Update `pi-dispatch/DESIGN.md`:
  - Replace the note that v1 avoids dashboard code with the intended Dispatch Board architecture.
  - Document `internal/dashboard`, public `/dashboard/*` HTTP endpoints, auth boundary, SSE polling, raw persisted status behavior, and read-only constraints.
- Update `pi-dispatch/CLAUDE.md` only if implementation adds durable developer conventions or package layout details worth preserving.
- No examples or changelog updates are required unless execution discovers an existing relevant changelog/example.

## Assumptions / Open Questions

- Q1: Dashboard command name is `pd dashboard`; accepted by user during planning.
- Q2: UI/product name is “Dispatch Board”; accepted by user during planning.
- Q3: Default port `8300` is available and distinct enough from mcp-broker’s default `8200`; if occupied, startup should fail clearly rather than silently changing ports.
- Q4: v1 can poll SQLite/log files at a modest fixed interval; no supervisor event bus is required.
- Q5: Log viewing can initially be bounded/read-window based rather than a full terminal emulator.
- Q6: `pd token rotate` manages a generic pd auth token at `$XDG_CONFIG_HOME/pd/auth-token` or `~/.config/pd/auth-token`; v1 uses it for Dispatch Board auth, but the name should not preclude future pd auth uses.

## Ordered Tasks

### T1: Add dashboard command shell and server lifecycle

Covers: AC-1, AC-6

- Add a thin Cobra command, likely in `pi-dispatch/cmd/pd/dashboard.go`, and register it from `cmd/pd/root.go`/`commands.go`.
- Implement flags: `--host` default `127.0.0.1`, `--port` default `8300`, and `--no-open`.
- Open `store.Open(cfg.DBPath())`, construct the internal dashboard handler, mount it under `/dashboard/`, redirect `/` to `/dashboard/`, and handle SIGINT/SIGTERM graceful shutdown.
- Add pi-dispatch-local helpers for loopback address validation and browser opening, adapted from mcp-broker patterns without importing mcp-broker internals.
- Make browser opening injectable or `internal/exec.Runner`-backed so tests can verify default auto-open behavior and `--no-open` suppression without launching a real browser.
- Ensure `pd dashboard --help` and invalid argument behavior follow repo Cobra conventions.

### T2: Add pd auth token support and rotation command

Covers: AC-2

- Add pi-dispatch-local auth support, likely under `pi-dispatch/internal/auth`, so the token can be generic pd auth rather than dashboard-specific.
- Generate or load a random auth token from `$XDG_CONFIG_HOME/pd/auth-token` or `~/.config/pd/auth-token` with restrictive file permissions, mirroring mcp-broker’s token/cookie flow conceptually.
- Add top-level `pd token rotate`, analogous to mcp-broker, that replaces the pd auth token, does not print the token value, and tells users to restart running dashboard servers to apply it.
- Implement middleware that accepts the token URL once, sets an HttpOnly/SameSite cookie, and protects public `/dashboard/`, `/dashboard/events`, and `/dashboard/api/*` paths.
- Add an unauthorized page that explains using the authenticated URL printed by `pd dashboard`.
- Keep auth local-only; do not add remote/network deployment scope.

### T3: Add read-only store/query support for dashboard APIs

Covers: AC-3, AC-5, AC-6

- Extend `pi-dispatch/internal/store` with dashboard-friendly read methods instead of doing N+1 queries in handlers where avoidable.
- Likely methods:
  - list task summaries with latest run metadata joined or batched.
  - get one task summary/detail by ID.
  - list events with `after_id` and `limit` for incremental refresh.
- Preserve existing store methods and CLI JSON shapes unless there is a clear reason to share view types.
- Do not call `reconcileTask` or write `unknown` statuses from dashboard store/API paths; dashboard reads raw persisted task/run state only.
- Add tests for new store read methods using temporary SQLite databases.

### T4: Implement dashboard HTTP API and SSE

Covers: AC-3, AC-5

- Add `pi-dispatch/internal/dashboard/dashboard.go` with `Dashboard.Handler() http.Handler`, embedded assets, and API routes.
- Candidate public routes, mounted under `/dashboard/`:
  - `GET /dashboard/api/tasks` returns task summaries, optionally with query filters such as status/search if useful server-side.
  - `GET /dashboard/api/tasks/{id}` returns task, latest run, and useful paths/metadata.
  - `GET /dashboard/api/tasks/{id}/events?after_id=&limit=` returns persisted events.
  - `GET /dashboard/api/tasks/{id}/logs?stream=stdout|stderr&offset=&limit=` returns bounded log content plus next offset.
  - `GET /dashboard/events` opens an SSE stream.
  Internally, the dashboard handler may register stripped paths such as `/api/tasks` and `/events` if the command mounts it with `http.StripPrefix`.
- Keep all API routes read-only and use GET only for v1 dashboard data.
- Implement SSE by polling SQLite/log file metadata on a modest interval and broadcasting compact update events to connected browsers.
- Avoid unbounded memory growth for SSE clients and log reads; bound buffers and drop slow client messages like mcp-broker’s nonblocking broadcast.
- Return structured JSON errors with appropriate HTTP status codes for missing tasks, invalid streams, bad offsets, and store failures.

### T5: Build embedded Dispatch Board UI

Covers: AC-4, AC-5

- Add `pi-dispatch/internal/dashboard/index.html` and favicon assets embedded by Go.
- Use mcp-broker dashboard as stylistic inspiration: dark, dense, operator-focused, with clear tabs/panels and monospace metadata.
- Make the UI a single-page app with no build step unless there is a strong reason to add tooling.
- Include:
  - Header with Dispatch Board title, connection status, and server/auth state hints.
  - Task list/table with status chips, repo, branch, created/updated times, and search/status filters.
  - Task detail panel with full prompt or prompt preview, repo/worktree paths, template, raw persisted task/run status, attempt, supervisor PID if exposed, ended time, exit code, error, and Pi session file.
  - Event timeline with event IDs, timestamps, type, message, and expandable payload if payloads become available.
  - stdout/stderr log viewer with stream toggle, refresh/follow behavior, and bounded reads.
- Ensure no UI element implies mutation capability; if future actions are mentioned, mark them explicitly out of scope rather than rendering disabled destructive controls.
- Include a concise status note that Dispatch Board shows persisted state only and does not reconcile stale supervisors; users can run `pd ps` or `pd status` for CLI reconciliation.
- Keep accessibility basics: semantic controls, keyboard-selectable task rows/buttons, readable contrast, and responsive layout.

### T6: Test command, auth, API, SSE, and UI asset behavior

Covers: AC-1, AC-2, AC-3, AC-5, AC-6

- Add `httptest` coverage for public dashboard routes, auth middleware behavior, unauthorized access, token-cookie flow, JSON APIs, missing task errors, and log window reads.
- Add tests for SSE connection setup and at least one polled update/broadcast path using controllable intervals or injectable clocks/pollers.
- Add Cobra command tests for flags/help/error behavior where practical without binding real ports.
- Add tests for `pd token rotate` covering token replacement, file path/permissions where portable, and non-disclosure of the token value.
- Add or preserve focused regression coverage for representative existing CLI behavior likely to share touched code: `pd ps --json`, `pd status --json <task-id>`, `pd events --json <task-id>`, `pd logs <task-id>`, and one mutation/control path such as `pd stop --json <task-id>`.
- Ensure existing CLI tests still pass unchanged.

### T7: Update documentation

Covers: AC-7

- Update `pi-dispatch/README.md` with user-facing command usage, public route/auth behavior, `pd token rotate`, and safety notes.
- Update `pi-dispatch/DESIGN.md` with architecture and design decisions.
- Update `pi-dispatch/CLAUDE.md` only if new conventions are introduced.
- Confirm documentation does not duplicate details better kept in DESIGN vs README per monorepo doc-purpose guidance.

## Verification Checklist

- [ ] V1: From `pi-dispatch`, run `make test` and confirm all tests pass.
- [ ] V2: From `pi-dispatch`, run `make lint` and confirm lint passes.
- [ ] V3: From `pi-dispatch`, run `make build` and confirm `pd` builds.
- [ ] V4: Manually run `pd dashboard --no-open` against a test or real pd state directory and confirm it prints an authenticated URL on port 8300.
- [ ] V5: Confirm unauthenticated HTTP requests to `/dashboard/`, `/dashboard/api/tasks`, and `/dashboard/events` are rejected or redirected to the unauthorized flow, while the printed token URL grants cookie access.
- [ ] V6: Create or use a sample task and confirm the UI displays task list, detail, latest run, events, and logs.
- [ ] V7: While the dashboard is open, create/update task state or append a test event/log line and confirm SSE-backed live updates appear without manual reload.
- [ ] V8: Confirm no HTTP route or UI control can steer, follow up, stop, remove, reconcile stale statuses, or otherwise mutate tasks/worktrees.
- [ ] V9: Confirm `pd token rotate` replaces the generic pd auth token without printing the token and that running dashboards must be restarted to apply it.
- [ ] V10: Confirm default dashboard startup invokes the injectable/browser opener, while `pd dashboard --no-open` suppresses it.
- [ ] V11: Confirm focused CLI regression checks cover `pd ps --json`, `pd status --json`, `pd events --json`, `pd logs`, and a representative mutation/control command.
- [ ] V12: Confirm Documentation Impact was followed: README and DESIGN updated, and CLAUDE updated only if needed.
- [ ] V13: If feasible before final handoff, run `make audit`; otherwise report why it was skipped.

## Known Issues / Follow-ups

- v1 is read-only and intentionally omits steer/followup/stop/rm controls and stale-status reconciliation; any future mutation or dashboard maintenance write support should require a separate design and approval model.
- v1 uses polling-backed SSE rather than supervisor-pushed events; if polling proves insufficient, add an event bus or store notification mechanism later.
- Artifacts such as summaries, diffs, PR URLs, screenshots, and test reports remain out of scope until pi-dispatch adds an artifacts table or equivalent durable state.
