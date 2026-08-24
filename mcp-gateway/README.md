# MCP Gateway

MCP Gateway is a locally secure, deny-by-default service foundation for governing MCP access. It is developed beside MCP Broker as an independent Go module and clean-start successor; it does not change MCP Broker behavior or migrate its state.

## Current status

The current executable implements the complete S1 filesystem, SQLite, admin authority, typed keyring/generation, strict loopback HTTP, control API, isolated dual-era MCP ingress, verified backup/restore, invalidation events, fixed admission, and lifecycle foundations. The S2 foundation adds the closed dynamic-server contract, shared bounded strict JSON, a durable server-domain repository, transaction-fenced server credential publication/invalidation, and authenticated desired-server and operation resources. Server definitions are secret-free and strictly validate the stdio or Streamable HTTP union before durable commit; creation/operation idempotency, snapshot pagination, strong ETags, exact preconditions, permanent tombstones, closed explicit-operation admission, refresh attachment, and behavioral supersession are enforced. A process-local manager now reconstructs enabled desired state after readiness, serializes each server behind a global-four nonblocking bound, fences stale results, and applies the fixed coalesced retry schedule through an injected driver. A bounded direct stdio supervisor provides clean runtime-only environments, process groups, framed output limits, safe exit classes, and verified graceful/forced cleanup. Behavioral lifecycle work fences and withdraws publication before stop, gates replacement after `stop_unconfirmed`, and publishes only the current replacement before operation success; ownership is memory-only, so restart never acts on stale PIDs and an uncatchable Gateway crash may require operator cleanup of an orphan. A Gateway-owned downstream foundation now adds strict local JSON-RPC envelopes/IDs, bounded stdio and Streamable HTTP exchanges, exact role-built MCP headers, exact modern/legacy/auto negotiation, immutable runtime-local legacy sessions, and a shared hardened remote factory with canonical destinations, fresh DNS validation, address-pinned dialing, platform TLS, and no proxy, redirect, cookie, compression, retry, or permissive SDK transport path. The production driver still performs no downstream activation work and fails closed until composition is wired. Asynchronous reconstruction now validates only the credential generations required by each transport before reaching that driver: exact static slot sets, bearer authority, or current OAuth registration/client/token bindings and expiry. Credential-free transports perform no keyring work. Missing, locked, interaction-required, unavailable, and unsupported provider states remain distinct safe status/reason classes; admission saturation affects only the dependent reconciliation. Listener readiness and unrelated runtimes do not wait for credential reads. First-signal shutdown fences and withdraws every active runtime synchronously, launches all runtime stops concurrently outside reconciliation admission, drains keyring consumers before storage closure, and remains bounded by the existing ten-second process deadline; unverified cleanup leaves the run marker unclean for startup verification. Production `/mcp` remains deny-all; downstream activation/publication, durable catalog resources, principals, grants, tool routing, invocation, and product UI workflows are not implemented.

## Development

Requirements: Go 1.25.13 or later, GNU Make, and the repository's development tools.

```bash
make build
make test
make test-keyring-native # isolated native backend, or an explicit prerequisite skip
make lint
make audit
```

From the repository root, `make build`, `make test`, and `make check` include MCP Gateway.

The shared `internal/strictjson` parser requires explicit byte/depth bounds, rejects invalid UTF-8, duplicate members, excessive depth or size, trailing values, and unknown members for closed destinations, and supplies object-order-independent canonical equality while preserving array order. `internal/contract` remains the sole S1/S2 vocabulary owner.

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

## Local service and control API

Start a verified installation on the exact default authority:

```bash
mcp-gateway serve --data-dir /path/to/mcp-gateway-data
```

Use `--listen 127.0.0.1:<port>` to select another numeric IPv4 loopback authority. Host aliases, wildcard and non-loopback binds, forwarding headers, alternate Host values, cross-origin requests, CORS, unknown paths, and unsupported methods are rejected. Successful startup emits one safe JSON line containing the authority and installation ID; detailed status requires admin authentication.

`GET /livez` reports only process liveness and `GET /readyz` reports only ready/not-ready. The online S1 control surface exchanges an admin bearer at `POST /api/v1/admin-sessions`, manages bounded admin credential metadata at `/api/v1/admin-credentials`, and exposes `GET /api/v1/system-status`. A browser client signs in by posting exact `{}` with the bearer, then keeps the returned CSRF value only in memory and uses the host-only, `HttpOnly`, `SameSite=Strict` session cookie. Session requests require the exact configured Origin; unsafe requests additionally require JSON and the CSRF header. Logout, expiry, parent revocation, shutdown, and restart close the session. Every API response is `no-store`, no endpoint emits CORS headers or ETags, and a newly created bearer appears only in its authenticated creation response.

