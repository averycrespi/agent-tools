# Agent Dispatch (ad)

Launch and manage autonomous background Pi coding-agent runs in isolated git worktrees inside the shared Lima sandbox.

Agent Dispatch is daemonless in v1: `ad run` creates durable task state, starts a detached supervisor, and returns a task ID for later inspection.

## Install

```bash
cd agent-dispatch && make install
```

## Quick Start

```bash
# From the main repository root, not an existing worktree
ad run "fix the failing tests"

ad ps
ad status <task-id>
ad logs -f <task-id>
ad events <task-id>
ad attach <task-id>
ad steer <task-id> "focus on the failing package"
ad followup <task-id> "run the full test suite now"
ad stop <task-id>
ad stop --force <task-id>
ad template list
```

`ad run --json`, `ad ps --json`, `ad status --json <task-id>`, and `ad events --json <task-id>` emit machine-readable JSON.

## Safety model

V1 always uses worktree-manager semantics and the shared sandbox-manager Lima VM. `ad` calls those managers as Go packages, so the `wt` and `sb` binaries do not need to be installed for `ad` itself. `ad run` requires the main repository root, not an existing worktree. The sandbox must be configured so the worktree base directory is mounted into the VM.

If the generated worktree path is not visible inside `sb`, `ad run` fails before starting the supervisor. Add the worktree base directory, usually `~/.local/share/wt/worktrees`, as a writable `sb` mount and recreate the Lima VM so the mount is applied.

## Configuration

Config file: `~/.config/ad/config.json`.

```json
{
  "database_path": "",
  "template_dirs": ["~/.config/ad/templates"]
}
```

Templates are standalone JSON files in configured template directories and define Pi launch options. The template name is the JSON filename without the `.json` extension. List configured templates with `ad template list`.

`ad run` supports launch overrides for Pi options including `--provider`, `--model`, `--thinking`, `--tools`, `--no-builtin-tools`, `--no-tools`, `--extension`, `--no-extensions`, `--skill`, `--no-skills`, `--prompt-template`, `--no-prompt-templates`, `--no-context-files`, `--system-prompt`, `--append-system-prompt`, and `--session-dir`. CLI flags override template values.

Example template:

```json
{
  "description": "Default sandboxed Pi coding agent",
  "agent": {
    "command": "pi",
    "thinking": "medium",
    "tools": [],
    "extensions": [],
    "skills": [],
    "session_dir": ""
  }
}
```

## Paths

- Database: `$XDG_STATE_HOME/ad/ad.db` or `~/.local/state/ad/ad.db`
- Task logs: `$XDG_STATE_HOME/ad/tasks/<task-id>/`
- Runtime sockets: `$XDG_RUNTIME_DIR/ad/tasks/<task-id>.sock`, falling back to state runtime paths

