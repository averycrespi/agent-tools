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
  auth/                 Bearer token auth: generation, file storage, HTTP middleware
  broker/               Orchestrator with ServerManager, AuditLogger, Approver interfaces;
                        MultiApprover fans requests to all approvers with shared timeout
```

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
- Startup retry timeouts intentionally do not cap interactive OAuth browser flows or their immediate post-authorization handshake; once real user auth begins, use the broker's parent startup context so login is not interrupted, and treat cancelled/denied OAuth as non-retryable
- go-keyring's macOS backend caps `Set` at ~4096 bytes because it pipes the value to `security -i`, whose interactive parser truncates lines at that length; large tokens (e.g. Atlassian JWTs) fail with `data passed to Set was too big`. All keychain writes/reads go through `keychainSet`/`keychainGet` in `oauth.go`. On `ErrSetDataTooBig`, `keychainSet` splits the value into sub-4KB `<key>.chunk.N` items (each written via go-keyring's secure stdin path, so the secret never reaches the `ps` table) and writes a `go-keyring-chunked:<count>` manifest at the primary key (manifest last, so a partial write never points at missing chunks); `keychainGet` reassembles transparently. Chunking only triggers on backends that report the cap (macOS) — Linux's secret-service stores large values directly. A successful direct (small) write clears any stale chunks from a prior large value, and `ClearCredentials` deletes the token's chunks too. `keychainChunkSize` is a var so tests can shrink it (see `keychain_chunk_test.go`)
- OAuth dynamic client registration (RFC 7591) is persisted in a second keychain entry per server (service: `mcp-broker`, key: `<serverName>.client`) so that refresh tokens survive restart — without it, every restart re-registers and the server rejects the prior refresh token
- Tool-call retry: `httpBackend.CallTool` and `ListTools` retry once on `isUnauthorized(err)` to work around transient refresh failures (e.g. [atlassian/atlassian-mcp-server#12](https://github.com/atlassian/atlassian-mcp-server/issues/12)); second failure propagates
- HTTP/SSE backends use plain client first, auto-upgrade to OAuth on 401 — do NOT use `client.NewOAuthStreamableHttpClient` directly as it proactively triggers OAuth flows even on non-OAuth servers
- OAuth callback port is deterministic per server name (FNV hash → ephemeral port range)
- Auth token file permissions: `0o600`, parent directories: `0o750`
- Auth token is 32 random bytes, hex-encoded (64 chars)
- Token comparison uses `crypto/subtle.ConstantTimeCompare`
- Dashboard auth uses `mcp-broker-auth` cookie (`HttpOnly`, `SameSite=Strict`)
- Telegram approver uses long-polling (`getUpdates?timeout=30`) — no inbound connections needed; correlates responses by Telegram `message_id`
- `expandEnv` for Telegram token/chat_id is applied at startup in `serve.go` via `os.ExpandEnv`, not in the config package
