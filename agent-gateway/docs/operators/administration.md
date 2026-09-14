# Administrator CLI and local administration

Audience: Gateway operators and automation authors

Purpose: Run local administration safely through the public CLI.

Agent Gateway's `agent-gateway --help` and subcommand help are the canonical command and flag reference. Only `agent-gateway` is published; see [retirement and operator cleanup](installation-migration.md#retired-executable-and-operator-cleanup) for stale executables. Examples and recovery guidance use `agent-gateway` directly. This guide owns operator procedures for installation roots, administrator authentication, output modes, and safe command execution. See [Administrative control plane](../design/administrative-control-plane.md) for normative defaults and trust boundaries.

## Shared principal administration and MCP policy

Keep identity and credential work under `agent-gateway principal` and **Access → Principals**. Each principal has one identity/state and at most one current agent credential. Its **MCP discovery visibility** setting affects discovery only and grants no access; manage call authority through MCP grants. Principal creation also creates the ordinary **Default Gateway access** grant for Gateway's six fixed MCP self-service tools, not downstream tools or future protocols.

Existing `--visibility`, API `visibility`, and creation `default_grant` names remain unchanged compatibility fields, not protocol-general grants. Principal JSON and credential representations, CLI output, defaults, and one-time sinks are unchanged; no protocol selector or MCP-settings endpoint is added. See [principal creation and credential procedures](access-control.md#create-and-inspect-principals) and the [normative identity boundary](../design/identity-and-authorization.md#shared-identity-and-mcp-policy-ownership). Do not rotate credentials, reinitialize, or convert backups for this wording clarification.

## Installation root

`make install` installs only `agent-gateway` from one implementation. Root and lock selection remain independent of executable basename. Credential prefixes, keyring identifiers, ports, and `mcp_gateway.*` self-service tools are unchanged; administrative API clients use the v2 contract above. Existing explicit-root commands remain supported. New installation defaults are canonical; an existing installation must follow the [explicit stopped migration procedure and selection matrix](installation-migration.md), not reinitialize or rotate credentials. Delivery of that capability does not perform or authorize live host adoption. Client provisioning has its own [migration and consumer qualification gate](access-control.md#existing-sandbox-migration-and-conflicts).

`--data-dir` has highest precedence. Without it, Gateway uses `$XDG_DATA_HOME/agent-gateway` when `XDG_DATA_HOME` is an absolute path. Otherwise it resolves the operating-system account home and uses `~/.local/share/agent-gateway`. A relative XDG value is rejected, and the `$HOME` environment variable is not an authority source. A legacy `mcp-gateway` entry in that selected base causes a nonsecret migration refusal, whether the canonical root also exists or not. Only an exact completed migration tombstone bound to the moved directory is accepted; unknown files/links and inspection errors refuse. No automatic relocation, fallback search or merge occurs. Recover custom service selections explicitly before changing XDG settings or starting a new process.

Use the same data directory for initialization, service startup, stopped-process recovery, and online commands:

```bash
agent-gateway initialize --data-dir /path/to/gateway-data
agent-gateway serve --data-dir /path/to/gateway-data
agent-gateway --data-dir /path/to/gateway-data status
```

The zero-argument installation uses the default root and stores its administrator bearer at `<effective-data-dir>/admin-bearer`. `initialize` creates owner-only paths, never overwrites an existing secret output, and prints safe next steps without printing the bearer.

Exact syntax and defaults:

- `agent-gateway initialize --help`
- `agent-gateway serve --help`
- `agent-gateway status --help`
- `agent-gateway admin --help`
- `agent-gateway admin credential --help`
- `agent-gateway admin reset --help`

## Start and inspect Gateway

The default service authority is `http://127.0.0.1:8210`:

```bash
agent-gateway initialize
agent-gateway serve
# In another terminal:
agent-gateway status
```

`serve --listen` accepts only a canonical numeric IPv4 loopback address and explicit port. Online `--address` accepts a canonical numeric `127/8` HTTP URL or an explicitly trusted hostname HTTP URL with a canonical decimal port (1–65535). Wildcard and non-loopback numeric destinations, URL userinfo, paths (including a trailing slash), queries, fragments, forwarding headers, redirects, ambient proxies, cookies, compression, and automatic transport retries are not accepted. When a selected loopback address refuses the connection, every online leaf reports `gateway_not_running` and renders the exact `agent-gateway serve` command for the selected address and explicit data directory. A hostname refusal instead directs you to check forwarding and the numeric-loopback service; a hostname is never a valid `--listen` value.

`GET /livez` is unauthenticated process liveness. `GET /readyz` reports only ready or not ready. Detailed `status` requires administrator authentication.

## Trusted local forwarding and sandbox administration

To reach the existing routes through a trusted VM/container forwarding hostname, explicitly allow that name on the host:

```bash
agent-gateway serve --allowed-host host.lima.internal
# Repeat --allowed-host for another independently trusted hostname.
```

Names use ASCII DNS labels of 1–63 letters, digits, or hyphens, at most 253 characters total. Labels cannot start/end with a hyphen; the final label cannot be entirely numeric. Single-label names are accepted. URLs, IP literals, ports, wildcards, underscores, empty labels, trailing dots, Unicode, whitespace, and controls are rejected. Use an ASCII punycode spelling if needed. Matching folds ASCII DNS case only and is exact: neither subdomains nor lookalike suffixes inherit access. DNS resolution never grants Host trust. Requests may omit a port or carry any decimal port from 1–65535 (including leading zeros); the port is ignored only for an explicitly listed hostname. The canonical numeric listener Host still requires its exact port. Omitting the flag allows no extra names.

The listener remains numeric IPv4 loopback. Arrange forwarding separately; Gateway has no Lima defaults, trusted-proxy mode, or forwarding-header support. All existing routes retain their own credentials, roles, methods, and limits. OAuth callback construction remains independent. Browser Origin policy is unchanged and includes the listener port: an allowed hostname is not a trusted browser origin, and it does not enable browser sign-in or CORS. Continue using the canonical numeric origin for the browser application. Browsers can send the alias Origin even while loading module assets, so opening an allowed alias may produce a blank page rather than a sign-in screen.

Selecting an HTTP hostname destination is an explicit trust decision about resolution and the entire forwarding path. The CLI may connect to the VM's host-forwarding address rather than guest loopback; it does not infer that address from `--allowed-host`. Plain HTTP provides no remote confidentiality or server authentication. This is not secure arbitrary-remote administration; use only a trusted local forwarding path.

Provision a separate administrator credential on the host, not the main administrator bearer and not an agent credential:

```bash
agent-gateway admin credential create --secret-output /safe/new/sandbox-admin
```

Retain the credential ID from the safe metadata output. Securely transfer only that owner-only bearer file into the sandbox, not Gateway's database or installation root. Then select the destination and credential explicitly inside the sandbox (use the port exposed by your forwarding setup):

```bash
agent-gateway status --address http://host.lima.internal:8210 \
  --admin-bearer-file /safe/sandbox-admin
```

This credential grants full administrator authority; hostname allowlisting does not reduce its privileges. When access ends, revoke that specific credential using another active host administrator:

```bash
agent-gateway admin credential revoke SANDBOX_CREDENTIAL_ID --yes
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
agent-gateway --data-dir /path/to/gateway-data \
  status --admin-bearer-file /safe/new/admin-bearer
```

See [Backup, restore, and recovery](backup-and-recovery.md) for replacement-authority workflows.

## Safe serve diagnostics

Warnings and errors are enabled by default, including upstream authentication/degraded transitions, unconfirmed cleanup, foreground OAuth failure/expiration, and actionable refresh failures. Use `serve --log-level info` for foreground OAuth required/completed and upstream recovery, or `debug` for actual attempts, retry/reset timing, detailed OAuth stages, routine refresh success, and payload-free invocation/authority/storage timing. Diagnostics are always JSON lines on stderr; result/problem formatting still follows `--output`/`--json`. For a deliberately started foreground service:

```bash
agent-gateway serve --log-level debug --output json 2>gateway-diagnostics.jsonl
# Inspect complete diagnostic lines; terminal problems have no schema_version.
jq -R 'fromjson? | select(.schema_version == 1)' gateway-diagnostics.jsonl
jq -R 'fromjson? | select(.event == "storage_reject" or .event == "diagnostic_loss")' gateway-diagnostics.jsonl
# Substitute a process_id and numeric upstream_ref observed in this file, not a server ID.
jq -R --arg p 'PROCESS_FROM_LOG' --argjson u 7 \
  'fromjson? | select(.schema_version == 1 and .process_id == $p and .upstream_ref == $u)' gateway-diagnostics.jsonl
```

Use `process_id` plus `call_id` to correlate a live attempt; `invocation_id` appears only after audit acknowledgment. Storage/authority mutation counters are owner-local. `storage_wait` followed by `storage_acquire` is ordinary contention. Rejection causes distinguish `capacity`, `expired`, `cancelled`, `stopped`, and `latched`; durability failures carry a closed `stage`. `writer_kind` distinguishes invocation admission, terminal annotation and coarse foreign work. Occupancy is a sample, and elapsed milliseconds are not a guarantee of throughput or an active transaction deadline.

At `info`, `reconciliation_displaced` explains that OAuth/lifecycle replacement displaced an operation; successful verified settlement appears as Superseded in operation detail/history, never Succeeded on behalf of its replacement. At the default `warn` level, `reconciliation_settlement_failure` reports `capacity`, `unavailable`, or `stopped` without resource IDs or raw errors. Read authenticated operation/audit details to identify the affected work. Unconfirmed cleanup or failed settlement remains fail-closed; repeated reauthorization does not repair it. Existing orphan rows are not repaired online: follow [stopped-process recovery](backup-and-recovery.md), and obtain separate authorization before stopping or restarting a live service. Do not infer replay safety from an absent log or a healthy replacement.

Use `process_id` plus `upstream_ref` to follow one upstream, and `attempt_ref` to distinguish actual reconciliation workers or foreground flows. A callback retains its flow reference; the ensuing reconciliation has a new attempt reference and the same upstream reference. These opaque counters are unrelated to resource names, URLs, or credentials. They are process-local, never reused within the process, and can be absent under diagnostic contention/capacity. Restart loses their meaning. There is deliberately no lookup that maps a reference to a server: use authenticated server/operation/flow/audit reads for time and public-reason context, not as proof of an exact mapping. Do not paste a resource or credential ID into the reference filter.

Typical subsequences (other producers may interleave, and records may be lost):

| Situation                    | Expected evidence                                                                                                                                       |
| ---------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Authentication needed        | `upstream_unhealthy` at warn with `operator_authentication_required`; preparing a foreground flow adds `oauth_required` at info                         |
| Authorization succeeds       | `oauth_completed` at info, then a new reconciliation attempt; `upstream_recovered` at info only after healthy active publication                        |
| Transient transport failure  | Debug attempt start/completion and `upstream_retry_scheduled`; warn transition identifies the connection/initialization/discovery phase and safe reason |
| Persistent identical failure | Individual attempts remain debug; the next repeated failure after one minute can emit a warning with an intervening `suppressed` count                  |
| Recovery                     | Suppression resets even at warn; info shows recovery when unhealthy history was retained; routine healthy catalog polls stay quiet                      |
| OAuth failure/expiration     | Warn `oauth_failed` or `oauth_expired`; detailed stage starts are debug; successful routine background refresh is debug, not info                       |
| Unconfirmed stop             | Warn `cleanup_uncertain` / `stop_unconfirmed`; inspect authenticated state and do not infer that another authorization attempt repairs cleanup          |

`reconciliation_settlement_failure` means the worker could not safely finish its durable operation/audit outcome and further reconciliation is blocked until stopped-process recovery. A Connect operation can therefore remain Running even though its runtime is degraded. Normal completion tolerates brief storage admission contention with four persistence-only acquisition attempts (25/50/100 ms waits); it never restarts the server or repeats uncertain writes. Exhausted normal-completion admission reports `cause: capacity`; other completion failures remain `unavailable`. A retained settlement failure reports stopped rather than inheriting an earlier catalog retry disposition. This is not an MCP initialization timeout, and increasing transport timeouts will not recover it.

`retry_scheduled` reports actual scheduled follow-up work, not an executing attempt. Reconciliation backoff caps at 60 seconds but retries are not exhausted. Catalog traversal uses its existing five-minute epoch grid instead: debug `catalog_poll_scheduled` records the installed delay (at most five minutes) without a retry ordinal. If fencing prevents that schedule, diagnostics do not claim a retry. The diagnostic retry ordinal continues beyond that cap and resets with backoff; it differs from the public capped retry index. `stopped` means no current retry was scheduled for that stopped work; `unknown` means the diagnostic does not establish the cause or next action. Elapsed milliseconds are capped at 24 hours (zero omitted), not a deadline. Missing success or completion never proves timeout, failure, or safe replay. `oauth_completed` alone does not mean the upstream is routable: check current server/catalog reads and the later recovery event. New unhealthy phase/reason/disposition transitions warn immediately; foreground operator actions are not suppressed as background retries. Unsummarized counts are discarded on recovery.

Logs omit credentials, headers, URLs, authorization codes, OAuth state/PKCE, tool names, argument keys/values, resource/credential IDs, downstream content/errors and subprocess output even at debug. They are lossy diagnostics, never audit evidence or permission to replay. Full queues drop newest; aggregate `diagnostic_loss` counts may themselves be unavailable on a broken sink. Shutdown waits at most one additional second for output. A stalled/broken destination can lose any severity or the terminal problem; a short write or forced exit can leave an incomplete final line. `fromjson?` above intentionally skips malformed lines—inspect the original file when investigating loss. Diagnostic loss alone does not make clean storage unclean or change a successful service exit. No rotation or retention service is included. The operator owns file permissions, retention, rotation, and deletion for both foreground captures and launchd log files; use owner-only captures (for example, set `umask 077` before redirection) and review only the necessary bounded interval. Choosing a level for an already running launchd job requires an explicitly authorized configuration/restart; reading or tailing a log does not restart Gateway.

## Output and failures

Human output is the default. Use `--output json` or the `--json` shorthand for the exact JSON projection. Conflicting output selectors fail before work begins.

Finite successes write to stdout. Finite and pre-start failures leave stdout empty and write one bounded problem to stderr; serve may additionally emit the separate diagnostic lines described above. Problems retain stable codes and typed exit classes so automation can distinguish invalid input, authentication, conflict, unavailable storage, and uncertain outcomes without parsing prose.

Lists return one page and use command-scoped `--limit`, `--cursor`, and filter flags. When another page exists, human output ends with `NEXT_CURSOR`; JSON retains the exact `next_cursor` member. Supply that cursor explicitly for the next page. Closed JSON requests reject duplicate, unknown, missing, or trailing values. Command input is intentionally split:

| Input mode                                  | Commands                                                                                                                                        |
| ------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| Direct flags only                           | `admin credential create`, `principal create`, `principal update`, `mcp server operation start`, `mcp grant update`, `mcp grant-request reject` |
| Strict `--file` only                        | `mcp server create`, `mcp server credential replace`                                                                                            |
| Direct flags or strict `--file`, never both | `mcp server update`, `mcp grant create`, `mcp grant-request approve`                                                                            |

Use `--file PATH` or `--file -` for the strict file form. `--file -` conflicts with `--admin-bearer-stdin`. Constrained grant and approval shapes require the file form; their direct forms cover the ordinary unconstrained case. Strict files accept permanent v1 `{"equals":{...}}` constraints and closed v2 `{"version":2,"equals":{...},"regex":{...}}` constraints, validate them against the matcher compiler's grammar and limits, and preserve lexical number and regex bytes through submission. Table output identifies `v1 equals` or the v2 equality/regex atom counts. Both grant creation and approval forms accept an optional human-readable `description` (`--description` in direct mode). Grant descriptions are display metadata; `mcp grant update` changes or clears only that metadata under an exact ETag. Credential issue, rotate, and revoke commands have no request-document input.

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
agent-gateway admin credential rotate OLD_CREDENTIAL_ID \
  --secret-output /safe/new/admin-bearer \
  --yes
agent-gateway status --admin-bearer-file /safe/new/admin-bearer
```

Rotation conditionally creates one non-expiring replacement, durably publishes and securely reopens the file, verifies its metadata and authentication, and only then conditionally revokes the named old credential. It never promotes the replacement into the default bearer path. If completion is uncertain, do not replay: retain the replacement file and use the rendered metadata command to inspect the old and new records. Before replacement verification, workflow-owned failures preserve old authority; after verified publication, an incomplete workflow may intentionally leave both credentials active.

If durable publication itself fails after creation, the output path may contain unverified secret material but must not be trusted or used as credential input; secure or remove it. This workflow does not revoke the old credential, but expiration or concurrent administrator action may still make it unusable. An active replacement record without a durably verified bearer may also exist. If the pre-rotation credential remains active, use it to inspect metadata; otherwise use another active administrator credential. Explicitly revoke an unusable replacement if present, and perform any later rotation as a fresh deliberate operation.

Use stopped-process `agent-gateway admin reset` only for all-authority recovery. The command tree migrated immediately: `admin credential ...` and `admin reset` are the only administrator spellings, and the legacy hyphenated forms perform no work and have no aliases.

For governed call evidence, `outcome_unknown` means the effect may already have happened. See [Invocation evidence and unknown outcomes](invocation-evidence.md). For backup and stopped-process failures, see [Backup, restore, and recovery](backup-and-recovery.md).

## Control-plane audit history

Use `agent-gateway audit list` and `agent-gateway audit get AUDIT_EVENT_ID`, or choose **Audit Log** in the browser navigation. This is the existing administrative audit history, including system and offline maintenance events, not an administrator directory or actor filter. Both consume the authenticated read-only `GET /api/v2/audit-events` and `GET /api/v2/audit-events/{id}` API. Generated `agent-gateway audit --help`, `agent-gateway audit list --help`, and `agent-gateway audit get --help` describe the command grammar. The [coverage matrix](../design/administrative-control-plane.md#control-plane-audit-coverage) identifies audited operator, system and offline actions and their regression evidence. Do not infer that an action never happened from an empty audit collection.

Collection responses return summaries, a next-page cursor, and `history` with `generation`, `oldest_retained`, and `pruned`. Only the newest 65,536 events are retained. Keep the generation separately from the oldest boundary; pruning advances the boundary within one generation, while a generation mismatch means histories must not be combined. After `stale_cursor`, discard the traversal, fetch a fresh first page, and compare its generation before using earlier records. Restore assigns a fresh generation and records an offline installation attempt in the replacement database; its success outcome is appended only after installation. An interruption may leave the new generation with a pending attempt and no outcome. Pin `generation` on item reads when following a previously displayed event. `audit_history_replaced` is a conflict, not a missing-record response. Never infer rollback or replay safety from an attempt without an outcome.

### Filters, pagination, and event interpretation

```bash
agent-gateway audit list --limit 50 --actor-type system --category server
agent-gateway audit list --outcome unknown \
  --from 2026-09-01T00:00:00.000000000Z \
  --until 2026-09-02T00:00:00.000000000Z --json
agent-gateway audit list --cursor OPAQUE_CURSOR --generation HISTORY_GENERATION \
  --actor-type system --category server
agent-gateway audit get AUDIT_EVENT_ID --generation HISTORY_GENERATION --json
```

All filters are conjunctive and apply to server history: `--actor-type`, `--credential-id`, `--category`, `--action`, `--target-type`, `--target-id`, `--outcome`, `--correlation-id`, `--from`, and `--until`. IDs are canonical Gateway IDs. Credential filtering matches either a performing operator or a known system initiator. The time range is inclusive `from`, exclusive `until`; provide both as fixed UTC timestamps with nine fractional digits, separated by at most 366 days. Category/action pairs and actor/target/outcome values use the closed API vocabulary. In the browser, From and Until use datetime pickers in your local timezone and convert selections to UTC automatically. Choose both bounds or clear both; Until must be later than From and the range cannot exceed 366 days.

Lists are descending sequence, not client-sorted timestamps. `--limit` is 1–100 (default 50); the CLI reads one page and never automatically retries. Keep the same filters and the returned `history.generation` when supplying `next_cursor`. The cursor is opaque and freezes the traversal's upper sequence watermark; later events require a fresh first page. On `stale_cursor`, discard all earlier pages and restart without the cursor, comparing generations. On `audit_history_replaced`, discard previous-history data and explicitly restart without the old generation. Neither response permits combining the two histories. JSON preserves the strict API representation, including nullable fields and decimal-string sequences; human output provides event facts, retention notes, and continuation guidance.

Browser filters query the server automatically and start page one: dropdowns apply immediately, IDs after a short pause, and From/Until only when both dates form a valid ordered range of at most 366 days. **More filters** contains IDs, dates and additional attribution/target selectors; its summary indicates active advanced filters and draft errors. The applied query remains visible, and invalid drafts do not replace results or prevent independent valid dropdown changes. **Clear filters** immediately clears both draft and applied values; filters remain usable with no matches. Back/Forward restores applied filters. Event links retain the applied query, and **Back to audit history** restarts the newest matching page rather than restoring an unverified traversal or losing filters. **Load older audit events** appends only compatible pages. **Refresh** restarts at the newest matching page. Cursors and generation comparisons live only in the authenticated session, not in URLs or browser storage; reload and a new session cannot compare with forgotten prior history. Stale cursors discard the traversal and fetch page one once with a notice. Replacement clears previous-history state and warns even if the fresh read fails. Pinned detail is discarded rather than reopening a potentially reused ID after replacement. A missing event is not proof of nonexecution. List targets link to supported server, principal, grant, or request routes without per-row resource discovery; unknown existence is checked at the destination, not asserted by the historical link. Known deleted/unavailable and unsupported targets remain plain text. Detail links to current resources only after verifying they still exist; failure to verify a link does not hide the audit evidence. Retention context and pruning warnings appear below results/detail; **Retention details** discloses generation and boundary facts. Narrow audit rows show labeled fields with full identities rather than requiring horizontal scrolling. Empty filtered results say **No matching audit events** and offer Clear filters; **No audit events yet** describes only unfiltered retained history.

Performer labels distinguish **Operator**, **System**, and **Offline maintenance**. A system event's optional initiating credential is attribution, not its performer or a named human. Attempts and outcomes are immutable separate events joined by correlation ID; `pending`, `failed`, `rejected`, and `unknown` must not be interpreted as success or rollback. The detail correlation link selects matching retained events. Administrative audit is separate from **MCP → Invocations** and **MCP → Requests**: request submissions and invocation evidence are not copied here.

See the [public audit contract](../design/public-contract.md#control-plane-audit-reads) for exact response shapes. Audit stores only credential IDs/fingerprints and allowlisted reason/problem codes, never raw secrets, raw error bodies, unrestricted snapshots, or invocation payloads. No export format or permanent-retention guarantee is provided.

## Command families

Use generated help rather than copying a full command inventory into documentation:

```bash
agent-gateway --help
agent-gateway mcp server --help
agent-gateway mcp catalog --help
agent-gateway principal --help
agent-gateway mcp grant --help
agent-gateway mcp grant-request --help
agent-gateway mcp invocation --help
agent-gateway audit --help
agent-gateway backup --help
agent-gateway admin credential --help
agent-gateway admin reset --help
```

Focused workflow ownership:

- [Upstream server configuration](upstream-servers.md)
- [Access control](access-control.md)
- [Invocation evidence and unknown outcomes](invocation-evidence.md)
- [Backup, restore, and recovery](backup-and-recovery.md)

Consult [Administrative control plane](../design/administrative-control-plane.md) for normative CLI and administrator trust boundaries, and [Public contract](../design/public-contract.md) for public limits and failure vocabulary. Return to the [documentation map](../README.md) or [Gateway README](../../README.md) for installation and common workflows.

## Browser persistence cutover

After upgrading, reload open tabs to load the bundled Agent Gateway client. Old `mcp_gateway_session` browser sessions require fresh sign-in; they are not converted to `agent_gateway_session` authority. Exact-origin sign-in, session bootstrap and logout responses expire the supplied old host-only cookie. When both names exist, only the canonical cookie can select a session. Service restart still invalidates all in-memory sessions; this cutover does not rotate administrator credentials or migrate host state.

The shared theme control preserves valid `system`, `light` and `dark` preferences: `agent_gateway_theme` wins when valid; otherwise a valid `mcp_gateway_theme` preference is copied to it. The old key is removed only after a successful canonical write. If writes fail, the saved old preference remains available for a later load and theme changes remain usable in memory. If storage cannot be read, the page starts with the system theme; malformed values are ignored. Do not clear browser storage as a migration prerequisite.

Reload and sign-in never replay mutations. If an old tab reports version skew, rejection or an uncertain result, retain its input/key/precondition, reload, sign in and inspect current resources before any deliberate follow-up. Browser persistence naming does not change routes, layout, Origin/CSRF policy, session expiry or revocation. Host and client changes remain separate: follow [stopped installation migration](installation-migration.md) and [client provisioning compatibility](access-control.md#existing-sandbox-migration-and-conflicts), including the still-required legacy client exports.

## Browser location cutover

Retired browser paths have **no aliases or redirects**. Principals now uses `#/principals` and Audit Log uses `#/audit-log`; their old grouped paths are invalid. This sidebar cleanup changes no API endpoints. Update bookmarks and browser automation to the canonical locations below. Old or invalid paths show a safe invalid-location notice and return to fixed navigation, not the corresponding resource.

| Old collection                | Canonical collection    |
| ----------------------------- | ----------------------- |
| `#/servers`                   | `#/mcp/servers`         |
| `#/catalog`                   | `#/mcp/tools`           |
| `#/access/principals`         | `#/principals`          |
| `#/grants`                    | `#/mcp/grants`          |
| `#/requests`                  | `#/mcp/access-requests` |
| `#/invocations`               | `#/mcp/invocations`     |
| `#/audit`, `#/activity/audit` | `#/audit-log`           |

Carry supported detail IDs and `/new` suffixes beneath the new collection. Server-owned destinations become `#/mcp/servers/{server-id}/operations/{id}`, `/auth-flows/{id}`, and `/descriptors/{id}`. Server `tab=activity` becomes `tab=operations`; status is the default and omits `tab=status`. Only declared destination-specific filters are accepted; copied valid filters remain supported, but cursors and secrets never belong in URLs. `#/principals` is canonical; `#/access/grants` and `#/access/requests` are retired by the MCP permission cutover below. `#/overview`, `#/sign-in`, `#/system`, System tabs and System create paths are unchanged. Hash routing remains in place; no pathname fallback is served.

The location cutover itself is not an installation or browser-persistence migration. Durable authority, ports, MCP ingress/self-service and OAuth callback identities remain compatible. Current browser persistence follows the cutover above; current root/service and provisioning names follow the separately documented installation and client migrations.

## MCP invocation namespace cutover

Upgrade the service, standalone CLI, bundled browser, API clients and automation together, then reload open tabs. Sign in again if restart invalidated the session. This clean cutover retains API v2; no aliases, redirects, fallback requests or automatic mutation replay are provided.

| Retired interface                            | Canonical interface                              |
| -------------------------------------------- | ------------------------------------------------ |
| `/api/v2/invocations`                        | `/api/v2/mcp/invocations`                        |
| `/api/v2/invocations/{id}`                   | `/api/v2/mcp/invocations/{id}`                   |
| `agent-gateway invocation list`              | `agent-gateway mcp invocation list`              |
| `agent-gateway invocation get INVOCATION_ID` | `agent-gateway mcp invocation get INVOCATION_ID` |
| `#/activity/invocations`                     | `#/mcp/invocations`                              |
| `#/activity/invocations/{id}`                | `#/mcp/invocations/{id}`                         |

Carry supported filters and detail IDs to the new locations; do not copy cursors or live/pause state into URLs. **MCP → Invocations** replaces **Activity → Agents**. **Audit Log** is the shared history destination at `#/audit-log`, replacing `#/activity/audit`. `/api/v2/audit-events` and `audit list/get` are unchanged and still include administrator, system, and offline-maintenance attribution. This is not an actor restriction or protocol filter. Overview remains shared; its existing MCP tool summaries are not cross-protocol metrics.

Retired API paths return not found; retired CLI commands fail locally before authority acquisition. Old or invalid browser links show the existing safe notice and fixed signed-in Overview or signed-out Sign in fallback, never an inferred resource lookup. Explicitly navigate using the current menu or update the bookmark and reload. After any rejected or uncertain operation, inspect current state before deciding on a new action; changing the route or signing in is never permission to replay an invocation or mutation.

Invocation JSON, CLI machine output, filters, limits, cursor/generation rules, read-only behavior and retention are unchanged. Existing invocation and audit rows/IDs remain readable, including history retained in backups. No migration, table/column change, protocol discriminator or persisted rewrite is needed. Historical authorization and redacted captures remain evidence of their original attempt, not current policy or proof of safe retry. Missing terminal evidence still means unknown outcome. Separate audit production, `invocations` invalidation events, credential bytes, backup lineage, MCP ingress/self-service and callback identities are unchanged. See [invocation evidence](invocation-evidence.md) for safe interpretation.

## MCP permission namespace cutover

Upgrade the service, standalone CLI, bundled browser, API consumers, and automation together. Reload open browser tabs after the upgrade and sign in again if the service restarted. This is a coordinated clean cutover within API v2, not a storage or credential migration. The sidebar starts with Overview, Principals, Audit Log, and System; **MCP** contains Servers, Tools, Grants, **Requests**, and **Invocations**. Requests retains the page title **Access requests**. Access requests approve MCP permissions, not network traffic or queued calls.

| Retired interface                     | Canonical interface                       |
| ------------------------------------- | ----------------------------------------- |
| `/api/v2/grants` and `/{id}`          | `/api/v2/mcp/grants` and `/{id}`          |
| `/api/v2/grant-requests` and `/{id}`  | `/api/v2/mcp/grant-requests` and `/{id}`  |
| `/api/v2/grant-requests/{id}/approve` | `/api/v2/mcp/grant-requests/{id}/approve` |
| `/api/v2/grant-requests/{id}/reject`  | `/api/v2/mcp/grant-requests/{id}/reject`  |
| `/api/v2/grant-constraints/validate`  | `/api/v2/mcp/grant-constraints/validate`  |
| `agent-gateway grant ...`             | `agent-gateway mcp grant ...`             |
| `agent-gateway grant-request ...`     | `agent-gateway mcp grant-request ...`     |
| `#/access/grants`, `/{id}`, `/new`    | `#/mcp/grants`, `/{id}`, `/new`           |
| `#/access/requests` and `/{id}`       | `#/mcp/access-requests` and `/{id}`       |

Methods, flags, representations, filters, ordering, counts, ETags, and confirmation semantics are unchanged. Carry only supported query parameters into new bookmarks. Principals and credentials stay top-level; MCP server/catalog routes, ingress, callbacks, and `mcp_gateway` self-service tools are unchanged. IDs, descriptions, historical rows, policy, audit/event names, and backup lineage are preserved without a database migration.

Old API paths return not found, old CLI commands fail locally, and old browser links show the existing invalid-location notice with a fixed fallback. There are no aliases, redirects, compatibility retries, or automatic mutation replay. Recover an old link by explicitly navigating through the current menu or updating the bookmark, then inspect the intended resource. After a rejected or uncertain mutation, inspect current state before a deliberate new action; changing the URL is never permission to replay it. Approval still does not execute the original tool call.

## Operator v2 cutover

Upgrade standalone CLI binaries, API clients, JSON scripts, and the service together. This is an intentional operator breaking change, not an installation migration. The current executable uses only the new grammar; there are no v1 HTTP handlers, top-level server/catalog aliases, redirects, or compatibility completions.

| Previous operator interface                                                                                   | Current interface                                                                                                                                                                                    |
| ------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `/api/v1/servers` and every child                                                                             | `/api/v2/mcp/servers` and the corresponding child                                                                                                                                                    |
| `/api/v1/catalog`                                                                                             | `/api/v2/mcp/catalog`                                                                                                                                                                                |
| Other administrative `/api/v1/*` resources, including sessions, events, credentials, status, and backups      | Corresponding `/api/v2/*` resources, outside the MCP namespace                                                                                                                                       |
| `server ...`, `catalog ...`                                                                                   | `mcp server ...`, `mcp catalog ...` in the current executable, including an explicitly renamed current binary                                                                                        |
| Server `auth-flows` API resources                                                                             | `oauth-flows` resources; the CLI subtree is `mcp server auth-flow ...`                                                                                                                               |
| Operator flow JSON `flow_state`                                                                               | `state`                                                                                                                                                                                              |
| Operator limits `s2_idempotency_records`                                                                      | `server_idempotency_records`                                                                                                                                                                         |
| Human-label server-status queries such as `Authorization required`                                            | Stable snake_case tokens such as `authorization_required`; presentation labels are unchanged                                                                                                         |
| Implicit legacy/table query modes and `representation=table`                                                  | One ordinary collection shape and default policy regardless of filters                                                                                                                               |
| Descriptor `retired=include/exclude/only` and `representation=summary`                                        | Omit status for all, use `status=available/retired`, and explicit `projection=full/summary` (default full)                                                                                           |
| Bare operation, principal, grant, and request pages without counts                                            | Their ordinary normalized query pages always include exact `total_count` and `offset`; grants/requests use enriched collection items                                                                 |
| Insertion-order defaults for servers, catalog, descriptors, principals, grants, and operation/request history | Defaults listed in the [normalized collection contract](../design/public-contract.md#normalized-administrative-collections); default limit 50, MCP inventory/catalog/descriptor/operation maximum 50 |

`projection=active` remains an exclusive operation read with `{items,has_more}`. Other ordinary pages do not acquire invented totals. The CLI's `--retired` descriptor selector translates to the new status query; API clients must use the new grammar. Exact grant/request policy fields and member-resource shapes are unchanged.

After a service upgrade, reload an already-open browser tab to load the bundled client, and sign in again if its session expired or still uses the legacy cookie. Browser fragments follow the [location cutover](#browser-location-cutover) above; layout is unchanged. Discard old page cursors; reload starts a fresh traversal. A failed or uncertain mutation during version skew is **not** permission to retry: retain its input/key/precondition, inspect current resources with the upgraded client, and resolve the outcome before any deliberate same-intent action. Never retry merely because the old tab or CLI cannot decode a response.

Existing same-key server work retains its durable identity across the route rename, including conflicts and interrupted outcomes. Database/backup lineage, stored enums, bearer verifiers/prefixes, keyring identifiers/generations, roots/locks, ports, `/mcp`, OAuth callback identities, `mcp_gateway.*` tools/schemas, and explicit installed selections are not rewritten by the operator v2 cutover. Canonical installation/service naming requires the [stopped migration](installation-migration.md). Current [client provisioning](access-control.md#provision-a-pi-agent-in-a-lima-sandbox) uses canonical markers/token paths and exports both `AGENT_GATEWAY_ENDPOINT` / `AGENT_GATEWAY_AGENT_TOKEN` and the retained `MCP_GATEWAY_ENDPOINT` / `MCP_GATEWAY_AGENT_TOKEN` aliases from one authority. No reinitialization, credential rotation, automatic relocation, live installation mutation, or external agent-config change is part of this cutover. Offline recovery now uses `storage verify` and `backup restore BACKUP_ID`; see the [recovery command cutover](backup-and-recovery.md#recovery-command-cutover) for removed spellings and changed CLI JSON. Historical acceptance evidence remains historical, not current release qualification.
