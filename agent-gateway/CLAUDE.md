# Agent Gateway

Audience: Gateway maintainers and coding agents

Purpose: Commands, ownership, editing and verification. The [documentation map](docs/README.md) links operator procedures and normative design.

## Development

Run commands from `agent-gateway/` unless noted:

```bash
make build                 # build agent-gateway from ./cmd/agent-gateway
make install               # install only agent-gateway into GOPATH/bin
make serve-demo            # build and serve an interactive isolated seeded Gateway
make test                  # disjoint unit/integration/harness/material/demo aggregate
make test-unit             # count-one dependency-light contract and algorithm tests
make test-integration      # count-one component/SQLite/filesystem/compatibility tests
make test-harness          # runner, fixture, report, and selector self-tests
make test-material         # deterministic credential-material composition
make suite-inventory       # JSON source/build-context/executable ownership
make test-e2e              # count-one real-binary suite
make test-security         # security/privacy source and sink evidence
make test-stress           # repeat only five named stress scenarios
make test-keyring-native   # typed native keyring evidence
make test-serve-demo       # real-process demo outcomes, lifecycle and cleanup
make test-browser          # one planner; five isolated browser leaves
make frontend-typecheck
make frontend-build
make frontend-verify-generated
make frontend-verify-supply-chain
make frontend-audit
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
make -C agent-gateway test-frontend-development
npm run ui:build
npm run ui:verify-generated
npm run ui:verify-supply-chain
npm run ui:audit
```

Use `make serve-demo` for isolated feature testing, never default-installation/native-keyring access. Dataset defaults to curated; `AGENT_GATEWAY_DEMO_DATASET=empty` selects first run. The Linux/macOS Go runner builds the `e2e` variant and owns two bounded local HTTP fixtures outside production. Its shell entry compiles a nonsecret bootstrap into ignored `.demo-bin/`; data/credentials are disposable. No Python/Node is needed. `AGENT_GATEWAY_DEMO_LISTEN=127.0.0.1:PORT` overrides `127.0.0.1:8211`. See frontend development for credentials/workflows. The former runner has no alias; demo evidence qualifies neither native keyring nor persistence across restarts.

