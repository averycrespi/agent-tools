# Pi Dispatcher (pd)

Pi Dispatcher (`pd`) is a background task runner for autonomous Pi coding-agent work.

It turns a prompt into a tracked task: `pd run` creates a fresh worktree through `wt`, checks that the worktree is mounted in the shared `sb` Lima sandbox, launches Pi in RPC mode, and records durable task/run metadata, logs, and event paths. Running tasks are managed by detached per-task supervisor processes, so the original terminal can exit while the agent continues.

Use `pd ps`, `pd status`, `pd wait`, `pd logs`, `pd stop`, `pd cleanup`, `pd dashboard`, and `pd mcp` to manage or inspect those tasks after launch. There is no central daemon; supervisors exist only for active tasks.

## Install

```bash
cd pi-dispatcher && make install
```

## Quick Start

```bash
# From the main repository root, not an existing worktree
pd run "fix the failing tests"
pd run --cleanup-worktree on-success "fix the failing tests"
pd run --max-duration 2h "fix the failing tests"

pd ps
pd status <task-id>
pd wait --timeout 30m <task-id>
pd logs -f <task-id>
pd dashboard
pd mcp
pd stop <task-id>...
pd stop --force <task-id>...
pd cleanup <task-id>...
pd cleanup --dry-run <task-id>...
pd rm <task-id>...
```

`pd stop`, `pd cleanup`, and `pd rm` accept one or more task IDs (at least one is required) and process each independently: a failure on one task does not stop the others, and the command exits non-zero if any task failed.

`pd run --json`, `pd ps --json`, `pd status --json <task-id>`, and `pd wait --json <task-id>` emit machine-readable JSON, including worktree cleanup policy/result fields. Mutation commands `pd stop --json`, `pd cleanup --json`, and `pd rm --json` emit a JSON array with one per-task result object (each carrying an `error` field when that task failed).

`pd status` shows launch options and terminal run metadata when available, including max duration, ended time, exit code, error message, and Pi session file. `pd ps` stays compact for scanning task IDs. `pd wait <task-id>` blocks until a task reaches a terminal state, prints the final status, and returns immediately if the task is already done. It exits 0 only for `succeeded`; `failed`, `stopped`, `unknown`, and timeout return exit code 1. Use `pd wait --timeout 10m <task-id>` to bound the wait.

`pd logs -f` follows stdout and stderr with `stdout:` / `stderr:` prefixes and prints task status, log path, and raw Pi event stream path before following. `pd status` shows the raw Pi event stream path for advanced debugging.

`pd dashboard` starts Pi Dispatcher Dashboard, a local read-only web UI for exploring tasks, worktree cleanup state, latest run metadata, launch options, the latest assistant response, raw Pi event stream paths, and stdout/stderr logs. By default it binds `127.0.0.1:8300`, prints an authenticated URL under `/dashboard/`, emits startup diagnostics and failed request logs to stderr, and opens the browser. Use `pd dashboard --no-open` to print the URL without opening a browser, or `--host` / `--port` to choose another loopback bind address. APIs and SSE live under `/dashboard/api/*` and `/dashboard/events`; `pd --verbose dashboard` logs all request/auth flow details. To keep the dashboard available whenever you're logged in on macOS, see [docs/launchd.md](docs/launchd.md).

`pd mcp` starts a stdio MCP server for trusted local MCP clients that need read-only Pi Dispatcher inspection. It exposes `list_tasks`, `get_task`, and `get_task_logs` tools, mirroring Dashboard-style task summaries, task detail/latest assistant response previews, and bounded stdout/stderr log windows. Stdout is reserved for the MCP JSON-RPC stream; diagnostics and startup failures go to stderr.

## Safety model

V1 always uses worktree-manager semantics and the shared sandbox-manager Lima VM. `pd` calls those managers as Go packages, so the `wt` and `sb` binaries do not need to be installed for `pd` itself. `pd run` requires the main repository root, not an existing worktree. The sandbox must be configured so the worktree base directory is mounted into the VM.

If the generated worktree path is not visible inside `sb`, `pd run` fails before starting the supervisor. Add the worktree base directory, usually `~/.local/share/wt/worktrees`, as a writable `sb` mount and recreate the Lima VM so the mount is applied.

