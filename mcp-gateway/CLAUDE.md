# mcp-gateway

Locally secure, deny-by-default MCP service and control plane.

## Development

Run commands from `mcp-gateway/` unless noted:

```bash
make build                 # build ./cmd/mcp-gateway
make install               # install the binary into GOPATH/bin
make test                  # race-enabled ordinary package suite
make test-unit             # count-one ordinary correctness
make test-integration      # race-enabled real SQLite/filesystem tests
make test-e2e              # count-one real-binary suite
make test-keyring-native   # typed native keyring evidence
make verify                # nonmutating module, format, and lint checks
make lint                  # golangci-lint
make fmt                   # goimports
make tidy                  # go mod tidy and verify
make audit                 # mutating full developer audit

go tool govulncheck ./...  # unsuppressed blocking Go vulnerability check
```

Frontend commands run from the repository root:

```bash
npm run ui:typecheck
npm run ui:dev
make -C mcp-gateway test-frontend-development
npm run ui:build
npm run ui:verify-generated
npm run ui:verify-supply-chain
npm run ui:audit
```

Use [docs/frontend-development.md](docs/frontend-development.md) for the two-process live-reload trust boundary. Use [docs/release-verification.md](docs/release-verification.md) for evidence tiers, acceptance, external qualification, and report adoption. Do not use a full acceptance run as the first integration or debugging loop.

Run `make verify` before committing Go changes. Run focused race-enabled tests for changed behavior; reserve complete count-one suites for their integration or release owner. Repeat only the dedicated named stress scenarios, never an entire package containing migration, retention, protocol, browser, or real-binary matrices.

## Package layout

```text
cmd/mcp-gateway/             Cobra composition root and public CLI
internal/composition/        Sole production graph construction, binding, start, and drain
internal/controlclient/      Strict public-loopback CLI transport, I/O, sinks, problems, and exits
internal/contract/           Canonical routes, problems, limits, states, representations, and manifests
internal/strictjson/         Bounded strict JSON and token-preserving value tree
internal/paths/              Owner-only installation paths and process ownership
internal/storage/            SQLite identity, migrations, durability, and latch
internal/servers/            Desired servers, operations, auth-flow lifecycle, and idempotency
internal/catalog/            Durable descriptors, normalization, active publication, and routes
internal/credentialauthority/ Current server credential resolution
internal/servercredentials/  Static and OAuth-client replacement cutover
internal/runtimes/           Process-local reconciliation and stdio supervision
internal/remote/             Hardened destination validation and HTTP transport construction
internal/oauth/              Resource/issuer trust, registration, flows, callback, and refresh
internal/downstream/         Raw bounded JSON-RPC and stdio/Streamable HTTP connections
internal/authorization/      Principals, credentials, grants, policy SQL, and admission leases
internal/discovery/          Principal-specific current-tool projection and cursors
internal/grantrequests/      Durable request workflow, evidence, dedupe, and adjudication
internal/selfservice/        Fixed admitted-subject tools and safe projections
internal/invocation/         One-shot call service, redaction, result projection, and audit SQL
internal/mcpingress/         Auth-first modern and legacy MCP adapters
internal/admin/              Administrator bearer and in-memory browser sessions
internal/api/                Strict control resources and embedded static allowlist
internal/httpboundary/       Listener, route classification, and early validation
internal/events/             Bounded invalidation-only delivery
internal/keyring/            Typed provider capability and opaque generations
internal/backup/             Verified backup and stopped restore
internal/lifecycle/          Startup, readiness, drain, and shutdown
internal/limits/             Reserved package marker; concrete limits stay component-owned
internal/dependencies/       Build-tagged dependency pin only
internal/testutil/           Deterministic test clocks, entropy, roots, scans, and processes
web/                        Authored TypeScript/Preact/CSS and development bridge
test/acceptance/            Exact-revision runner, reports, external evidence, and adopter
test/e2e/                   Shared real-binary and browser harnesses
test/security/              Security/privacy source and sink evidence
test/keyringnative/         Isolated native keyring evidence
test/material/              External-package material acquisition evidence
docs/                       Canonical operational and maintainer guides
```

`internal/dependencies` is only a dependency pin. Production files must not import `internal/testutil`; it is test-only.

## Editing invariants

### Contracts and boundaries

- `internal/contract` is the only source for public routes, safe problems, media/protocol values, fixed limits, closed states/reasons/events, resource mechanics, approved secret sinks, and behavior manifests. Change the corresponding contract tests and [DESIGN.md](DESIGN.md) deliberately when intended behavior changes.
- `internal/strictjson` is the dependency-neutral parser for API, downstream, OAuth, and catalog input. Use explicit positive size/depth bounds, reject duplicate/unknown/trailing values for closed shapes, and preserve lexical number tokens where policy or canonical evidence depends on them.
- Keep exact numeric-loopback validation, route classification, and admission ahead of authentication or body work. Every API response remains `no-store`; never add CORS authority.
- Keep administrator and agent credentials, middleware, identifiers, and invalidation channels separate. Raw secrets never enter configuration, arguments, environment variables, URLs, logs, metrics, events, SQLite, backups, browser storage, or read APIs.
- The official MCP SDK remains behind Gateway-owned authentication, classification, limits, and lifecycle. Only the ingress handler boundary and build-tagged dependency pin may import it; never add a second SDK list cache, subscription, transport owner, or active-capability consumer.

