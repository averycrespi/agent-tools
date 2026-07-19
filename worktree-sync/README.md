# Worktree Sync

`worktree-sync` continuously projects registered Git repositories into an isolated tmux server. Each repository gets one `wts-<repo-id>` session, a stable base window rooted at the primary worktree, and one managed window for every eligible linked worktree. `wts` is the administrative CLI; `wtsd` is the portable foreground daemon.

The daemon learns desired state from `git worktree list --porcelain -z`, not directory scanning. Worktrees created by ordinary Git, humans, or sandboxed agents are therefore detected without an MCP or sandbox API when their registered paths are visible consistently on the host.

## Requirements and installation

- Go 1.25+
- Git 2.40+
- tmux 3.2+
- macOS or Linux for the core; macOS for LaunchAgent management

```bash
make install # installs both wts and wtsd
```

All managed tmux objects use the dedicated `wts` socket. They do not share or modify the default tmux server or the older `wt` socket.

## Quick start

```bash
# Register the current primary, non-bare repository.
# The default worktree root is $XDG_DATA_HOME/worktree-sync/worktrees.
wts repo add

# Or register one or more existing roots, including an existing wt root.
wts repo add --id my-repo \
  --worktree-root ~/.local/share/wt/worktrees/my-repo \
  /path/to/primary

wts worktree create feature/example --repo-id my-repo
wts status
wts attach my-repo

# Foreground operation on macOS or Linux.
wtsd

# Per-user macOS LaunchAgent.
wts daemon install
wts daemon status
```

Registration accepts only an existing primary non-bare worktree. Explicit worktree roots must already exist and resolve safely. The generated default root is created with private permissions. Repository IDs contain only letters, numbers, `_`, and `-`, are at most 64 characters, and must be supplied explicitly when basenames collide or are unsafe.

## Commands

```text
wts config path|edit|validate|refresh
wts repo add|list|remove
wts worktree path|create|remove|rm|setup|launch
wts attach [--repo-id repository-id]
wts status [--repo-id repository-id | --all] [--json]
wts reconcile [--repo-id repository-id | --all]
wts cleanup [--prune-git repository-id] [--remove-orphaned-tmux repository-id]
wts daemon install|uninstall|start|stop|status|logs
```

Commands infer a repository from the current primary/worktree path only when the result is unambiguous. Otherwise use `--repo-id`. `status` and `reconcile` accept `--all` to target every registered repository.

`wts worktree create <branch> [--repo-id <id>]` checks out an existing local branch or creates a missing branch from `--from <revision>` (the current HEAD by default). `--from` is rejected when the branch already exists. Paths are collision-safe under `<allowed-root>/<repo-id>/<branch-slug>`. `wts worktree remove` delegates worktree safety to Git. It never deletes a branch unless `--delete-branch` or `--force-delete-branch` is supplied; `--force` applies only to `git worktree remove`.

`wts config edit` uses `VISUAL` or `EDITOR`, validates the temporary edited copy, and atomically replaces the live file only when valid. `config refresh` explicitly migrates older configuration versions and fills newly introduced defaults before atomically saving.

## Configuration

The default path is `${XDG_CONFIG_HOME:-~/.config}/worktree-sync/config.json`. State and locks use `${XDG_STATE_HOME:-~/.local/state}/worktree-sync`; managed creation defaults to `${XDG_DATA_HOME:-~/.local/share}/worktree-sync/worktrees`.

```json
{
  "version": 2,
  "global": {
    "reconcile_interval": "30s",
    "debounce": "250ms",
    "command_timeout": "20s"
  },
  "repositories": [
    {
      "id": "example",
      "primary_root": "/Users/me/src/example",
      "common_git_dir": "/Users/me/src/example/.git",
      "allowed_worktree_roots": [
        "/Users/me/.local/share/worktree-sync/worktrees"
      ],
      "copy_actions": [{ "source": ".env.example", "destination": ".env" }],
      "setup_actions": [
        {
          "argv": ["npm", "install"],
          "env": { "CI": "true" },
          "timeout": "5m"
        }
      ],
      "launch_command": "pi",
      "setup_policy": "wts-created",
      "launch_policy": "wts-created"
    }
  ]
}
```

