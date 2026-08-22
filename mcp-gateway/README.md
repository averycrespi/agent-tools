# MCP Gateway

MCP Gateway is a locally secure, deny-by-default service foundation for governing MCP access. It is developed beside MCP Broker as an independent Go module and clean-start successor; it does not change MCP Broker behavior or migrate its state.

## Current status

The current executable is an inert S1 foundation. It exposes CLI help but does not open a listener, create authority, or implement downstream servers, principals, grants, tool routing, invocation, or product UI workflows. Those capabilities must not be inferred from the scaffold.

## Development

Requirements: Go 1.25.13 or later, GNU Make, and the repository's development tools.

```bash
make build
make test
make lint
make audit
```

From the repository root, `make build`, `make test`, and `make check` include MCP Gateway.

Install the binary with:

```bash
make install
mcp-gateway --help
```

## Security boundary

The completed S1 service is designed to bind one exact numeric IPv4 loopback authority, defaulting to `127.0.0.1:8210`, and to reject all unrecognized paths, methods, credential domains, forwarding headers, and protocol claims. Raw authority must never be stored in configuration, URLs, logs, browser storage, SQLite, backups, or read APIs.

Loopback limits network reachability; it does not isolate processes running as the same operating-system user. Treat untrusted same-user processes as able to attempt connections to the Gateway.

The executable remains deny-by-default while the control, storage, keyring, HTTP, and MCP boundaries are added and verified in later S1 milestones. See [DESIGN.md](DESIGN.md) for the intended system contract and [CLAUDE.md](CLAUDE.md) for development conventions.
