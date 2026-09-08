# Administrator CLI and local administration

Audience: Gateway operators and automation authors

Purpose: Run local administration safely through the public CLI.

Generated `mcp-gateway --help` and subcommand help are the canonical command and flag reference. This guide owns operator procedures for installation roots, administrator authentication, output modes, and safe command execution. See [Administrative control plane](../design/administrative-control-plane.md) for normative defaults and trust boundaries.

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

`serve --listen` accepts only a canonical numeric IPv4 loopback address and explicit port. Online `--address` accepts a canonical numeric `127/8` HTTP URL or an explicitly trusted hostname HTTP URL with a canonical decimal port (1–65535). Wildcard and non-loopback numeric destinations, URL userinfo, paths (including a trailing slash), queries, fragments, forwarding headers, redirects, ambient proxies, cookies, compression, and automatic transport retries are not accepted. When a selected loopback address refuses the connection, every online leaf reports `gateway_not_running` and renders the exact `mcp-gateway serve` command for the selected address and explicit data directory. A hostname refusal instead directs you to check forwarding and the numeric-loopback service; a hostname is never a valid `--listen` value.

`GET /livez` is unauthenticated process liveness. `GET /readyz` reports only ready or not ready. Detailed `status` requires administrator authentication.

## Trusted local forwarding and sandbox administration

To reach the existing routes through a trusted VM/container forwarding hostname, explicitly allow that name on the host:

```bash
mcp-gateway serve --allowed-host host.lima.internal
# Repeat --allowed-host for another independently trusted hostname.
```

Names use ASCII DNS labels of 1–63 letters, digits, or hyphens, at most 253 characters total. Labels cannot start/end with a hyphen; the final label cannot be entirely numeric. Single-label names are accepted. URLs, IP literals, ports, wildcards, underscores, empty labels, trailing dots, Unicode, whitespace, and controls are rejected. Use an ASCII punycode spelling if needed. Matching folds ASCII DNS case only and is exact: neither subdomains nor lookalike suffixes inherit access. DNS resolution never grants Host trust. Requests may omit a port or carry any decimal port from 1–65535 (including leading zeros); the port is ignored only for an explicitly listed hostname. The canonical numeric listener Host still requires its exact port. Omitting the flag allows no extra names.

The listener remains numeric IPv4 loopback. Arrange forwarding separately; Gateway has no Lima defaults, trusted-proxy mode, or forwarding-header support. All existing routes retain their own credentials, roles, methods, and limits. OAuth callback construction remains independent. Browser Origin policy is unchanged and includes the listener port: an allowed hostname is not a trusted browser origin, and it does not enable browser sign-in or CORS. Continue using the canonical numeric origin for the browser application. Browsers can send the alias Origin even while loading module assets, so opening an allowed alias may produce a blank page rather than a sign-in screen.

Selecting an HTTP hostname destination is an explicit trust decision about resolution and the entire forwarding path. The CLI may connect to the VM's host-forwarding address rather than guest loopback; it does not infer that address from `--allowed-host`. Plain HTTP provides no remote confidentiality or server authentication. This is not secure arbitrary-remote administration; use only a trusted local forwarding path.

Provision a separate administrator credential on the host, not the main administrator bearer and not an agent credential:

```bash
mcp-gateway admin credential create --secret-output /safe/new/sandbox-admin
```

Retain the credential ID from the safe metadata output. Securely transfer only that owner-only bearer file into the sandbox, not Gateway's database or installation root. Then select the destination and credential explicitly inside the sandbox (use the port exposed by your forwarding setup):

```bash
mcp-gateway status --address http://host.lima.internal:8210 \
  --admin-bearer-file /safe/sandbox-admin
```

This credential grants full administrator authority; hostname allowlisting does not reduce its privileges. When access ends, revoke that specific credential using another active host administrator:

```bash
mcp-gateway admin credential revoke SANDBOX_CREDENTIAL_ID --yes
```

Also remove the `--allowed-host` entry and restart Gateway to block new requests through that name. Host removal is not credential revocation: the bearer remains usable through other accepted hosts until separately revoked or expired. Revocation blocks the bearer independently even while the hostname remains allowed. Securely remove the transferred file when no longer needed.

