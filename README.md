# Agent Tools

[![CI](https://github.com/averycrespi/agent-tools/actions/workflows/ci.yml/badge.svg)](https://github.com/averycrespi/agent-tools/actions/workflows/ci.yml)

My tools for working with AI coding agents: controlled external access that keeps upstream credentials on the host. Use individual tools or combine them to fit your workflow.

## Tools at a Glance

| Tool                            | Purpose                                               | Runs on         |
| ------------------------------- | ----------------------------------------------------- | --------------- |
| [MCP Broker](#mcp-broker)       | Apply rules and per-call human approval to MCP tools  | Host            |
| [Agent Gateway](#agent-gateway) | Give agents scoped access to MCP tools                | Host            |
| [HTTP Broker](#http-broker)     | Inject credentials into proxied HTTP/HTTPS requests   | Host            |
| [Local Git MCP](#local-git-mcp) | Perform authenticated Git remote operations over MCP  | Host subprocess |
| [TypeSafe MCP](#typesafe-mcp)   | Evaluate agent-defined questions with TypeSafe models | Host subprocess |

## Choosing and Combining Tools

These tools are independent, not a mandatory stack:

- **Execution:** Bring your own client environment. These access tools do not manage VMs or guest provisioning and do not require Lima or a particular agent harness.
- **MCP access:** Choose MCP Broker or Agent Gateway based on the permission model below. Both connect agents to backend MCP servers.
- **Git access:** Run Local Git MCP as a stdio backend behind either Broker or Gateway, using that service's access controls and invocation history.
- **Model evaluation:** Run TypeSafe MCP behind Gateway for agent-defined Choice, Score, and Noul questions, with explicit paid-inference authorization.
- **Non-MCP traffic:** HTTP Broker handles ordinary HTTP/HTTPS clients. It complements MCP access rather than routing through it.

### MCP Broker or Agent Gateway?

Both keep upstream credentials outside the sandbox, but approval means different things:

|                | MCP Broker                                                   | Agent Gateway                                                          |
| -------------- | ------------------------------------------------------------ | ---------------------------------------------------------------------- |
| Access model   | Rules allow, deny, or require human approval for a tool call | Per-agent grants scope access to servers, tools, or matching arguments |
| Human approval | Resolves an individual waiting call                          | Grants permissions; does not approve a queued tool call                |
| Agent workflow | Call tools under operator-defined rules                      | Discover tools, inspect access, and request additional permissions     |

They have separate configuration and state; Gateway does not migrate Broker settings.

## Tool Summaries

### MCP Broker

`mcp-broker` proxies MCP servers through a single host-side endpoint when you want rule-based access with optional per-call human approval.

- Applies allow, deny, or require-approval rules to tool calls.
- Collects human decisions through a web dashboard, with optional Telegram approval.
- Records tool calls in a searchable SQLite audit log and displays discovered tools and rules.

See the [MCP Broker README](mcp-broker/README.md) for setup and usage.

<a id="mcp-gateway"></a>

### Agent Gateway

`agent-gateway` provides a local MCP endpoint when you want separate agent identities and scoped permissions that agents can request through MCP.

- Denies access unless granted, with scopes for servers, tools, or matching arguments and optional expiry.
- Manages upstream credentials and OAuth; agents receive a separate Gateway credential, not upstream service secrets.
- Provides a web application and CLI for administration, with redacted invocation history and control-plane audit records.

Only the canonical executable is published. Existing installations retain explicit-root operation and the [post-migration installation safety guidance](agent-gateway/docs/operators/installation-safety.md); publication never removes stale installed artifacts. The source directory and Go module are `agent-gateway`; durable and MCP identities remain unchanged.

See the [Agent Gateway README](agent-gateway/README.md) for setup and usage.

### HTTP Broker

`http-broker` is a host-side HTTP/HTTPS forward proxy for clients such as `curl` and SDKs that need authenticated access outside MCP.

- Applies rules to intercept, tunnel, or deny traffic, injecting host-held credentials into intercepted requests.
- Binds each credential to allowed destination hosts, independently of request rules.
- Records proxy traffic in an audit log with a read-only web dashboard.

Enforcement is **cooperative**: clients must honour `HTTP_PROXY`/`HTTPS_PROXY`. Clients can bypass the proxy, so it is not a containment boundary. See the [security model](http-broker/docs/security-model.md) for details.

See the [HTTP Broker README](http-broker/README.md) for setup and usage.

### Local Git MCP

`local-git-mcp` exposes authenticated Git remote operations to agents through a host-side stdio MCP server.

- Supports pushing, pulling, fetching, cloning GitHub repositories, and inspecting remotes and remote refs.
- Uses the host's existing Git, SSH keys, and credential helpers without copying those credentials into the sandbox.
- Runs as a subprocess behind MCP Broker or Agent Gateway, with no separate config, persistent state, or network listener.

See the [Local Git MCP README](local-git-mcp/README.md) for setup and usage.

### TypeSafe MCP

`typesafe-mcp` is a stateless stdio backend for TypeSafe inference.

- Exposes exactly `evaluate` and `list_models`, with strict input and response contracts.
- Sends mixed agent-defined questions in one bounded, authenticated HTTPS request, without retries or action policy.
- Reads only `TYPESAFE_API_KEY`, compatible with Gateway's secret environment slots.

Inference discloses submitted data to TypeSafe and consumes quota. See the [TypeSafe MCP README](typesafe-mcp/README.md) for limits, privacy considerations, and secret-free Gateway configuration.

## Installation

Requirements:

- Go 1.26.6 or later and GNU Make
- A supported operating-system keyring for Agent Gateway server credentials

From the repository root, run the install command for the tools you need:

```bash
make -C mcp-broker install
make -C agent-gateway install
make -C http-broker install
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

GitHub Actions checks affected tools on pull requests and all tools on `main`, manual runs, and a weekly schedule. See the [contributor guidance](CLAUDE.md#development) for test ownership and focused checks, and [CI guidance](CLAUDE.md#ci) for selection, caching, and required checks.

## Deprecated Tools

<details>
<summary>Unmaintained tools and their final versions</summary>

These tools are no longer maintained, but their final versions remain available in the repository history.

| Tool                  | Last commit                                                                                                                  | Reason                                                                               |
| --------------------- | ---------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------ |
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
