# Worktree Sync

`worktree-sync` continuously mirrors registered Git worktrees into isolated tmux sessions. Use `wts` to manage repositories and worktrees; run `wtsd` to keep tmux synchronized in the foreground or as a macOS LaunchAgent.

## Mental model

Git is the source of truth. For each registered repository, `worktree-sync` maintains:

```text
registered repository "api"
└── tmux session wts-api
    ├── base          → primary Git worktree
    ├── feature-auth  → linked worktree
    └── scratch       → manual window, preserved
```

Each repository gets one `wts-<repository-id>` session on the dedicated `wts` tmux socket. The primary worktree maps to the stable `base` window, and each eligible linked worktree maps to another window. Manual windows are never adopted or removed.

The daemon reads `git worktree list --porcelain -z`; it does not discover desired state by scanning directories. Worktrees created with ordinary Git are therefore supported when their paths are visible to the host and fall under an allowed root.

## Requirements and installation

Requirements:

- Go 1.25 or newer
- Git
- tmux

Install both binaries:

```bash
make install
```

This installs:

- `wts` — the administrative CLI
- `wtsd` — the foreground reconciliation daemon

## Five-minute walkthrough

From the primary checkout of a non-bare repository:

```bash
# Register the current repository. The repository basename becomes its ID.
wts repo add

# Create a linked worktree and a new branch from origin/main.
wts worktree create feature/auth --from origin/main

# Inspect this repository's Git, tmux, and action state.
wts status

# Attach to its managed tmux session.
wts attach
```

Without a configured or command-line creation root, managed creation defaults to:

```text
${XDG_DATA_HOME:-~/.local/share}/worktree-sync/worktrees/
└── <repository-id>/
    └── <branch-slug>/
```

For example, `feature/auth` in repository `api` is normally created at:

```text
~/.local/share/worktree-sync/worktrees/api/feature-auth
```

Use `--repo-id` when the current directory does not identify the intended repository:

```bash
wts worktree create feature/auth --repo-id api
wts status --repo-id api
wts attach --repo-id api
```

`wts status --all` and `wts reconcile --all` target every registered repository.

## Common workflows

### Register a repository with custom roots

The creation root controls where `wts worktree create` places worktrees. Additional allowed roots permit discovery of worktrees created through ordinary Git. Every root must already exist and canonicalize safely:

```bash
wts repo add \
  --id api \
  --worktree-root ~/.local/share/wt/worktrees \
  --allowed-worktree-root /Volumes/external-worktrees \
  /path/to/primary
```

Registration accepts only a primary, non-bare worktree. Repository IDs contain at most 64 letters, numbers, hyphens, or underscores. Supply `--id` when the basename is unsafe or collides with another repository.

To configure roots for future registrations, add them to the existing `global` object with `wts config edit`. The resulting object can look like:

```json
{
  "reconcile_interval": "30s",
  "debounce": "250ms",
  "command_timeout": "20s",
  "default_worktree_creation_root": "/Users/me/.local/share/wt/worktrees",
  "default_allowed_worktree_roots": ["/Volumes/external-worktrees"]
}
```

Configured roots must already exist and be absolute, canonical paths. Registration copies the selected creation root and complete allowlist into the new repository; changing global defaults does not alter existing repositories. The creation root is automatically allowed. `--worktree-root` replaces the default creation root, while one or more `--allowed-worktree-root` values replace the default additional allowlist for that registration. Allowed-root ordering has no effect on creation.

To ignore configured additional roots for one new registration, use `--no-default-allowed-roots`; it cannot be combined with `--allowed-worktree-root`, and the creation root remains allowed.

Manage an existing registration without editing identity fields:

```bash
wts repo roots show
wts repo roots set-creation /new/creation/root
wts repo roots add-allowed /Volumes/external-worktrees
wts repo roots remove-allowed /old/root
```

These commands infer the current repository or accept `--repo-id`. Setting creation keeps the former root allowed and affects only future `wts`-created worktrees; it does not move existing ones. Removing the active creation root, an unavailable root, or a root still needed by an enumerated live worktree is rejected. A running daemon observes the saved config automatically; otherwise run the reconciliation command printed by the CLI.

