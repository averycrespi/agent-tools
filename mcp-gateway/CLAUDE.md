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
internal/contract/      Immutable S1 routes, problems, limits, and representations
internal/paths/         Owner-only paths and process ownership
internal/storage/       SQLite identity, migrations, durability, latch
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

- `internal/contract` is the only source for S1 routes, problems, limits, media/protocol values, resource shapes, and approved secret sinks; do not duplicate them in later packages.
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
- Runtime state is never serialized or resumed after restart.
- Test subprocesses use `testutil.BinaryRunner` with a positive configured timeout and per-stream byte cap. It captures stdout/stderr separately, reports truncation, and guarantees direct-child cancellation; component-specific process-group behavior belongs with that component.
- The official MCP SDK sits behind Gateway-owned authentication, classification, binding, limits, and lifecycle.
- Do not add downstream server, principal, grant, routing, invocation, OAuth-flow, or product-UI packages during S1.

## Dependency flow

`cmd/mcp-gateway` wires concrete adapters into narrow package-owned interfaces. Keep storage, keyring, HTTP, and protocol dependencies behind their owning packages; do not create a shared monorepo module. Use injectable clocks, entropy, sinks, runners, authenticators, and validators only where the owning behavior requires deterministic tests.
