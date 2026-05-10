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
pd logs -f <task-id>
pd events <task-id>
pd attach <task-id>
pd steer <task-id> "focus on the failing package"
pd followup <task-id> "run the full test suite now"
pd stop <task-id>
pd stop --force <task-id>
pd template list
pd template validate
pd template show <template>
pd template render <template>
```

`pd run --json`, `pd ps --json`, `pd status --json <task-id>`, and `pd events --json <task-id>` emit machine-readable JSON.

## Safety model

V1 always uses worktree-manager semantics and the shared sandbox-manager Lima VM. `pd` calls those managers as Go packages, so the `wt` and `sb` binaries do not need to be installed for `pd` itself. `pd run` requires the main repository root, not an existing worktree. The sandbox must be configured so the worktree base directory is mounted into the VM.

If the generated worktree path is not visible inside `sb`, `pd run` fails before starting the supervisor. Add the worktree base directory, usually `~/.local/share/wt/worktrees`, as a writable `sb` mount and recreate the Lima VM so the mount is applied.

## Configuration

Config file: `~/.config/pd/config.json`.

```json
{
  "database_path": "",
  "template_dirs": ["~/.config/pd/templates"]
}
```

Templates are standalone JSON files in configured template directories and define Pi launch options. The template name is the JSON filename without the `.json` extension. Templates are decoded strictly: unknown fields such as `name`, `command`, or `mode` are errors. List configured templates with `pd template list`, validate them with `pd template validate [template]`, inspect one with `pd template show <template>`, and preview the Pi argv with `pd template render <template>`.

`pd run` supports launch overrides for Pi options including `--provider`, `--model`, `--thinking`, `--tools`, `--no-builtin-tools`, `--no-tools`, `--extension`, `--no-extensions`, `--skill`, `--no-skills`, `--prompt-template`, `--no-prompt-templates`, `--no-context-files`, `--system-prompt`, `--append-system-prompt`, and `--session-dir`. CLI flags override template values.

Example template:

```json
{
  "description": "Default sandboxed Pi coding agent",
  "agent": {
    "thinking": "medium",
    "tools": [],
    "extensions": [],
    "skills": [],
    "session_dir": ""
  }
}
```

## Paths

- Database: `$XDG_STATE_HOME/pd/pd.db` or `~/.local/state/pd/pd.db`
- Task logs: `$XDG_STATE_HOME/pd/tasks/<task-id>/`
- Runtime sockets: `$XDG_RUNTIME_DIR/pd/tasks/<task-id>.sock`, falling back to state runtime paths
