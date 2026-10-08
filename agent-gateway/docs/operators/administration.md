# Administrator CLI and local administration

Audience: Gateway operators and automation authors

Purpose: Run local administration safely through the public CLI.

Agent Gateway's `agent-gateway --help` and subcommand help are the canonical command and flag reference. Only `agent-gateway` is published; see [retirement and operator cleanup](installation-safety.md#retired-executable-and-operator-cleanup) for stale executables. Examples and recovery guidance use `agent-gateway` directly. This guide owns operator procedures for installation roots, administrator authentication, output modes, and safe command execution. See [Administrative control plane](../design/administrative-control-plane.md) for normative defaults and trust boundaries.

For a new installation, follow **Installation root → Start and inspect → Administrator authentication**. Before any write, read [confirmation](#confirmations-and-one-time-values) and [retry discipline](#retry-discipline). For an existing deployment, follow [upgrade and compatibility](upgrade-compatibility.md) rather than reinitializing it. Investigate with [audit history](#control-plane-audit-history) or [serve diagnostics](#safe-serve-diagnostics); both are evidence, not permission to replay.

## Installation root

`make install` installs only `agent-gateway` from one implementation. Root and lock selection remain independent of executable basename. Credential prefixes, keyring identifiers, ports, and `mcp_gateway.*` self-service tools are unchanged; administrative API clients use the [v2 compatibility contract](upgrade-compatibility.md#operator-v2-cutover). Existing explicit-root commands remain supported. New installation defaults are canonical; preserve existing roots and follow the [post-migration selection and safety guidance](installation-safety.md), never reinitialize or rotate credentials for naming. The migration capability is retired; tombstones and explicit custom-root support remain. Manual client configuration has its own [migration and consumer qualification gate](access-control.md#existing-sandbox-migration-and-conflicts).

`--data-dir` has highest precedence. Without it, Gateway uses `$XDG_DATA_HOME/agent-gateway` when `XDG_DATA_HOME` is an absolute path. Otherwise it resolves the operating-system account home and uses `~/.local/share/agent-gateway`. A relative XDG value is rejected, and the `$HOME` environment variable is not an authority source. Legacy `mcp-gateway` paths, files, links and tombstones do not affect implicit selection and are not inspected. The selected root still requires safe ownership, permissions, integrity and exclusive ownership for mutations. No automatic relocation, fallback search or merge occurs. Recover custom service selections explicitly before changing XDG settings or starting a new process.

Use the same data directory for initialization, service startup, stopped-process recovery, and online commands:

```bash
agent-gateway init --data-dir /path/to/gateway-data --confirm
agent-gateway serve --data-dir /path/to/gateway-data
agent-gateway --data-dir /path/to/gateway-data doctor
```

The zero-argument installation uses the default root and stores its administrator bearer at `<effective-data-dir>/admin-bearer`. `init` inspects and confirms missing setup, creates owner-only storage and initial administrator authority, prepares the initial CA, and writes its public certificate to `<root>/http-ca.pem`. Existing state, credentials and CA are preserved. `initialize` remains a quiet alias. Missing existing credential material never triggers a reset. Noninteractive changes require `--confirm`; no service, proxy or client trust changes occur. Interrupted setup resumes only demonstrably missing work. Unresolved markers, protected-generation evidence or nonempty WAL/journal state refuse rather than replay. A fully configured idle running installation may return an inspection-only no-op; changes require stopped ownership. If init reports active WAL or journal data, stop the selected Gateway cleanly and rerun init. Do not delete SQLite sidecars or change their permissions to force inspection; active data is not corruption.

Exact syntax and defaults:

- `agent-gateway init --help`
- `agent-gateway serve --help`
- `agent-gateway doctor --help`
- `agent-gateway admin --help`
- `agent-gateway admin credential --help`
- `agent-gateway maintenance reset-admin-credentials --help`
- `agent-gateway http credential --help`

## Start and inspect Gateway

The default service authority is `http://127.0.0.1:8210`:

```bash
agent-gateway init --confirm
agent-gateway serve
# In another terminal:
agent-gateway doctor --online
```

`serve` also enables HTTP proxying at `127.0.0.1:8212` by default. Select a custom
address with `--http-proxy-listen`, or use `--clear-http-proxy-listen` for MCP-only
startup; these selections conflict. Missing CA signing material or a failed bind
prevents readiness. Existing managed disabled/custom selections remain unchanged;
see [HTTP listener upgrade guidance](upgrade-compatibility.md#http-listener-default-cutover)
and [manual proxy/client trust setup](http-proxy.md).

`serve --listen` accepts only a canonical numeric IPv4 loopback address and explicit port. Online `--address` accepts a canonical numeric `127/8` HTTP URL or an explicitly trusted hostname HTTP URL with a canonical decimal port (1–65535). Wildcard and non-loopback numeric destinations, URL userinfo, paths (including a trailing slash), queries, fragments, forwarding headers, redirects, ambient proxies, cookies, compression, and automatic transport retries are not accepted. When a selected loopback address refuses the connection, every online leaf reports `gateway_not_running` and renders the exact `agent-gateway serve` command for the selected address and explicit data directory. A hostname refusal instead directs you to check forwarding and the numeric-loopback service; a hostname is never a valid `--listen` value.

`GET /livez` is unauthenticated process liveness. `GET /readyz` reports only ready or not ready. `doctor` replaces the old top-level `status` (no alias). It reports independent checks as verified, failed, presence-only, absent, stopped or not checked; an unreachable listener is not proof of a stopped process. It shows one line per condition, with additional indented diagnostics only when needed. The absolute data directory and selection source appear once; paths inside it are relative, while external paths remain absolute. Installed service and log paths appear separately when safely available. `doctor --verbose` adds explanations for every check without running extra checks; `--json` retains structured details regardless of verbosity. File presence proves neither authority nor signing usability. `doctor --verify-storage` opts into expensive stopped, closed-generation inspection without recovery. `doctor --online` adds authenticated public-API status using `--admin-bearer-file` or the selected default. Protected keyring material is not probed. A partial checklist never claims whole-installation readiness.

### Serving without optional evidence

`doctor --online` and browser **System → Status** show serving, optional history,
and diagnostic delivery separately. A history-only failure does not mean MCP/HTTP/Git
forwarding stopped. Read the history state and pressure reason first: checkpoint
readers, budget reservation and low disk space have different causes. Unavailable
measurements are not zero usage; an absent WAL is separately identified. Inspect
disk capacity and the selected history artifacts without deleting them or performing
live recovery. Existing HTTP policy preview (`http test-access` or **HTTP → Grants →
Test access**) checks policy without dispatching a request.

Diagnostic **Writing** means a Write is outstanding, not a proven timeout. Inspect
the configured stderr destination and last successful write when pressure/failure
is reported; a timer cannot cancel every native writer. Cumulative dropped/invalid
counts survive failed log summaries. Log timestamps mark observation before queueing,
not the time delayed output arrived. Never restart or replay execution merely to
repair missing evidence.

Process observations count live owner boundaries independently of both sinks.
Execution pipelines are not proof of downstream effects; HTTP requests include the
separate Git subset, while CONNECT is separate. A reported Git success is upstream
content, not a Gateway-confirmed successful push. History delivery counts submissions;
Recorded activity still counts committed record changes. Do not compare these as
success rates or infer active work by subtraction. Restart resets process epochs and
counters; crash loss and missing terminals remain unknown. Live occupancy comes from
Resource limits and the HTTP work/stream/tunnel owners.

## Administrator authentication

Online administrator authentication never prompts. It resolves exactly one bearer source:

1. `--admin-bearer-file PATH` selects an explicit owner-readable file.
2. `--admin-bearer-stdin` reads exclusively from standard input.
3. With neither flag, Gateway reads `<effective-data-dir>/admin-bearer`.

The explicit file and stdin selectors conflict. Administrator bearers are never accepted in argv or environment variables. `--data-dir` selects the default credential location; it does not grant private database or keyring access to online commands. Online commands use only the public HTTP API at the explicitly selected destination.

For a replacement bearer created by reset or restore, select it explicitly:

```bash
agent-gateway --data-dir /path/to/gateway-data \
  doctor --online --admin-bearer-file /safe/new/admin-bearer
```

See [Backup, restore, and recovery](backup-and-recovery.md) for replacement-authority workflows.

## Confirmations and one-time values

Commands with irreversible or authority-changing consequences require a controlling terminal confirmation unless `--yes` is explicitly supported and selected. Confirmation never implies automatic replay.

Administrator credential creation and agent credential issue or rotation can publish a one-time bearer to a prepared controlling terminal or a newly created non-symlink `0600` owner-only `--secret-output` file. Administrator rotation requires a fresh owner-only `--secret-output` file so it can durably reopen and authenticate the replacement before revocation. `agent credential issue` accepts only an empty credential slot; `agent credential rotate` accepts only an occupied slot and atomically invalidates the prior agent authority without overlap. Normal stdout contains only safe metadata and guidance; JSON contains only metadata. Neither contains the bearer. Server credential input is write-only. OAuth authorization URLs are shown once through a prepared terminal and may be opened only by explicit request. Metadata reads cannot recover a lost bearer, submitted secret, or authorization URL.

A bearer-sink preparation failure occurs before credential mutation submission: choose a new output path, or a controlling terminal where supported, and submit deliberately. A failure after Gateway acknowledges credential creation is different: the credential may be active while its bearer is permanently lost. Read current metadata and explicitly rotate or revoke; never replay the original mutation merely because the one-time value was not published.

Do not put secrets in shell arguments, environment variables, ordinary output capture, or reusable files. A copied browser value remains in the operating-system clipboard until overwritten.

## Retry discipline

The CLI never retries automatically. Reads that fail before request handoff are safe to repeat after checking Gateway availability. Mutations report whether failure occurred before handoff or may be uncertain; use the resource-specific read and idempotency guidance before an explicit retry.

An exact idempotency key and canonical input digest may permit deliberate same-intent replay for commands that advertise that contract. For an ordinary ETag-capable mutation, omitting `--etag` performs one authenticated item read, validates the returned identity/revision/header ETag, and uses that value once. Supplying `--etag` pins the explicit validated value and skips that convenience read. Agent credential issue and rotate always read the agent once to enforce empty-versus-occupied slot intent; an explicit ETag must match that observed item. Neither mode refreshes a stale precondition or replays after conflict or uncertainty. Lost one-time output is not a reason to replay a credential or OAuth operation.

## Administrator rotation and migration

Use the online routine rotation command while Gateway is running:

```bash
agent-gateway admin credential rotate OLD_CREDENTIAL_ID \
  --secret-output /safe/new/admin-bearer \
  --yes
agent-gateway doctor --online --admin-bearer-file /safe/new/admin-bearer
```

Rotation conditionally creates one non-expiring replacement, durably publishes and securely reopens the file, verifies its metadata and authentication, and only then conditionally revokes the named old credential. It never promotes the replacement into the default bearer path. If completion is uncertain, do not replay: retain the replacement file and use the rendered metadata command to inspect the old and new records. Before replacement verification, workflow-owned failures preserve old authority; after verified publication, an incomplete workflow may intentionally leave both credentials active.

If durable publication itself fails after creation, the output path may contain unverified secret material but must not be trusted or used as credential input; secure or remove it. This workflow does not revoke the old credential, but expiration or concurrent administrator action may still make it unusable. An active replacement record without a durably verified bearer may also exist. If the pre-rotation credential remains active, use it to inspect metadata; otherwise use another active administrator credential. Explicitly revoke an unusable replacement if present, and perform any later rotation as a fresh deliberate operation.

Use stopped-process `agent-gateway maintenance reset-admin-credentials` only for all-authority recovery. The command tree migrated immediately: `admin credential ...` and `maintenance reset-admin-credentials` are the only administrator spellings, and the legacy hyphenated forms perform no work and have no aliases.

For governed call evidence, `outcome_unknown` means the effect may already have happened. See [Invocation evidence and unknown outcomes](invocation-evidence.md). For backup and stopped-process failures, see [Backup, restore, and recovery](backup-and-recovery.md).

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
agent-gateway doctor --online --address http://host.lima.internal:8210 \
  --admin-bearer-file /safe/sandbox-admin
```

This credential grants full administrator authority; hostname allowlisting does not reduce its privileges. When access ends, revoke that specific credential using another active host administrator:

```bash
agent-gateway admin credential revoke SANDBOX_CREDENTIAL_ID --yes
```

Also remove the `--allowed-host` entry and restart Gateway to block new requests through that name. Host removal is not credential revocation: the bearer remains usable through other accepted hosts until separately revoked or expired. Revocation blocks the bearer independently even while the hostname remains allowed. Securely remove the transferred file when no longer needed.

<a id="shared-principal-administration-and-mcp-policy"></a>

## Shared agent administration and MCP policy

Keep identity and credential work under `agent-gateway agent` and **Agents**. Each agent has one identity/state and at most one current agent credential. Its **MCP discovery visibility** setting affects discovery only and grants no access; manage call authority through MCP grants. Agent creation also creates the ordinary **Default Gateway access** grant for Gateway's six fixed MCP self-service tools, not downstream tools or future protocols.

Existing `--visibility`, API `visibility`, and creation `default_grant` names remain unchanged compatibility fields, not protocol-general grants. Every `Principal` JSON representation now includes `http_default`; credential-slot semantics and one-time sinks are unchanged. No protocol selector or MCP-settings endpoint is added. See [agent creation and credential procedures](access-control.md#create-and-inspect-principals) and the [normative identity boundary](../design/identity-and-authorization.md#shared-identity-and-mcp-policy-ownership). Do not rotate credentials, reinitialize, or convert backups for this wording clarification.

## HTTP policy administration

`agent-gateway http --help` groups scoped credentials, grants, agent HTTP defaults and policy-only Test access. Use **HTTP → Grants** or `http grant list|get|create|update|delete`; `http default get|update` operates on an agent ID without changing MCP defaults. `http test-access --file PATH` previews policy without DNS, dispatch or secret resolution. The [HTTP access-control guide](access-control.md#http-grants-and-test-access) owns complete file shapes, examples, precedence and limitations. All writes retain the shared strict-file, exact-ETag, confirmation and no-replay mechanics. This surface starts no production proxy. Strict clients must follow the [agent HTTP-default cutover](upgrade-compatibility.md#principal-http-default-client-cutover); unknown outcomes require inspection, never replay.

## Git configuration and access

`agent-gateway git` manages repositories, Git-only grants/credentials and independent
routing through `/api/v2/git`. Enabled HTTPS origins classify smart Git separately
from ordinary HTTP and authorize every requested ref action before forwarding a
push. Ordinary GitHub HTTP remains governed by HTTP policy. Git permissions and
secrets never inherit HTTP defaults, request grants or HTTP credentials. Use
`agent-gateway git --help` for the command tree. The dedicated **Git** browser
section manages Routing, Repositories, Grants and Credentials, separately from HTTP/MCP.
Canonical destinations are immutable; alias edits never retarget authority.
Adding a push rule explicitly enables read. Secret controls are write-only and
clear on submission, cancellation, navigation and session loss. Review changes
before confirmation; uncertain mutations offer no replay.

Create a repository from a strict JSON file with deliberate public access:

```json
{
  "name": "Team repository",
  "url": "https://github.com/team/repository",
  "aliases": ["https://github.com/team/repository.git"],
  "credential_id": null
}
```

```bash
agent-gateway git repository create --file /private/repository.json --yes
agent-gateway git repository list
agent-gateway git repository get ID
agent-gateway git repository update ID --file /private/repository.json --yes
agent-gateway git repository delete ID --yes
agent-gateway git grant create --file /private/grant.json --yes
agent-gateway git grant list
agent-gateway git grant get ID
agent-gateway git grant update ID --file /private/grant.json --yes
agent-gateway git grant delete ID --yes
agent-gateway git routing-profile get
agent-gateway git routing-profile update --file /private/profile.json --yes
agent-gateway git credential list
agent-gateway git credential get ID
agent-gateway git credential create --file /private/credential.json --yes
agent-gateway git credential update ID --file /private/metadata.json --yes
agent-gateway git credential rotate ID --file /private/rotation.json --yes
agent-gateway git credential delete ID --yes
```

A grant file requires `principal_id`, `repository_id`, nullable `description`,
nullable `expires_at`, and `policy`. A read-only policy is
`{"version":1,"read":true,"refs":[]}`. To permit branch creation/update, include
`{"ref":{"kind":"prefix","value":"refs/heads/team/"},"actions":["create","update"]}`
in `refs`; deletion remains denied. Write-only grants reject. Exact selectors
use `kind:"exact"` and a fully qualified ref. Different destinations require new
repository identities and grants; aliases only add/remove terminal `.git`
spelling at the same origin/base. Metadata update retains the canonical URL.

Use **Git → Routing** to inspect enforcement status and edit enabled HTTPS origins.
Add `https://github.com` to route GitHub smart-HTTPS Git through Git policy and
credentials. Review the complete current/proposed origin list before saving;
removing an origin returns it to ordinary HTTP policy. This does not create
repository grants or configure client proxy/CA trust. Revision conflicts retain
your draft for explicit review; an uncertain save must not be replayed.

CLI profile files are exactly `{"origins":["https://github.com"]}`. Origins normalize
to explicit effective ports; repository deletion never removes profile intent.
The read-only `active:true` field reports the enforcement gate. Activation refuses
while a matching opaque tunnel is still settling; let that owner finish before
submitting a new conditional update. Existing work is not canceled. Enabled origins
refuse opaque CONNECT even after the last repository is removed. Receive-pack
discovery and large-push flush probes require read plus a push-capable grant, but
never authorize a later push. Unsupported signed pushes, push options, SHA-256,
HTTP-compressed push bodies, dumb HTTP, LFS and SSH fail closed. HTTP 200 does not
prove that the upstream accepted a push; use the native Git result. Local fixture
coverage does not qualify live GitHub, native installation or client adoption.

Credential files require `name`, exact HTTPS `origin`, safe `recipe:{header,prefix}`
and write-only `secret`; metadata files omit secret, and rotation files contain
only secret. Deliberately supply existing HTTPS-compatible material using a
private file or stdin, never argv/environment, host helper execution or credential
store scanning. Do not save material in source, backups or browser storage.
Selected missing/unavailable material has no public-access or old-generation
fallback. Every retained repository reference, including deleted configuration,
prevents credential deletion or incompatible origin/recipe edits. Restore
invalidates protected material; deliberately resupply it after recovery.

All edits/deletes/rotations use exact ETags. Omission performs one validated
preflight; explicit `--etag` is never refreshed. Authority changes fence pending
admissions and retain atomic administrative audit. No command replays an uncertain
mutation. The [Git public contract](../design/public-contract.md#git-configuration-resources)
owns exact shapes, bounds and failures.

## Git traffic history

Use **Git → Traffic**, `GET /api/v2/git/traffic`, or:

```bash
agent-gateway git traffic list --limit 50
agent-gateway git traffic get TRAFFIC_ID
```

Lists are newest-first with bounded `--limit`/`--cursor` continuation; JSON retains
exact admission and optional completion evidence. History is bounded and may be
pruned. Stale cursors require a fresh read, not another Git operation. Each
classified authenticated exchange has its own Git record when durable admission
is available; it is not duplicated as an ordinary HTTP request. Discovery and
flush-only probes are distinct from pushes. No guessed CONNECT association or
traffic-to-grant shortcut is provided.

**Admission**, **Transport**, and **Upstream report** answer different questions.
Allowed does not mean dispatched; HTTP 200 and clean transfer do not mean push
success. Read completion does not establish a valid local checkout. A complete
supported receive-pack report can say reported success, failure, or partial
success. This is an upstream claim, not independent verification of repository
effects. Missing, malformed, interrupted, oversized or unsupported reports remain
unknown, including a partial report missing one requested command. Git push
observation requests identity encoding; an unexpectedly encoded response is
relayed live but not interpreted. Ordinary HTTP encoding is unchanged.

Configured repository name/destination, revisions, up to four applicable grant
references plus their total, command operation counts, and transport facts are
immutable admission-time evidence. Later rename/deletion/rotation never rewrites
that history. Legacy rows without policy facts or terminal observation remain
explicitly unavailable/unknown. Observed refs/OIDs, packs, request headers,
command fingerprints, arbitrary upstream messages and secrets are not persisted.

Blocked/unsupported exchanges are not authorized; unavailable Git credentials
never fall back to public access. For an uncertain push, inspect the native Git
result and reconcile the relevant remote refs with an independently authorized
read before deciding on another operation. Do not infer rollback or safe retry
from missing completion; a persistence failure does not replace a result already
relayed to the live client.

For clients, use HTTPS smart-Git remotes with the configured canonical spelling or
explicit `.git` alias. Follow [proxy and CA setup](http-proxy.md) to activate the
Gateway proxy, transfer only its public CA and the supported agent proxy
credential through protected client configuration, and configure Git's HTTPS
proxy and CA trust (`http.proxy`/`http.sslCAInfo`) in the client environment. Never
disable certificate verification or transfer the upstream Git credential to the
client. Keep host Git helpers, SSH, LFS, alternate transports and direct-egress
bypass outside this integration. Source/Chromium/local TLS fixtures do not qualify
live GitHub or actual sandbox egress, installed services, or native key custody.

## Scoped HTTP credentials

Use **HTTP → Credentials** to create, inspect, edit, rotate or delete a reusable HTTPS credential. It has one host/port boundary, one header, an optional fixed prefix, and one write-only secret. `Authorization` with `Bearer ` and custom API-key headers are supported. Wildcard hosts require both `*.example.com` spelling and explicit opt-in; they do not cover the apex. Transport-control headers cannot be overwritten. Credentials alone grant no HTTP access and do not start a proxy.

```bash
agent-gateway http credential list
agent-gateway http credential get ID
agent-gateway http credential create --file /private/create.json --yes
agent-gateway http credential update ID --file /private/metadata.json --yes
agent-gateway http credential rotate ID --file /private/rotation.json --yes
agent-gateway http credential delete ID --yes
```

Create files contain `name`, `boundary:{host,port,allow_wildcard}`, `recipe:{header,prefix}`, and `secret`. Update files contain the same complete metadata without `secret`; rotation files contain only `secret`. Treat input files as secrets and manage their permissions and removal yourself; Gateway neither persists nor deletes your source file. Never put a secret in argv, an environment variable or a browser URL. Browser input clears after submission, cancellation, navigation and sign-out; stored values cannot be revealed.

Update, rotate and delete accept `--etag ETAG`; omission performs one validated read first. A stale revision requires inspection and a new decision, never automatic replay. Referencing grants are visible in detail: edits must preserve their entire scope, and deletion is blocked until references are removed. Rotation preserves identity and changes future admissions; a failed or uncertain rotation may leave authority unavailable. Inspect metadata before submitting another secret. Restoring a backup invalidates HTTP credential material even if old keyring entries survive; deliberately rotate to supply fresh authority. Existing MCP server credentials are separate and unchanged.

## HTTP traffic history

Use **HTTP → Traffic** or the separate read-only commands:

```bash
agent-gateway http traffic list --destination example.com --type request --outcome succeeded
agent-gateway http traffic get TRAFFIC_ID
```

`--destination` searches a case-insensitive literal hostname/IP substring:
`github` matches `api.github.com`. It never searches paths or interprets wildcard
or regex syntax. Exact filters remain `--principal-id`, `--connect-id`, `--type`,
`--decision`, and `--outcome`. Lists accept the usual `--limit`, `--cursor`, and
`--output json` controls.

In the browser, **Agent** searches current display names using the same accent/case
and limited typo tolerance as MCP Invocations, or a literal recorded agent-ID
substring. Missing current names do not erase recorded IDs; names are recognition,
not admission-time authority. Existing exact-ID diagnostic links stay exact and
show a removable **Exact agent ID** constraint. Agent and Destination host text
applies after a short pause; both accept up to 256 UTF-8 bytes without control
characters. **Clear filters** removes all constraints. Searches combine with the
other filters, including recorded CONNECT ID, across available history before
pagination. Applied filters stay in the URL through reload and Back navigation;
changing them starts a fresh matching traversal, not a filter of loaded rows. Detail preserves historical
policy selectors and credential-generation references even after grants change;
they are not current authority. Request paths, queries, headers, bodies and secrets
are never traffic evidence. CONNECT tunnels expose no inner requests. An allowed
record without completion means unknown outcome, not proof of nonexecution or
permission to retry. No grant-creation or replay action is available.

**HTTP transfer** distinguishes missing terminal evidence, historical termination
details unavailable, a recorded incomplete transfer, and an observed clean HTTP
transfer. HEAD instead reports **Headers prepared (HEAD)**: final delivery belongs
to the HTTP server and is not acknowledged by this evidence. Detail shows the
observed operation stage and condition; optional request-context cancellation or
timeout is a separate snapshot, not attribution to the client or upstream.
An HTTP 200, terminal-looking event or clean transfer does not establish application
success. Client closure can follow normal application completion, but these facts
do not establish that it did. Unknown effects remain unsafe to automatically retry.

JSON completion and summary projections carry the optional closed `termination`
object; summaries additionally expose `completion_recorded`. Historical completions
without termination details remain unchanged, including historical `succeeded`
outcomes. Upgrade the bundled Gateway, CLI and UI together; older strict readers
cannot read newly retained termination fields. No discarded error text or content
can be recovered.

The browser starts Live, pauses it when loading older records, and retains at most
500 records before requiring narrower filters or a return to newest. Manual refresh
replaces the loaded window. Shared retention can expire a cursor; the browser then
restarts at newest with a notice. MCP Invocations remains separate and unchanged.

On ordinary startup, existing selected traffic-schema-1 stores are fully validated
and transactionally receive empty HTTP tables in the same file before readiness.
MCP history and generation bindings are preserved; no upgrade command or replacement
pair is needed. Backups remain paired and restore both evidence domains. The stopped
`maintenance migrate-traffic-storage` command still rejects an already selected pair.
[Proxy listener selection and manual client setup](http-proxy.md) remain separate from control administration.

## Control-plane audit history

Use `agent-gateway audit list` and `agent-gateway audit get AUDIT_EVENT_ID`, or choose **Audit Log** in the browser navigation. This is the existing administrative audit history, including system and offline maintenance events, not an administrator directory or actor filter. Both consume the authenticated read-only `GET /api/v2/audit-events` and `GET /api/v2/audit-events/{id}` API. Generated `agent-gateway audit --help`, `agent-gateway audit list --help`, and `agent-gateway audit get --help` describe the command grammar. The [coverage matrix](../design/administrative-control-plane.md#control-plane-audit-coverage) identifies audited operator, system and offline boundaries; [implementation evidence](../maintainers/implementation-evidence.md#audit-producer-evidence) records regression provenance. Do not infer that an action never happened from an empty audit collection.

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

All filters are conjunctive and apply to server history: `--actor-type`, `--credential-id`, `--category`, `--action`, `--target-type`, `--target-id`, `--outcome`, `--correlation-id`, `--from`, and `--until`. IDs are canonical Gateway IDs. Credential filtering matches either a performing operator or a known system initiator. The time range is inclusive `from`, exclusive `until`; provide both as fixed UTC timestamps with nine fractional digits, separated by at most 366 days. Category/action pairs and actor/target/outcome values use the closed API vocabulary. These exact credential/time capabilities remain in the API and CLI, not the ordinary browser filter surface.

Lists are descending sequence, not client-sorted timestamps. `--limit` is 1–100 (default 50); the CLI reads one page and never automatically retries. Keep the same filters and the returned `history.generation` when supplying `next_cursor`. The cursor is opaque and freezes the traversal's upper sequence watermark; later events require a fresh first page. On `stale_cursor`, discard all earlier pages and restart without the cursor, comparing generations. On `audit_history_replaced`, discard previous-history data and explicitly restart without the old generation. Neither response permits combining the two histories. JSON preserves the strict API representation, including nullable fields and decimal-string sequences; human output provides event facts, retention notes, and continuation guidance.

The browser directly offers five filters: **Event**, **Performer**, **Target type**, **Target**, and **Outcome**. Target is the only text field: it matches current names with bounded typo tolerance or literal partial IDs across retained history, before pagination. Renames change recognition/search of older events; deleted targets remain searchable by ID. A search cursor whose matching targets changed must restart. Dropdowns apply immediately and Target after a short pause. Invalid drafts do not replace results or block independent valid filters. **Reset** clears draft and applied values. Old browser time/credential filters are removed when opening their links, never retained invisibly; old exact-target links become visible Target searches. Back/Forward and event links retain applied filters; returning to the list or choosing **Refresh** starts the newest matching page. **Load older audit events** appends compatible pages only. Cursor/generation comparisons are session-local, so reload loses that baseline. Replacement discards previous-history state and warns, even if the fresh read fails. A missing event never proves nonexecution. **Retained history** appears only below the main list; its **Retention details** disclosure exposes generation and boundary facts. The [browser audit contract](../design/browser-control-plane.md#audit-clients) owns exact state, link and presentation behavior.

Performer labels distinguish **Operator**, **System**, and **Offline maintenance**. A system event's optional initiating credential is attribution, not its performer or a named human. Attempts and outcomes are immutable separate events joined by correlation ID; `pending`, `failed`, `rejected`, and `unknown` must not be interpreted as success or rollback. Event detail embeds **Related events**, selected by the recorded correlation ID, with a subtle selected-event marker and manual refresh/**Load older events** controls. The count distinguishes fetched events from an injected selected event. Both tables separate Target type from the current name-first Target and full ID. **Correlation ID** remains copyable in ordinary **Event details**, alongside **Initiating credential**; absent diagnostics are omitted. The ordinary table no longer offers correlation-ID entry; old filtered links still show a removable context chip. Related events are retained evidence, not a complete transaction trace. Administrative audit is separate from **MCP → Invocations** and **MCP → Requests**: request submissions and invocation evidence are not copied here.

See the [public audit contract](../design/public-contract.md#control-plane-audit-reads) for exact response shapes. Audit stores only credential IDs/fingerprints and allowlisted reason/problem codes, never raw secrets, raw error bodies, unrestricted snapshots, or invocation payloads. No export format or permanent-retention guarantee is provided.

## Safe serve diagnostics

Schema version 2 follows the [operator diagnostic policy](../design/administrative-control-plane.md#operator-diagnostic-disclosure-policy): default-level failures include bounded explanations, affected resources and native facts where available. `untrusted_excerpt` identifies bounded child/library text; escaped controls and visible truncation prevent terminal injection and unbounded records. Known source credentials are masked, but unknown or transformed third-party secrets cannot be universally recognized. Treat logs as confidential operational artifacts and review/redact them before sharing. Public API, problem JSON and durable history/audit disclosure rules are unchanged. Source changes do not upgrade or qualify installed binaries.

Warnings and errors are enabled by default, including upstream authentication/degraded transitions, unconfirmed cleanup, foreground OAuth failure/expiration, actionable refresh failures, and recovery after a retained unhealthy incident. Use `serve --log-level info` for foreground OAuth required/completed, or `debug` for actual attempts, retry/reset timing, detailed OAuth stages, routine refresh success, and payload-free invocation/authority/storage timing. Diagnostics are always JSON lines on stderr; result/problem formatting still follows `--output`/`--json`. For a deliberately started foreground service:

```bash
# Use a private directory and a new capture file; do not overwrite an old log.
(umask 077; set -C; agent-gateway serve --log-level debug --output json 2>gateway-diagnostics.jsonl)
# Inspect complete diagnostic lines; terminal problems have no schema_version.
jq -R 'fromjson? | select(.schema_version == 2)' gateway-diagnostics.jsonl
jq -R 'fromjson? | select(.event == "storage_reject" or .event == "diagnostic_loss")' gateway-diagnostics.jsonl
# Substitute a process_id and numeric upstream_ref observed in this file, not a server ID.
jq -R --arg p 'PROCESS_FROM_LOG' --argjson u 7 \
  'fromjson? | select(.schema_version == 2 and .process_id == $p and .upstream_ref == $u)' gateway-diagnostics.jsonl
```

Use `process_id` plus `call_id` to correlate a live attempt; optional `invocation_id` is capture correlation, not proof of a retained row. Terminal-annotation success means enqueue acceptance, not persistence. Storage/authority mutation counters are owner-local. Control storage has one nonqueueing writer; authority retains bounded gate waiting. Rejection causes distinguish capacity, cancellation, stop and latch; durability failures carry a closed `stage`. Occupancy is a sample, and elapsed milliseconds are not a throughput guarantee or active transaction deadline.

For HTTP proxy failures, first distinguish Gateway-generated errors from upstream application responses. Gateway errors carry `Proxy-Status: AgentGateway; error=...`: 400 means invalid request, 407 requires proxy authentication, 403 means policy/address refusal, 429/503 can mean bounded capacity, 503 otherwise means unavailable authority/material, 502 means upstream connection/TLS/protocol failure, and 504 means upstream timeout. Upstream statuses and bodies are preserved without Gateway error provenance. Do not infer permission to retry, especially after response interruption or uncertain effects.

An approved HTTP/Git/opaque CONNECT destination can use another address from its already validated DNS answer set if TCP establishment fails. All candidates share one finite dial budget; no new DNS query or policy permission is acquired. This is not another application operation. Only one connected candidate receives TLS/application bytes, and TLS validation or any post-handoff failure never triggers fallback. When all TCP candidates fail, a typed timeout among them produces 504; otherwise connection failure produces 502. These responses and diagnostic `exchange` facts do not establish whether a later application handoff executed an effect. Inspect the authoritative upstream before deciding on a caller-owned retry; neither multiple TCP attempts nor missing history changes that rule.

Look for default-level `http_proxy_rejected` (pre-dispatch infrastructure) or `http_proxy_failure` (capacity/transport/panic) warnings. Match a received `Gateway-Request-ID` or CONNECT `Gateway-Connection-ID` to diagnostic `proxy_id` when available; these random values confer no authority and do not prove a traffic row exists. Old warnings and parser-level failures may have no correlation. `stage` distinguishes the observed operation; `expired` requires typed timeout/deadline evidence, while `duration_ms` is elapsed handling time, not stage duration. An empty Traffic view or missing diagnostic does not rule out execution: the bounded diagnostic sink is deliberately lossy. After headers or CONNECT establishment, a failure can appear as EOF/reset rather than another HTTP error. A preceding upstream 200 can still have an incomplete transfer even if a client consumed an application-level terminal stream event.

```bash
jq -R 'fromjson? | select(.event == "http_proxy_rejected" or .event == "http_proxy_failure")' gateway-diagnostics.jsonl
```

At `info`, `reconciliation_displaced` explains that OAuth/lifecycle replacement displaced an operation; successful verified settlement appears as Superseded in operation detail/history, never Succeeded on behalf of its replacement. At the default `warn` level, `reconciliation_settlement_failure` reports `capacity`, `unavailable`, or `stopped` with bounded local cause/operation detail where available. Read authenticated operation/audit details to reconcile the affected work. Unconfirmed cleanup or failed settlement remains fail-closed; repeated reauthorization does not repair it. Existing orphan rows are not repaired online: follow [stopped-process recovery](backup-and-recovery.md), and obtain separate authorization before stopping or restarting a live service. Do not infer replay safety from an absent log or a healthy replacement.

To identify an upstream, open **Status → Technical details → Diagnostic correlation**, or run `agent-gateway mcp server get SERVER_ID --json` and inspect `runtime.diagnostic_correlation`. Match both fresh `process_id` and `upstream_ref` to the warning; `attempt_ref` distinguishes workers/flows. These opaque process-local references are not resource IDs, PIDs or credentials, and restart invalidates their meaning. API references are decimal text; stderr uses numbers. Missing correlation means unavailable correlation, not absence of work; default failure details do not depend on it. A local `resource` may directly name the affected server/tool, destination or path. Never substitute resource/credential IDs or cached prior-process references.

Every warning/error has a fixed `action`, never upstream-provided instructions:

| Action                         | Safe next step                                                                                                                  |
| ------------------------------ | ------------------------------------------------------------------------------------------------------------------------------- |
| `inspect_status`               | Read authenticated server/system status; the diagnostic does not establish a specific remedy.                                   |
| `authorize_upstream`           | Inspect Authentication and current flow status before deliberately authorizing.                                                 |
| `inspect_credentials`          | Inspect credential/keyring posture; do not copy credentials into logs.                                                          |
| `inspect_connection`           | Inspect connection settings and reachability without replaying tool calls.                                                      |
| `inspect_configuration`        | Inspect transport, protocol and catalog configuration against the public reason.                                                |
| `wait_scheduled_retry`         | Gateway already scheduled follow-up; wait and inspect status rather than starting duplicate work.                               |
| `inspect_settlement_no_replay` | Inspect operations and cleanup; never replay uncertain execution. Follow stopped-process recovery only with separate authority. |
| `storage_recovery`             | Follow [backup and recovery](backup-and-recovery.md); do not attempt online repair or replay.                                   |
| `inspect_diagnostic_sink`      | Inspect log destination, permissions and capacity; missing records prove nothing about execution.                               |
| `no_action`                    | Healthy publication was observed after retained unhealthy history; no diagnostic action is needed.                              |

Typical subsequences (other producers may interleave, and records may be lost):

| Situation                    | Expected evidence                                                                                                                                       |
| ---------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Authentication needed        | `upstream_unhealthy` at warn with `operator_authentication_required`; preparing a foreground flow adds `oauth_required` at info                         |
| Authorization succeeds       | `oauth_completed` at info, then a new reconciliation attempt; `upstream_recovered` at warn only after healthy active publication                        |
| Transient transport failure  | Debug attempt start/completion and `upstream_retry_scheduled`; warn transition identifies the connection/initialization/discovery phase and safe reason |
| Persistent identical failure | Individual attempts remain debug; the next repeated failure after one minute can emit a warning with an intervening `suppressed` count                  |
| Recovery                     | Warn shows one recovery when unhealthy history was retained; routine healthy catalog polls stay quiet                                                   |
| OAuth failure/expiration     | Warn `oauth_failed` or `oauth_expired`; detailed stage starts are debug; successful routine background refresh is debug, not info                       |
| Unconfirmed stop             | Warn `cleanup_uncertain` / `stop_unconfirmed`; inspect authenticated state and do not infer that another authorization attempt repairs cleanup          |

`reconciliation_settlement_failure` blocks further reconciliation until stopped-process recovery: Connect can remain Running while its runtime is degraded. This is not an MCP initialization timeout; raising transport timeouts or repeating authorization cannot repair it. Normal completion retries only bounded pre-mutation storage acquisition, never server execution or uncertain writes. See the [settlement contract](../design/downstream-servers.md) for exact bounds.

`retry_scheduled` means scheduled follow-up, not an executing attempt; `stopped` means no retry was scheduled for that stopped work, and `unknown` establishes neither cause nor next action. Elapsed time is not a deadline. `oauth_completed` alone does not establish routability: read current server/catalog state. Recovery and suppressed counts describe retained observations only; loss, eviction and restart can erase evidence. The [diagnostic contract](../design/administrative-control-plane.md#serve-diagnostics) owns timing, suppression and correlation mechanics. Missing completion never proves timeout, rollback or safe replay.

Current logs omit most underlying errors and operational names/paths even at debug; that is an implementation limitation, not the intended disclosure policy. Useful local error text and context should be retained with targeted secret masking and bounded output. Actual credentials, private keys, authorization/cookie values, OAuth secrets and sensitive payloads remain excluded. Bounded child failure excerpts require source-aware handling; generic redaction cannot guarantee arbitrary child text is secret-free. Review logs before forwarding or publishing them. They are lossy diagnostics, never audit evidence or permission to replay. Full queues drop newest; aggregate `diagnostic_loss` counts may themselves be unavailable on a broken sink. Shutdown waits at most one additional second for output. A stalled/broken destination can lose any severity or the terminal problem; a short write or forced exit can leave an incomplete final line. `fromjson?` above intentionally skips malformed lines—inspect the original file when investigating loss. Diagnostic loss alone does not make clean storage unclean or change a successful service exit. No rotation or retention service is included. The operator owns file permissions, retention, rotation, and deletion for both foreground captures and launchd log files; use owner-only captures (for example, set `umask 077` before redirection) and review only the necessary bounded interval. Choosing a level for an already running launchd job requires an explicitly authorized configuration/restart; reading or tailing a log does not restart Gateway.

## Output and failures

Human output is the default. Online resource commands and service commands accept `--output json` or `--json`; conflicting selectors fail before work begins. `init`, `doctor`, maintenance and CA commands use `--json`. CA `--output` is a certificate file path, never a format selector; `--stdout` explicitly streams public PEM and conflicts with JSON.

Stopped mutations display their safe plan on stderr before consent and mutation, including with `--confirm`. With `--json`, stderr is a diagnostic JSON-lines stream: a plan may precede a terminal problem. Successful finite results alone use stdout; errors leave stdout empty. Dry-run plans are results on stdout and need no confirmation. Doctor emits one partial checklist, with authenticated public status under `system` when `--online` succeeds; individual failed checks remain visible rather than being suppressed by a single global status.

Finite successes write to stdout. Finite and pre-start failures leave stdout empty and write one bounded problem to stderr; serve may additionally emit the separate diagnostic lines described above. Problems retain stable codes and typed exit classes so automation can distinguish invalid input, authentication, conflict, unavailable storage, and uncertain outcomes without parsing prose.

Lists return one page and use command-scoped `--limit`, `--cursor`, and filter flags. When another page exists, human output ends with `NEXT_CURSOR`; JSON retains the exact `next_cursor` member. Supply that cursor explicitly for the next page. Closed JSON requests reject duplicate, unknown, missing, or trailing values. Command input is intentionally split:

| Input mode                                  | Commands                                                                                                                                |
| ------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------- |
| Direct flags only                           | `admin credential create`, `agent create`, `agent update`, `mcp server operation start`, `mcp grant update`, `mcp grant-request reject` |
| Strict `--file` only                        | `mcp server create`, `mcp server credential replace`                                                                                    |
| Direct flags or strict `--file`, never both | `mcp server update`, `mcp grant create`, `mcp grant-request approve`                                                                    |

Use `--file PATH` or `--file -` for the strict file form. `--file -` conflicts with `--admin-bearer-stdin`. Constrained grant and approval shapes require the file form; their direct forms cover the ordinary unconstrained case. Strict files accept permanent v1 `{"equals":{...}}` constraints and closed v2 `{"version":2,"equals":{...},"regex":{...}}` constraints, validate them against the matcher compiler's grammar and limits, and preserve lexical number and regex bytes through submission. Table output identifies `v1 equals` or the v2 equality/regex atom counts. Both grant creation and approval forms accept an optional human-readable `description` (`--description` in direct mode). Grant descriptions are display metadata; `mcp grant update` changes or clears only that metadata under an exact ETag. Credential issue, rotate, and revoke commands have no request-document input.

## Command families

Use generated help rather than copying a full command inventory into documentation:

```bash
agent-gateway --help
agent-gateway mcp server --help
agent-gateway mcp catalog --help
agent-gateway agent --help
agent-gateway mcp grant --help
agent-gateway mcp grant-request --help
agent-gateway mcp invocation --help
agent-gateway audit --help
agent-gateway backup --help
agent-gateway admin credential --help
agent-gateway maintenance reset-admin-credentials --help
```

Focused workflow ownership:

- [Upstream server configuration](upstream-servers.md)
- [Access control](access-control.md)
- [Invocation evidence and unknown outcomes](invocation-evidence.md)
- [Backup, restore, and recovery](backup-and-recovery.md)

Consult [Administrative control plane](../design/administrative-control-plane.md) for normative CLI and administrator trust boundaries, and [Public contract](../design/public-contract.md) for public limits and failure vocabulary. Return to the [documentation map](../README.md) or [Gateway README](../../README.md) for installation and common workflows.
