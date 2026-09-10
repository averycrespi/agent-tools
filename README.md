# Agent Tools

[![CI](https://github.com/averycrespi/agent-tools/actions/workflows/ci.yml/badge.svg)](https://github.com/averycrespi/agent-tools/actions/workflows/ci.yml)

My tools for working with AI coding agents. Pairs well with my [agent-config](https://github.com/averycrespi/agent-config).

This repo is opinionated. It provides sandboxed execution and broker-backed external access that make coding agents safer and easier to run day to day. Use it as-is, fork it, or cherry-pick the tools that fit your setup.

## Overview

- **[Sandbox Manager](#sandbox-manager-sb)** — Manage a Lima VM sandbox for isolated agent environments
- **[MCP Broker](#mcp-broker)** — Proxy that lets sandboxed agents use external tools without holding secrets
- **[MCP Gateway](#mcp-gateway)** — Agent-first MCP access with scoped permissions, host-held credentials, and browser-based administration
- **[HTTP Broker](#http-broker)** — MITM HTTP/HTTPS forward proxy that injects credentials for sandboxed agents
- **[Local Git MCP](#local-git-mcp)** — Stdio MCP server for authenticated git remote operations
- **[Local Gomod Proxy](#local-gomod-proxy)** — Host-side Go module proxy for sandboxed agents

## How the Tools Fit Together

![Diagram showing how the tools connect to each other](assets/tool-relationships.svg)

### MCP Broker or MCP Gateway?

Both keep upstream credentials outside the sandbox and control access to MCP tools, but they use different permission models:

- **MCP Broker** uses rules to allow, deny, or send individual tool calls for human approval.
- **MCP Gateway** uses per-agent identities and scoped grants. Agents can request additional permissions; approval changes access rather than queuing a tool call.

Choose the model that fits your workflow. They are independent services with separate configuration and state; Gateway does not migrate Broker settings.

## Tools

### Sandbox Manager (sb)

Running AI agents with full host access is risky — one bad command can trash your environment. Containers help, but they're optimized for application isolation, not interactive development. What you want is a full VM that feels like a real development machine, is cheap to create and destroy, and can be provisioned to match your workflow.

`sb` wraps Lima to manage a lightweight Linux VM on macOS:

- `sb create` spins up a provisioned Ubuntu VM with a host-matching UID, writable mounts, and any tools your provisioning scripts install.
- `sb shell` drops you in.
- `sb provision` re-provisions a running VM.
- `sb destroy` tears it down.

The sandbox protects host integrity and credential custody; it is not a data-loss-prevention boundary. Guest network egress is intentionally allowed by default, so keep secrets and sensitive private data out of the VM unless you accept that the agent can transmit them.

See the [sandbox-manager README](sandbox-manager/README.md) for more information.

### MCP Broker

AI agents need to call external APIs (GitHub, Jira, Slack), but giving a sandboxed agent credentials or direct MCP access defeats the point of the sandbox. What you want is a single broker that holds the credentials, enforces policy on every tool call, and gives you a place to see and approve what the agent is doing.

`mcp-broker` runs on the host, holds the secrets, and exposes backend MCP servers through a single endpoint:

- The user connects their individual MCP servers to the MCP Broker.
- Agents connect to the broker as their only MCP server, without receiving upstream service credentials.
- Rules control which MCP tools are auto-allowed, auto-denied, or sent for human approval.
- Tool calls are recorded in a searchable SQLite audit log.
- A web dashboard handles approval requests in real time and surfaces the configured rules, discovered tools, and searchable audit log.

See the [mcp-broker README](mcp-broker/README.md) for more information.

### MCP Gateway

Coding agents need external tools, but they shouldn't need your API keys or unrestricted access to every connected service. What you want is one place to connect MCP servers, decide what each agent can do, and see what happened.

`mcp-gateway` runs locally and exposes your MCP servers through a single controlled endpoint:

- **Agent-first** — Agents discover tools, inspect their access, and request additional permissions through MCP.
- **Credentials stay outside the sandbox** — Gateway manages upstream credentials and OAuth; agents receive a separate Gateway credential, not your service secrets.
- **Scoped by default** — Access is denied unless granted. Give each agent permissions for specific servers, tools, or matching tool arguments, with optional expiry.
- **Sandbox-agnostic** — No dependency on Lima, containers, or a particular agent harness. Connect local clients directly or sandboxed clients through trusted local forwarding.
- **Operator-friendly** — Manage servers, agents, grants, and access requests through an embedded web application or CLI, with redacted invocation history and control-plane audit records.

See the [mcp-gateway README](mcp-gateway/README.md) for more information.

### HTTP Broker

MCP Broker and MCP Gateway keep upstream credentials out of the sandbox for MCP tool calls. An agent that reaches for `curl`, an SDK, or any ordinary HTTP client is back to holding its own.

`http-broker` applies the same premise to raw HTTP. It is a host-native forward proxy that decides per connection whether to intercept, tunnel, or deny, injects credentials the sandbox never holds, and records every request to an audit log surfaced through a read-only dashboard.

```json
{
  "name": "github-issues",
  "host": "api.github.com",
  "path": "/repos/*/*/issues",
  "mode": "intercept",
  "inject": { "set": { "Authorization": "Bearer ${cred.gh_bot}" } }
}
```

Every credential carries bound hosts, so a rule-authoring slip cannot send a token somewhere it does not belong.

Enforcement is **cooperative** — it rests on the sandbox honouring `HTTP_PROXY`/`HTTPS_PROXY`, so it is not a containment boundary. See the [http-broker README](http-broker/README.md) and its [security model](http-broker/docs/security-model.md) for what it does and does not guarantee.

### Local Git MCP

Sandboxed agents can do most git operations locally — staging, committing, diffing, rebasing — because those don't need authentication. But pushing, pulling, and fetching require credentials that the sandbox intentionally doesn't have. What you want is a host-side helper that performs just the credentialed operations on the agent's behalf, without ever exposing your SSH keys or credential store to the sandbox.

`local-git-mcp` is a stdio MCP server that runs on the host and shells out to the user's existing `git` setup:

- Six tools — `push`, `pull`, `fetch`, `clone_github_repo`, `list_remote_refs`, and `list_remotes` — cover every remote operation an agent typically needs.
- Uses the host's existing SSH keys and credential helpers; no tokens or keys ever cross into the sandbox.
- Runs as a stdio backend behind MCP Broker or MCP Gateway, so remote operations go through the chosen service's access controls and invocation history.
- No config, no state, no network listener — spawned as a subprocess over stdio.

See the [local-git-mcp README](local-git-mcp/README.md) for more information.

### Local Gomod Proxy

Sandboxed agents often work in Go projects that depend on private modules hosted in private GitHub repositories. On the host, those dependencies resolve transparently via the user's git credentials. Inside the sandbox, those credentials are intentionally absent — so `go mod download` fails for any private dependency.

`local-gomod-proxy` is a minimal HTTP Go module proxy that runs on the host and bridges the gap:

- Public modules are reverse-proxied to `proxy.golang.org`.
- Private modules (matched by `GOPRIVATE`) are fetched via `go mod download` on the host, inheriting its git credentials, and streamed back to the sandbox.
- Git credentials stay on the host; the sandbox reaches the proxy over Lima's host-local bridge and carries none.

See the [local-gomod-proxy README](local-gomod-proxy/README.md) for more information.

## Installation

Requirements:

- Go 1.25.13 or later and GNU Make
- macOS and Lima for Sandbox Manager (`brew bundle` installs Lima from the repository root)
- A supported operating-system keyring for MCP Gateway server credentials

From the repository root, install all tools:

```bash
make install
```

Or install only the tools you need:

```bash
make -C sandbox-manager install
make -C mcp-broker install
make -C mcp-gateway install
make -C http-broker install
make -C local-git-mcp install
make -C local-gomod-proxy install
```

Each tool's README covers its configuration and runtime requirements.

## Development

In addition to the installation requirements, development uses Node.js/npm for hooks and formatting, and Python 3 for CI selection and gate tests.

```bash
npm install  # install development dependencies and Git hooks
make build  # build all Go tools
make check  # check CI selection, formatting, lint, and ordinary tool correctness
```

On macOS, `make setup` combines Homebrew dependencies, development dependencies, and installation of all tools.

GitHub Actions checks affected tools on pull requests and all tools on `main`, manual runs, and a weekly schedule. See the [contributor guidance](CLAUDE.md#development) for test ownership and focused checks, and [CI guidance](CLAUDE.md#ci) for selection, caching, and required checks.

## Deprecated Tools

<details>
<summary>Unmaintained tools and their final versions</summary>

These tools are no longer maintained, but their final versions remain available in the repository history.

| Tool                  | Last commit                                                                                                                  | Reason                                                               |
| --------------------- | ---------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------- |
| `worktree-manager`    | [`20b0fb924c`](https://github.com/averycrespi/agent-tools/tree/20b0fb924c97b2058e181ce08f721214bbf80e5c/worktree-manager)    | Deprecated in favor of Herdr for workspace and worktree management.  |
| `worktree-sync`       | [`20b0fb924c`](https://github.com/averycrespi/agent-tools/tree/20b0fb924c97b2058e181ce08f721214bbf80e5c/worktree-sync)       | Deprecated in favor of Herdr for workspace and worktree management.  |
| `pi-session-analyzer` | [`7f52e38085`](https://github.com/averycrespi/agent-tools/tree/7f52e380857a435b25ba85a6c3c7e8865e04cd1d/pi-session-analyzer) | Built as an experiment and not carried forward.                      |
| `pi-dispatcher`       | [`d1f7ae3da4`](https://github.com/averycrespi/agent-tools/tree/d1f7ae3da4aa70616ee2ee6161eaf22e81cd4c51/pi-dispatcher)       | Replaced by the scheduled-tasks Pi extension.                        |
| `pi-orchestrator`     | [`3e799fa7c1`](https://github.com/averycrespi/agent-tools/tree/3e799fa7c1b568f8d5abe1faf9335f7ba18ad0b1/pi-orchestrator)     | Replaced by the scheduled-tasks Pi extension.                        |
| `telegram-mcp`        | [`3d9dc4338b`](https://github.com/averycrespi/agent-tools/tree/3d9dc4338b27123184783c80ada6a6aa5e5b7f0f/telegram-mcp)        | Retired; the standalone notification server is no longer maintained. |
| `agent-mailbox`       | [`4378f6ef71`](https://github.com/averycrespi/agent-tools/tree/4378f6ef71ea25961b3bb8e08053dfc8ff0302eb/agent-mailbox)       | Replaced by `telegram-mcp`.                                          |
| `local-gh-mcp`        | [`1f7cfd126f`](https://github.com/averycrespi/agent-tools/tree/1f7cfd126fe10f5f3107db771a06450c2adc0d92/local-gh-mcp)        | Deprecated in favor of the official GitHub MCP server.               |
| `broker-cli`          | [`0251368f3b`](https://github.com/averycrespi/agent-tools/tree/0251368f3b209242d6edcc7b916f476f810cb584/broker-cli)          | Replaced by the `mcp-broker` Pi extension.                           |
| `hindsight`           | [`164ffccbc0`](https://github.com/averycrespi/agent-tools/tree/164ffccbc010cc41c0a1330f8f1a5570ae61199f/hindsight)           | An experimental memory solution that was ultimately abandoned.       |

</details>

## Related

- [agent-config](https://github.com/averycrespi/agent-config) — My configuration for working with AI coding agents

## License

MIT
