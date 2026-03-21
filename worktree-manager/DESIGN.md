# worktree-manager Design

## Motivation

Working with AI coding agents across multiple branches means repetitive setup: create a git worktree, open a tmux window, copy config files, launch the agent. Reverse it all when done. Multiply by several concurrent tasks and it's a lot of ceremony.

`wt` reduces that to `wt add <branch>` and `wt rm <branch>`. One command to create a fully configured workspace. One command to tear it down. One command (`wt attach`) to jump between them.

It's agent-agnostic — configure it to launch Claude Code, Cursor, or anything else.

## Architecture

```
cmd/              Cobra CLI (thin wrappers)
  root.go         Service construction, flags, completion
  add.go          wt add
  rm.go           wt rm
  attach.go       wt attach
  config.go       wt config {path,edit,refresh}

internal/
  exec/           Runner interface abstracting os/exec
  config/         Config loading + XDG path functions
  git/            Git worktree operations
  tmux/           Tmux session/window operations
  workspace/      Orchestration tying git + tmux + config together
```

### Dependency Flow

```
cmd → workspace.Service → git.Client    → exec.Runner
                        → tmux.Client   → exec.Runner
                        → config.Config
```

All external commands flow through `exec.Runner`, an interface with `Run`, `RunDir`, and `RunInteractive` methods. Tests inject mock runners to verify command arguments without executing real processes.

### Key Design Decisions

**exec.Runner interface.** Every shell command goes through this interface. This makes the git and tmux clients fully testable with mock runners — tests verify exact argument construction without touching git or tmux.

**Interface segregation in workspace.** The workspace package defines its own `gitClient` and `tmuxClient` interfaces containing only the methods it needs, rather than depending on the concrete types. This keeps the coupling minimal and tests focused.

**Config-driven behavior.** What to launch, which files to copy, which scripts to run — all driven by `~/.config/wt/config.json`. The tool has no hardcoded knowledge of any specific agent or workflow.

**Idempotent operations.** `wt add` skips steps already completed (worktree exists, window exists). `wt rm` skips resources already removed. This makes it safe to re-run commands without side effects.

**Tmux socket isolation.** All tmux operations use `-L wt`, a dedicated socket separate from the user's default tmux. This prevents `wt` from interfering with existing tmux sessions.

**XDG base directories.** Config in `$XDG_CONFIG_HOME/wt`, data in `$XDG_DATA_HOME/wt`. Falls back to `~/.config` and `~/.local/share` per the XDG spec.

### Workspace Lifecycle

**Add (`wt add feat/thing`):**

```
git worktree add ~/.local/share/wt/worktrees/myrepo/myrepo-feat-thing -b feat/thing
  → copy .env.local, .claude/settings.local.json (from config)
  → run scripts/setup.sh (from config)
tmux new-window -t wt-myrepo -n feat-thing -c <worktree-path>
  → send-keys "claude" (from config)
```

**Remove (`wt rm feat/thing`):**

```
git worktree remove ~/.local/share/wt/worktrees/myrepo/myrepo-feat-thing
tmux kill-window -t wt-myrepo:feat-thing
  → optionally: git branch -d feat/thing
```

**Attach (`wt attach` or `wt attach feat/thing`):**

```
If inside wt socket → tmux switch-client
If outside          → tmux attach-session
If branch given     → target specific window
```

### Error Handling

- Git and tmux failures propagate as errors and halt the operation
- File copy and setup script failures log warnings but don't halt — these are best-effort
- All commands reject being run from a worktree (except `attach`, which resolves back to the main repo)