The embedded `/` shell has a restrictive self-only content security policy and no product workflow. `/oauth/callback` consumes one recognized state through a fixed eight-slot nonblocking registry and returns only fixed nonreflecting no-store HTML with a deny-all CSP; invalid, replayed, expired, stale, or saturated callbacks expose no query or dependency text. Authenticated `/api/v1/backups` supports bounded create/list/read/delete operations; creation requires a 1–128 byte visible-ASCII `Idempotency-Key`, and retries by the same admin authority return the original safe metadata.

Authenticated `GET /api/v1/events` is a best-effort SSE freshness channel. It emits only closed safe invalidations, including `admin_credentials`, `system_status`, `backups`, `servers`, and `server_operations` for implemented resources, never secrets or event IDs. There is no replay cursor: reconnecting clients must reload authenticated snapshots. Sixteen streams with sixteen buffered invalidations each are admitted globally; slow consumers are disconnected rather than allowed to queue unbounded work. Streams close with their session or bearer and on shutdown.

## MCP ingress

Production agent authentication is deny-all, so no deployed `/mcp` request reaches protocol classification. The replaceable authenticated seam nevertheless isolates the two supported wire eras for later slices: `2026-07-28` is stateless and requires matching `Mcp-Protocol-Version` and per-request `_meta.protocolVersion`; `2025-11-25` uses bounded, in-memory sessions bound to the reauthenticated principal and credential. Modern requests never accept legacy session state, malformed modern claims never downgrade, and all legacy sessions disappear on invalidation, expiry, deletion, shutdown, or restart. No tools or list-change capability are advertised.

MCP work, MCP streams, and legacy sessions have independent compiled nonblocking limits of 32, 32, and 128. Saturation rejects immediately instead of queuing. Positive protocol fixtures use only an injected test authenticator; the executable does not issue or accept production agent authority.

## Downstream protocol compatibility

The activation-ready downstream seam selects each runtime independently. `modern` sends only `server/discover` with `2026-07-28`, fixed `mcp-gateway/s2` client information, and explicit empty capabilities. `legacy` sends exact `2025-11-25` `initialize` followed by `notifications/initialized`. `auto` probes modern once and falls back only for a strict matching `-32601` with absent/null data or one of the two exact HTTP 400 text responses; valid modern results, `-32022`, HTTP 404, malformed evidence, authentication/network/TLS failures, redirects, 429, and 5xx never downgrade. A `-32022` may cause one same-transport mutually supported modern retry.

Fallback closes the probe first and requires verified stdio process cleanup before opening a fresh legacy transport. Streamable HTTP closure cancels every active exchange and does not report a verified stop until each exchange has returned; expiry of the caller's stop context is unconfirmed. Modern HTTP rejects all session state. A bounded legacy HTTP session is immutable and scoped to one runtime; loss or replacement closes that runtime rather than retrying or rerouting a call. Each prepared `tools/call` is one-shot and copies only its pinned upstream name and validated argument object. Its monotonic marker flips immediately before the first OS pipe write or HTTP RoundTripper handoff: earlier failures are `pre_start`, while every later failure is `start_uncertain`, independent of network return details.

Caller and lifecycle cancellation always close the local request and never replay. Modern stdio emits at most one cancellation notification with exact modern metadata; modern HTTP emits none. Legacy stdio/HTTP emits at most one notification on the bound runtime/session without modern metadata. Cancellation after completion emits nothing. No initialization cache, list-change subscription, multi-round-trip client feature, automatic cancellation/reconnect, or pre-Streamable HTTP+SSE path exists.

The process-local current-runtime capability is deliberately opaque and rejects serialization. It binds one server/tool/upstream/runtime identity plus desired, credential, and catalog revisions. Acquisition is immediate and ordered: one of 32 global permits, then one of four per-server permits, followed by current route/runtime/authority/catalog/drain revalidation. Server saturation returns the global permit; every stale, withdrawn, draining, unavailable, canceled, or saturated result is typed `pre_start`. A lease executes or cancels once, releases server before global admission, and preserves the call marker's classification. There is no queue, reroute, replay, outcome ledger, or enumeration API. Production composition does not publish or consume capabilities yet, and `/mcp` remains deny-all.

