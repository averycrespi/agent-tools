# mcp-broker

MCP proxy that lets sandboxed agents use external tools without holding secrets.

## Development

```bash
make build              # go build -o mcp-broker ./cmd/mcp-broker
make install            # go install ./cmd/mcp-broker
make test               # go test -race ./...
make test-integration   # go test -race -tags=integration ./...
make test-e2e           # go test -race -tags=e2e -timeout=60s ./test/e2e/...
make lint               # go tool golangci-lint run ./...
make fmt                # go tool goimports -w .
make tidy               # go mod tidy && go mod verify
make audit              # tidy + fmt + lint + test + govulncheck
```

Run `make audit` before committing. Integration tests use `//go:build integration`.
E2E tests use `//go:build e2e` and live in `test/e2e/`. They build and run the real binary as a subprocess.

## Architecture

Single binary, single port. `/mcp` for agents, `/dashboard/` for the web dashboard.

Pipeline: tool call → rules check → optional approval → proxy to backend → audit.

```
cmd/mcp-broker/         CLI entry point (Cobra)
internal/
  config/               JSON config with XDG paths, default backfill on load
  rules/                Glob matching (filepath.Match), first-match-wins
  audit/                SQLite (ncruces/go-sqlite3, WASM, no CGO), WAL mode
  server/               Backend interface with stdio, HTTP, SSE, and OAuth transports
  dashboard/            Embedded HTML, SSE updates, implements Approver interface
  telegram/             Telegram Bot API polling approver (opt-in, outbound-only)
  hooks/                Bounded async approval observers and direct command runner
  auth/                 Role credentials: migration, locking, atomic store, middleware
  broker/               Orchestrator with ServerManager, AuditLogger, Approver, and
                        ApprovalObserver interfaces; MultiApprover fans one broker-owned request
```

## Dashboard UI

The embedded dashboard in `internal/dashboard/index.html` should use the shared Agent Tools dashboard language:

- Dark blue-black base tokens: `--bg #080b10`, `--panel #101722`, `--panel-strong #151f2d`, `--panel-sunken #05070b`, `--text #e6edf3`, `--muted #8b98a8`, `--line #263244`.
- Product identity comes from `--product-accent` and `--product-accent-2`; MCP Broker uses green/amber (`#22c55e`, `#f59e0b`).
- Use system UI and monospace font stacks; do not add external web-font dependencies.
- Keep shared component treatment consistent: 28px header icons, 12px panels/cards, 8px controls, subtle translucent panels, selected rows/tabs marked with the product accent, status dots with a soft glow, and pill chips/badges for compact state.
- Keep `favicon.svg` in the shared 64x64 rounded-square family, changing only the glyph/accent for the product identity.

## Conventions