The [demo supervision contract](docs/maintainers/frontend-development.md#demo-supervision) owns process identity, deadlines, cleanup, and CI cache isolation. Retain one-shot mutations, read-only readiness polling, and fail-closed cleanup.

Use [frontend development](docs/maintainers/frontend-development.md) for the two-process live-reload trust boundary. Use [release verification](docs/maintainers/release-verification.md) for lint memory budgets and release evidence. Do not use a full acceptance run as the first integration or debugging loop.

Run `make verify` before committing Go changes. Run focused race-enabled tests for changed behavior; reserve complete count-one suites for their integration or release owner. Repeat only the dedicated named stress scenarios, never an entire package containing migration, retention, protocol, browser, or real-binary matrices.

## Package layout

```text
cmd/agent-gateway/             Cobra composition root and public CLI
internal/composition/        Sole production graph construction, binding, start, and drain
internal/controlclient/      Strict public-control CLI transport, I/O, sinks, problems, and exits
internal/contract/           Canonical routes, problems, limits, states, representations, and manifests
internal/strictjson/         Bounded strict JSON and token-preserving value tree
internal/paths/              Owner-only installation paths, process ownership and retained tombstone recognition
internal/service/            Canonical LaunchAgent settings, lifecycle and bounded inspection
internal/storage/            SQLite identity, migrations, durability, and latch
internal/servers/            Desired servers, operations, auth-flow lifecycle, and idempotency
internal/catalog/            Durable descriptors, normalization, active publication, and routes
internal/credentialauthority/ Current server credential resolution
internal/servercredentials/  MCP credential cutover
internal/httpcredentials/    Scoped HTTP credentials
internal/httpca/             Installation CA lifecycle
internal/httpproxy/          Opt-in HTTP/CONNECT engine
internal/runtimes/           Process-local reconciliation and stdio supervision
internal/remote/             Hardened destination validation and HTTP transport construction
internal/oauth/              Resource/issuer trust, registration, flows, callback, and refresh
internal/downstream/         Raw bounded JSON-RPC and stdio/Streamable HTTP connections
internal/accesstarget/       MCP target values and scope comparisons
internal/httppolicy/         Pure HTTP v1 policy and canonical targets
internal/authorization/      Principals, credentials, grants, policy SQL, and admission leases
internal/discovery/          Principal-specific current-tool projection and cursors
internal/grantrequests/      Durable request workflow, evidence, dedupe, and adjudication
internal/selfservice/        Fixed admitted-subject tools and safe projections
internal/activity/           Common evidence values
internal/invocation/         MCP details, one-shot calls, redaction, projection, and SQL
internal/audit/              Immutable control-plane audit SQL, bounded validation, and reads
internal/mcpingress/         Auth-first modern and legacy MCP adapters
internal/admin/              Administrator bearer and in-memory browser sessions
internal/api/                Strict control resources and embedded static allowlist
internal/httpboundary/       Listener, route classification, and early validation
internal/diagnostics/        Typed, bounded serve-only stderr diagnostics and sole slog adapter
internal/events/             Bounded invalidation-only delivery
internal/keyring/            Typed provider capability and opaque generations
internal/backup/             Verified backup and stopped restore
internal/lifecycle/          Startup, readiness, drain, and shutdown
internal/testutil/           Deterministic test clocks, entropy, roots, scans, and processes
web/                        Authored TypeScript/Preact/CSS and development bridge
test/acceptance/            Exact-revision runner, reports, external evidence, and adopter
test/e2e/                   Shared real-binary and browser harnesses
test/security/              Security/privacy source and sink evidence
test/keyringnative/         Isolated native keyring evidence
test/material/              External-package material acquisition evidence
docs/                       Role-oriented operator, maintainer, and design documentation
```

Production files must not import `internal/testutil`; it is test-only. Fixed admission controls stay component-owned.

## Editing invariants

### Contracts and boundaries

- `internal/contract` is the executable source for vocabulary, bounds, mechanics, sinks and manifests. Update its tests and [owning design](DESIGN.md#documentation-authority) together.
- Use dependency-neutral `internal/strictjson` for API/downstream/OAuth/catalog input: positive byte/depth bounds, closed fields, no duplicates/trailing values, and policy/evidence-preserving lexical numbers.
- Keep exact numeric-loopback listener validation and explicit hostname Host matching separate from port-sensitive Origin trust. Keep early Host validation, route classification, and admission ahead of authentication or body work. Every API response remains `no-store`; never add CORS authority.
- Keep administrator and agent credentials, middleware, identifiers, and invalidation channels separate. Raw secrets never enter configuration, arguments, URLs, logs, metrics, events, SQLite, backups, browser storage, or read APIs. Only supported client token/proxy-URL exports and runtime-resolved clean stdio secret slots permit environment delivery; never add ambient or administrator environment-secret fallback.
- The official MCP SDK remains behind Gateway-owned authentication, classification, limits, and lifecycle. Only the ingress handler boundary may import it; never add a second SDK list cache, subscription, transport owner, or active-capability consumer.

- Keep `httppolicy` pure; authorization owns HTTP policy and transactional references: [contract](docs/design/identity-and-authorization.md#persisted-http-authority).

### Ownership and composition

- `internal/composition` is the sole production constructor and lifecycle owner for the authorization, discovery, invocation, runtime, catalog, OAuth, and keyring graph. Root consumes narrow complete bundles; it must not create a second authenticator, repository, route consumer, or active-capability path.
- Keep [MCP/HTTP evidence](docs/design/invocation-and-ingress.md#http-traffic-evidence) and [paired storage](docs/design/storage-and-recovery.md) boundaries.
- Domain owners retain SQL; storage owns DDL. Cross-owner mutations use supplied transactions, never nested mutation admission.
- Keep external work outside unrelated locks/admissions. Arm durable intent before authority-affecting external work; fail closed without online repair or replay.
- Preserve the [authority](docs/design/invocation-and-ingress.md#agent-authentication-and-leases) and [storage admission](docs/design/storage-and-recovery.md) contracts: never wait for authority while holding storage, extend acquisition deadlines into active SQL, or duplicate actual-owner occupancy.

### Diagnostic ownership

Follow the [serve diagnostic contract](docs/design/administrative-control-plane.md#serve-diagnostics). Retain the exact-path logging import guard and sole `diagnostics.New` AST guard in `newServeCmd`. Blocked fixtures must release their owned sink and join `Done`; never close inherited stderr or replace an outstanding writer.

### Runtime, transport, and cleanup

- Follow [installation safety](docs/operators/installation-safety.md): preserve legacy-path refusal, exact completed-tombstone recognition and explicit existing/custom roots. The migrator is retired; never clear tombstones, reservations or recovery markers as naming cleanup. LaunchAgent commands remain bounded to five seconds/1 MiB, retain child identity through cleanup and never replay mutations.
- `internal/service` owns canonical LaunchAgent management without private database or credential access. Keep strict literal plist parsing, stable nonblocking management locking, installed-value preservation, loaded intent and one-shot bootout/bootstrap. The composition source guard registers only `internal/service/runner_unix.go` as its `exec.Command` owner; production entry allows only absolute `/bin/launchctl` and `/bin/ps`. Utility children remain unreaped until group cleanup; never signal Gateway PIDs or substitute basename scans for installation ownership. Darwin cleanup may accept `EPERM` only with fixed-size singleton-group proof of the matching owned zombie; additional members, incomplete evidence or inspection failure retain the error, without retrying the signal. Keep the bounded one-record query, not the unbounded-retry slice helper. Native tests require separate disposable-resource consent; Linux fixtures are not native proof.
- Runtime state, handles, routes, OAuth transients, sessions, and cursors are process-local. Never serialize or resume them after restart.
- `internal/remote` is the sole production downstream/OAuth/proxy HTTP client and transport factory; only `internal/controlclient` handles public administration. Select the [engine](docs/design/invocation-and-ingress.md#http-proxy-engine) only explicitly; CA commands remain stopped and composition-owned.
- Direct stdio uses validated absolute executables, literal arguments, exact working directories, clean environments, fresh process groups, bounded streams, and identity-validated TERM/KILL/reap cleanup. Never signal an unverified PID or treat unconfirmed stop as success.
- Downstream calls are one-shot. Preserve the pre-start versus start-uncertain marker, pinned capability revalidation, no reroute, and no automatic retry/reconnect behavior.
- Reconciliation completion reattempts only typed pre-mutation storage admission refusal, within the fixed four-attempt bound. Release the lifecycle lock during acquisition backoff and revalidate the exact work/generation/drain fence before each attempt; never replay external work or uncertain persistence. Explicit catalog refresh holds the lifecycle lock through terminal-operation mutation cleanup, matching admitted reconciliation completion. An operation row can be readable before the writer is released; E2E scenarios must use `WaitSettledOperation` before the next mutation, not terminal-state polling alone.
- Drain fences invocation admission and routes before stopping producers, drains keyring consumers before storage closure, and leaves an unclean marker whenever cleanup is unconfirmed.

### Browser and CLI

- Every sentence earns its place: default to labels, values, actionable errors. Helper text only clarifies non-obvious choices or prevents concrete mistakes. Put implementation details/general caveats in docs, secondary diagnostics in accessible disclosures, warnings at the risk. Scope trust distinctions once; don't repeat headings/statuses or just shorten redundant prose.

- Build web source deterministically to the exact `internal/api/static` allowlist. Build/test-only Node/Vite code must neither enter production imports nor write production assets.
- Before completing UI/interaction changes, exercise affected states in a real browser and inspect desktop/narrow screenshots per [visual verification](docs/maintainers/frontend-development.md#visual-verification). Tests, DOM snapshots, screenshot generation/hashes are not visual inspection.
- Follow the [table conventions](docs/design/administrative-control-plane.md#table-conventions) and shared [implementation contract](docs/maintainers/frontend-development.md#table-implementation).
- Compose `web/src/` owners for location grammar, theme persistence, session epochs, visible refresh, mutation state, accessible primitives, and one-time sinks. No independent storage, timers, streams, fetch mutation/retry, clipboard, opener, or active-content paths.
- Keep the development proxy trusted and loopback-only, with closed selectors and segment-bounded control API routes; never add Gateway startup, MCP ingress, OAuth callback, or production ownership.
- Online CLI commands acquire one selected administrator bearer and use only `internal/controlclient`. There is no prompt, argv, or environment fallback. `--data-dir` selects credential location, never private storage/keyring/domain access. Keep separate stdout/stderr, typed exits, strict input, exact ETags, prepared one-time sinks, confirmation, and no automatic replay.
- Administrator commands are only `admin credential ...` and stopped `admin reset`; direct flags and `--file` are mutually exclusive. Omitted mutation ETags get one validated preflight; explicit ETags are never refreshed. Request approval reads submitted restrictions and stops on explicit ETag mismatch; agent issue/rotate preflight empty/occupied slot intent. Administrator rotation must durably publish, securely reopen, authenticate, and verify the replacement before one conditional old-credential revoke; never compensate or replay.

### Tests, docs, and release evidence

- Test subprocesses use `testutil.BinaryRunner` or the shared E2E/acceptance harness with finite deadlines, bounded output, fresh process groups, and explicit process/listener/temporary-root cleanup.
- The `e2e` build tag replaces only the composition provider factory with deterministic test material. Ordinary builds select the native keyring backend; neither selection is public configuration.
- Routine authorization, server, catalog, and invocation fixtures use `internal/testutil/storagefixture`: one real initialization creates a closed, checkpointed immutable in-memory image per test process, then each fixture copies it into its independently owned root and uses normal `storage.Open` validation. The subpackage keeps storage dependencies out of the general testutil helpers. Never use it for initialization, historical migration, restore, fault-injection, or recovery evidence; those owners retain real initialization. A failed copy/open removes its newly created database, preserving the original error and any cleanup error so the same owner can retry; refusal to overwrite a preexisting database never removes it. No database connection, mutable file, WAL/SHM sidecar, or keyring authority is shared between fixtures.
- Keep focused tests with their behavior owner. `test/acceptance/suite_selection.go` derives exact executable selectors from source/package ownership and build constraints; add tests beside their owner instead of maintaining Makefile name regexes. `make suite-inventory` exposes selected and platform-inapplicable identities, and `go run ./test/acceptance/cmd suite-plan <owner>` prints the checked command plan. Unknown tags, missing owners, empty leaves, or duplicate/foreign executable selection fail closed. Large persistence, migration, protocol, browser, and real-binary suites stay count one. Repeated execution belongs only to dedicated named stress scenarios.
- `run-suite <owner> --json` writes bounded Go test events to stdout; `AGENT_GATEWAY_TEST_JSON=1` enables this for planned Make commands. Selection, deadlines, and cleanup are unchanged, and output delivery failures fail the command. Test processes remain race-enabled; spawned Gateway binaries retain the default non-race `go build -tags=e2e` instrumentation unless build flags explicitly request otherwise. Definition dry runs have five-second/64 KiB bounds. `TestSuiteJSONSubprocess` is a fixture-only entry point, skipped directly and exercised by the structured-output pass/fail owner.
- Repository source guards use exact paths and symbols. Extend an allowlist only for a deliberate ownership change; never broaden a guard to make an unrelated refactor pass. Keep the narrow discovery conversion suppressions limited to reversible sign-bit mapping, bounded nanoseconds, and bounded page positions.
- README is the user entry point; `docs/README.md` maps documentation by role and task; DESIGN is the normative architecture index and `docs/design/` contains its domain chapters. This file owns maintainer commands, layout, dependency flow, and non-obvious editing constraints. Detailed workflows belong to `docs/operators/` and `docs/maintainers/`.
- Release evidence must remain bound to a clean unchanged revision and immutable definitions. Native and external gaps stay typed and visible; a failed blocking owner blocks. Follow [release verification](docs/maintainers/release-verification.md) rather than copying acceptance command inventories here.

## Dependency flow

`cmd/agent-gateway` wires concrete adapters into narrow package-owned interfaces. Keep storage, keyring, HTTP, process, and protocol dependencies behind their owning packages. Avoid a shared monorepo module; copy small local interfaces and helpers when boundaries need the same shape.

Inject clocks, entropy, sinks, runners, authenticators and validators only for deterministic owner tests. Keep constructors/test seams package-private unless the product requires a public boundary.
