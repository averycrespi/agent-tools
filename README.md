# Agent Tools

A collection of tools that reduce the friction of working with AI coding agents.

## Getting Started

Requires Go 1.25+ and macOS.

```bash
# Install system dependencies
brew bundle

# Install all tools
make install

# Install individual tools
cd worktree-manager && make install
cd sandbox-manager && make install
cd mcp-broker && make install
cd local-git-mcp && make install
cd local-gh-mcp && make install
cd broker-cli && make install
```

## Tools

### Worktree Manager (wt)

Running multiple AI agents across different branches means a lot of repetitive setup: create a worktree, open a tmux window, copy config files, launch the agent. Tear it all down when you're done. Multiply by several concurrent tasks and it's a lot of ceremony.

`wt` reduces that to two commands. `wt add <branch>` spins up a fully configured worktree — tmux window, config files copied, agent launched. `wt rm <branch>` tears it down. `wt attach` lets you jump between worktrees. It's agent-agnostic: configure it to launch Claude Code, Cursor, or anything else.

See the [README](worktree-manager/README.md) for more information.

### Sandbox Manager (sb)

Running AI agents with full host access is risky — one bad command can trash your environment. Containers help, but they're optimized for application isolation, not interactive development. What you want is a full VM that feels like a real development machine, is cheap to create and destroy, and can be provisioned to match your workflow.

`sb` wraps Lima to manage a lightweight Linux VM on macOS. `sb create` spins up a provisioned Ubuntu VM with matching UID/GID, writable mounts, and any tools your provisioning scripts install. `sb shell` drops you in. `sb destroy` tears it down. Configuration drives resource allocation, file copying, and provisioning scripts.

See the [README](sandbox-manager/README.md) for more information.

### MCP Broker

Autonomous agents need to call external APIs (GitHub, Jira, Slack), but giving a sandboxed agent credentials and network access defeats the point of the sandbox. Punching holes per-tool doesn't scale — a real workflow needs dozens of permissions, and every new tool triggers another prompt.

`mcp-broker` runs on the host, holds the secrets, and exposes backend MCP servers through a single endpoint. Agents connect to it as their only MCP server — no credentials, no network access required. Glob-based policy rules control which tools are allowed, sensitive operations require human approval via a web dashboard, and every call is audit-logged in SQLite.

See the [README](mcp-broker/README.md) for more information, or the [architecture diagram](mcp-broker/ARCHITECTURE.md) for a visual overview of the request flow.

### Local Git MCP

Sandboxed agents can do most git operations locally — staging, committing, diffing, rebasing — because those don't need authentication. But pushing, pulling, and fetching require credentials that the sandbox intentionally doesn't have.

`local-git-mcp` is a stdio MCP server that runs on the host where SSH keys and credential helpers are available. It exposes five tools — `git_push`, `git_pull`, `git_fetch`, `git_list_remote_refs`, and `git_list_remotes` — over MCP. Designed to be used as a backend for mcp-broker, letting agents push and pull without ever touching credentials.

See the [README](local-git-mcp/README.md) for more information.

### Local GH MCP

Sandboxed agents need to interact with GitHub — creating PRs, checking CI status, reading issues, debugging workflow failures — but giving them credentials defeats the purpose of sandboxing. The official GitHub MCP server requires OAuth or personal access tokens.

`local-gh-mcp` is a stdio MCP server that runs on the host where `gh` CLI is already authenticated. It exposes 24 tools across PRs, issues, workflow runs, caches, and search — over MCP. Designed to be used as a backend for mcp-broker, letting agents interact with GitHub without managing additional credentials.

See the [README](local-gh-mcp/README.md) for more information.

### Broker CLI

Some agents speak MCP natively — but others work better with a CLI. `broker-cli` is an alternative frontend for the MCP broker, for agents that prefer to interact via shell commands instead of connecting as an MCP client.

`broker-cli` connects to the MCP broker, discovers available tools at startup, and builds a subcommand tree — one command per tool, grouped by namespace. Each command gets typed flags generated from the tool's JSON Schema. Output is always a JSON array on stdout; errors are JSON on stderr.

See the [README](broker-cli/README.md) for more information.

## Related

- [claudefiles](https://github.com/averycrespi/claudefiles) — My opinionated resources for working with Claude Code

## License

MIT