- Errors are wrapped with context: `fmt.Errorf("doing X: %w", err)`
- Listener binds loopback only. `serve.go` calls `server.ValidateLoopbackAddr` before `ListenAndServe`, which rejects anything but `127.0.0.0/8`, `::1`, or `localhost`. The bearer token is defense-in-depth; the network boundary is the load-bearing security boundary. Sandboxed agents reach the broker via Lima's user-mode forwarding of `host.lima.internal` to the host's loopback — do not relax this to support non-loopback binds
- `/mcp` requests are wrapped by `limitRequestBody`; keep body-limit changes scoped to MCP requests so dashboard/API routes are unaffected
- Audit write errors are intentionally discarded (`_ =`) — the pipeline should not fail because audit failed
- Logger nil handling is package-specific: broker/dashboard tolerate nil, while manager/backend construction expects a real logger. Do not add new nil-logger call paths without either guarding calls or providing a default logger.
- `expandEnv` in server package uses `os.ExpandEnv` — supports `$VAR` and `${VAR}` anywhere in the value (e.g., `"Bearer $TOKEN"`)
- Config file permissions: `0o600` for files, `0o750` for directories
- `mcp-go` HTTP client constructor is `client.NewStreamableHttpClient` (lowercase h); Streamable HTTP backends must include `transport.WithHTTPTimeout(httpBackendTimeout(srv))` so hung backend requests are bounded
- `ncruces/go-sqlite3` only needs the `driver` blank import; do not import deprecated `embed`
- OAuth is auto-detected via 401 responses; tokens stored in OS keychain via `go-keyring` (service: `mcp-broker`, key: server name)
- Startup retry timeouts intentionally do not cap interactive OAuth browser flows or their immediate post-authorization handshake. Startup and runtime browser flows use the broker's parent context with `oauthFlowTimeout` (five minutes); treat timeout/cancel/denial as non-retryable
- go-keyring's macOS backend caps `Set` at ~4096 bytes because it pipes the value to `security -i`, whose interactive parser truncates lines at that length; large tokens (e.g. Atlassian JWTs) fail with `data passed to Set was too big`. All keychain writes/reads go through `keychainSet`/`keychainGet` in `oauth.go`. On `ErrSetDataTooBig`, `keychainSet` splits the value into sub-4KB `<key>.chunk.N` items (each written via go-keyring's secure stdin path, so the secret never reaches the `ps` table) and writes a `go-keyring-chunked:<count>` manifest at the primary key (manifest last, so a partial write never points at missing chunks); `keychainGet` reassembles transparently. Chunking only triggers on backends that report the cap (macOS) — Linux's secret-service stores large values directly. A successful direct (small) write clears any stale chunks from a prior large value, and `ClearCredentials` deletes the token's chunks too. `keychainChunkSize` is a var so tests can shrink it (see `keychain_chunk_test.go`)
- OAuth dynamic client registration (RFC 7591) is persisted in a second keychain entry per server (service: `mcp-broker`, key: `<serverName>.client`) so that refresh tokens survive restart — without it, every restart re-registers and the server rejects the prior refresh token
- Tool-call retry: `httpBackend.CallTool` and `ListTools` retry once on `isUnauthorized(err)` to work around transient refresh failures (e.g. [atlassian/atlassian-mcp-server#12](https://github.com/atlassian/atlassian-mcp-server/issues/12)); second failure propagates
- Streamable HTTP session recovery: a terminated or missing MCP session rebuilds and initializes the client, then retries the rejected operation once. `httpBackend` uses an RW mutex plus stale-client identity check so concurrent calls share one reconnect; SSE backends use the same type but have no reconnect function
- HTTP/SSE backends use plain client first, auto-upgrade to OAuth on 401 — do NOT use `client.NewOAuthStreamableHttpClient` directly as it proactively triggers OAuth flows even on non-OAuth servers
- OAuth callback port is deterministic per server name (FNV hash → ephemeral port range)
- Canonical `agent-token` and `admin-token` files are `0o600` under `0o750` directories; each is 32 random bytes hex-encoded and the pair must remain distinct
- Legacy `auth-token` is migration-only input that maps to agent; never restore it as request-time or reload fallback
- `/mcp` accepts agent only; dashboard/root/catch-all accepts admin only; `/healthz` and `/dashboard/unauthorized` are public
- Role mutations hold the advisory lock and use durable atomic replacement; request paths read one immutable pair and compare in constant time
- `SIGHUP` reloads rules and role files independently without listener/backend churn; invalid candidates retain only their prior role
- Dashboard auth uses only admin in the `mcp-broker-auth` cookie (`HttpOnly`, `SameSite=Strict`); printing/opening its tokenized URL requires interactive stdout
- Provisioning may reference only `agent-token`; `token show` and `token rotate` always require an explicit `agent|admin` argument
- Telegram approver uses long-polling (`getUpdates?timeout=30`) — no inbound connections needed; correlates responses by Telegram `message_id`
- `expandEnv` for Telegram token/chat_id is applied at startup in `serve.go` via `os.ExpandEnv`, not in the config package
- Approval hooks are observers, never approvers: emit only after final `require-approval` passes reject/no-approver gates and immediately before `Approver.Review`; hook status/output must never affect authorization, proxying, or audit
- The broker owns each approval's 128-bit ID and UTC timestamp; the same typed `ApprovalRequest` goes to hooks, dashboard, and Telegram. Preserve valid grant metadata on base/default fallthrough and never include raw grant/auth tokens
- Hook config is startup-only. Preserve raw `$VAR`/`${VAR}` values through config load/refresh and expand handler environment once in `internal/hooks` construction
- Hook admission must remain a non-blocking try operation bounded by both queue slots and queued bytes. Handler deadlines start at admission, queued work must recheck expiry/cancellation before process start, and jobs are never retried
- Hook logs are metadata-only: event, handler index, request ID, duration/status, and drop/timeout classification. Never log command, argv, environment, tool input, stdout/stderr, or raw subprocess errors
- Dispatcher close has one accepting/closed linearization point: reject new work, drain queued byte reservations, cancel running commands through the broker lifetime, and wait only under the caller context. Concurrent/repeated `Observe`/`Close` must remain race-safe
- Hook commands use direct exec; leave stdout/stderr nil so `os/exec` attaches the operating-system null device without broker-owned copy pipes. macOS/Linux runners create process groups, TERM then KILL the group with bounded grace, and always wait the direct child; other targets intentionally guarantee only direct-process cancellation
