# MCP Gateway

**Connect your agents to external tools without handing them your service credentials.**

MCP Gateway brings your MCP servers behind one local endpoint, with separate identities and scoped permissions for each agent. It holds upstream credentials, checks access before every tool call, and gives you a browser application and CLI to manage the system.

Agents get the tools they need. You keep control over what they can do.

## Why Gateway?

### Agent-first

Agents can discover available tools, inspect their permissions, and request additional access without leaving MCP. Discovery is configurable per agent, so you can expose only authorized tools or let agents find tools they may need to request.

Access requests change permissions; they do not queue tool calls. After approval, the agent makes a new call.

### Scoped access, not shared authority

Give each agent its own identity and credential instead of sharing one all-access token. Grant access to a whole server or a specific tool, narrow it with argument constraints, and set an expiry when access should be temporary.

Gateway denies calls by default and checks current policy before execution. Rotate or revoke an agent's access without distributing new upstream credentials.

### Service credentials stay with Gateway

Gateway manages upstream authentication, including server credentials and OAuth. Agents authenticate with a separate Gateway credential; they never need your upstream API keys or OAuth tokens.

Administrator and agent credentials are separate, and Gateway uses the operating-system keyring for server secrets rather than falling back to plaintext storage.

### Sandbox-agnostic

Use Gateway with your preferred MCP client and sandbox setup. It does not require Sandbox Manager, Lima, or a particular agent harness.

Gateway listens on loopback. Local clients connect directly; VMs and containers need a trusted local forwarding path. It is a local access-control service, not an internet-facing gateway or a replacement for sandbox isolation.

### Operator-friendly

Use the embedded browser application or CLI to configure servers, manage agent identities and grants, review access requests, and investigate calls. Invocation history provides bounded, redacted evidence; a separate audit history records control-plane changes.

The Grants table shows expiry and numeric constraint counts; Requests shows requested duration and constraint counts in separate columns. Both use **No expiry** for non-expiring access and **0** for no argument constraints. Cancelled requests use a neutral grey state label.

Backup, restore, and recovery procedures support ongoing operation—not just initial setup.

### Explicit about uncertain outcomes

Gateway never queues or automatically replays tool calls. If a handoff leaves the outcome unknown, it reports that uncertainty rather than retrying an operation that may already have taken effect.

## Installation

Requirements: Go 1.25.13 or later, GNU Make, and a supported operating-system keyring for server credentials.

From the `mcp-gateway` directory:

```bash
make install
```

The installed `mcp-gateway` binary owns its data directory, SQLite database, administrator authority, and local service lifecycle. See [Administrator CLI and local administration](docs/operators/administration.md) for installation paths and credential selection.

## Quick start

Initialize the default owner-only installation and start the loopback service:

```bash
mcp-gateway initialize
mcp-gateway serve
```

In another terminal, verify the running Gateway:

```bash
mcp-gateway status
```

`initialize` creates a new administrator bearer file and prints safe next steps, never the bearer value. `status` reads that default bearer and uses the public loopback control API. Open `http://127.0.0.1:8210/` to use the embedded administrator application.

