# Downstream Servers

Audience: Maintainers and contributors changing server authority, runtimes, transport, OAuth, and catalog publication

Authority: Normative product design

This chapter owns the behavior and invariants described below. Operational procedures remain in the linked guides; exact executable contract values remain owned by `internal/contract` and must agree with this chapter.

## Server and catalog authority

Operator configuration procedures are canonical in [upstream server configuration](../operators/upstream-servers.md). `internal/servers` owns desired-server, authority, operation, auth-flow, cursor, and idempotency SQL; `internal/catalog` separately owns durable catalog SQL. A server's durable desired authority remains distinct from process-local runtime and active catalog state. The server repository stores permanent canonical uppercase ULID identities and permanent namespace tombstones. A server keeps one canonical sanitized transport definition while nondeleted, an immutable namespace/ID, a monotonic desired revision, and UTC creation/update/deletion metadata. Creation enforces 1,024 lineage identities, 64 nondeleted servers, and 32 enabled servers atomically; deletion increments the desired revision once, clears the transport, and never frees identity or namespace.

Every server begins with OAuthRegistration fence revision `0` and independent `static_credential`, `oauth_client`, and `oauth_tokens` revisions at `0`. Credential metadata may hold only a validated opaque keyring handle; no keyring generation bytes are represented. Publishing or invalidating one credential kind increments only that kind. The repository supplies a narrow callback that validates captured desired, target-kind credential, and optional registration/flow revisions and updates metadata plus initial-token flow activation directly on the keyring coordinator's existing `*sql.Tx`; it never opens a nested `Store.Mutate`.

Operations capture desired and all credential revisions, use closed kinds/states/reasons, and permit only the declared scheduled/running transitions into immutable terminal state. Startup interruption is one marker-armed mutation. The repository retains the newest 64 terminal operations per server, never prunes nonterminal work, and advances a durable retained-floor watermark whenever pruning occurs. Server and operation lists capture insertion-sequence upper watermarks; later inserts are excluded and a cursor whose unconsumed position crossed the retained floor fails stale.

Server idempotency stores no response body or secret-bearing request. It records only the parent admin credential ULID, method, route, visible-ASCII key, canonical request hash, exact precondition, safe server/operation references, desired revision, and fixed 24-hour timestamps. Lookup and conflict resolution occur before operation precondition evaluation. Matching retries replay the safe reference, differences conflict, records expire exactly at the retention boundary, expired records are deterministically removed, and 1,024 unexpired records reject new keys without eviction. Every durable mutation uses `storage.Store.Mutate`; storage admission is never held over external work, and a storage latch maps to the repository's fail-closed unavailable class.

The schema has no runtime ID, PID, session, route capability, external request ID, OAuth state/verifier/code/authorization URL, token, exchange work, attempt, provider body/header, native error text, or raw secret column. A failed retained OAuth flow may store only its closed stage, existing public reason, and nullable bounded HTTP status, correlated by its existing flow ID. Runtime and secret bytes therefore have no repository input or durable representation.

## Keyring capability and generation cutover

Credential-free transports skip keyring access. Provider capability or keyring admission failures remain isolated to the dependent server.

The `go-keyring` process-global functions sit behind instance-local adapters. Gateway's secret-free startup probe invokes no Get/Set/Delete or prompt presentation: Linux inspects the session D-Bus Secret Service, attempts only a nonpresenting unlock and dismisses any returned prompt object; macOS checks default-keychain metadata with bounded, output-discarding `security` commands. The snapshot is one of `ready`, `absent`, `locked`, `interaction_required`, `unavailable`, or `unsupported`, with a safe remediation code; it does not predict whether a later operation will interact. Missing items remain distinct from backend absence, and unknown native failures become unavailable without preserving native diagnostics.

