# Worktree Manager (wt)

Manage git worktrees with tmux integration. One command to spin up a worktree with a tmux window and agent launch, one command to tear it down.

## Install

```bash
cd worktree-manager && make install
```

Requires Go 1.25+ and tmux.

## Quick Start

```bash
# Create a worktree for a branch
wt add feat/my-feature

# Create and immediately attach
wt add -a feat/my-feature

# Attach to the tmux session
wt attach

# Attach to a specific branch window
wt attach feat/my-feature

# Remove a worktree
wt rm feat/my-feature

# Remove worktree and delete the branch
wt rm -d feat/my-feature
```

## Commands

### `wt add <branch> [-a]`

Creates a worktree for the given branch:

1. Creates a git worktree at `~/.local/share/wt/worktrees/<repo>/<repo>-<branch>`
2. Copies any configured files from the main repo
3. Runs any configured setup scripts
4. Creates a tmux window in the `wt-<repo>` session
5. Sends the configured launch command (e.g. `claude`)

Skips any steps already completed. Pass `-a` to attach after creation.

### `wt rm <branch> [-d | -D]`

Removes the worktree for the given branch:

1. Removes the git worktree
2. Kills the tmux window

Does **not** delete the branch unless `-d` (safe delete) or `-D` (force delete) is passed.

### `wt attach [branch]`

Attaches to the tmux session. If a branch is given, switches to that window. If already inside the `wt` tmux socket, uses `switch-client` instead of `attach-session`.

Works from both the main repo and worktrees.

### `wt config`

| Subcommand          | Description                                            |
| ------------------- | ------------------------------------------------------ |
| `wt config edit`    | Open config in `$EDITOR` (creates defaults if missing) |
| `wt config path`    | Print config file path                                 |
| `wt config refresh` | Create or update config with latest defaults           |

## Configuration

Config file: `~/.config/wt/config.json` (follows XDG)

```json
{
  "launch_command": "claude",
  "setup_scripts": ["scripts/setup.sh"],
  "copy_files": [".claude/settings.local.json", ".env.local"]
}
```

| Field            | Type     | Description                                                                     |
| ---------------- | -------- | ------------------------------------------------------------------------------- |
| `launch_command` | string   | Command sent to tmux window after creation (e.g. `"claude"`)                    |
| `setup_scripts`  | string[] | Scripts to run in the worktree after creation (paths relative to worktree root) |
| `copy_files`     | string[] | Files to copy from the main repo to the worktree (paths relative to repo root)  |

## Paths

| Resource     | Location                                             |
| ------------ | ---------------------------------------------------- |
| Worktrees    | `~/.local/share/wt/worktrees/<repo>/<repo>-<branch>` |
| Config       | `~/.config/wt/config.json`                           |
| Tmux socket  | `-L wt`                                              |
| Tmux session | `wt-<repo>`                                          |
| Tmux window  | `<sanitized-branch>`                                 |

Branch names are sanitized: non-alphanumeric characters (except hyphens) become hyphens. `feat/my-thing` becomes `feat-my-thing`.

## Development

```bash
make build    # Build to ./wt
make install  # Install to $GOPATH/bin/wt
make test     # Run tests with race detector
make lint     # Run golangci-lint
make fmt      # Format with goimports
make tidy     # go mod tidy + verify
make audit    # tidy + fmt + lint + test + govulncheck
```