For an interactive checkout-only sandbox, use `make -C mcp-gateway serve-demo` from the repository root. It provides local echo/arithmetic/document tools, sample agents and grants, a pending request, and real invocation history without the normal installation or native keyring. `DEMO_DATASET=empty` selects a fresh empty environment; `DEMO_LISTEN=127.0.0.1:PORT` overrides its default port 8211. See [frontend development](docs/maintainers/frontend-development.md#use-a-disposable-feature-branch-gateway) for protected credentials, separate Vite, requirements, and Ctrl-C cleanup. This is not an installed CLI command.

For trusted local VM/container forwarding, `mcp-gateway serve --allowed-host host.lima.internal` admits that exact hostname without changing the numeric-loopback listener or browser Origin policy. Online `--address http://host.lima.internal:8210` explicitly selects the forwarding destination; plain HTTP is not secure arbitrary-remote administration. Follow [sandbox administration](docs/operators/administration.md#trusted-local-forwarding-and-sandbox-administration) to provision and revoke a separate administrator credential. Removing a hostname is not credential revocation.

## Common workflows

Generated `mcp-gateway --help` and subcommand help are the exact command reference.

- Resolve local paths, authenticate the CLI, select output, and inspect status with [Administrator CLI and local administration](docs/operators/administration.md).
- Register an upstream, supply credentials, complete OAuth, and inspect catalogs with [Upstream server configuration](docs/operators/upstream-servers.md). For provider-specific callback URIs, authorization-server metadata URLs, and scopes, see [OAuth compatibility settings](docs/operators/upstream-servers.md#oauth-compatibility-settings).
- Create principals, issue agent credentials, and manage grants or requests with [Access control](docs/operators/access-control.md). For Pi in a Lima guest, follow [agent provisioning](docs/operators/access-control.md#provision-a-pi-agent-in-a-lima-sandbox).
- Investigate redacted call history and uncertain handoff with [Invocation evidence and unknown outcomes](docs/operators/invocation-evidence.md).
- Inspect control-plane history with `mcp-gateway audit list`, `audit get AUDIT_EVENT_ID`, or the browser's Audit destination. See [audit filters, retention, and restore continuity](docs/operators/administration.md#control-plane-audit-history).
- Create backups or perform stopped-process verification, restore, and administrator reset with [Backup, restore, and recovery](docs/operators/backup-and-recovery.md).

Routine administrator-key rollover is online and replacement-first. Follow the [administrator rotation procedure](docs/operators/administration.md#administrator-rotation-and-migration); use stopped-process reset only for all-authority recovery.

If an online command proves that the selected loopback Gateway is stopped, its error renders the matching `mcp-gateway serve` command, including nondefault address and data-directory selections. Gateway never automatically replays a mutation or governed tool call. Follow the command-specific read guidance before deciding whether an explicit retry is safe.

## Security

- Loopback limits network reachability but does not isolate untrusted processes running as the same operating-system user.
- Raw administrator, agent, server, and OAuth secrets must not be placed in arguments, environment variables, configuration, URLs, logs, SQLite, backups, browser storage, or read APIs.
- Gateway is deny by default: only a current credential for an active principal can discover tools, and a governed call requires a current policy `ALLOW` before one immediate attempt.
- One-time secrets and OAuth URLs use prepared terminal, owner-only file, browser display, clipboard, or opener sinks. Lost one-time values cannot be recovered from metadata.
- An `outcome_unknown` result means an effect may already have occurred; an explicit retry may duplicate it.
- Native keyring operations may prompt, fail, or outlive cancellation. Gateway never falls back to plaintext credential storage.

See the [DESIGN](DESIGN.md) overview for the trust-boundary map, [Invocation and MCP ingress](docs/design/invocation-and-ingress.md) for normative call semantics, and [Invocation evidence](docs/operators/invocation-evidence.md) for operator interpretation.

## Documentation

Use the [documentation map](docs/README.md) to choose material by role and task.

### Gateway administrators

- [Administrator CLI and local administration](docs/operators/administration.md)
- [Run as a macOS launchd agent](docs/operators/launchd.md)
- [Upstream server configuration](docs/operators/upstream-servers.md)
- [Access control](docs/operators/access-control.md)
- [Invocation evidence and unknown outcomes](docs/operators/invocation-evidence.md)
- [Backup, restore, and recovery](docs/operators/backup-and-recovery.md)

### Maintainers and coding agents

- [Maintainer and agent guidance](CLAUDE.md)
- [Frontend development](docs/maintainers/frontend-development.md)
- [Release verification and acceptance evidence](docs/maintainers/release-verification.md)

### System design

- [Normative architecture index](DESIGN.md)
- [Domain design chapters](docs/design/)

## Development

Maintainers should start with [CLAUDE.md](CLAUDE.md) for package ownership, editing invariants, and verification commands.

```bash
make build
make test-unit  # fast contract and algorithm feedback
make test       # disjoint unit, integration, harness, material, demo-runner coverage
make verify
npm run ui:typecheck
npm run ui:build
```

`make suite-inventory` reports test ownership and build-context applicability. Browser, E2E, security, stress, and native evidence remain explicit leaves rather than hidden work in the fast unit path. `make test-browser` batches its five disjoint leaves through one planner while retaining separate test processes and Gateway builds. Add `TEST_JSON=1` to Go suite targets for structured execution events without changing selection or instrumentation.

Use the [frontend development guide](docs/maintainers/frontend-development.md) for the separate trusted live-reload process and production asset boundary. Use the [release verification guide](docs/maintainers/release-verification.md) for release evidence and failure discipline.

## Coexistence with MCP Broker

MCP Gateway and MCP Broker are independent tools. Use distinct listen authorities and data directories. Installing or starting Gateway does not alter Broker configuration, role tokens, sessions, audit records, or behavior.
