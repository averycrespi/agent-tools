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
pd run --cleanup-worktree on-success "fix the failing tests"

pd ps
pd status <task-id>
pd wait --timeout 30m <task-id>
pd logs -f <task-id>
pd dashboard
pd steer <task-id> "focus on the failing package"
pd followup <task-id> "run the full test suite now"
pd stop <task-id>
pd stop --force <task-id>
pd cleanup <task-id>
pd cleanup --dry-run <task-id>
pd rm <task-id>
```

`pd run --json`, `pd ps --json`, `pd status --json <task-id>`, and `pd wait --json <task-id>` emit machine-readable JSON, including worktree cleanup policy/result fields. Mutation commands `pd steer --json`, `pd followup --json`, `pd stop --json`, `pd cleanup --json`, and `pd rm --json` emit JSON success responses.

`pd status` shows launch options and terminal run metadata when available, including ended time, exit code, error message, and Pi session file. `pd ps` stays compact for scanning task IDs. `pd wait <task-id>` blocks until a task reaches a terminal state, prints the final status, and returns immediately if the task is already done. It exits 0 only for `succeeded`; `failed`, `stopped`, `unknown`, and timeout return exit code 1. Use `pd wait --timeout 10m <task-id>` to bound the wait.

`pd logs -f` follows stdout and stderr with `stdout:` / `stderr:` prefixes and prints task status, log path, and raw Pi event stream path before following. `pd status` shows the raw Pi event stream path for advanced debugging.

`pd dashboard` starts Pi Dispatch Dashboard, a local read-only web UI for exploring tasks, worktree cleanup state, latest run metadata, launch options, the latest assistant response, raw Pi event stream paths, and stdout/stderr logs. By default it binds `127.0.0.1:8300`, prints an authenticated URL under `/dashboard/`, emits startup diagnostics and failed request logs to stderr, and opens the browser. Use `pd dashboard --no-open` to print the URL without opening a browser, or `--host` / `--port` to choose another loopback bind address. APIs and SSE live under `/dashboard/api/*` and `/dashboard/events`; `pd --verbose dashboard` logs all request/auth flow details.

## Safety model

V1 always uses worktree-manager semantics and the shared sandbox-manager Lima VM. `pd` calls those managers as Go packages, so the `wt` and `sb` binaries do not need to be installed for `pd` itself. `pd run` requires the main repository root, not an existing worktree. The sandbox must be configured so the worktree base directory is mounted into the VM.

If the generated worktree path is not visible inside `sb`, `pd run` fails before starting the supervisor. Add the worktree base directory, usually `~/.local/share/wt/worktrees`, as a writable `sb` mount and recreate the Lima VM so the mount is applied.

Automatic cleanup is disabled by default. Use `pd run --cleanup-worktree on-success` to remove a pd-created worktree after a successful run, or `pd run --cleanup-worktree on-terminal` after `succeeded`, `failed`, or `stopped` completion. Cleanup is best-effort, branch-preserving, and non-forced through worktree-manager; dirty or otherwise blocked worktrees are kept and the cleanup failure is recorded without changing task status, exit code, or `pd wait` semantics. Automatic cleanup only removes worktrees created by that `pd run`; reused/pre-existing worktrees are skipped.

`pd cleanup <task-id>` explicitly removes the associated task worktree for a terminal task while preserving task metadata, logs, Pi event streams, database rows, and branch. `pd cleanup --dry-run <task-id>` reports the target and safety properties without mutating the worktree or cleanup state.

`pd rm <task-id>` removes inactive task metadata, logs, and stale control sockets only. It refuses `starting`, `running`, and `stopping` tasks; stop them first. It does not remove the worktree or branch.

Pi Dispatch Dashboard is read-only in v1. It does not expose steer, follow-up, stop, remove, worktree mutation, control-socket, or stale-status reconciliation actions. It shows persisted SQLite state as-is; run `pd ps` or `pd status` when you want CLI inspection to reconcile stale supervisors to `unknown`.

Pi Dispatch Dashboard requires local auth because prompts, repo paths, logs, and session file paths can be sensitive. The pd auth token is stored at `$XDG_CONFIG_HOME/pd/auth-token` or `~/.config/pd/auth-token` with restrictive permissions. Visiting the printed token URL sets an HttpOnly dashboard cookie. Rotate the token with `pd token rotate`; restart any running dashboard servers afterward.

## Configuration

Config file: `~/.config/pd/config.json`.

```json
{
  "database_path": "",
  "default_worktree_cleanup_policy": "never"
}
```

`default_worktree_cleanup_policy` accepts `never`, `on-success`, or `on-terminal`; `pd run --cleanup-worktree <policy>` overrides it per run and persists the launch-time policy for the detached supervisor.

`pd run` supports direct Pi launch options including `--provider`, `--model`, `--thinking`, `--tools`, `--no-builtin-tools`, `--no-tools`, `--extension`, `--no-extensions`, `--skill`, `--no-skills`, `--no-context-files`, `--system-prompt`, `--append-system-prompt`, `--session-dir`, `--cleanup-worktree`, and repeatable `--env KEY=VALUE`. Each run stores the effective launch options, exact Pi argv, cleanup policy/result, and environment variable names in SQLite for debugging; environment values are passed to the run but are not persisted. `pd status` and Dashboard Overview show cleanup state, non-prompt launch fields, and environment variable names.

## Paths

- Config file: `$XDG_CONFIG_HOME/pd/config.json` or `~/.config/pd/config.json`
- Auth token: `$XDG_CONFIG_HOME/pd/auth-token` or `~/.config/pd/auth-token`
- Database: `$XDG_STATE_HOME/pd/pd.db` or `~/.local/state/pd/pd.db`
- Task logs: `$XDG_STATE_HOME/pd/tasks/<task-id>/`
- Runtime sockets: `$XDG_RUNTIME_DIR/pd/tasks/<task-id>.sock`, falling back to state runtime paths
