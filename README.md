# agent-tools

A collection of tools that reduce the friction of working with AI coding agents.

## Tools

### Worktree Manager (wt)

Running multiple AI agents across different branches means a lot of repetitive setup: create a worktree, open a tmux window, copy config files, launch the agent. Tear it all down when you're done. Multiply by several concurrent tasks and it's a lot of ceremony.

`wt` reduces that to two commands. `wt add <branch>` spins up a fully configured worktree — tmux window, config files copied, agent launched. `wt rm <branch>` tears it down. `wt attach` lets you jump between worktrees. It's agent-agnostic: configure it to launch Claude Code, Cursor, or anything else.

See the [README](worktree-manager/README.md) for more information.

### MCP Broker

Autonomous agents need to call external APIs (GitHub, Jira, Slack), but giving a sandboxed agent credentials and network access defeats the point of the sandbox. Punching holes per-tool doesn't scale — a real workflow needs dozens of permissions, and every new tool triggers another prompt.

`mcp-broker` runs on the host, holds the secrets, and exposes backend MCP servers through a single endpoint. Agents connect to it as their only MCP server — no credentials, no network access required. Glob-based policy rules control which tools are allowed, sensitive operations require human approval via a web dashboard, and every call is audit-logged in SQLite.

See the [README](mcp-broker/README.md) for more information.

### Sandbox Manager (sb)

Running AI agents with full host access is risky — one bad command can trash your environment. Containers help, but they're optimized for application isolation, not interactive development. What you want is a full VM that feels like a real development machine, is cheap to create and destroy, and can be provisioned to match your workflow.

`sb` wraps Lima to manage a lightweight Linux VM on macOS. `sb create` spins up a provisioned Ubuntu VM with matching UID/GID, writable mounts, and any tools your provisioning scripts install. `sb shell` drops you in. `sb destroy` tears it down. Configuration drives resource allocation, file copying, and provisioning scripts.

See the [README](sandbox-manager/README.md) for more information.

## License

MIT