Registration writes canonical path fields; internal repository identity is derived from `common_git_dir`. Treat the config as trusted host configuration. Setup commands are explicit argv arrays run in the worktree with inherited environment plus overrides and bounded cancellation. Copy sources and destinations are root-relative, cannot escape through symlinks, and are atomically published without overwriting existing destinations. `launch_command` is sent literally to the existing managed worktree window's interactive shell, which interprets it; `wts worktree launch` does not create a window or attach to the session. Do not configure untrusted text.

`setup_policy` and `launch_policy` accept `none`, `manual`, `wts-created`, or `all`; modes are cumulative and default to `manual`. `wts-created` adds automation for worktrees created by `wts`, while `all` also automates externally discovered worktrees. An atomic action ledger records success and failure by repository, worktree identity, trigger, and action-definition digest; complete scans prune vanished identities and obsolete definitions. Failed actions remain visible in status and do not retry indefinitely; use `wts worktree setup|launch --rerun` or change the definition to make an attempt eligible again. Inspection windows are created even when setup fails.

## Reconciliation and safety

A full scan runs at daemon startup and on `reconcile_interval`. Config, Git administration, and allowed-root filesystem notifications are debounced nudges only; missed events, watcher errors, daemon downtime, and restarts recover on a later full scan.

Ownership is determined by tmux user options containing a schema, repository identity, role, and object identity. Names never confer ownership. Reconciliation:

- creates missing base/worktree projections;
- repairs owned names, working directories, and metadata;
- removes duplicate or stale **owned** worktree windows only after complete Git and tmux snapshots;
- preserves every untagged/manual window;
- reports and refuses to adopt an untagged or foreign session-name collision.

Automatic reconciliation never invokes `git worktree remove`, branch deletion, recursive directory deletion, Git pruning, or the default tmux socket. `wts cleanup` without flags is a dry run. Git pruning requires `--prune-git <repo-id>`; owned orphan removal requires `--remove-orphaned-tmux <repo-id>`. Neither action is implied by a generic apply flag. Removing a registered repository deletes only its tagged tmux objects and leaves Git worktrees and branches unchanged; a session containing scratch windows survives.

Detached and locked live worktrees are projected. Missing/prunable worktrees and paths outside allowed canonical roots are reported but do not get windows. Invalid config or incomplete snapshots fail closed for destructive reconciliation.

## LaunchAgent and portability

`wts daemon install` idempotently installs `~/Library/LaunchAgents/dev.agent-tools.worktree-sync.plist` with the absolute sibling `wtsd` path, an explicit `PATH`, `RunAtLoad`, `KeepAlive`, and separate logs under `~/Library/Logs`. `wts daemon logs` shows the last 100 stdout and stderr lines by default; use `--lines` to change the history or `--follow` to stream updates. launchd does not load shell profiles or rotate logs. See [docs/launchd.md](docs/launchd.md) and the reviewed [example plist](examples/launchd/dev.agent-tools.worktree-sync.plist).

On Linux, LaunchAgent commands return an unsupported-platform error; run `wtsd` directly. The core has no Lima integration or path translation. A transparently mounted sandbox needs no special configuration when Git and the host observe consistent registered paths.

## Troubleshooting

- Run `wts config validate` after hand edits.
- Use `wts status --json` for deterministic versioned health, desired/actual resources, conflicts, prunable/report-only worktrees, action failures, and daemon state.
- A foreign or untagged `wts-<repo-id>` session must be renamed or removed manually; it is never adopted.
- If launchd cannot find Git or tmux, install them in one of the generated plist's explicit PATH locations or run `wtsd` in a shell to diagnose.
- Use `wts daemon logs --follow` to stream diagnostics. The files are `~/Library/Logs/worktree-sync.log` and `~/Library/Logs/worktree-sync.error.log`.