### Use an existing branch

If the branch already exists, `wts` checks it out without creating it:

```bash
wts worktree create existing-branch
```

`--from` applies only when creating a missing branch and accepts any Git revision:

```bash
wts worktree create feature/auth --from origin/main
wts worktree create experiment --from abc1234
```

Using `--from` with an existing branch is rejected instead of silently ignoring the revision.

### Use worktrees created by ordinary Git

```bash
git worktree add /allowed/root/api/external-feature external-feature
```

The next filesystem nudge or periodic reconciliation discovers the worktree and creates its managed tmux window. Its canonical path must be beneath one of the repository's `allowed_worktree_roots`.

### Remove a worktree

```bash
# Preserve the branch.
wts worktree remove feature/auth

# Remove the worktree, then safely delete its merged branch.
wts worktree remove feature/auth --delete-branch

# Explicitly override Git's dirty-worktree and branch safety checks.
wts worktree remove feature/auth --force --force-delete-branch
```

`--force` applies only to `git worktree remove`. Branch deletion always requires its own flag. Remove, setup, and launch resolve an exact branch name first, then an existing absolute, relative, or symlinked path.

### Run setup actions

```bash
wts worktree setup feature/auth
wts worktree setup feature/auth --rerun
```

Setup runs configured `copy_actions` followed by `setup_actions`. Copy sources are relative to the registered primary worktree; destinations are relative to the target linked worktree. Both sides reject symlink escapes, and copies never overwrite an existing destination. Setup commands use explicit argv arrays rather than an implicit shell.

### Launch a configured command

```bash
wts worktree launch feature/auth
wts worktree launch feature/auth --rerun
```

Launch types `launch_command` into the worktree's existing managed tmux window and sends Enter. It does not create a window or attach to the session. The CLI reports whether launch started or why it was skipped without printing the configured command.

## Configuration

Configuration is stored at:

```text
${XDG_CONFIG_HOME:-~/.config}/worktree-sync/config.json
```

State and locks use `${XDG_STATE_HOME:-~/.local/state}/worktree-sync`.

Useful commands:

```bash
wts config path
wts config validate
wts config edit
wts config refresh
```

`config edit` opens a temporary copy using `VISUAL` or `EDITOR`, validates it, and atomically replaces the live file only when valid. `config refresh` explicitly migrates older versions and fills new defaults. Version-1 and version-2 configurations must be refreshed before other commands run. Version 2 migrates deterministically: the first former default allowed root becomes `default_worktree_creation_root`, and each repository's first allowed root becomes its `worktree_creation_root`; the complete allowlists are preserved.

A legacy passive-only policy cannot map exactly to the cumulative policy modes. If refresh reports one, back up and edit the version-1 file directly: either enable the matching `*_explicit` field to migrate it to `all`, or disable the `*_passive` field to migrate it to `none`. Then run refresh and, if desired, use `wts config edit` to select `manual`.

```bash
cp "$(wts config path)" "$(wts config path).bak"
"${VISUAL:-${EDITOR:-vi}}" "$(wts config path)"
wts config refresh
```

A registered repository without automation resembles:

```json
{
  "version": 3,
  "global": {
    "reconcile_interval": "30s",
    "debounce": "250ms",
    "command_timeout": "20s",
    "default_worktree_creation_root": "/Users/me/.local/share/wt/worktrees",
    "default_allowed_worktree_roots": ["/Volumes/external-worktrees"]
  },
  "repositories": [
    {
      "id": "api",
      "primary_root": "/Users/me/src/api",
      "common_git_dir": "/Users/me/src/api/.git",
      "worktree_creation_root": "/Users/me/.local/share/wt/worktrees",
      "allowed_worktree_roots": [
        "/Users/me/.local/share/wt/worktrees",
        "/Volumes/external-worktrees"
      ],
      "setup_policy": "manual",
      "launch_policy": "manual"
    }
  ]
}
```

Registration writes canonical path fields, stores the selected root in `worktree_creation_root`, and snapshots the complete safety boundary in `allowed_worktree_roots`. The creation root must appear in that allowlist. `common_git_dir` is Git's shared administrative directory and is also the source of the internal repository identity used for tmux ownership and durable state; there is no separate identity field to edit.

