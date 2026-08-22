# MCP Gateway

MCP Gateway is a locally secure, deny-by-default service foundation for governing MCP access. It is developed beside MCP Broker as an independent Go module and clean-start successor; it does not change MCP Broker behavior or migrate its state.

## Current status

The current executable implements the S1 filesystem and SQLite foundation, stopped-process verification, and offline admin initialization/reset. It does not open a listener or implement online credential/session APIs, downstream servers, principals, grants, tool routing, invocation, or product UI workflows. Those capabilities must not be inferred from the offline foundation.

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

## Offline admin authority

Initialize a new installation and publish its one-time admin bearer to a new owner-only file:

```bash
mcp-gateway initialize --data-dir /path/to/mcp-gateway-data --secret-output /safe/new/bearer-file
```

Omit `--secret-output` only in an interactive terminal; the bearer then goes to the controlling terminal, never standard output or standard error. The output file is created exclusively at mode `0600` and contains exactly the bearer and one newline. The command publishes the bearer before activating its verifier, stores only a domain-separated verifier and safe fingerprint, and emits one safe JSON result on standard output.

Rotate all admin authority while the Gateway is stopped:

```bash
mcp-gateway admin-reset --data-dir /path/to/mcp-gateway-data --secret-output /safe/new/replacement-file
```

A successful reset revokes every prior admin bearer and activates the published replacement in one storage transaction. A failed secret publication activates nothing; existing known authority remains valid.

## Offline storage verification

Run `restore --verify-current` only after stopping every Gateway process that owns the installation:

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

The command requires an owner-only installation, acquires its exclusive process lock, verifies the Gateway identity, current schema and migration history, configured SQLite durability and size bounds, and full database integrity. Only then does it durably clear an armed or malformed mutation marker. A normal startup is still required after verification; `serve` arrives with its owning S1 milestone and is not implemented yet.

Failures emit one safe JSON line and exit with status 1. `gateway_running` means another process owns the installation; `secret_output_unavailable` means the one-time sink could not be completed; and storage failures intentionally hide filesystem and SQLite details.

## Security boundary

The completed S1 service is designed to bind one exact numeric IPv4 loopback authority, defaulting to `127.0.0.1:8210`, and to reject all unrecognized paths, methods, credential domains, forwarding headers, and protocol claims. Raw authority must never be stored in configuration, URLs, logs, browser storage, SQLite, backups, or read APIs.

Loopback limits network reachability; it does not isolate processes running as the same operating-system user. Treat untrusted same-user processes as able to attempt connections to the Gateway.

The executable remains deny-by-default while the control, storage, keyring, HTTP, and MCP boundaries are added and verified in later S1 milestones. See [DESIGN.md](DESIGN.md) for the intended system contract and [CLAUDE.md](CLAUDE.md) for development conventions.
