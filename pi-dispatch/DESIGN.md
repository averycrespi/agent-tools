# Pi Dispatch Design

Pi Dispatch (`pd`) is a local job runner for autonomous Pi coding-agent runs.

## V1 architecture

- `pd run` creates a headless worktree through the worktree-manager package, ensures the sandbox-manager Lima VM is available, verifies the worktree path is mounted in the sandbox, creates task/run records in SQLite, and starts a detached `pd supervisor` process.
- The supervisor owns the sandboxed `pi --mode rpc` process and a per-task Unix socket for steering, follow-up, and stop requests.
- The supervisor treats `agent_end` with empty queues as terminal for a one-shot run, requests Pi state, records the session file when reported, then closes the Pi RPC process and records terminal run metadata.
- Stop requests mark the task as stopping, send Pi RPC `abort`, and finalize as stopped. `pd stop --force` additionally schedules process termination after a bounded grace period.
- Blocking Pi extension UI requests are auto-cancelled in headless mode; fire-and-forget UI requests are logged.
- SQLite stores compact task, run, and event metadata, including supervisor PID, end time, exit code, error message, and Pi session file. Raw stdout/stderr and Pi RPC JSONL records are stored as files under the task state directory.
- Inspection commands reconcile stale starting/running/stopping tasks to `unknown` when the supervisor PID is gone; stale control socket files are ignored.
- `pd dashboard` starts Pi Dispatch Dashboard, an on-demand loopback HTTP server for read-only task exploration. It is not a daemon and runs only while the command is active.

## State model

V1 uses three tables: `tasks`, `runs`, and `events`. Terminal state is recorded on the run row with `ended_at`, `exit_code`, `error_message`, and `pi_session_file`; the task row carries the latest summary status. Dashboard APIs use read-only store queries over these same tables and read stdout/stderr log files from the run paths.

Artifacts such as summaries, diffs, PR URLs, test reports, screenshots, exported sessions, and dashboard result cards are a vNext concept and should be added later as an `artifacts` table if needed.

## Pi Dispatch Dashboard

Pi Dispatch Dashboard lives inside pi-dispatch under `internal/dashboard` and is served by `pd dashboard`. The command binds a loopback-only HTTP server on `127.0.0.1:8300` by default, redirects `/` to `/dashboard/`, mounts the embedded UI and APIs under `/dashboard/`, prints an authenticated token URL, and opens the browser unless `--no-open` is passed. The Overview tab can show the latest assistant response by reading the run's host-persisted Pi event stream and extracting the last assistant message from those events.

The public dashboard surface is:

- `GET /dashboard/` for the embedded single-page Explorer UI.
- `GET /dashboard/api/tasks` for task summaries with latest run metadata.
- `GET /dashboard/api/tasks/{id}` for task detail and latest run metadata.
- `GET /dashboard/api/tasks/{id}/events` for persisted task events, with `after_id` and `limit` query parameters.
- `GET /dashboard/api/tasks/{id}/logs` for bounded stdout/stderr log windows, with `stream`, `offset`, and `limit` query parameters.
- `GET /dashboard/events` for polling-backed SSE snapshots.

Dashboard auth uses the generic pd auth token at `$XDG_CONFIG_HOME/pd/auth-token` or `~/.config/pd/auth-token`. Requests without a valid token or dashboard cookie cannot access the UI, APIs, or SSE stream. `pd token rotate` replaces the token without printing the secret; running dashboard servers must be restarted to apply a rotated token.

Pi Dispatch Dashboard is strictly read-only in v1. It does not expose mutation routes or UI controls for steer, follow-up, stop, remove, worktree changes, or control-socket operations. It also does not perform stale-status reconciliation, because CLI reconciliation writes `unknown` statuses to SQLite. Dashboard status displays raw persisted state; users can run `pd ps` or `pd status` when they want explicit CLI reconciliation.

## Boundaries

The worktree-manager package owns worktree creation and setup scripts. The sandbox-manager package owns sandbox lifecycle and non-interactive command execution. `pd` owns dispatch, supervision, durable state, and inspection.