The internal OAuth trust resolver starts from the exact registered HTTPS MCP resource. It honors one valid challenge `resource_metadata` URL without silent fallback, otherwise tries endpoint-specific then root RFC 9728 metadata, selects only an exact permitted issuer, and tries RFC 8414 before OpenID metadata. Consumed metadata is strict, duplicate-free, typed, depth/size bounded, audience matched, and required to support authorization code plus PKCE S256. Every machine fetch uses the hardened remote factory; same-resource and explicitly configured canonical DNS origins may use restricted addresses, while cross-origin metadata, issuers, and delegated endpoints remain public-address-only unless explicitly trusted.

Static registration binds the exact selected issuer, client ID, callback, resource, and metadata-supported token authentication method without network work. Dynamic mode uses an advertised registration endpoint only when no exact unexpired authority is reusable, sends one unauthenticated native-client request, chooses Basic then Post then None, and never retries. Only a strict bounded `201` response with the exact callback, code/refresh grants, selected method, and method-consistent secret/expiry can publish. Public registration commits one revision directly; confidential registration atomically commits its revision and opaque `oauth_client` generation through the keyring coordinator. Registration-management tokens/URIs and extensions are discarded. Authenticated flow creation now admits at most 16 active flows globally and one per server, persists only safe five-minute lifecycle metadata, and returns one exact authorization URL containing independent 32-byte state and S256 verifier material. State, verifier, endpoint binding, and requested scopes remain process-local; URLs are never retained, reads and backups expose no transient, new flows supersede preparation, exchanging conflicts, cancellation is terminally idempotent, and startup interrupts nonterminal records. The callback consumes state before code/error/issuer handling, validates the captured issuer rule, makes one exact Basic/Post/None authorization-code request, and accepts only a bounded opaque Bearer response with nonexpanding effective scope. A versioned complete token set is published under desired/registration/client/token/flow/drain fences; flow success and keyring activation commit atomically, while every post-authorization persistence failure leaves neither old nor candidate authority usable. The write-only credential-replacement route accepts one exact complete stdio/bearer slot set or one static confidential OAuth client secret after current desired and credential preconditions pass. It publishes one ordinary old-or-new keyring generation together with exactly one credential revision and a safe `credential_replace` operation, leaves the Server desired revision and ETag unchanged, and fences an affected runtime before cutover. There is no idempotency replay; a lost response is recovered through Server and operation reads. OAuth refresh is process-local and single-flight per server while holding the global keyring operation admission across authority reads, one exact hardened request, and installation. Known expiry becomes eligible at `min(60s,max(5s,lifetime/10))` before expiry; unknown expiry refreshes only after recognized `invalid_token`. Confidential omission retains the old refresh token, public clients require distinct rotation, and no request asks for broader scopes. Pre-handoff failure preserves current authority; uncertain post-handoff, `invalid_grant|invalid_client`, invalid public rotation, or any post-success cutover fault withdraws routes, invalidates old/new authority, and requires foreground reauthorization. A transport-neutral activation probe may refresh and retry itself once; `insufficient_scope` stages sorted foreground step-up without refresh or replay. A dormant raw catalog traverser now consumes this policy through the runtime request seam: it sends `tools/list` from an empty cursor, admits four traversals globally without queueing, bounds each page to 4 MiB/15 seconds and each traversal to 32 pages/60 seconds, rejects cursor/name/collision/count failures, and isolates malformed descriptors as safe issues before normalization. Each isolated raw tool then projects to a closed SDK-independent descriptor: object-root JSON Schema 2020-12 compiles with a rejecting external loader and local-fragment references only; unknown fields are removed; annotation defaults are materialized; combined schemas/canonical descriptors remain within 96/128 KiB; and RFC 8785 canonical bytes receive a lowercase SHA-256 fingerprint. Modern HTTP may retain unique typed nested `x-mcp-header` bindings only; argument mirroring accepts present nonnull validated string/boolean/safe-integer values and applies the pinned `=?base64?...?=` wrapper when required. Stable candidates bind `(server_id,upstream_name)`. A successful normalized candidate now commits one fenced durable catalog revision in SQLite before any future active publication. Immutable `(server_id,upstream_name)` identities retain IDs across retirement/reappearance; success alone retires absent tools, empty success is valid, and per-server/global projected identity limits reject without mutation. Authenticated descriptor list/member resources expose current and retired durable evidence through revision/filter/watermark cursors. Backups retain these safe facts, while restart/restore still has no active catalog, route, traversal, or header-binding state. A process-local active registry now reserves global/per-server capacity, holds the reservation through durable commit and immutable publication, advances a process-specific generation for every publish/withdraw/state change, and serves authenticated `/api/v1/catalog` generation-bound pages. Production starts that registry empty. The catalog coordinator now computes the exact installation/server epoch-grid offset, admits at most four traversals globally and one per server, coalesces poll and explicit refresh work, retains healthy snapshots as stale on safe refresh failure, and cancels timers/work on withdrawal or drain. It retains no SDK cache or subscription state; concrete runtime client and route-capability composition lands with the next integration step. Explicit credential disconnect and first delete withdraw and verify runtime stop before invalidating current local authority. Static/bearer disconnect deletes only its local generation; OAuth disconnect preserves registration/client authority, performs at most one refresh-token then distinct access-token RFC 7009 request through the validated hardened endpoint, and never retries remote failure. Missing revocation support and remote failure are safe observations and never restore authority. Local deletion failure enters `cleanup_pending`; explicit retry deletes only nonauthoritative keyring candidates and performs no remote request or authority restoration. Delete remains tombstoned while invalidating every present credential and registration domain. Shutdown fences runtime publication and credential status first, cancels OAuth producers, drains the keyring coordinator before SQLite closure on normal and error exits, and does not wait for a context-free native keyring call; an operation returning late cannot publish authority, status, or routes.

