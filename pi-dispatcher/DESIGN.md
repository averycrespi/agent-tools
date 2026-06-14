# Pi Dispatcher Design

Pi Dispatcher (`pd`) is a local job runner for autonomous Pi coding-agent runs.

## V1 architecture

- `pd run` creates a headless worktree through the worktree-manager package, ensures the sandbox-manager Lima VM is available, verifies the worktree path is mounted in the sandbox, creates task/run records in SQLite, and starts a detached `pd supervisor` process.
- The supervisor owns the sandboxed `pi --mode rpc` process and a per-task Unix socket for stop requests.
- The supervisor treats `agent_end` as terminal for a one-shot run, requests Pi state, records the session file when reported, then closes and waits for the Pi RPC process, records terminal run metadata, and performs optional post-run worktree cleanup when the persisted launch-time policy allows it.
- Stop requests mark the task as stopping, send Pi RPC `abort`, and finalize as stopped. Normal stops schedule process termination after a bounded grace period; `pd stop --force` skips the grace period and kills immediately. `pd run --max-duration <duration>` persists a per-run wall-clock cap; the detached supervisor enforces it from the persisted `started_at` time, aborts/kills Pi as needed, and finalizes the run as failed with a `max duration exceeded` error.
- Blocking Pi extension UI requests are auto-cancelled in headless mode.
- SQLite stores compact task and run metadata, including supervisor PID, launch options, exact Pi argv, environment variable names, per-run max duration, worktree cleanup policy/result, end time, exit code, error message, and Pi session file. Environment variable values are passed to the run but are not persisted. Raw stdout/stderr and Pi RPC JSONL records are stored as files under the task state directory.
- Inspection commands reconcile stale starting/running/stopping tasks to `unknown` when the supervisor PID is gone; stale control socket files are ignored.
- `pd wait` polls persisted task state, applies the same stale-supervisor reconciliation as inspection commands, returns immediately for terminal tasks, and can bound the wait with `--timeout`.
- `pd dashboard` starts Pi Dispatcher Dashboard, an on-demand loopback HTTP server for read-only task exploration. It is not a daemon and runs only while the command is active.
- `pd mcp` starts an on-demand stdio MCP server for read-only task exploration by trusted local MCP clients. It is not a daemon and runs only while the command is active.

## State model

V1 uses two tables: `tasks` and `runs`. Launch-time agent options, exact Pi argv, environment variable names, and max duration are recorded on the run row with `agent_options_json`, `pi_argv_json`, `env_var_names_json`, and `max_duration_seconds`. Environment variable values are not persisted. Terminal state is recorded on the run row with `ended_at`, `exit_code`, `error_message`, and `pi_session_file`; the task row carries the latest summary status.

Worktree cleanup is task-level state. The task row records `worktree_cleanup_policy`, whether the worktree was created by this `pd run`, cleanup status/error, cleanup attempt time, and removal time. Cleanup is separate from task/run terminal status: cleanup failures do not alter exit code or `pd wait` success/failure behavior. Automatic cleanup is branch-preserving, non-forced, best-effort, and only targets worktrees created by the current task. Manual `pd cleanup <task-id>...` removes task-owned external resources while preserving task history; `pd rm <task-id>...` forgets pd metadata/logs/socket state only. `pd stop`, `pd cleanup`, and `pd rm` each accept one or more task IDs and apply the operation to every task independently, so a failure on one task does not prevent the rest from being processed.

Dashboard APIs and MCP tools use read-only store queries over these same tables and read stdout/stderr log files from the run paths. The raw Pi RPC event stream remains a file artifact referenced by the latest run.

Artifacts such as summaries, diffs, PR URLs, test reports, screenshots, exported sessions, and dashboard result cards are a vNext concept and should be added later as an `artifacts` table if needed.

## Pi Dispatcher Dashboard

Pi Dispatcher Dashboard lives inside pi-dispatcher under `internal/dashboard` and is served by `pd dashboard`. The command binds a loopback-only HTTP server on `127.0.0.1:8300` by default, redirects `/` to `/dashboard/`, mounts the embedded UI and APIs under `/dashboard/`, prints an authenticated token URL, and opens the browser unless `--no-open` is passed. The Overview tab shows non-empty, non-prompt launch options and environment variable names, and can show the latest assistant response by reading the run's host-persisted Pi event stream and extracting the last assistant message from those events.

The public dashboard surface is:

- `GET /dashboard/` for the embedded single-page Explorer UI.
- `GET /dashboard/api/tasks` for task summaries with latest run metadata.
- `GET /dashboard/api/tasks/{id}` for task detail and latest run metadata.
- `GET /dashboard/api/tasks/{id}/logs` for bounded stdout/stderr log windows, with `stream`, `offset`, and `limit` query parameters.
- `GET /dashboard/events` for polling-backed SSE snapshots.

Dashboard auth uses the generic pd auth token at `$XDG_CONFIG_HOME/pd/auth-token` or `~/.config/pd/auth-token`. Requests without a valid token or dashboard cookie cannot access the UI, APIs, or SSE stream. `pd token rotate` replaces the token without printing the secret; running dashboard servers must be restarted to apply a rotated token.

Pi Dispatcher Dashboard is strictly read-only in v1. It displays persisted cleanup policy/result/error fields but does not initiate, retry, or reconcile cleanup. It does not expose mutation routes or UI controls for stop, cleanup, remove, worktree changes, or control-socket operations. It also does not perform stale-status reconciliation, because CLI reconciliation writes `unknown` statuses to SQLite. Dashboard status displays raw persisted state; users can run `pd ps` or `pd status` when they want explicit CLI reconciliation.

## Pi Dispatcher MCP server

`pd mcp` lives inside pi-dispatcher and starts a stdio MCP server for trusted local clients. Stdout is reserved for MCP JSON-RPC messages; diagnostics and startup failures go to stderr. The server uses the launching user's filesystem permissions and does not have Dashboard's HTTP token/cookie layer because it does not listen on the network.

The MCP tool surface is Dashboard-equivalent and read-only:

- `list_tasks` returns task summaries with latest-run metadata.
- `get_task` returns one task detail with latest-run metadata and a bounded latest assistant response preview extracted from the host-persisted Pi event stream.
- `get_task_logs` returns a bounded stdout or stderr log window for the latest run, with offset, next offset, and file size metadata.

MCP tools do not initiate stop, cleanup, remove, worktree changes, control-socket requests, supervisor commands, or stale-status reconciliation. They omit full prompt text, exact Pi argv, system prompt / append-system-prompt values, and environment variable values. They may expose prompt previews, non-prompt launch options, environment variable names, run metadata, local state paths, latest assistant response previews, and bounded log content.

## Boundaries

The worktree-manager package owns worktree creation and setup scripts. The sandbox-manager package owns sandbox lifecycle and non-interactive command execution. `pd` owns dispatch, supervision, durable state, and inspection.