Any Get/Set/Delete may invoke OS-managed interaction, fail, or outlive cancellation because `go-keyring` v0.2.7 is context-free. One process-global nonblocking `keyring_work` permit bounds outstanding operations; saturation rejects immediately and cancellation does not release the slot before the backend call returns. This accepted MVP limitation never permits file/configuration fallback. The MVP is unsuitable for unattended credential access; hardening is required before unattended deployment or after any unexpected dialog, cancellation-surviving call, or keyring-induced service blockage.

A keyring namespace binds the installation ULID, an immutable Gateway-derived resource-owner ULID, and one closed kind: `static_credential`, `oauth_client`, or `oauth_tokens`. Secret payloads are limited to 256 KiB, base64url encoded into stored values no larger than 3,000 bytes, and identified outside the provider only by random opaque handles. Chunks are written before a versioned owner/kind/handle/length/SHA-256 manifest, so no partial generation reads. Read verifies every binding, bound, decoded length, and digest; deletion handles complete or interrupted generations.

SQLite registers a non-authoritative candidate before the first keyring write, making crash leftovers discoverable without persisting secret bytes. After writing and reading back a complete generation, one latched transaction advances the Gateway revision, selects its opaque handle as authority, invokes any domain callback, and moves the prior handle to bounded cleanup metadata.

Server callbacks make the candidate handle and one independent credential-kind revision current atomically under captured desired, authority, registration, and drain fences. Invalidation removes current keyring authority and nulls server authority metadata in the same transaction before best-effort deletion; failed deletion leaves only bounded non-authoritative cleanup metadata.

At most 64 candidates exist per owner/kind. Startup or replacement cleanup removes only non-authoritative candidates; ordinary interruption before commit preserves old authority, while interruption after commit exposes only the new complete generation.

The explicit installation seam used after authorization-server success invalidates both old and candidate authority on write, verification, publication, or acknowledged post-commit failure before cleanup, so that failure path serves neither generation.

Deterministic injected tests cover Darwin and Linux mappings, prompt dismissal, generation faults, N/N+1 candidates, and every cutover boundary. The native target uses an isolated Linux D-Bus/home/Secret Service environment when its prerequisites exist. macOS keychain search/default state is changed only in an explicitly confirmed disposable login context and is restored afterward; ordinary hosts receive a clear prerequisite skip.

## Direct stdio supervision

`internal/runtimes` owns the bounded direct stdio supervisor used by production lifecycle and protocol composition. It executes only the validated absolute executable with literal arguments and the exact working directory, creates a fresh process group, and supplies a clean environment containing only declared non-secret values plus runtime-resolved secret slots. It never invokes a shell, performs PATH lookup, or inherits ambient proxy or dynamic-loader variables. Process and group ownership remain memory-only.

Stdout is one bounded NDJSON frame stream: the 4 MiB ceiling excludes the newline delimiter, and an incomplete final frame is protocol-invalid. Stdout and concurrently drained stderr have independent token buckets with the compiled 8 MiB/s rate and 8 MiB burst; excess fails only that runtime with the safe `output_limit` class.

At most 64 KiB of stderr is retained privately in memory and no raw process output, exit detail, or secret is exported to status, events, logs, or persistence. The supervisor admits 32 processes without waiters and exposes only safe process-exit classifications.

Normal stop closes protocol input, signals only the captured and reverified process group, waits three seconds, then permits a forced group kill and two-second reap window. Ownership mismatch or an unverified reap fails closed and can be retried against the same process-local handle.

The reconciliation manager owns a generation-fenced active publisher: every behavioral trigger synchronously fences and withdraws the old publication, retains exact process-local ownership, requires the injected driver to confirm stop before starting a replacement, conditionally publishes only the current candidate, and records operation success last. Unconfirmed stop retains only a cleanup handle, reports `stop_unconfirmed`, and gates replacement; if a concurrent verified stop has already removed both the driver handle and runtime ownership, that absence confirms cleanup instead of creating a permanent blocked stop.