Automatic cleanup is disabled by default. Use `pd run --cleanup-worktree on-success` to remove a pd-created worktree after a successful run, or `pd run --cleanup-worktree on-terminal` after `succeeded`, `failed`, or `stopped` completion. Cleanup is best-effort, branch-preserving, and non-forced through worktree-manager; dirty or otherwise blocked worktrees are kept and the cleanup failure is recorded without changing task status, exit code, or `pd wait` semantics. Automatic cleanup only removes worktrees created by that `pd run`; reused/pre-existing worktrees are skipped.

`pd cleanup <task-id>...` explicitly removes the associated task worktree for one or more terminal tasks while preserving task metadata, logs, Pi event streams, database rows, and branch. `pd cleanup --dry-run <task-id>...` reports the target and safety properties without mutating the worktree or cleanup state.

`pd rm <task-id>...` removes inactive task metadata, logs, and stale control sockets only. It refuses `starting`, `running`, and `stopping` tasks; stop them first. It does not remove the worktree or branch.

Pi Dispatcher Dashboard and `pd mcp` are read-only in v1. They do not expose stop, remove, worktree mutation, control-socket, or stale-status reconciliation actions. They show persisted SQLite state as-is; run `pd ps` or `pd status` when you want CLI inspection to reconcile stale supervisors to `unknown`.

Pi Dispatcher Dashboard requires local auth because prompts, repo paths, logs, and session file paths can be sensitive. The pd auth token is stored at `$XDG_CONFIG_HOME/pd/auth-token` or `~/.config/pd/auth-token` with restrictive permissions. Visiting the printed token URL sets an HttpOnly dashboard cookie. Rotate the token with `pd token rotate`; restart any running dashboard servers afterward. `pd mcp` uses stdio instead of HTTP auth and exposes local pd metadata and bounded log previews to the launching MCP client, so configure it only for trusted local clients. Like Dashboard, it omits full prompt text, exact Pi argv, system-prompt fields, and environment variable values; environment variable names may be shown for debugging.

## Configuration

Config file: `~/.config/pd/config.json`.

```json
{
  "database_path": "~/.local/state/pd/pd.db",
  "default_worktree_cleanup_policy": "never"
}
```

`pd config refresh` creates the file with defaults filled in, writing `database_path` resolved to its actual default (`$XDG_STATE_HOME/pd/pd.db` or `~/.local/state/pd/pd.db`). Set `database_path` to point the SQLite database elsewhere; a leading `~` is expanded, and an explicit value is preserved across refreshes.

`default_worktree_cleanup_policy` accepts `never`, `on-success`, or `on-terminal`; `pd run --cleanup-worktree <policy>` overrides it per run and persists the launch-time policy for the detached supervisor.

`pd run` supports direct Pi launch options including `--provider`, `--model`, `--thinking`, `--tools`, `--no-builtin-tools`, `--no-tools`, `--extension`, `--no-extensions`, `--skill`, `--no-skills`, `--no-context-files`, `--system-prompt`, `--append-system-prompt`, `--session-dir`, `--cleanup-worktree`, `--max-duration`, and repeatable `--env KEY=VALUE`. `--max-duration` accepts Go duration strings such as `30m`, `2h`, or `1h30m`; `0` or omission leaves the run unlimited. When the limit expires, the supervisor aborts/kills Pi as needed and finalizes the run as `failed` with a `max duration exceeded` error. Each run stores the effective launch options, exact Pi argv, cleanup policy/result, max duration, and environment variable names in SQLite for debugging; environment values are passed to the run but are not persisted. `pd status` and Dashboard Overview show cleanup state, non-prompt launch fields, max duration, and environment variable names.

## Paths

- Config file: `$XDG_CONFIG_HOME/pd/config.json` or `~/.config/pd/config.json`
- Auth token: `$XDG_CONFIG_HOME/pd/auth-token` or `~/.config/pd/auth-token`
- Database: `$XDG_STATE_HOME/pd/pd.db` or `~/.local/state/pd/pd.db`
- Task logs: `$XDG_STATE_HOME/pd/tasks/<task-id>/`
- Runtime sockets: `$XDG_RUNTIME_DIR/pd/tasks/<task-id>.sock`, falling back to state runtime paths