### Setup and launch automation

An automated repository can add:

```json
{
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
```

`setup_policy` and `launch_policy` are cumulative:

| Value         | Manual command | Automate `wts`-created worktrees | Automate externally discovered worktrees |
| ------------- | -------------- | -------------------------------- | ---------------------------------------- |
| `none`        | No             | No                               | No                                       |
| `manual`      | Yes            | No                               | No                                       |
| `wts-created` | Yes            | Yes                              | No                                       |
| `all`         | Yes            | Yes                              | Yes                                      |

Both policies default to `manual`. Automatic launch is considered only when a managed worktree window is newly created.

An atomic action ledger records success and failure by repository, worktree identity, origin, and action-definition digest. Both outcomes suppress repeated automatic attempts. Change the definition or pass `--rerun` to make another attempt. Failed actions remain visible in status, and a setup failure never hides the worktree's inspection window.

Configuration is trusted host input. Setup argv and environment values are passed directly to the selected executable. `launch_command` is trusted shell input interpreted by the interactive tmux shell; never populate it from untrusted text.

### Inspect status and use it in automation

```bash
wts status
wts status --verbose
wts status --all --json
wts status --all --json --check
```

Healthy repositories remain one-line summaries. Attention, degraded, and conflict states expand automatically with sorted diagnostics, affected worktrees, action failures, and safe next steps; `--verbose` also lists healthy desired worktrees and managed windows. `--check` prints the same complete human or JSON report and exits nonzero whenever any selected status requires attention, so JSON remains valid on stdout while the check error goes to stderr.

Status JSON schema version 2 uses stable typed fields rather than internal Git/tmux or raw `launchctl` objects. The daemon state is `running`, `stopped`, `not_installed`, `unsupported`, or `unavailable`. `--verbose` and `--json` are mutually exclusive.

## Reconciliation and safety

A full scan runs at daemon startup and on `reconcile_interval`. Configuration, Git administration, and allowed-root filesystem events debounce an earlier full scan. Periodic scans recover from missed events, unavailable mounts, daemon downtime, and restarts.

Ownership comes only from tmux metadata containing the schema, derived repository identity, role, and object identity. Names never confer ownership. Reconciliation can:

- create missing managed sessions and windows;
- repair owned names, working directories, and metadata;
- remove duplicate or stale owned worktree windows after complete snapshots; and
- report conflicts, missing paths, outside-root worktrees, and prunable records.

Automatic reconciliation never:

- invokes `git worktree remove`;
- deletes branches or directories;
- runs `git worktree prune`;
- adopts or removes manual, untagged, malformed, or foreign tmux resources; or
- touches the default tmux socket.

A managed window whose pane changes directory is considered drifted. Reconciliation restores its configured worktree directory with `respawn-window -k`, which terminates the pane's current shell or process. Use an untagged scratch window for work that should not be reset.

Detached and locked live worktrees remain eligible. Invalid configuration or incomplete Git/tmux snapshots fail closed.

### Explicit cleanup

Without flags, cleanup is reporting-only:

```bash
wts cleanup
```

Mutating modes require a repository ID:

```bash
wts cleanup --prune-git api
wts cleanup --remove-orphaned-tmux api
```

`--prune-git` runs repository-wide `git worktree prune` only after Git still reports stale metadata. `--remove-orphaned-tmux` removes only correctly owned worktree windows whose identities are absent from current desired state. It preserves manual and foreign windows and rechecks ownership immediately before removal.

Unregistering a repository removes only its tagged tmux resources and leaves Git worktrees and branches unchanged:

```bash
wts repo remove api
```

A session containing scratch windows survives unregister.

## Running the daemon

### Foreground on macOS or Linux

```bash
wtsd
```

`wtsd --help` describes foreground operation. Unknown flags and positional arguments fail before configuration is loaded or reconciliation starts. SIGINT and SIGTERM cancel in-flight work and stop the daemon cleanly.

### macOS LaunchAgent