A disabled/deleted retry performs cleanup without activation, while display-only revisions preserve the active generation. Process and group IDs are never persisted, so restart cannot kill a reused PID; an uncatchable Gateway crash may orphan a child for operator cleanup.

First-signal drain increments the process-local epoch, fences and withdraws all routes before launching every at-most-32 runtime stop concurrently outside the global-four reconciliation path. Late work cannot publish, keyring consumers drain before storage closes, and the process still obeys the ten-second deadline; any unconfirmed runtime stop leaves the durable run marker unclean rather than claiming verified shutdown.

## Downstream protocol and remote transport

`internal/downstream` owns monotonic runtime-local request IDs and strict closed JSON-RPC envelopes before protocol-era projection. Its stdio connection writes bounded NDJSON only through the supervised runtime and requires verified runtime stop on closure. Its Streamable HTTP connection emits only role-built JSON Content-Type, JSON/SSE Accept, protocol, method, optional name, validated parameter mirrors, runtime-bound legacy session, and server-scoped Authorization; bounded JSON or one bounded SSE event is returned without inheriting inbound authority, IDs, sessions, progress/task fields, headers, or metadata. Runtime-root closure cancels active HTTP exchanges and waits for each exchange to return before confirming transport stop; expiry of the stop context remains unconfirmed.

Each downstream runtime independently selects `modern`, `legacy`, or `auto`. Modern discovery and every later modern request carry exact `2026-07-28`, fixed `mcp-gateway/s2` client information, and explicit empty capabilities. Legacy initialization selects exact `2025-11-25`, omits modern metadata, and sends one `notifications/initialized`. Auto fallback destroys the probe and requires verified stdio reap before constructing a fresh legacy coordinator; only a strict matching `-32601` with absent/null data, either exact HTTP 400 text body, or the Python SDK's closed HTTP 400 JSON error naming the requested modern version and a known version set containing exact legacy `2025-11-25` qualifies. Valid DiscoverResult and `-32022` are modern evidence, with at most one strict mutually-supported modern retry; HTTP 404 and every malformed, authentication, timeout, network, redirect, 429, or 5xx case reject downgrade. Modern HTTP is sessionless. A legacy HTTP session is at most 512 bytes, memory-only, immutable, and runtime-bound; loss closes the runtime and never retries or reroutes a call.

A call object copies one pinned upstream name and validated argument object, constructs one exact `tools/call`, and is consumed after one execution attempt. Its monotonic state flips immediately before the first OS pipe write or HTTP RoundTripper handoff. Validation, closed-runtime, or cancellation failures before that point are `pre_start`; any partial write, connect/TLS/network error, response-read failure, session loss, or lifecycle cancellation after it is `start_uncertain`, without inference from a generic returned error. Once the exact current session has yielded a complete wire response, envelope, bound, status, media-type, or JSON failure is `response_invalid` and therefore known rather than start-uncertain. Local cancellation is always applied. Modern stdio may send exactly one metadata-bearing `notifications/cancelled`, modern HTTP sends none, and legacy stdio/HTTP may send exactly one metadata-free notification on the bound session. The call registry lets runtime withdrawal cancel active attempts without replay or reroute; late cancellation after completion emits nothing.

## OAuth authority and catalog publication

### Outbound remote boundary

`internal/remote` is the shared non-bypassable outbound substrate for downstream and OAuth roles. It canonicalizes scheme/host/default port, rejects userinfo and fragments, rejects queries unless the role explicitly permits a bounded OAuth query, supports only HTTPS plus the exact downstream IP-literal loopback HTTP exception, resolves every connection fresh, rejects any disallowed answer, and dials the first validated address directly while preserving the original TLS hostname and platform roots. Trusted roles may explicitly permit restricted addresses only after their own origin authority check. Each exchange uses a fresh no-proxy/no-cookie/no-redirect/no-compression/no-keepalive client, validates response header count/bytes/value bounds before a bounded body read, and owns body closure. Production downstream and OAuth source guards prohibit permissive HTTP convenience and SDK transport paths.

