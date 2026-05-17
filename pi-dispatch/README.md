# Pi Dispatch (pd)

Launch and manage autonomous background Pi coding-agent runs in isolated git worktrees inside the shared Lima sandbox.

Pi Dispatch is daemonless in v1: `pd run` creates durable task state, starts a detached supervisor, and returns a task ID for later inspection.

## Install

```bash
cd pi-dispatch && make install
```

## Quick Start

```bash
# From the main repository root, not an existing worktree
pd run "fix the failing tests"

pd ps
pd status <task-id>
pd wait --timeout 30m <task-id>
pd logs -f <task-id>
pd dashboard
pd steer <task-id> "focus on the failing package"
pd followup <task-id> "run the full test suite now"
pd stop <task-id>
pd stop --force <task-id>
pd rm <task-id>
pd rm --worktree <task-id>
```

`pd run --json`, `pd ps --json`, `pd status --json <task-id>`, and `pd wait --json <task-id>` emit machine-readable JSON. Mutation commands `pd steer --json`, `pd followup --json`, `pd stop --json`, and `pd rm --json` emit JSON success responses.

`pd status` shows launch options and terminal run metadata when available, including ended time, exit code, error message, and Pi session file. `pd ps` stays compact for scanning task IDs. `pd wait <task-id>` blocks until a task reaches a terminal state, prints the final status, and returns immediately if the task is already done. It exits 0 only for `succeeded`; `failed`, `stopped`, `unknown`, and timeout return exit code 1. Use `pd wait --timeout 10m <task-id>` to bound the wait.

`pd logs -f` follows stdout and stderr with `stdout:` / `stderr:` prefixes and prints task status, log path, and raw Pi event stream path before following. `pd status` shows the raw Pi event stream path for advanced debugging.

`pd dashboard` starts Pi Dispatch Dashboard, a local read-only web UI for exploring tasks, latest run metadata, launch options, the latest assistant response, raw Pi event stream paths, and stdout/stderr logs. By default it binds `127.0.0.1:8300`, prints an authenticated URL under `/dashboard/`, emits startup diagnostics and failed request logs to stderr, and opens the browser. Use `pd dashboard --no-open` to print the URL without opening a browser, or `--host` / `--port` to choose another loopback bind address. APIs and SSE live under `/dashboard/api/*` and `/dashboard/events`; `pd --verbose dashboard` logs all request/auth flow details.

## Safety model

V1 always uses worktree-manager semantics and the shared sandbox-manager Lima VM. `pd` calls those managers as Go packages, so the `wt` and `sb` binaries do not need to be installed for `pd` itself. `pd run` requires the main repository root, not an existing worktree. The sandbox must be configured so the worktree base directory is mounted into the VM.

If the generated worktree path is not visible inside `sb`, `pd run` fails before starting the supervisor. Add the worktree base directory, usually `~/.local/share/wt/worktrees`, as a writable `sb` mount and recreate the Lima VM so the mount is applied. `pd` does not automatically remove worktrees after failed launches; use `pd rm --worktree <task-id>` when you want to clean up the associated worktree.

`pd rm <task-id>` removes inactive task metadata, logs, and stale control sockets. It refuses `starting`, `running`, and `stopping` tasks; stop them first. `pd rm --worktree <task-id>` also removes the associated worktree through worktree-manager semantics and does not delete the branch.

Pi Dispatch Dashboard is read-only in v1. It does not expose steer, follow-up, stop, remove, worktree mutation, control-socket, or stale-status reconciliation actions. It shows persisted SQLite state as-is; run `pd ps` or `pd status` when you want CLI inspection to reconcile stale supervisors to `unknown`.

Pi Dispatch Dashboard requires local auth because prompts, repo paths, logs, and session file paths can be sensitive. The pd auth token is stored at `$XDG_CONFIG_HOME/pd/auth-token` or `~/.config/pd/auth-token` with restrictive permissions. Visiting the printed token URL sets an HttpOnly dashboard cookie. Rotate the token with `pd token rotate`; restart any running dashboard servers afterward.

## Configuration

Config file: `~/.config/pd/config.json`.

```json
{
  "database_path": ""
}
```

`pd run` supports direct Pi launch options including `--provider`, `--model`, `--thinking`, `--tools`, `--no-builtin-tools`, `--no-tools`, `--extension`, `--no-extensions`, `--skill`, `--no-skills`, `--no-context-files`, `--system-prompt`, `--append-system-prompt`, `--session-dir`, and repeatable `--env KEY=VALUE`. Each run stores the effective launch options, exact Pi argv, and environment variable names in SQLite for debugging; environment values are passed to the run but are not persisted. `pd status` and Dashboard Overview show non-prompt launch fields and environment variable names.

## Paths

- Config file: `$XDG_CONFIG_HOME/pd/config.json` or `~/.config/pd/config.json`
- Auth token: `$XDG_CONFIG_HOME/pd/auth-token` or `~/.config/pd/auth-token`
- Database: `$XDG_STATE_HOME/pd/pd.db` or `~/.local/state/pd/pd.db`
- Task logs: `$XDG_STATE_HOME/pd/tasks/<task-id>/`
- Runtime sockets: `$XDG_RUNTIME_DIR/pd/tasks/<task-id>.sock`, falling back to state runtime paths
