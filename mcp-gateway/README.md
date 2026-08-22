# MCP Gateway

MCP Gateway is a locally secure, deny-by-default service foundation for governing MCP access. It is developed beside MCP Broker as an independent Go module and clean-start successor; it does not change MCP Broker behavior or migrate its state.

## Current status

The current executable implements the S1 filesystem and SQLite foundation plus stopped-process verification. It does not open a listener, create admin authority, or implement downstream servers, principals, grants, tool routing, invocation, or product UI workflows. Those capabilities must not be inferred from the storage foundation.

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

## Offline storage verification

`restore --verify-current` is the only operational command currently exposed. Run it only after stopping every Gateway process that owns the installation:

```bash
mcp-gateway restore --verify-current --data-dir /path/to/mcp-gateway-data
```

On success it emits one JSON line:

```json
{
  "ok": true,
  "operation": "restore",
  "mode": "verify_current",
  "installation_id": "01ARZ3NDEKTSV4RRFFQ69G5FAV",
  "revision": "0"
}
```

The command requires an owner-only installation, acquires its exclusive process lock, verifies the Gateway identity, current schema and migration history, configured SQLite durability and size bounds, and full database integrity. Only then does it durably clear an armed or malformed mutation marker. A normal startup is still required after verification; the `serve` and `initialize` commands arrive with their owning S1 milestones and are not implemented yet.

Failures emit one safe JSON line and exit with status 1. `gateway_running` means another process owns the installation; `storage_unavailable` intentionally hides filesystem and SQLite details.

## Security boundary

The completed S1 service is designed to bind one exact numeric IPv4 loopback authority, defaulting to `127.0.0.1:8210`, and to reject all unrecognized paths, methods, credential domains, forwarding headers, and protocol claims. Raw authority must never be stored in configuration, URLs, logs, browser storage, SQLite, backups, or read APIs.

Loopback limits network reachability; it does not isolate processes running as the same operating-system user. Treat untrusted same-user processes as able to attempt connections to the Gateway.

The executable remains deny-by-default while the control, storage, keyring, HTTP, and MCP boundaries are added and verified in later S1 milestones. See [DESIGN.md](DESIGN.md) for the intended system contract and [CLAUDE.md](CLAUDE.md) for development conventions.
