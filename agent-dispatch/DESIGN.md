# Agent Dispatch Design

Agent Dispatch (`ad`) is a local job runner for autonomous Pi coding-agent runs.

## V1 architecture

- `ad run` creates a headless `wt` worktree, ensures `sb` is available, verifies the worktree path is mounted in the sandbox, creates task/run records in SQLite, and starts a detached `ad supervisor` process.
- The supervisor owns the sandboxed `pi --mode rpc` process and a per-task Unix socket for steering, follow-up, and stop requests.
- The supervisor treats `agent_end` with empty queues as terminal for a one-shot run, then closes the Pi RPC process and marks the task succeeded.
- Blocking Pi extension UI requests are auto-cancelled in headless mode; fire-and-forget UI requests are logged.
- SQLite stores compact task, run, and event metadata. Raw stdout/stderr and Pi RPC JSONL records are stored as files under the task state directory.

## State model

V1 uses three tables: `tasks`, `runs`, and `events`. This is intentionally dashboard-ready while avoiding v1-only dashboard code.

Artifacts such as summaries, diffs, PR URLs, test reports, screenshots, exported sessions, and dashboard result cards are a vNext concept and should be added later as an `artifacts` table if needed.

## Boundaries

`wt` owns worktree creation and setup scripts. `sb` owns sandbox lifecycle and non-interactive command execution. `ad` owns dispatch, supervision, durable state, and inspection.