## Administrator authentication

Online administrator authentication never prompts. It resolves exactly one bearer source:

1. `--admin-bearer-file PATH` selects an explicit owner-readable file.
2. `--admin-bearer-stdin` reads exclusively from standard input.
3. With neither flag, Gateway reads `<effective-data-dir>/admin-bearer`.

The explicit file and stdin selectors conflict. Administrator bearers are never accepted in argv or environment variables. `--data-dir` selects the default credential location; it does not grant private database or keyring access to online commands. Online commands use only the public HTTP API at the explicitly selected destination.

For a replacement bearer created by reset or restore, select it explicitly:

```bash
mcp-gateway --data-dir /path/to/gateway-data \
  status --admin-bearer-file /safe/new/admin-bearer
```

See [Backup, restore, and recovery](backup-and-recovery.md) for replacement-authority workflows.

## Output and failures

Human output is the default. Use `--output json` or the `--json` shorthand for the exact JSON projection. Conflicting output selectors fail before work begins.

Finite successes write to stdout. Finite and pre-start failures leave stdout empty and write one bounded problem to stderr. Problems retain stable codes and typed exit classes so automation can distinguish invalid input, authentication, conflict, unavailable storage, and uncertain outcomes without parsing prose.

Lists return one page and use command-scoped `--limit`, `--cursor`, and filter flags. When another page exists, human output ends with `NEXT_CURSOR`; JSON retains the exact `next_cursor` member. Supply that cursor explicitly for the next page. Closed JSON requests reject duplicate, unknown, missing, or trailing values. Command input is intentionally split:

| Input mode                                  | Commands                                                                                                                            |
| ------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------- |
| Direct flags only                           | `admin credential create`, `principal create`, `principal update`, `server operation start`, `grant update`, `grant-request reject` |
| Strict `--file` only                        | `server create`, `server credential replace`                                                                                        |
| Direct flags or strict `--file`, never both | `server update`, `grant create`, `grant-request approve`                                                                            |

Use `--file PATH` or `--file -` for the strict file form. `--file -` conflicts with `--admin-bearer-stdin`. Constrained grant and approval shapes require the file form; their direct forms cover the ordinary unconstrained case. Strict files accept permanent v1 `{"equals":{...}}` constraints and closed v2 `{"version":2,"equals":{...},"regex":{...}}` constraints, validate them against the matcher compiler's grammar and limits, and preserve lexical number and regex bytes through submission. Table output identifies `v1 equals` or the v2 equality/regex atom counts. Both grant creation and approval forms accept an optional human-readable `description` (`--description` in direct mode). Grant descriptions are display metadata; `grant update` changes or clears only that metadata under an exact ETag. Credential issue, rotate, and revoke commands have no request-document input.

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

For governed call evidence, `outcome_unknown` means the effect may already have happened. See [Invocation evidence and unknown outcomes](invocation-evidence.md). For backup and stopped-process failures, see [Backup, restore, and recovery](backup-and-recovery.md).

## Control-plane audit history

