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
- `mcp-gateway admin --help`
- `mcp-gateway admin credential --help`
- `mcp-gateway admin reset --help`

## Start and inspect Gateway

The default service authority is `http://127.0.0.1:8210`:

```bash
mcp-gateway initialize
mcp-gateway serve
# In another terminal:
mcp-gateway status
```

`serve --listen` accepts only a canonical numeric IPv4 loopback address and explicit port. Online `--address` accepts only a canonical numeric `127/8` HTTP URL. Hostnames, wildcard and non-loopback addresses, forwarding headers, alternate Host forms, redirects, proxies, cookies, compression, and automatic transport retries are not accepted. When a selected loopback address refuses the connection, every online leaf reports `gateway_not_running` and renders the exact `mcp-gateway serve` command for the selected address and explicit data directory. Start that service before retrying the online command.

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

Lists return one page and use command-scoped `--limit`, `--cursor`, and filter flags. When another page exists, human output ends with `NEXT_CURSOR`; JSON retains the exact `next_cursor` member. Supply that cursor explicitly for the next page. Closed JSON requests reject duplicate, unknown, missing, or trailing values. Command input is intentionally split:

| Input mode                                  | Commands                                                                                                            |
| ------------------------------------------- | ------------------------------------------------------------------------------------------------------------------- |
| Direct flags only                           | `admin credential create`, `principal create`, `principal update`, `server operation start`, `grant-request reject` |
| Strict `--file` only                        | `server create`, `server credential replace`                                                                        |
| Direct flags or strict `--file`, never both | `server update`, `grant create`, `grant-request approve`                                                            |

Use `--file PATH` or `--file -` for the strict file form. `--file -` conflicts with `--admin-bearer-stdin`. Constrained grant and approval shapes require the file form; their direct forms cover the ordinary unconstrained case. Credential issue, rotate, and revoke commands have no request-document input.

## Confirmations and one-time values

Commands with irreversible or authority-changing consequences require a controlling terminal confirmation unless `--yes` is explicitly supported and selected. Confirmation never implies automatic replay.

Administrator credential creation and agent credential issue or rotation can publish a one-time bearer to a prepared controlling terminal or a newly created non-symlink `0600` owner-only `--secret-output` file. Administrator rotation requires a fresh owner-only `--secret-output` file so it can durably reopen and authenticate the replacement before revocation. `principal credential issue` accepts only an empty credential slot; `principal credential rotate` accepts only an occupied slot and atomically invalidates the prior agent authority without overlap. Normal stdout contains only safe metadata and guidance; JSON contains only metadata. Neither contains the bearer. Server credential input is write-only. OAuth authorization URLs are shown once through a prepared terminal and may be opened only by explicit request. Metadata reads cannot recover a lost bearer, submitted secret, or authorization URL.

A bearer-sink preparation failure occurs before credential mutation submission: choose a new output path, or a controlling terminal where supported, and submit deliberately. A failure after Gateway acknowledges credential creation is different: the credential may be active while its bearer is permanently lost. Read current metadata and explicitly rotate or revoke; never replay the original mutation merely because the one-time value was not published.

Do not put secrets in shell arguments, environment variables, ordinary output capture, or reusable files. A copied browser value remains in the operating-system clipboard until overwritten.

## Retry discipline

The CLI never retries automatically. Reads that fail before request handoff are safe to repeat after checking Gateway availability. Mutations report whether failure occurred before handoff or may be uncertain; use the resource-specific read and idempotency guidance before an explicit retry.

An exact idempotency key and canonical input digest may permit deliberate same-intent replay for commands that advertise that contract. For an ordinary ETag-capable mutation, omitting `--etag` performs one authenticated item read, validates the returned identity/revision/header ETag, and uses that value once. Supplying `--etag` pins the explicit validated value and skips that convenience read. Agent credential issue and rotate always read the principal once to enforce empty-versus-occupied slot intent; an explicit ETag must match that observed item. Neither mode refreshes a stale precondition or replays after conflict or uncertainty. Lost one-time output is not a reason to replay a credential or OAuth operation.

## Administrator rotation and migration

Use the online routine rotation command while Gateway is running:

```bash
mcp-gateway admin credential rotate OLD_CREDENTIAL_ID \
  --secret-output /safe/new/admin-bearer \
  --yes
mcp-gateway status --admin-bearer-file /safe/new/admin-bearer
```

Rotation conditionally creates one non-expiring replacement, durably publishes and securely reopens the file, verifies its metadata and authentication, and only then conditionally revokes the named old credential. It never promotes the replacement into the default bearer path. If completion is uncertain, do not replay: retain the replacement file and use the rendered metadata command to inspect the old and new records. Before replacement verification, workflow-owned failures preserve old authority; after verified publication, an incomplete workflow may intentionally leave both credentials active.

If durable publication itself fails after creation, the output path may contain unverified secret material but must not be trusted or used as credential input; secure or remove it. This workflow does not revoke the old credential, but expiration or concurrent administrator action may still make it unusable. An active replacement record without a durably verified bearer may also exist. If the pre-rotation credential remains active, use it to inspect metadata; otherwise use another active administrator credential. Explicitly revoke an unusable replacement if present, and perform any later rotation as a fresh deliberate operation.

Use stopped-process `mcp-gateway admin reset` only for all-authority recovery. The command tree migrated immediately: `admin credential ...` and `admin reset` are the only administrator spellings, and the legacy hyphenated forms perform no work and have no aliases.

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
mcp-gateway admin credential --help
mcp-gateway admin reset --help
```

Focused workflow ownership:

- [Server configuration and credentials](server-configuration.md)
- [Principals, grants, and requests](access-policy.md)
- [Invocation evidence and unknown outcomes](invocation-evidence.md)
- [Backup, restore, and recovery](recovery.md)

Consult [DESIGN](../DESIGN.md) for normative trust boundaries, state transitions, limits, and failure semantics. Return to the [Gateway README](../README.md) for installation and common workflows.
