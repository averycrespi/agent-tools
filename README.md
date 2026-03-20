# agent-tools

Tools for working with AI coding agents.

## Tools

### [wt](worktree-manager/) — Worktree Manager

Manage git worktree workspaces with tmux integration. One command to spin up a workspace (worktree + tmux window + agent launch), one command to tear it down.

```bash
wt add <branch>       # Create workspace
wt rm <branch>        # Remove workspace
wt attach [branch]    # Attach to session/window
wt config edit        # Configure launch command, setup scripts, copy files
```

Install: `cd worktree-manager && go install .`

## License

MIT
