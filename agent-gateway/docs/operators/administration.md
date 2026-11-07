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

The zero-argument installation uses the default root and stores its administrator bearer at `<effective-data-dir>/admin-bearer`. Ordinary `init` creates only in an absent or empty safe directory. It asks for default-no confirmation (`[y/N]`); `--confirm` permits noninteractive consent. Setup provisions control/traffic storage, the protected master key, initial administrator authority, MCP and HTTP/Git proxy capability, the CA and its public `<root>/http-ca.pem` export. It is ready for `serve` but not listening. No supervisor, client trust, shell settings or client proxy settings are installed. `initialize` remains a quiet alias.

An existing completed installation returns an already-initialized result and suggests `serve` or `doctor`, with no changes. This does not authenticate a bearer or validate optional history, even while Gateway is running; it is not a current health check. Missing credentials or certificates never authorize regeneration or a reset.

For recognized interrupted setup only, inspect the selected directory with `doctor`, then explicitly continue using the same directory and recorded credential destination:

```bash
agent-gateway doctor --data-dir /path/to/gateway-data
agent-gateway init --resume --data-dir /path/to/gateway-data --confirm
```

Resume retains one stopped owner, reconciles durable evidence and completes only proven-missing work. It preserves established key, CA and credential identity. Unknown/damaged evidence, changed files, missing established material, uncertain publication, unresolved recovery markers or nonempty WAL/journal state can require a separate stopped diagnosis rather than continuation. A refusal is not permission to rerun an uncertain step, replace a credential, delete the directory, remove evidence or discard SQLite sidecars. Keep `<root>/initialization` with the installation; it is local progress evidence, not a portable configuration export. `serve` refuses incomplete initialization. For portable configuration, initialize/start normally, then use [online configuration import](#import-configuration-online).

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
agent-gateway doctor
```

`serve` also enables HTTP proxying at `127.0.0.1:8212` by default. Select a custom
address with `--http-proxy-listen`, or use `--clear-http-proxy-listen` for MCP-only
startup; these selections conflict. Missing CA signing material or a failed bind
prevents readiness. Existing managed disabled/custom selections remain unchanged;
see [HTTP listener upgrade guidance](upgrade-compatibility.md#http-listener-default-cutover)
and [manual proxy/client trust setup](http-proxy.md).

`serve --listen` accepts only a canonical numeric IPv4 loopback address and explicit port. Online `--address` accepts a canonical numeric `127/8` HTTP URL or an explicitly trusted hostname HTTP URL with a canonical decimal port (1–65535). Wildcard and non-loopback numeric destinations, URL userinfo, paths (including a trailing slash), queries, fragments, forwarding headers, redirects, ambient proxies, cookies, compression, and automatic transport retries are not accepted. When a selected loopback address refuses the connection, every online leaf reports `gateway_not_running` and renders the exact `agent-gateway serve` command for the selected address and explicit data directory. A hostname refusal instead directs you to check forwarding and the numeric-loopback service; a hostname is never a valid `--listen` value.

`GET /livez` is unauthenticated process liveness. `GET /readyz` reports only listener readiness, not installation identity. Plain `doctor` automatically reads readiness and authenticated public status using `--admin-bearer-file` or the selected default, with a shared three-second live deadline. It adopts status only after matching selected installation evidence; a different listener, unknown legacy identity, missing credential or timeout leaves live evidence explicitly unavailable/unverified. File presence never proves usable authority. Explicit hostname forwarding remains an operator trust decision, not cryptographic listener authentication.

Default output leads with the selected directory, running/reachable state and actionable problems/setup tasks. Healthy supporting facts and unverified capabilities appear with `--verbose`, without additional work. Paths inside the displayed directory are relative; external paths remain absolute. JSON retains all independent `checks` plus matched API status under `system`. Its `result` is `attention` for known failure/degradation, otherwise `incomplete` for missing setup/live evidence, otherwise `healthy`. Emitting a checklist exits zero, including partial/attention results; usage, selection and output failures remain nonzero. A healthy sibling never hides a failed check.

Doctor never repairs, clears markers, opens writable SQLite, scans integrity, accesses protected custody, refreshes tokens, starts OAuth/subprocesses, probes upstreams or invokes tools. Stopped-only metadata uses the existing no-create lock and refuses nonempty WAL/journal; normal startup and explicit stopped operations retain full validation. `--online` and `--verify-storage` are removed without aliases. Imported unset credentials and explicit authorization tasks are reported without resolving material; disabled servers are inactive, not warnings. External client CA trust remains unverified. Native supervisor configuration/logs and client environments are not inspected. Missing files alone never justify administrator reset, reinitialization or replacement of established keys.

### Serving without optional evidence

`doctor --verbose` and browser **System → Status** show serving, optional history,
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
  doctor --admin-bearer-file /safe/new/admin-bearer
```

See [Recovery and fresh initialization](backup-and-recovery.md) for replacement-authority workflows.

## Export portable configuration

Run the authenticated online command against the selected Gateway:

```bash
(umask 077; agent-gateway export config > gateway-config.json)
# Explicitly select a different installation's administrator credential:
(umask 077; agent-gateway --data-dir /path/to/gateway-data export config > gateway-config.json)
```

`agent-gateway export --help` and `agent-gateway export config --help` describe the
command. It always writes one versioned JSON document to stdout, without progress,
confirmation or a human summary. There is no file flag, output-mode selector,
per-protocol filter or secret-inclusion option. The shell owns file creation and
permissions: `umask 077` makes a new file owner-private, but does not repair an
existing file's permissions. Use a new private destination, verify the exit status
and discard incomplete output after failure. Diagnostics go to stderr; do not merge
stderr into the export.

The export contains agent settings, MCP definitions and intended enabled settings,
exact MCP/HTTP/Git policies and bindings, Git repositories/routing intent, and
nonsecret credential definitions with portable secret references. It preserves
expired grants and inactive deleted targets; it does not restore them to active
access. No healthy upstream, configured secret value or external connection is
needed. The command changes no storage or upstream state.

Secret values, administrator/agent bearers, OAuth tokens/client secrets,
installation master keys/CA identity, internal IDs/revisions, history, pending
requests, operations and runtime/session state are excluded by projection, not by
scanning. Supported MCP stdio secret slots and credential bindings remain explicit
references even when material is absent. Ordinary authored strings are preserved:
review arguments, ordinary environment values, URLs, headers and names before
sharing because accidental secrets in unsupported fields cannot be detected
reliably. Keep exports private even though supported secret material is excluded.

This command emits the sole [version-two format](../design/public-contract.md#version-two-resource-and-bundle-contract),
with permanent typed keys on every definition and grant. Individual backend
exports use the identical definition envelope. Version one and `init --from` are
removed without translation or parallel legacy support. Resource CLI import/export
uses that same public API and format. Top-level `import` accepts a bundle or one
definition; resource imports remain exactly one matching definition. Browser
import controls are a separate surface.

The current command's format is independent of database schema versions and is the portable configuration transfer/recovery artifact, not
an installation backup or secret backup. Absolute executable/working-directory
paths and configured OAuth callback addresses may still need deliberate adaptation
on another host. Export supplies no material; [online import](#import-configuration-online)
uses the running installation's administrator API. Installation-snapshot backup/restore is [removed without artifact cleanup](backup-and-recovery.md#backup-and-restore-removal); unrelated stopped recovery remains supported.

## Import configuration online

For a new installation, initialize and start it normally before importing:

```bash
agent-gateway init --data-dir /path/to/new-gateway --confirm
agent-gateway serve --data-dir /path/to/new-gateway
```

In another terminal, select the receiving installation's administrator credential:

```bash
agent-gateway --data-dir /path/to/new-gateway import --file gateway-config.json --dry-run
agent-gateway --data-dir /path/to/new-gateway import --file gateway-config.json --confirm --json
# Standard input can supply the same document; do not also use bearer stdin.
agent-gateway --data-dir /path/to/new-gateway import --file - --dry-run < gateway-config.json
agent-gateway --data-dir /path/to/new-gateway doctor --verbose
```

`agent-gateway import --help` describes the top-level grammar. It accepts the sole version-two
bundle or individual definition, not a second file format. Inspect every advisory
creation, reserved-key binding, dependency and declared credential/activation
requirement before confirming. There is no prompt: without `--confirm` or
`--dry-run`, nothing is submitted. JSON output preserves the complete public result;
human output lists every creation, including reservations and singleton routing.
Whole-gateway export remains **only** `export config`; it includes all retained
configuration without filters, dependency traversal or selected-subgraph modes.

The CLI calls `POST /api/v2/config/preview` for read-only dry runs and
`POST /api/v2/config/import` for confirmed atomic creation against that running
instance. API clients may use the same administrator bearer or browser session.
These queryless API operations use the normal
administrator authentication, Origin/CSRF and JSON-media-type controls. The [API contract](../design/public-contract.md#online-validation-and-create-only-import)
owns exact representations and bounds. Never place bearer/secret values in a
portable definition, URL, shell argument or shared transcript.

### Import or export one resource

Use `import` and `export` beneath `agent`, `mcp server`, `mcp grant`,
`http credential`, `http grant`, `git credential`, `git repository`, or `git grant`.
Each import accepts exactly one matching definition, never a bundle or implicit
dependencies. For bulk input use top-level `import --file PATH`; do not pass
bundles to resource commands. Export selects a permanent public key,
not the internal ID used by get/update/delete, and includes no dependencies.

```bash
# Source: stdout is one backend version-2 JSON document; keep it private.
(umask 077; agent-gateway agent export build-agent > agent.json)
# Destination: inspect advisory bindings and requirements, then create once.
agent-gateway agent import --file agent.json --dry-run
agent-gateway agent import --file agent.json --confirm
# Standard input is supported; bearer input must use a different source.
agent-gateway mcp grant import --file - --dry-run < grant.json
agent-gateway mcp server import --file server.json --confirm --json
```

No confirmation means no submission; there is no prompt. `--dry-run` and
`--confirm` are mutually exclusive. A dry run calls only backend read-only preview.
Human output names creations, bindings and declared requirements; `--json` retains
the public structured result. Actual import reports configuration `committed` and
activation `pending` or `not_required`, not credential availability or readiness.
Failure after handoff never retries. Inspect lists by key and server status after
an uncertain result; do not blindly reimport. Broken stdout can leave a partial
export file; discard it rather than treating it as usable input.

Ordinary resource `create` commands accept optional `--key`; JSON create files
also accept `key` (MCP uses its existing `namespace`). Supply the key only once.
Omission asks the backend for a human-friendly permanent key, including an omitted
MCP namespace. Lists, reads and creation output retain that key alongside local
IDs; MCP's namespace is its key. `get`, updates and deletions still address IDs.

Preview is read-only and advisory, not a reservation or a promise that import will
still succeed. Neither validation nor preview reads secret values, launches a
process, contacts an upstream or starts OAuth. Import revalidates current state
and creates exactly the explicit graph in one atomic configuration/audit
transaction. References can bind existing resources with matching typed keys;
there is no remapping, automatic dependency creation or implicit grant. Any
submitted existing/reserved key conflicts, even identically. Domain/capacity,
dangling-reference, conflict, SQL or mandatory-audit failure leaves no partial
configuration. Review authored strings for accidental secrets before sharing.

The keyless Git routing profile has one narrow creation rule: explicit input may
initialize only an untouched empty revision-one seed with no historical
initialization-import receipt. Even importing empty origins consumes that one-time
eligibility; a later identical import or a profile edited/reset to empty conflicts.
Omitting routing leaves it unchanged. This permits a whole-bundle transfer into a
fresh initialized instance without authorizing updates to established routing.

A successful import acknowledges **committed configuration**, not successful
activation. Imported enabled servers enter normal lifecycle only after commit;
missing material or startup failure leaves visible blocked/failed resources. There
is no new first-reload fence. Check `mcp server list/get` and runtime status rather
than reimporting a failed server. Import never infers consent to authorize OAuth
or acquire credential material. Disabled imports remain disabled.

After import:

1. Run `doctor --verbose` to identify unset material. Keep each bound definition;
   supply HTTP/Git values with `http credential rotate ID --file PATH` or
   `git credential rotate ID --file PATH`. Supply MCP static/client secrets with
   `mcp server credential replace ID --file PATH`; authorize OAuth separately using
   the [upstream procedure](upstream-servers.md).
2. Use `agent list`, then `agent credential issue ID --secret-output NEW_PATH` for
   required fresh agent bearers. Source tokens have no imported authority.
3. Verify activation independently. A new deliberate reload may be appropriate
   after correcting a blocked server; inspect the exact operation and never
   automatically replay an uncertain mutation.
4. Configure clients deliberately. Import does not replace the receiving
   installation's administrator authority, encryption key, traffic identity or CA.
   For a new installation, explicitly adopt its endpoint, agent credentials and
   public CA; source trust does not transfer.

If the import response/commit acknowledgment is lost, do not automatically submit
again. Inspect resources by their typed keys and runtime status to establish
current committed state; an identical resubmission is still a conflict, not an
idempotent retry. Configuration files are reusable creation input, never updates,
synchronization or an ongoing source of authority.

Historical initialization imports are not resumable in this version. Preserve
all retained input, plans, receipts and databases and obtain a separately reviewed
recovery plan for an incomplete old import. Completed old initialization remains
a read-only no-op, even with retained temporary input; this binary performs no
legacy cleanup. Ordinary interrupted setup still uses explicit `init --resume`.

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
agent-gateway doctor --admin-bearer-file /safe/new/admin-bearer
```

Rotation conditionally creates one non-expiring replacement, durably publishes and securely reopens the file, verifies its metadata and authentication, and only then conditionally revokes the named old credential. It never promotes the replacement into the default bearer path. If completion is uncertain, do not replay: retain the replacement file and use the rendered metadata command to inspect the old and new records. Before replacement verification, workflow-owned failures preserve old authority; after verified publication, an incomplete workflow may intentionally leave both credentials active.

If durable publication itself fails after creation, the output path may contain unverified secret material but must not be trusted or used as credential input; secure or remove it. This workflow does not revoke the old credential, but expiration or concurrent administrator action may still make it unusable. An active replacement record without a durably verified bearer may also exist. If the pre-rotation credential remains active, use it to inspect metadata; otherwise use another active administrator credential. Explicitly revoke an unusable replacement if present, and perform any later rotation as a fresh deliberate operation.

Use stopped-process `agent-gateway maintenance reset-admin-credentials` only for all-authority recovery. The command tree migrated immediately: `admin credential ...` and `maintenance reset-admin-credentials` are the only administrator spellings, and the legacy hyphenated forms perform no work and have no aliases.

For governed call evidence, `outcome_unknown` means the effect may already have happened. See [Invocation evidence and unknown outcomes](invocation-evidence.md). For stopped-process failures, see [Recovery and fresh initialization](backup-and-recovery.md).

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
agent-gateway doctor --address http://host.lima.internal:8210 \
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

Follow [Git setup](git.md) for repositories, read/ref grants, Git-only credentials, routing profiles and a first native read-only check. Git policy is independent of HTTP and MCP. Enabling an origin prevents opaque-tunnel bypass; removing it returns traffic to ordinary HTTP policy. Review the complete proposed routing list and never replay an uncertain update.

## Git traffic history

Use **Git → Traffic** or `agent-gateway git traffic list|get`. The [Git evidence guide](git.md#git-traffic-history) explains admission, transport and upstream-reported outcomes. HTTP 200 is not proof of push success; missing completion is unknown, not permission to retry.

## Complete missing credential setup

Imported definitions can retain their scopes and references before values are
supplied. **Not configured** means no material has been initialized, not that an
upstream rejected or expired it. **Sign-in required** identifies initial OAuth
authorization. Unset required authentication blocks only dependent operations;
Gateway never substitutes ambient credentials or public access.

Use the existing definition: supply HTTP/Git values with **Rotate** (or
`http credential rotate` / `git credential rotate`), and MCP values with
`mcp server credential replace`. For OAuth, supply a required client secret first,
then start the server's authorization flow. Keep the ID and bindings; do not create
a duplicate credential or use dummy values. Reads/JSON expose only safe setup
state (`material_state` for HTTP/Git), never values. Ordinary credential creation
still requires a real secret; only the internal import staging seam creates unset
scoped definitions. See [online configuration import](#import-configuration-online) for create-only transfer and separate activation results.

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

Update, rotate and delete accept `--etag ETAG`; omission performs one validated read first. A stale revision requires inspection and a new decision, never automatic replay. Referencing grants are visible in detail: edits must preserve their entire scope, and deletion is blocked until references are removed. Rotation preserves identity and changes future admissions; a failed or uncertain rotation may leave authority unavailable. Inspect metadata before submitting another secret. Deliberate credential rotation may be needed after checking expiry or revocation. Installation-snapshot restore is removed; preserve [historical artifacts and key custody](backup-and-recovery.md#backup-and-restore-removal).

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

Browser Agent and Destination host searches apply across retained history before
pagination. Agent names are current recognition, not admission-time authority;
recorded IDs survive missing names. Exact-ID/CONNECT diagnostic links keep their
constraints visible. Changing filters starts a fresh traversal. See the
[browser contract](../design/browser-control-plane.md) for controls and URL behavior.
Detail preserves historical policy selectors and credential-generation references,
not current authority. Request paths, queries, headers, bodies and secrets
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
pair is needed. Preserve retained traffic through [history export](backup-and-recovery.md#export-optional-history). Installation-snapshot backup/restore and legacy single-store traffic cutover are removed. Existing selected pairs retain their normal startup validation; no migration command or automatic extraction remains.
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

In the browser, filter by Event, Performer, Target type, Target and Outcome. Target searches current names or literal partial IDs across retained history; renames affect recognition, not recorded authority. Live starts enabled, polls only while visible and pauses replacement when you load older events. Refresh starts newest; cursor/generation changes require a fresh traversal. The [browser audit contract](../design/browser-control-plane.md#audit-clients) owns exact filtering, link and presentation behavior. A missing event never proves nonexecution.

Performers are **Operator**, **System**, or **Offline maintenance**. A system event's initiating credential is attribution, not a named human. Attempts and outcomes are separate immutable events joined by correlation ID; `pending`, `failed`, `rejected` and `unknown` establish neither success nor rollback. Read **Related events** manually for that correlation, not a complete transaction trace. MCP request submissions and invocation evidence are not copied into administrative audit.

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

Current human stderr and schema-2 serve diagnostics retain bounded, source-masked causes and relevant operational names/paths. Public API/problem JSON remains separately constrained; not every failure has correlation or complete detail. Actual credentials, private keys, authorization/cookie values, OAuth secrets and sensitive payloads remain excluded. Bounded child failure excerpts require source-aware handling; generic redaction cannot guarantee arbitrary child text is secret-free. Review logs before forwarding or publishing them. They are lossy diagnostics, never audit evidence or permission to replay. Full queues drop newest; aggregate `diagnostic_loss` counts may themselves be unavailable on a broken sink. Shutdown waits at most one additional second for output. A stalled/broken destination can lose any severity or the terminal problem; a short write or forced exit can leave an incomplete final line. `fromjson?` above intentionally skips malformed lines—inspect the original file when investigating loss. Diagnostic loss alone does not make clean storage unclean or change a successful service exit. No rotation or retention service is included. The operator owns file permissions, retention, rotation, and deletion for both foreground captures and launchd log files; use owner-only captures (for example, set `umask 077` before redirection) and review only the necessary bounded interval. Choosing a level for an already running launchd job requires an explicitly authorized configuration/restart; reading or tailing a log does not restart Gateway.

## Output and failures

Human output is the default except for `export config`, which always emits its versioned JSON document and has no output-mode selector. Other online resource commands and `serve` accept `--output json` or `--json`; conflicting selectors fail before work begins. `init`, `doctor`, maintenance and CA commands use `--json`. CA `--output` is a certificate file path, never a format selector; `--stdout` explicitly streams public PEM and conflicts with JSON.

Stopped mutations display their safe plan on stderr before consent and mutation, including with `--confirm`. With `--json`, stderr is a diagnostic JSON-lines stream: a plan may precede a terminal problem. Successful finite results alone use stdout; errors leave stdout empty. Dry-run plans are results on stdout and need no confirmation. Doctor emits one partial checklist, with authenticated public status under `system` only when selected-installation evidence matches; individual failed checks remain visible rather than being suppressed by a single global status.

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
agent-gateway history --help
agent-gateway admin credential --help
agent-gateway maintenance reset-admin-credentials --help
```

Focused workflow ownership:

- [Upstream server configuration](upstream-servers.md)
- [Access control](access-control.md)
- [Invocation evidence and unknown outcomes](invocation-evidence.md)
- [Recovery and fresh initialization](backup-and-recovery.md)

Consult [Administrative control plane](../design/administrative-control-plane.md) for normative CLI and administrator trust boundaries, and [Public contract](../design/public-contract.md) for public limits and failure vocabulary. Return to the [documentation map](../README.md) or [Gateway README](../../README.md) for installation and common workflows.
