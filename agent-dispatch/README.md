# Agent Dispatch (ad)

Launch and manage autonomous background Pi coding-agent runs in isolated git worktrees inside the shared `sb` sandbox.

Agent Dispatch is daemonless in v1: `ad run` creates durable task state, starts a detached supervisor, and returns a task ID for later inspection.

## Install

```bash
cd agent-dispatch && make install
```

## Quick Start

```bash
ad run "fix the failing tests"
ad ps
ad status <task-id>
ad logs -f <task-id>
ad events <task-id>
ad attach <task-id>
ad stop <task-id>
```

## Safety model

V1 always uses a `wt` worktree and the shared `sb` sandbox. `ad run` requires the main repository root, not an existing worktree. The sandbox must be configured so the `wt` worktree base directory is mounted into the VM.

## Configuration

Config file: `~/.config/ad/config.json`.

```json
{
  "default_template": "",
  "database_path": "",
  "template_dirs": ["~/.config/ad/templates"]
}
```

Templates are standalone JSON files in configured template directories and define Pi launch options.
