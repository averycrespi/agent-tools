# CLI and local administration

Audience: Gateway operators and automation authors

Purpose: Run local administration safely through the public CLI.

Generated `mcp-gateway --help` and subcommand help are the canonical command and flag reference. This guide owns local defaults, administrator authentication, output modes, and safe command execution.

## Installation root

`--data-dir` has highest precedence. Without it, Gateway uses `$XDG_DATA_HOME/mcp-gateway` when `XDG_DATA_HOME` is an absolute path. Otherwise it resolves the operating-system account home and uses `~/.local/share/mcp-gateway`. A relative XDG value is rejected, and the `$HOME` environment variable is not an authority source.

Use the same data directory for initialization, service startup, stopped-process recovery, and online commands:

```bash
mcp-gateway initialize --data-dir /path/to/gateway-data
mcp-gateway serve --data-dir /path/to/gateway-data
mcp-gateway --data-dir /path/to/gateway-data status
```

The zero-argument installation uses the default root and stores its administrator bearer at `<effective-data-dir>/admin-bearer`. `initialize` creates owner-only paths, never overwrites an existing secret output, and prints safe next steps without printing the bearer.

Exact syntax and defaults:

- `mcp-gateway initialize --help`
- `mcp-gateway serve --help`
- `mcp-gateway status --help`
- `mcp-gateway admin-credential --help`

## Start and inspect Gateway

The default service authority is `http://127.0.0.1:8210`:

```bash
mcp-gateway initialize
mcp-gateway serve
# In another terminal:
mcp-gateway status
```

`serve --listen` accepts only a canonical numeric IPv4 loopback address and explicit port. Online `--address` accepts only a canonical numeric `127/8` HTTP URL. Hostnames, wildcard and non-loopback addresses, forwarding headers, alternate Host forms, redirects, proxies, cookies, compression, and automatic transport retries are not accepted.

`GET /livez` is unauthenticated process liveness. `GET /readyz` reports only ready or not ready. Detailed `status` requires administrator authentication.

## Administrator authentication

Online administrator authentication never prompts. It resolves exactly one bearer source:

1. `--admin-bearer-file PATH` selects an explicit owner-readable file.
2. `--admin-bearer-stdin` reads exclusively from standard input.
3. With neither flag, Gateway reads `<effective-data-dir>/admin-bearer`.

The explicit file and stdin selectors conflict. Administrator bearers are never accepted in argv or environment variables. `--data-dir` selects the default credential location; it does not grant private database or keyring access to online commands. Online commands use only the public loopback HTTP API.

For a replacement bearer created by reset or restore, select it explicitly:

```bash
mcp-gateway --data-dir /path/to/gateway-data \
  status --admin-bearer-file /safe/new/admin-bearer
```

See [Backup, restore, and recovery](recovery.md) for replacement-authority workflows.

## Output and failures

Human output is the default. Use `--output json` or the `--json` shorthand for the exact JSON projection. Conflicting output selectors fail before work begins.

Finite successes write to stdout. Finite and pre-start failures leave stdout empty and write one bounded problem to stderr. Problems retain stable codes and typed exit classes so automation can distinguish invalid input, authentication, conflict, unavailable storage, and uncertain outcomes without parsing prose.

Lists return one page and use command-scoped `--limit`, `--cursor`, and filter flags. Supply the returned cursor explicitly for the next page. JSON input comes from one strict document selected with `--file PATH` or `--file -`; closed requests reject duplicate, unknown, missing, or trailing values.

## Confirmations and one-time values

Commands with irreversible or authority-changing consequences require a controlling terminal confirmation unless `--yes` is explicitly supported and selected. Confirmation never implies automatic replay.

Administrator and agent credential creation can publish a one-time bearer to a prepared controlling terminal or a newly created owner-only `--secret-output` file. Normal output contains metadata only. Server credential input is write-only. OAuth authorization URLs are shown once through a prepared terminal and may be opened only by explicit request. Metadata reads cannot recover a lost bearer, submitted secret, or authorization URL.

Do not put secrets in shell arguments, environment variables, ordinary output capture, or reusable files. A copied browser value remains in the operating-system clipboard until overwritten.

## Retry discipline

The CLI never retries automatically. Reads that fail before request handoff are safe to repeat after checking Gateway availability. Mutations report whether failure occurred before handoff or may be uncertain; use the resource-specific read and idempotency guidance before an explicit retry.

An exact idempotency key and canonical input digest may permit deliberate same-intent replay for commands that advertise that contract. ETag-protected updates require the exact loaded value and never silently refetch it. Lost one-time output is not a reason to replay a credential or OAuth operation.

For governed call evidence, `outcome_unknown` means the effect may already have happened. See [Invocation evidence and unknown outcomes](invocation-evidence.md). For backup and stopped-process failures, see [Backup, restore, and recovery](recovery.md).

## Command families

Use generated help rather than copying a full command inventory into documentation:

```bash
mcp-gateway --help
mcp-gateway server --help
mcp-gateway catalog --help
mcp-gateway principal --help
mcp-gateway grant --help
mcp-gateway grant-request --help
mcp-gateway invocation --help
mcp-gateway backup --help
mcp-gateway admin-credential --help
```

Focused workflow ownership:

- [Server configuration and credentials](server-configuration.md)
- [Principals, grants, and requests](access-policy.md)
- [Invocation evidence and unknown outcomes](invocation-evidence.md)
- [Backup, restore, and recovery](recovery.md)

Consult [DESIGN](../DESIGN.md) for normative trust boundaries, state transitions, limits, and failure semantics. Return to the [Gateway README](../README.md) for installation and common workflows.
