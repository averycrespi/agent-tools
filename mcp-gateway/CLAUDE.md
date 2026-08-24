# mcp-gateway

Locally secure, deny-by-default MCP service and control foundation.

## Development

```bash
make build              # go build -o mcp-gateway ./cmd/mcp-gateway
make install            # go install ./cmd/mcp-gateway
make test               # go test -race ./...
make test-integration   # go test -race -tags=integration ./...
make test-e2e           # go test -race -tags=e2e -timeout=60s ./test/e2e/...
make test-keyring-native # isolated native backend or explicit prerequisite skip
make lint               # go tool golangci-lint run ./...
make fmt                # go tool goimports -w .
make tidy               # go mod tidy && go mod verify
make audit              # tidy + fmt + lint + test + govulncheck
```

Run `make audit` before committing. Unit tests are colocated and race-enabled. Integration tests use `//go:build integration`; real-binary tests use `//go:build e2e` under `test/e2e/`.

## Package layout

```text
cmd/mcp-gateway/        Cobra composition root only
internal/contract/      Immutable S1/S2 routes, problems, limits, states, and representations
internal/strictjson/    Dependency-neutral bounded strict JSON and canonical equality
internal/paths/         Owner-only paths and process ownership
internal/storage/       SQLite identity, migrations, durability, latch
internal/servers/       Durable S2 identities, desired authority, operations, cursors, idempotency
internal/runtimes/      Process-local reconciliation plus bounded direct stdio supervision
internal/remote/        Shared canonical URL, address policy, pinned dial, and HTTP exchange
internal/oauth/         Bounded protected-resource and issuer trust graph
internal/downstream/    Raw bounded JSON-RPC plus stdio/Streamable HTTP connections
internal/admin/         Admin bearer and in-memory session authority
internal/api/           Strict resources, JSON, and safe problems
internal/httpboundary/  Listener, route ownership, and early validation
internal/events/        Bounded invalidation-only delivery
internal/limits/        Compiled nonblocking admission limits
internal/mcpingress/    Auth-first modern and legacy MCP adapters
internal/keyring/       Typed capability and opaque generations
internal/backup/        Verified backup and stopped restore
internal/lifecycle/     Startup, readiness, drain, and shutdown
internal/testutil/      Deterministic clocks, entropy, roots, scans, and processes
```

The package directories begin as documented seams and gain implementation only in their owning milestone. `internal/dependencies` exists solely to keep selected S1 dependencies pinned until those packages consume them. Production files must not import `internal/testutil`; it is shared only by tests.

## Invariants