### OAuth trust and metadata

`internal/oauth` builds one process-local trust graph from the exact registered HTTPS MCP resource. One present challenge metadata value is authoritative or fails; otherwise endpoint-specific RFC 9728 metadata may fall back to root metadata only on absence.

Resource strings are compared exactly against the branch-specific audience, at least one unique issuer is required, and `header` is mandatory when bearer methods are advertised. A sole same-resource-origin issuer may be selected automatically; every cross-origin or multiple-issuer choice requires the exact desired issuer.

The selected issuer is fetched in RFC 8414 then OpenID well-known order, must match byte-for-byte, and must advertise code response/grant plus PKCE S256. Required and optional consumed members are exactly typed while extensions are discarded.

Registered resources and issuer identifiers prohibit queries; standard metadata and delegated endpoint URLs allow only the compiled bounded query. Canonical explicit trusted origins are unique lowercase HTTPS DNS origins without default ports or paths.

The registered-resource origin and explicitly trusted origins may resolve to restricted addresses; all other machine fetches retain the public-address policy.

### Client registration

A static registration is local and binds the exact graph issuer/resource, configured client ID, fixed numeric-loopback callback, and an explicitly metadata-supported token endpoint authentication method. Dynamic registration requires the advertised role endpoint and no reusable exact unexpired authority.

It sends one unauthenticated bounded RFC 7591 native-client JSON request with the exact callback, code response, code/refresh grants, and one method selected Basic → Post → None; it never probes or retries. Publication accepts only bounded extensible JSON at `201` whose client ID, sole callback, response/grant support, returned method, and secret/expiry shape match.

Confidential results require a nonempty secret; an omitted expiry or expiry `0` means non-expiring, while any nonzero expiry must be strictly future. They atomically publish the registration row, `oauth_client` credential revision/handle, keyring authority, and verified candidate generation in one marker-armed transaction.

Public None results publish only the registration revision. Static confidential registrations obtain client-secret authority only from the write-only replacement route.

Stale desired/registration/client/flow or drain results are orphan cleanup only; registration-management artifacts and unknown extensions are discarded.

### Foreground authorization flows

Foreground flow creation first commits one safe `preparing` record under the global-16/per-server-one bound, then performs discovery/registration outside SQLite admission. Independent 32-byte state and verifier material, S256 challenge, exact graph/registration/revision binding, endpoint snapshot, and sorted requested scopes remain in one process-local registry.

Only the creation response receives the exact canonical authorization URL; durable rows, later reads, events, logs, backups, and browser assets cannot represent it or its state/verifier. Publication to `awaiting_callback` rechecks desired, registration, and flow identity; superseded or late DCR cannot publish.

New preparation supersedes `preparing`/`awaiting_callback`, `exchanging` conflicts, cancellation is terminally idempotent, exact five-minute expiry excludes exchanging work, behavioral server mutation fences flows, and startup interrupts every nonterminal record. The callback validates one nonempty state before immediate global-eight admission, then consumes it before inspecting code/error/issuer.

The captured RFC 9207 support bit controls exact `iss` presence and byte equality for both code and error branches. A valid code commits `exchanging`, rechecks every captured fence, loads confidential client authority only from keyring when required, and sends one exact form to the bound token endpoint through the hardened factory.

Basic percent-encodes credentials before the sole Authorization header and also sends the nonsecret client ID in form for authorization-server lookup; Post places client ID/secret only in form; None sends client ID only. A strict bounded `200 application/json` Bearer response may narrow but never expand requested scopes; omission preserves requested or unspecified/default semantics.

The versioned complete token generation binds server, issuer, registration revision, resource, scopes, issuance, optional expiry, access token, and optional refresh token. Post-authorization cutover fences prior tokens, and activation atomically clears the keyring fence and marks the unchanged exchanging flow succeeded; any candidate, storage, staleness, drain, or acknowledged post-commit failure invalidates token authority and fails the flow.

