# agent-tools

A collection of CLI tools that reduce the friction of working with AI coding agents.

## Tools

### Worktree Manager (wt)

Running multiple AI agents across different branches means a lot of repetitive setup: create a worktree, open a tmux window, copy config files, launch the agent. Tear it all down when you're done. Multiply by several concurrent tasks and it's a lot of ceremony.

`wt` reduces that to two commands. `wt add <branch>` spins up a fully configured workspace — git worktree, tmux window, config files copied, agent launched. `wt rm <branch>` tears it down. `wt attach` lets you jump between workspaces. It's agent-agnostic: configure it to launch Claude Code, Cursor, or anything else.

See the [README](worktree-manager/README.md) for more information.

## License

MIT