- `internal/contract` is the only source for S1/S2 routes, problems, limits/deadlines, media/protocol values, resource/union shapes, closed states/reasons/events, mechanics, and approved secret sinks; do not duplicate them in later packages.
- `internal/strictjson` is the dependency-neutral parser for API, downstream, OAuth, and catalog boundaries. Supply explicit positive size/depth bounds; closed destinations reject unknown members. Do not reintroduce package-local permissive decoders or equality that depends on object member order.
- Exact default authority is `127.0.0.1:8210`; do not broaden accepted bind or Host forms. Keep validation and route classification ahead of authentication/body work, and preserve the control-auth to admin-work nonblocking permit transfer.
- API JSON must remain size/depth bounded and reject invalid UTF-8, duplicate members, unknown members, and trailing values. Every API response is `no-store` and no response enables CORS.
- Keep admin and agent credential domains, middleware, identifiers, and invalidation channels separate.
- Production MCP authentication is deny-all. Positive tests inject an authenticator; never add production principals or grants in S1.
- Raw secrets never enter SQLite, configuration, argv, logs, metrics, URLs, events, backups, browser storage, or read APIs.
- SQLite security mutations arm durable intent before beginning and fail closed on uncertain outcomes. No online repair or replay.
- Keyring startup probing is secret-free and presents no prompt. Get/Set/Delete may interact or outlive cancellation as a documented MVP limitation; admit only one globally, retain its slot until return, and never fall back to plaintext.
- Limits are compiled constants, acquired in the documented order, and reject without queuing. Event streams release authenticated-admin admission after authentication and retain only their own bounded stream/buffer permits so they cannot starve status or recovery.
- SSE is invalidation-only: no IDs, replay, cursor, secret, or authority. Overflow disconnects the stream; clients recover through authenticated snapshot reads.
- The first process signal drains and closes ephemeral registries within the fixed deadline; the second forces exit. Drain the keyring coordinator before closing storage so late context-free results cannot commit.
- `internal/servers` is the only owner of S2 SQL. Its sanitized contract-typed desired input cannot carry secret payloads; SQLite may contain only safe desired/tombstone facts, independent authority revisions and opaque handles, operation and auth-flow history/watermarks, and safe-reference idempotency metadata.
- Every standalone server-domain mutation uses `storage.Store.Mutate`. Keyring publication/invalidation is the sole exception: a repository callback updates authority metadata directly on the coordinator-owned existing `*sql.Tx` under captured desired, credential, optional registration/flow, and drain fences. That callback must never call `Store.Mutate`. Keep keyring, process, network, OAuth, initialization, discovery, and transport work outside mutation admission; map a latched store fail closed and never repair online.
- Runtime state is never serialized or resumed after restart. Direct stdio execution uses only a validated absolute executable, literal arguments, exact working directory, clean declared/runtime-secret environment, and a fresh process group; ownership and retained diagnostics stay process-local and raw output never reaches public or durable sinks. Stop closes input, revalidates the captured process group before TERM and KILL, uses exact 3/2-second graceful/forced windows, and reports success only after reap; never signal an unverified PID or claim hard-crash orphan prevention. Behavioral work synchronously fences publication, withdraws before stop, starts only after confirmed cleanup, conditionally publishes the current generation, and marks operation success last; unconfirmed cleanup retains its exact handle and gates replacement. Shutdown uses the separate all-runtime drain: synchronously fence/withdraw all, then stop every owned runtime concurrently outside global-four reconciliation, drain keyring consumers before storage closes, and never mark the run clean when stop proof is unavailable.
- Test subprocesses use `testutil.BinaryRunner` with a positive configured timeout and per-stream byte cap. It captures stdout/stderr separately, reports truncation, and guarantees direct-child cancellation; component-specific process-group behavior belongs with that component.
- `internal/remote` is the only production owner of outbound `http.Client`/`http.Transport` construction. It validates canonical destinations, resolves fresh and pins the selected validated address at dial, retains platform TLS hostname verification, and disables proxies, redirects, cookies, compression, keepalive reuse, and convenience APIs. Downstream and OAuth roles must use this factory and add stricter role policy rather than constructing clients.
- `internal/oauth` owns the exact registered-resource trust anchor, RFC 9728 endpoint/root discovery, issuer selection, RFC 8414/OpenID order, strict extensible metadata projection, canonical explicit trusted-origin policy, and static/dynamic registration. A present challenge metadata URL never silently falls back; public reachability never selects an issuer; cross-origin or multiple issuers require the exact desired pin. Only the resource origin and explicit trusted DNS origins may permit restricted machine-fetch addresses. DCR is one unauthenticated request, selects Basic then Post then None, and publishes confidential client authority only through a marker-armed keyring/repository callback. Foreground flow preparation persists only safe lifecycle metadata; state, verifier, endpoints, and requested scopes stay process-local, while the authorization URL appears once in the creation response. Registration publication is additionally fenced by the current preparing flow. Callback handling admits eight globally before consuming state, validates issuer before either branch, and makes one exact bound token request. Initial complete token authority uses post-authorization-success cutover; keyring activation and flow success share one transaction, while every persistence fault invalidates authority. Write-only static/OAuth-client replacement validates safe shape and revisions before decoding values, uses ordinary old-or-new complete-generation cutover, and atomically records one credential revision plus operation. Refresh holds one admitted keyring operation across current token/client reads, exact hardened HTTP, and the shared post-AS-success cutover; uncertain after-send or invalid rotation invalidates authority and never retries a tool call. The activation-probe policy may refresh and retry that read-only probe once, while insufficient scope stages foreground step-up with zero replay. Disconnect/delete invalidate local authority before one-shot best-effort RFC 7009 work and local generation deletion; `cleanup_pending` retry is local-only and can never replay revocation or restore authority.
- `internal/downstream` owns raw JSON-RPC IDs/envelopes, exact outbound MCP application headers, bounded JSON/SSE bodies, stdio/HTTP connection closure, exact modern/legacy/auto negotiation, fresh verified fallback, runtime-local immutable legacy sessions, one-shot call construction, transport-specific cancellation, the pre-start/start-uncertain handoff marker, and opaque immediate current-runtime capabilities. Fixed downstream identity is `mcp-gateway/s2`. Capability acquisition is global 32 then server four, revalidates every bound revision after both permits, releases server before global, never waits, and has no production consumer. Modern evidence never downgrades; modern HTTP is sessionless and never posts cancellation; legacy session loss closes the runtime. Mark immediately before OS write/RoundTripper entry and never infer attempt state from returns. Do not use SDK `Client.Connect`, `CommandTransport`, automatic cancellation/retry/reconnect, cached list paths, subscriptions, or list-change machinery.
- The official MCP SDK sits behind Gateway-owned authentication, classification, binding, limits, and lifecycle.
- The current S2 checkpoint implements the closed vocabulary, shared strict JSON, durable server/operation/idempotency authority foundation, transaction-fenced server-scoped keyring publication/invalidation, desired-server plus explicit-operation APIs, transport-neutral runtime reconciliation/retry scheduling, the bounded direct stdio supervisor, injected stop-before-start/publication ordering, concurrent deadline-bounded all-runtime drain, hardened remote/downstream transport factories, exact downstream era negotiation, call handoff/cancellation, the unconsumed current-runtime capability seam, composed OAuth trust/registration, foreground flow creation, callback exchange, initial token authority, write-only static/OAuth-client replacement, fenced single-flight token refresh, the dormant one-probe retry/step-up policy, and stop-ordered credential disconnect/delete with one-shot revocation plus local-only cleanup retry. These seams are not yet production activation: verified cleanup and activation-ready compatibility exist, but do not infer token replacement/refresh/revocation, catalog traversal, concrete composition/publication, agent-visible routing/invocation, or product-UI behavior; add behavior only in its owning task.

## Dependency flow

`cmd/mcp-gateway` wires concrete adapters into narrow package-owned interfaces. Keep storage, keyring, HTTP, and protocol dependencies behind their owning packages; do not create a shared monorepo module. Use injectable clocks, entropy, sinks, runners, authenticators, and validators only where the owning behavior requires deterministic tests.