```bash
wts daemon install
wts daemon status
wts daemon logs
wts daemon logs --follow
wts daemon stop
wts daemon start
wts daemon restart
wts daemon uninstall
```

Installation manages only `~/Library/LaunchAgents/dev.agent-tools.worktree-sync.plist`. It uses the absolute sibling `wtsd`, an explicit `PATH`, the effective absolute XDG config/state/data homes, `RunAtLoad`, `KeepAlive`, and separate logs under `~/Library/Logs`. Rerun install after changing XDG homes. launchd does not load shell profiles or rotate logs.

`stop` unloads the job but preserves the installed plist, so `KeepAlive` cannot restart it. `start` loads a stopped installation, and `restart` performs an explicit unload/load cycle. Status distinguishes running, stopped-but-installed, and not installed.

See [docs/launchd.md](docs/launchd.md) and the reviewed [example plist](examples/launchd/dev.agent-tools.worktree-sync.plist).

LaunchAgent commands are unsupported on Linux; run `wtsd` directly under your preferred process supervisor. The core has no Lima dependency or path translation.

## Command reference

| Command                                                             | Purpose                                                            |
| ------------------------------------------------------------------- | ------------------------------------------------------------------ |
| `wts config path\|edit\|validate\|refresh`                          | Inspect, edit, validate, or migrate configuration                  |
| `wts repo add [path]`                                               | Register a primary worktree                                        |
| `wts repo list`                                                     | List registered repositories                                       |
| `wts repo remove <repository-id>`                                   | Stop managing a repository without deleting Git worktrees          |
| `wts repo roots show\|set-creation\|add-allowed\|remove-allowed`    | Inspect or update one repository's worktree roots                  |
| `wts worktree path <branch>`                                        | Print an existing or planned worktree path                         |
| `wts worktree create <branch>`                                      | Create a worktree and reconcile its tmux window                    |
| `wts worktree remove\|rm <path-or-branch>`                          | Remove a worktree with explicit branch safety                      |
| `wts worktree setup <worktree>`                                     | Run configured copy and setup actions                              |
| `wts worktree launch <worktree>`                                    | Run the configured command in its managed tmux window              |
| `wts attach`                                                        | Attach to the current repository's managed session                 |
| `wts status`                                                        | Show progressive status; use `--verbose`, `--json`, or `--check`   |
| `wts reconcile`                                                     | Reconcile the current repository; use `--all` for every repository |
| `wts cleanup`                                                       | Inspect or explicitly remove stale Git and tmux state              |
| `wts daemon install\|uninstall\|start\|stop\|restart\|status\|logs` | Manage the macOS LaunchAgent                                       |

Repository-selecting commands accept `--repo-id <id>` when current-directory inference is not appropriate. Run `wts <command> --help` for flags, safety details, and examples.

## Troubleshooting

### Repository context cannot be selected

The CLI distinguishes an empty registry, an unregistered Git repository, a directory outside registered worktrees, and failed Git inspection. Follow the printed `wts repo add <primary-path>` instruction for an unregistered repository, or run from a registered primary/linked worktree and pass `--repo-id <id>` when current-directory inference is not appropriate.

### “sessions should be nested with care”

You are attaching from inside tmux. Detach first, or deliberately unset `TMUX` for the command:

```bash
env -u TMUX wts attach
```

### A setup or launch action was skipped

Check the repository's policy and `wts status`. The action may be disabled, unconfigured, or already recorded in the ledger. Use `--rerun` for an intentional retry.

### A session-name collision is reported

An untagged or foreign `wts-<repository-id>` session is never adopted or attached. Rename or remove it manually after confirming ownership. `wts attach` locates a managed session by complete ownership metadata, rechecks its stable session ID, and suggests an explicit reconcile when no owned session exists.

### The LaunchAgent cannot find Git or tmux

Install them under `/opt/homebrew/bin`, `/usr/local/bin`, `/usr/bin`, or `/bin`, or run `wtsd` in a terminal to compare environments. Inspect diagnostics with:

```bash
wts daemon logs --follow
```

The underlying files are `~/Library/Logs/worktree-sync.log` and `~/Library/Logs/worktree-sync.error.log`.