## Fixed limits and shutdown

All limits are compiled and reject excess work without queuing. Principal live S1 bounds are 128 ordinary HTTP requests, 32 control-auth attempts, 16 authenticated admin operations, 8 health requests, 128 admin sessions, 32 MCP operations, 32 MCP streams, 128 legacy sessions, 16 event streams, one backup operation, and one context-free keyring operation. Retained S1 bounds include 128 admin credentials, 64 backups, 1,024 backup idempotency records for 24 hours, 64 keyring candidates per owner/kind, and a 1 GiB SQLite database.

The S2 contract additionally fixes 1,024 server identities, 64 nondeleted and 32 enabled servers, 32 runtimes, four global/one per-server reconciliations, four catalog traversals, 16 global/one per-server OAuth flows, eight callback exchanges, 32 global/four per-server dispatches, 256 active tools per server and 2,048 globally, and 512 durable tool identities per server and 4,096 globally. The durable repository enforces the identity, nondeleted/enabled-server, 64-terminal-operation, and 1,024-record S2 idempotency bounds. System status reports live server identity, nondeleted server, runtime, reconciliation, OAuth-flow, callback/exchange-work, and S2 idempotency occupancies; owners not yet implemented remain zero.

The first `SIGINT` or `SIGTERM` makes readiness false, rejects new non-recovery work, drains keyring consumers, and closes sessions, MCP state, and event streams before a shutdown bounded to ten seconds. A second signal exits immediately. Graceful shutdown removes the durable run marker; an unclean or forced restart retains it and performs full SQLite identity, pragma, migration, size, and integrity verification before becoming ready. No session, stream, or in-flight work resumes after any restart.

## Keyring capability and generations

Gateway wraps `go-keyring` with the closed capability states `ready`, `absent`, `locked`, `interaction_required`, `unavailable`, and `unsupported`. Its secret-free startup probe performs no Get/Set/Delete or prompt presentation, but `ready` is only a snapshot: later operations may invoke OS-managed interaction, fail, or outlive cancellation because `go-keyring` v0.2.7 is context-free. Returned errors fail dependent work closed, and Gateway never falls back to configuration or plaintext files.

One process-global, nonblocking `keyring_work` slot permits a single operation. Saturation rejects immediately, and cancellation does not release the slot until the backend call actually returns. This MVP is unsuitable for unattended credential access; guaranteed-nonprompting/context-bounded operations are deferred until before unattended deployment or the first observed unexpected dialog, cancellation-surviving call, or keyring-induced service blockage.

The closed raw-secret destinations preserve the S1 one-time `controlling_terminal` and `owner_only_file` sinks and declare only the S2 write-only ingress points `admin_credential_replacement`, `dcr_client_secret`, `authorization_code_token_response`, `refresh_response`, and `authoritative_generation_refresh_copy`. Dynamic client, initial authorization-code, write-only static/OAuth-client replacement, validated refresh responses, and authoritative refresh-token copying are implemented.