Callback bodies are fixed nonreflecting HTML. Write-only replacement validates the exact stdio/bearer slot set or static Basic/Post OAuth mode before decoding secret values, then uses ordinary complete-generation cutover.

The credential metadata increment, safe `credential_replace` operation, and foreground-flow supersession share the coordinator-owned transaction; desired revision and ETag do not change. A process-local fence withdraws publication before commit, and failed commit recovery reconstructs old authority.

### Refresh and challenge recovery

Refresh is per-server single-flight and holds global keyring admission across exact authority reads, rediscovery, one hardened request, and the shared post-AS-success installation protocol. Eligibility uses the bounded lifetime lead.

Hardened downstream HTTP projects only bounded recognized Bearer challenge fields; modern discovery, legacy initialization, and the first raw catalog page terminate with a typed credential-replacement disposition and no same-client refresh or replay. Reconciliation consumes at most one recognized invalid-token disposition for the exact generation.

The refresh uses the challenged candidate's desired, registration, client, and token fence without generic status or trigger callbacks; successful cutover withdraws and verifies stop before exact current-authority reacquisition and a fresh runtime replays only the challenged stage once. Staleness, uncertainty, drain, stop-unconfirmed, or a second challenge cannot replay.

Confidential omission copies the old refresh token, public clients require distinct rotation, and effective scope cannot expand. Pre-handoff failure preserves authority; uncertain post-handoff, invalid client/grant, bad public rotation, or any post-success persistence/staleness/drain fault synchronously withdraws and invalidates authority for foreground reauthorization.

Insufficient scope is projected for a process-local sorted-union step-up and performs no refresh or replay. The concrete raw catalog path preserves that disposition only on its first page; insufficient scope is projected for foreground step-up, while later-page challenges become terminal authentication failures.

### Catalog traversal and normalization

Traversal uses the Gateway runtime request seam from empty cursor with global-four immediate admission, exact 15-second page/60-second total deadlines, 32-page/4-MiB bounds, cursor cycle and name/collision/count rejection, and isolated bounded raw descriptor issues. Isolated entries project to a closed SDK-independent descriptor and stable `(server_id,upstream_name)` key.

Input/output schemas must compile as object-root JSON Schema 2020-12 with local-fragment references only through an external-rejecting loader; unknown descriptor/annotation fields are removed, effective annotation defaults materialized, and 96-KiB combined schema/128-KiB canonical bounds enforced. RFC 8785 canonical bytes produce lowercase SHA-256 fingerprints.

Only modern HTTP input properties may retain unique typed nested `x-mcp-header` bindings; present nonnull validated scalar arguments mirror through canonical string/boolean/safe-integer values and SEP-2243 base64 wrapping. A normalized candidate commits one complete durable revision under current desired/registration/credential/catalog fences.

Projected per-server/global identity capacity is checked before immutable ID allocation; success alone retires omissions, reappearance reuses identity, safe issues aggregate by closed class, and failure changes no snapshot facts. Descriptor reads use revision/filter/insertion-watermark cursors and include retired evidence.

### Active catalog and discovery

A process-local registry serializes every capacity reservation, durable commit, immutable active replacement, stale retention, and withdrawal under one global lock; active generation cursors and per-server status change atomically, while a post-commit stale runtime publishes nothing. Only exactly current active generations are discoverable and callable.

Its discovery seam returns deep-cloned descriptors only from exactly current server snapshots with separate process and active generation evidence. Discovery first loads the exact pending credential binding, authorization revision, and every valid active structural grant in one SQL view at one caller-pinned timestamp, then captures that catalog snapshot.