Use `mcp-gateway audit list` and `mcp-gateway audit get AUDIT_EVENT_ID`, or choose **Audit** immediately above System in the browser sidebar. Both consume the authenticated read-only `GET /api/v1/audit-events` and `GET /api/v1/audit-events/{id}` API. Generated `mcp-gateway audit --help`, `mcp-gateway audit list --help`, and `mcp-gateway audit get --help` describe the command grammar. The [coverage matrix](../design/administrative-control-plane.md#control-plane-audit-coverage) identifies audited operator, system and offline actions and their regression evidence. Do not infer that an action never happened from an empty audit collection.

Collection responses return summaries, a next-page cursor, and `history` with `generation`, `oldest_retained`, and `pruned`. Only the newest 65,536 events are retained. Keep the generation separately from the oldest boundary; pruning advances the boundary within one generation, while a generation mismatch means histories must not be combined. After `stale_cursor`, discard the traversal, fetch a fresh first page, and compare its generation before using earlier records. Restore assigns a fresh generation and records an offline installation attempt in the replacement database; its success outcome is appended only after installation. An interruption may leave the new generation with a pending attempt and no outcome. Pin `generation` on item reads when following a previously displayed event. `audit_history_replaced` is a conflict, not a missing-record response. Never infer rollback or replay safety from an attempt without an outcome.

### Filters, pagination, and event interpretation

```bash
mcp-gateway audit list --limit 50 --actor-type system --category server
mcp-gateway audit list --outcome unknown \
  --from 2026-09-01T00:00:00.000000000Z \
  --until 2026-09-02T00:00:00.000000000Z --json
mcp-gateway audit list --cursor OPAQUE_CURSOR --generation HISTORY_GENERATION \
  --actor-type system --category server
mcp-gateway audit get AUDIT_EVENT_ID --generation HISTORY_GENERATION --json
```

All filters are conjunctive and apply to server history: `--actor-type`, `--credential-id`, `--category`, `--action`, `--target-type`, `--target-id`, `--outcome`, `--correlation-id`, `--from`, and `--until`. IDs are canonical Gateway IDs. Credential filtering matches either a performing operator or a known system initiator. The time range is inclusive `from`, exclusive `until`; provide both as fixed UTC timestamps with nine fractional digits, separated by at most 366 days. Category/action pairs and actor/target/outcome values use the closed API vocabulary.

Lists are descending sequence, not client-sorted timestamps. `--limit` is 1–100 (default 50); the CLI reads one page and never automatically retries. Keep the same filters and the returned `history.generation` when supplying `next_cursor`. The cursor is opaque and freezes the traversal's upper sequence watermark; later events require a fresh first page. On `stale_cursor`, discard all earlier pages and restart without the cursor, comparing generations. On `audit_history_replaced`, discard previous-history data and explicitly restart without the old generation. Neither response permits combining the two histories. JSON preserves the strict API representation, including nullable fields and decimal-string sequences; human output provides event facts, retention notes, and continuation guidance.

The browser's **Apply filters** queries the server and starts page one; filters remain usable with no matches. Back/Forward restores applied filters. **Load older audit events** appends only compatible pages. **Refresh** restarts at the newest matching page. Cursors and generation comparisons live only in the authenticated session, not in URLs or browser storage; reload and a new session cannot compare with forgotten prior history. Stale cursors discard the traversal and fetch page one once with a notice. Replacement clears previous-history state and warns even if the fresh read fails. Pinned detail is discarded rather than reopening a potentially reused ID after replacement. A missing event is not proof of nonexecution. Detail links to current server, principal, grant, or request resources only after verifying they still exist; failure to verify a link does not hide the audit evidence.

Performer labels distinguish **Operator**, **System**, and **Offline maintenance**. A system event's optional initiating credential is attribution, not its performer or a named human. Attempts and outcomes are immutable separate events joined by correlation ID; `pending`, `failed`, `rejected`, and `unknown` must not be interpreted as success or rollback. The detail correlation link selects matching retained events. Audit is separate from Invocation History and Requests: request submissions and invocation evidence are not copied here.

See the [public audit contract](../design/public-contract.md#control-plane-audit-reads) for exact response shapes. Audit stores only credential IDs/fingerprints and allowlisted reason/problem codes, never raw secrets, raw error bodies, unrestricted snapshots, or invocation payloads. No export format or permanent-retention guarantee is provided.

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
mcp-gateway audit --help
mcp-gateway backup --help
mcp-gateway admin credential --help
mcp-gateway admin reset --help
```

Focused workflow ownership:

- [Upstream server configuration](upstream-servers.md)
- [Access control](access-control.md)
- [Invocation evidence and unknown outcomes](invocation-evidence.md)
- [Backup, restore, and recovery](backup-and-recovery.md)

Consult [Administrative control plane](../design/administrative-control-plane.md) for normative CLI and administrator trust boundaries, and [Public contract](../design/public-contract.md) for public limits and failure vocabulary. Return to the [documentation map](../README.md) or [Gateway README](../../README.md) for installation and common workflows.