Secrets are split into bounded encoded chunks and become readable only after a digest-bound manifest is written. SQLite stores an opaque handle, installation/resource-owner ULIDs, one closed kind (`static_credential`, `oauth_client`, or `oauth_tokens`), revision, and bounded candidate cleanup metadata—never secret bytes. For server-scoped generations, the coordinator invokes a repository callback on its existing marker-armed transaction, so the verified candidate and exactly one independent credential-kind revision become current together under captured desired, credential, optional registration/flow, and drain fences. Invalidation commits the kind revision and null authority before best-effort generation deletion. Ordinary replacement preserves old-or-new authority; the explicit post-authorization-server-success failure path invalidates both old and candidate authority before cleanup. Interrupted or stale candidates remain non-authoritative and are bounded to 64 per owner/kind.

`make test-keyring-native` uses an isolated D-Bus session and temporary home with Secret Service on Linux. On macOS it changes the login keychain search state only when `MCP_GATEWAY_DISPOSABLE_MACOS_KEYCHAIN=1` explicitly confirms a disposable user context, then restores that state. Otherwise it reports an explicit prerequisite skip rather than touching the user's keychain or Gateway namespace.

## Verified backup and offline recovery

Backup creation uses SQLite's online backup API, closes and integrity-checks the staged database, records installation/schema/source-revision metadata and SHA-256, and atomically publishes one owner-only generation. At most one backup is created at a time and 64 published generations are retained; excess work rejects without queuing. Backups contain SQLite non-secret state only—no raw admin bearer, keyring value, session, stream, or in-flight runtime state.

Restore one published generation only after stopping Gateway, and publish replacement admin authority to a new owner-only sink:

```bash
mcp-gateway restore 01ARZ3NDEKTSV4RRFFQ69G5FAV \
  --data-dir /path/to/mcp-gateway-data \
  --secret-output /safe/new/restored-admin-bearer
```

Restore verifies the artifact ID, installation binding, supported schema, source revision, size, SHA-256, and full SQLite integrity; stages one complete database; forward-migrates an accepted S1 schema-3 backup before rekey or generation replacement; revokes every restored admin verifier; publishes and activates one new non-expiring bearer; removes old WAL/SHM sidecars; and atomically selects the replacement. Current backups include safe S2 desired/tombstone, authority-revision, operation, auth-flow lifecycle, and idempotency rows, but never runtime facts, OAuth state/verifier/URLs/endpoints, raw secrets, sessions, MCP state, keyring values, or in-flight work. A successful result has `mode:"backup"` and includes `backup_id`; normal `serve` startup must still verify the replacement before readiness.

To verify and clear the current stopped generation without replacement, run `restore --verify-current` only after stopping every Gateway process that owns the installation:

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

The command requires an owner-only installation, acquires its exclusive process lock, verifies the Gateway identity, current schema and migration history, configured SQLite durability and size bounds, and full database integrity. Only then does it durably clear an armed or malformed mutation marker. A normal `serve` startup is still required after verification.

Failures emit one safe JSON line and exit with status 1. `gateway_running` means another process owns the installation; `secret_output_unavailable` means the one-time sink could not be completed; and storage failures intentionally hide filesystem and SQLite details.

## Coexistence with MCP Broker

MCP Gateway is installed and operated independently beside MCP Broker. It does not read or migrate Broker configuration, SQLite state, role tokens, sessions, or audit records. Use distinct listen authorities; choosing Gateway's default `127.0.0.1:8210` does not alter Broker. Gateway remains the clean-start successor foundation, while Broker continues to provide its existing routing and approval product behavior.

## Security boundary

The completed S1 service is designed to bind one exact numeric IPv4 loopback authority, defaulting to `127.0.0.1:8210`, and to reject all unrecognized paths, methods, credential domains, forwarding headers, and protocol claims. Raw authority must never be stored in configuration, URLs, logs, browser storage, SQLite, backups, or read APIs.

Loopback limits network reachability; it does not isolate processes running as the same operating-system user. Treat untrusted same-user processes as able to attempt connections to the Gateway.

The executable remains deny-by-default: production MCP authentication and all downstream routing are unavailable. See [DESIGN.md](DESIGN.md) for the system contract and [CLAUDE.md](CLAUDE.md) for development conventions.