### Ownership and composition

- `internal/composition` is the sole production constructor and lifecycle owner for the authorization, discovery, invocation, runtime, catalog, OAuth, and keyring graph. Root consumes narrow complete bundles; it must not create a second authenticator, repository, route consumer, or active-capability path.
- Preserve package SQL ownership. Server SQL stays in `internal/servers`, catalog SQL in `internal/catalog`, online principal/grant SQL in `internal/authorization`, request SQL in `internal/grantrequests`, invocation SQL in `internal/invocation`, and migration DDL in `internal/storage`. Cross-owner mutations use existing supplied-transaction seams rather than nested mutation admission.
- Keep storage/keyring/network/process work outside unrelated locks and admissions. Mutations that may expose authority must arm durable intent before uncertain external work and fail closed; never add online repair or automatic replay.
- Limits are compiled, nonqueueing, and acquired in the documented order. Preserve actual-owner occupancy rather than placeholders or summed duplicates.

### Runtime, transport, and cleanup

- Runtime state, handles, routes, OAuth transients, sessions, and cursors are process-local. Never serialize or resume them after restart.
- `internal/remote` is the sole production downstream/OAuth HTTP client and transport factory. The only separate client is `internal/controlclient` for canonical numeric-loopback public administration.
- Direct stdio uses validated absolute executables, literal arguments, exact working directories, clean environments, fresh process groups, bounded streams, and identity-validated TERM/KILL/reap cleanup. Never signal an unverified PID or treat unconfirmed stop as success.
- Downstream calls are one-shot. Preserve the pre-start versus start-uncertain marker, pinned capability revalidation, no reroute, and no automatic retry/reconnect behavior.
- Drain fences invocation admission and routes before stopping producers, drains keyring consumers before storage closure, and leaves an unclean marker whenever cleanup is unconfirmed.

### Browser and CLI

- Authored web source builds deterministically to the exact `internal/api/static` allowlist. Development Node/Vite code is build/test-only and must not enter the production import graph or write production assets.
- Preserve the single owners under `web/src/`: location grammar, theme persistence, session epochs, visible refresh, mutation state, accessible primitives, and one-time sinks. Domain pages compose these owners; they do not add independent storage, timers, streams, fetch mutation/retry, clipboard, opener, or active-content paths.
- The development proxy remains a trusted loopback-only process with a closed selector grammar and segment-bounded control API proxy. It never owns Gateway startup, MCP ingress, OAuth callback, or production behavior.
- Online CLI commands acquire one selected administrator bearer and then use `internal/controlclient` only. There is no prompt, argv, or environment fallback. `--data-dir` selects credential location; it never opens private storage, keyring, or domain packages. Preserve separate stdout/stderr, typed exits, strict input, exact ETags, prepared one-time sinks, confirmation, and no automatic replay.

### Tests, docs, and release evidence

- Test subprocesses use `testutil.BinaryRunner` or the shared E2E/acceptance harness with finite deadlines, bounded output, fresh process groups, and explicit process/listener/temporary-root cleanup.
- The `e2e` build tag replaces only the composition provider factory with deterministic test material. Ordinary builds select the native keyring backend; neither selection is public configuration.
- Keep focused tests with their behavior owner. Large persistence, migration, protocol, browser, and real-binary suites stay count one. Repeated execution belongs only to dedicated named stress scenarios.
- Repository source guards use exact paths and symbols. Extend an allowlist only for a deliberate ownership change; never broaden a guard to make an unrelated refactor pass. Keep the narrow discovery conversion suppressions limited to reversible sign-bit mapping, bounded nanoseconds, and bounded page positions.
- README is the user entry point; DESIGN is normative architecture and behavior; this file is maintainer commands, layout, dependency flow, and non-obvious editing constraints. Detailed workflows belong to the manifest-owned guides under `docs/`.
- Release evidence must remain bound to a clean unchanged revision and immutable definitions. Native and external gaps stay typed and visible; a failed blocking owner blocks. Follow the release guide rather than copying acceptance command inventories here.

## Dependency flow

`cmd/mcp-gateway` wires concrete adapters into narrow package-owned interfaces. Keep storage, keyring, HTTP, process, and protocol dependencies behind their owning packages. Avoid a shared monorepo module; copy small local interfaces and helpers when boundaries need the same shape.

Use injectable clocks, entropy, sinks, runners, authenticators, and validators only where the owning behavior needs deterministic tests. Keep constructors and test seams package-private unless a public boundary is part of the product contract.