`all` includes every current descriptor; `requestable` excludes only an applicable unconstrained DENY; and `allowed-only` requires an applicable ALLOW and no applicable unconstrained DENY, so constrained grants deliberately over-approximate call-time visibility without exposing policy advice. Results sort by external name then immutable tool ID and project only normalized MCP Tool fields with the external name replacing the upstream name.

Stale, absent, unavailable, or draining state yields no discovery descriptors; grant IDs/constraints/evidence, administrative descriptor fields, bindings, routes, capabilities, runtime handles, and mutable references remain private. A distinct fixed-width `mgw_dc1_` raw-base64url cursor authenticates the complete principal/credential/revision/visibility/time/catalog-generation/method/position binding with a process-local random 32-byte HMAC key; integrity is checked before payload interpretation, malformed syntax is distinct from stale or tampered binding, restart changes the key/process generation, and neither the key nor any credential/grant secret enters the cursor.

A page contains at most 100 tools and the exact complete compact JSON-RPC response bytes supplied by the ingress encoder contain at most 4 MiB. Packing measures standard Tool encodings against the actual empty final or cursor-bearing envelope, preflights the cursor-free final page because omission is non-monotonic, verifies the selected response's exact size, emits no final cursor, and has no page-count cap.

Only failure to fit the first projected tool is resource limit; malformed tools or encoder disagreement fail as encoding unavailability. One lock-coherent catalog-generation recheck detects publication, staleness, withdrawal, replacement, drain, or restart without retrying: a first-page mismatch is authorization-unavailable and a continuation mismatch is stale cursor.

### Polling and explicit refresh

Backups include only SQLite facts, and a new process starts a new empty active generation. Polling uses an injected strict-after-now five-minute epoch grid with the installation/server SHA-256 offset, one traversal per server, and the existing global-four immediate admission.

Explicit refresh attaches its one durable operation to exact in-flight poll work without lifecycle restart; waiter cancellation does not cancel owner work. A challenge completion is delivered only after singleflight removal, and the manager admits one exact poll/explicit handoff with no operation for an unattached poll and no queue or duplicate consumer.

The attached operation remains running across refresh, withdrawal, verified stop, and fresh first-page traversal; independent desired, authority, lifecycle, or drain changes supersede it. Fresh completion alone schedules one next strict-grid poll from the then-current clock.

Healthy nonchallenge failures retain one stale safe issue and the old immutable snapshot, while withdrawal/drain cancels work and timers. Runtime-losing traversal failures return synchronously to the coordinator so it persists and withdraws the failed catalog before handing runtime failure to reconciliation; the request seam does not race that ownership through a second failure report. The traverser has no SDK list cache/subscription path.

### Capability publication and cleanup

A successful active commit with an exact runtime atomically swaps opaque per-tool capabilities pinned to every runtime/authority/catalog revision. Under the same active-publication ownership, direct external-name resolution returns one immutable server/tool/upstream/revision/fingerprint snapshot, its publication-compiled input validator, and that exact capability without consulting discovery or acquiring dispatch capacity.

Immediate global-then-server admission revalidates after permits and again at execution; normalized modern-HTTP bindings alone derive outbound headers from validated arguments. Stale state rejects new work, withdrawal fences and cancels leases, and no ingress or root package directly consumes or enumerates these capabilities; the sole production consumer is the composed invocation service after acknowledged ALLOW admission.

Disconnect/delete reconciliation uses the same stop-before-cleanup path: current local authority is invalidated before remote work, OAuth disconnect preserves registration/client authority, and delete clears every present credential plus registration authority without changing its tombstone. Terminal catalog state is persisted under the current desired/registration/credential/catalog fence before operation success: disconnect is durable/active unavailable, disable is durable stale and active absent, and delete is durable retired and active absent.

A validated revocation endpoint receives one refresh-token request followed by one distinct access-token request using one metadata-supported Basic/Post/None method; absence/failure is observed safely and never restores authority. Local generation deletion failure is `cleanup_pending`; explicit retry performs candidate deletion only, with no remote replay or authority restoration.
