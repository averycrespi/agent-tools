# MCP Gateway

MCP Gateway is a locally secure, deny-by-default service for governing access to MCP servers. It runs on an exact numeric loopback address, keeps administrator and agent credentials separate, applies principal-specific grants before tool execution, and retains bounded redacted invocation evidence.

Gateway is installed and operated independently beside MCP Broker. It does not read or migrate Broker configuration or state.

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

## Current capabilities

Gateway provides:

- strict local HTTP control and modern/legacy MCP ingress;
- durable server configuration, credential and OAuth authority, runtime supervision, and active catalog publication;
- permanent principals, one current agent credential per principal, immutable grants, and self-service grant requests;
- governed tool calls with at most one automatic attempt and bounded redacted invocation evidence;
- an embedded browser application and a matching public-HTTP administration CLI;
- verified backups, stopped-process restore and recovery, and ordered shutdown.

The [DESIGN](DESIGN.md) overview and its linked domain chapters are the source of truth for architecture, behavior, limits, failure semantics, and security decisions.

## Common workflows

Generated `mcp-gateway --help` and subcommand help are the exact command reference.

- Resolve local paths, authenticate the CLI, select output, and inspect status with [Administrator CLI and local administration](docs/operators/administration.md).
- Register an upstream, supply credentials, complete OAuth, and inspect catalogs with [Upstream server configuration](docs/operators/upstream-servers.md).
- Create principals, issue agent credentials, and manage grants or requests with [Access control](docs/operators/access-control.md).
- Investigate redacted call history and uncertain handoff with [Invocation evidence and unknown outcomes](docs/operators/invocation-evidence.md).
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
make test
make verify
npm run ui:typecheck
npm run ui:build
```

Use the [frontend development guide](docs/maintainers/frontend-development.md) for the separate trusted live-reload process and production asset boundary. Use the [release verification guide](docs/maintainers/release-verification.md) for release evidence and failure discipline.

## Coexistence with MCP Broker

MCP Gateway and MCP Broker are independent tools. Use distinct listen authorities and data directories. Installing or starting Gateway does not alter Broker configuration, role tokens, sessions, audit records, or behavior.
