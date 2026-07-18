# Worktree Sync

`worktree-sync` continuously projects registered Git worktrees into an isolated tmux server. The `wts` CLI manages registration, lifecycle, status, cleanup, and the macOS LaunchAgent; `wtsd` runs the portable foreground daemon.

This tool is under active implementation. See `wts --help` for the authoritative command surface and [DESIGN.md](DESIGN.md) for intended behavior.
