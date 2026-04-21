# Agent Tools

A collection of tools that reduce the friction of working with AI coding agents.

## Overview

- **[Worktree Manager](#worktree-manager-wt)** — Manage git worktrees with tmux integration
- **[Sandbox Manager](#sandbox-manager-sb)** — Manage a Lima VM sandbox for isolated agent environments
- **[MCP Broker](#mcp-broker)** — Proxy that lets sandboxed agents use external tools without holding secrets
- **[Broker CLI](#broker-cli)** — CLI frontend for the MCP broker
- **[Local Git MCP](#local-git-mcp)** — Stdio MCP server for authenticated git remote operations
- **[Local GH MCP](#local-gh-mcp)** — Stdio MCP server for GitHub operations via the gh CLI
- **[Agent Gateway](#agent-gateway)** — Host-native HTTP/HTTPS proxy that injects credentials into sandboxed agent traffic
- **[Local Gomod Proxy](#local-gomod-proxy)** — Host-side Go module proxy for sandboxed agents

## Getting Started

Requirements:

- Go 1.25+
- GNU Make
- macOS for `sandbox-manager` (requires Lima)

```bash
# Install dependencies on macOS
# For Linux: install `tmux` from your preferred package manager
brew bundle

# Install all tools
make install

# Or, to install individual tools
cd worktree-manager && make install
cd sandbox-manager && make install
cd mcp-broker && make install
cd broker-cli && make install
cd local-git-mcp && make install
cd local-gh-mcp && make install
cd agent-gateway && make install
cd local-gomod-proxy && make install
```

## Tools

### Worktree Manager (wt)

Running multiple AI agents across different branches means a lot of repetitive setup: create a worktree, open a tmux window, copy config files, launch the agent. Tear it all down when you're done. Multiply by several concurrent tasks and it's a lot of ceremony.

`wt` simplifies that flow to a pair of commands:

- `wt add <branch>` spins up a fully configured worktree — tmux window, config files copied, agent launched.
- `wt rm <branch>` tears it down, optionally deleting the branch as well.

See the [worktree-manager README](worktree-manager/README.md) for more information.

### Sandbox Manager (sb)

Running AI agents with full host access is risky — one bad command can trash your environment. Containers help, but they're optimized for application isolation, not interactive development. What you want is a full VM that feels like a real development machine, is cheap to create and destroy, and can be provisioned to match your workflow.

`sb` wraps Lima to manage a lightweight Linux VM on macOS:

- `sb create` spins up a provisioned Ubuntu VM with matching UID/GID, writable mounts, and any tools your provisioning scripts install.
- `sb shell` drops you in.
- `sb provision` re-provisions a running VM.
- `sb destroy` tears it down.

See the [sandbox-manager README](sandbox-manager/README.md) for more information.

### MCP Broker

AI agents need to call external APIs (GitHub, Jira, Slack), but giving a sandboxed agent credentials or direct MCP access defeats the point of the sandbox. What you want is a single broker that holds the credentials, enforces policy on every tool call, and gives you a place to see and approve what the agent is doing.

`mcp-broker` runs on the host, holds the secrets, and exposes backend MCP servers through a single endpoint:

- The user connects their individual MCP servers to the MCP Broker.
- Agents connect to the broker as their only MCP server, with no secrets exposed to the agent.
- Rules control which MCP tools are auto-allowed, auto-denied, or sent for human approval.
- Every tool call is audit-logged in SQLite for maximum observability.
- A web dashboard handles approval requests in real time and surfaces the configured rules, discovered tools, and searchable audit log.

See the [mcp-broker README](mcp-broker/README.md) for more information, or the [architecture diagram](mcp-broker/ARCHITECTURE.md) for a visual overview of the request flow.

### Broker CLI

Some agents speak MCP natively, but others work better by running shell commands — and writing a wrapper per tool means keeping a second set of stubs in sync with whatever the broker currently exposes. What you want is a CLI that mirrors the broker's tool list automatically, with typed flags and predictable JSON output.

`broker-cli` connects to the MCP broker, discovers available tools at startup, and builds the full command tree on the fly:

- One subcommand per tool, grouped by namespace (e.g. `broker-cli git push --remote origin`).
- Typed flags generated from each tool's JSON Schema, with `--raw-field` and `--raw-input` escape hatches for complex inputs.
- Output is always a JSON array on stdout; errors are a JSON object on stderr — easy to pipe into `jq`.
- Tool list is cached to keep repeated calls fast.

See the [broker-cli README](broker-cli/README.md) for more information.

### Local Git MCP

Sandboxed agents can do most git operations locally — staging, committing, diffing, rebasing — because those don't need authentication. But pushing, pulling, and fetching require credentials that the sandbox intentionally doesn't have. What you want is a host-side helper that performs just the credentialed operations on the agent's behalf, without ever exposing your SSH keys or credential store to the sandbox.

`local-git-mcp` is a stdio MCP server that runs on the host and shells out to the user's existing `git` setup:

- Five tools — `git_push`, `git_pull`, `git_fetch`, `git_list_remote_refs`, and `git_list_remotes` — cover every remote operation an agent typically needs.
- Uses the host's existing SSH keys and credential helpers; no tokens or keys ever cross into the sandbox.
- Designed to sit behind `mcp-broker`, so the broker's rules and audit log apply to every push and pull.
- No config, no state, no network listener — spawned as a subprocess over stdio.

See the [local-git-mcp README](local-git-mcp/README.md) for more information.

### Local GH MCP

Sandboxed agents need to interact with GitHub — opening PRs, reading issues, checking CI, debugging workflow failures — but giving them credentials defeats the purpose of sandboxing. There's an official GitHub MCP server, but it requires OAuth (with a GitHub App) or a powerful personal access token. Meanwhile, the host machine already has `gh` authenticated. What you want is a host-side MCP server that reuses that existing `gh` login instead of demanding its own secret.

`local-gh-mcp` is a stdio MCP server that runs on the host and shells out to the `gh` CLI:

- Covers PRs, issues, workflow runs, Actions caches, and search across repos — over two dozen tools in all.
- Uses the host's existing `gh auth login`; no tokens or OAuth flow inside the sandbox.
- Read tools return structured Markdown (not raw JSON) with authors flattened to `@login` and long bodies truncated, which is a better fit for LLM consumption.
- Designed to sit behind `mcp-broker`, so the broker's rules and audit log apply to every GitHub call.

See the [local-gh-mcp README](local-gh-mcp/README.md) for more information.

### Agent Gateway

Sandboxed agents need to call external APIs (GitHub, npm, LLM providers), but giving them real credentials defeats the purpose of sandboxing. What you want is a transparent proxy that holds credentials on the host, injects them into matching requests, and blocks or holds anything suspicious for human review — without the agent ever seeing the real token.

`agent-gateway` runs on the host and intercepts all HTTPS traffic from the sandbox via `HTTPS_PROXY`:

- Sandboxes receive dummy credentials and point `HTTPS_PROXY` at the gateway; real credentials are never inside the sandbox.
- HCL rules match on host, method, path, headers, and body fields; matched requests get credentials injected at the header level.
- Three verdicts: auto-allow (with injection), auto-deny, or hold for human approval via an embedded web dashboard.
- Every intercepted request is audit-logged in SQLite — matched rule, agent identity, verdict, and which credential (by name) was used.
- MITM TLS via a local root CA; HTTP/2 supported end-to-end.

See the [agent-gateway README](agent-gateway/README.md) for more information.

### Local Gomod Proxy

Sandboxed agents often work in Go projects that depend on private modules hosted in private GitHub repositories. On the host, those dependencies resolve transparently via the user's git credentials. Inside the sandbox, those credentials are intentionally absent — so `go mod download` fails for any private dependency.

`local-gomod-proxy` is a minimal HTTP Go module proxy that runs on the host and bridges the gap:

- Public modules are reverse-proxied to `proxy.golang.org` with zero host CPU overhead.
- Private modules (matched by `GOPRIVATE`) are fetched via `go mod download` on the host, inheriting its git credentials, and streamed back to the sandbox.
- Git credentials stay on the host; the sandbox reaches the proxy over Lima's host-local bridge and carries none.

See the [local-gomod-proxy README](local-gomod-proxy/README.md) for more information.

## Related

- [agent-config](https://github.com/averycrespi/agent-config) — My configuration for working with AI coding agents

## License

MIT
