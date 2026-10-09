# Agent Tools

[![CI](https://github.com/averycrespi/agent-tools/actions/workflows/ci.yml/badge.svg)](https://github.com/averycrespi/agent-tools/actions/workflows/ci.yml)

Tools that give AI coding agents controlled access to external services while keeping service credentials on the host.

## Tools at a Glance

| Tool                            | Purpose                                                        |
| ------------------------------- | -------------------------------------------------------------- |
| [Agent Gateway](#agent-gateway) | Give agents scoped access to MCP tools and HTTP/HTTPS services |
| [Local Git MCP](#local-git-mcp) | Perform authenticated Git remote operations over MCP           |
| [TypeSafe MCP](#typesafe-mcp)   | Evaluate agent-defined questions with TypeSafe models          |

## Choosing and Combining Tools

Use your preferred agent harness and client environment. Agent Gateway controls access to MCP tools and HTTP/HTTPS services; Local Git MCP and TypeSafe MCP are optional backends you can run behind it or another compatible MCP client.

## Tool Summaries

### Agent Gateway

`agent-gateway` provides a local MCP endpoint and HTTP/HTTPS forward proxy with separate agent identities and scoped access controls.

- Denies access unless granted for MCP tools, with scoped permissions that agents can request through self-service tools.
- Controls HTTP/HTTPS traffic with separate per-agent defaults and grants, supporting HTTPS interception, credential injection, and opaque tunnels.
- Manages upstream credentials and OAuth; agents receive a separate Gateway credential, not upstream service secrets.
- Provides a web application and CLI for administration, with redacted MCP invocation and HTTP traffic history plus control-plane audit records.

See the [Agent Gateway README](agent-gateway/README.md) for setup and usage.

### Local Git MCP

`local-git-mcp` exposes authenticated Git remote operations to agents through a host-side stdio MCP server.

- Supports pushing, pulling, fetching, cloning GitHub repositories, and inspecting remotes and remote refs.
- Uses the host's existing Git, SSH keys, and credential helpers without copying those credentials into the sandbox.
- Runs as a subprocess behind Agent Gateway, with no separate config, persistent state, or network listener.

See the [Local Git MCP README](local-git-mcp/README.md) for setup and usage.

### TypeSafe MCP

`typesafe-mcp` lets agents evaluate questions with TypeSafe models through MCP.

- Supports choosing between options, assigning scores, and estimating probabilities for yes/no questions.
- Returns structured results for agents to use in their workflows.
- Runs as a stateless stdio backend behind Gateway or another compatible MCP client.

See the [TypeSafe MCP README](typesafe-mcp/README.md) for setup and usage.

## Installation

Requirements:

- Go 1.26.9 or later and GNU Make
- For Agent Gateway, an owner-only data directory and protected installation master key for encrypted credential custody

From the repository root, run the install command for the tools you need:

```bash
make -C agent-gateway install
make -C local-git-mcp install
make -C typesafe-mcp install
```

Or install all tools:

```bash
make install
```

Each tool's README covers its configuration and runtime requirements.

## Development

In addition to the installation requirements, development uses Node.js/npm for hooks and formatting, and Python 3 for CI selection and gate tests.

```bash
npm install  # install development dependencies and Git hooks
make build  # build all Go tools
make check  # check CI selection, formatting, lint, and ordinary tool correctness
```

`make setup` combines development dependencies and installation of all tools.

See the [contributor guidance](CLAUDE.md#development) for focused checks and CI requirements.

## Deprecated Tools

<details>
<summary>Unmaintained tools and their final versions</summary>

These tools are no longer maintained, but their final versions remain available in the repository history.

| Tool                  | Last commit                                                                                                                  | Reason                                                                               |
| --------------------- | ---------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------ |
| `mcp-broker`          | [`ef0edecd86`](https://github.com/averycrespi/agent-tools/tree/ef0edecd86e5ab9d81db76dd95afd7df968c3810/mcp-broker)          | Superseded by Agent Gateway.                                                         |
| `http-broker`         | [`ef0edecd86`](https://github.com/averycrespi/agent-tools/tree/ef0edecd86e5ab9d81db76dd95afd7df968c3810/http-broker)         | Superseded by Agent Gateway.                                                         |
| `sandbox-manager`     | [`e9f0dfd3a0`](https://github.com/averycrespi/agent-tools/tree/e9f0dfd3a06461aa068e429349539e929b960357/sandbox-manager)     | Retired with repository-owned guest provisioning; bring your own client environment. |
| `local-gomod-proxy`   | [`586ed5d1aa`](https://github.com/averycrespi/agent-tools/tree/586ed5d1aa925778bf7a98e69a9308d1d4eaad94/local-gomod-proxy)   | No longer needed.                                                                    |
| `worktree-manager`    | [`20b0fb924c`](https://github.com/averycrespi/agent-tools/tree/20b0fb924c97b2058e181ce08f721214bbf80e5c/worktree-manager)    | Deprecated in favor of Herdr for workspace and worktree management.                  |
| `worktree-sync`       | [`20b0fb924c`](https://github.com/averycrespi/agent-tools/tree/20b0fb924c97b2058e181ce08f721214bbf80e5c/worktree-sync)       | Deprecated in favor of Herdr for workspace and worktree management.                  |
| `pi-session-analyzer` | [`7f52e38085`](https://github.com/averycrespi/agent-tools/tree/7f52e380857a435b25ba85a6c3c7e8865e04cd1d/pi-session-analyzer) | Built as an experiment and not carried forward.                                      |
| `pi-dispatcher`       | [`d1f7ae3da4`](https://github.com/averycrespi/agent-tools/tree/d1f7ae3da4aa70616ee2ee6161eaf22e81cd4c51/pi-dispatcher)       | Replaced by the scheduled-tasks Pi extension.                                        |
| `pi-orchestrator`     | [`3e799fa7c1`](https://github.com/averycrespi/agent-tools/tree/3e799fa7c1b568f8d5abe1faf9335f7ba18ad0b1/pi-orchestrator)     | Replaced by the scheduled-tasks Pi extension.                                        |
| `telegram-mcp`        | [`3d9dc4338b`](https://github.com/averycrespi/agent-tools/tree/3d9dc4338b27123184783c80ada6a6aa5e5b7f0f/telegram-mcp)        | Retired; the standalone notification server is no longer maintained.                 |
| `agent-mailbox`       | [`4378f6ef71`](https://github.com/averycrespi/agent-tools/tree/4378f6ef71ea25961b3bb8e08053dfc8ff0302eb/agent-mailbox)       | Replaced by `telegram-mcp`, which is now also retired.                               |
| `local-gh-mcp`        | [`1f7cfd126f`](https://github.com/averycrespi/agent-tools/tree/1f7cfd126fe10f5f3107db771a06450c2adc0d92/local-gh-mcp)        | Deprecated in favor of the official GitHub MCP server.                               |
| `broker-cli`          | [`0251368f3b`](https://github.com/averycrespi/agent-tools/tree/0251368f3b209242d6edcc7b916f476f810cb584/broker-cli)          | Replaced by the `mcp-broker` Pi extension.                                           |
| `hindsight`           | [`164ffccbc0`](https://github.com/averycrespi/agent-tools/tree/164ffccbc010cc41c0a1330f8f1a5570ae61199f/hindsight)           | An experimental memory solution that was ultimately abandoned.                       |

</details>

## Related

- [agent-config](https://github.com/averycrespi/agent-config) — My configuration for working with AI coding agents

## License

MIT
