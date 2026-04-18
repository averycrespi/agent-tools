# local-gomod-proxy Design

## Motivation

Sandboxed AI agents often work in Go projects that depend on private modules hosted in private GitHub repositories. On the host, those dependencies resolve transparently via the user's git credentials (SSH keys, credential helpers). Inside a sandbox (Lima VM managed by `sb`), those credentials are intentionally absent — so `go mod download` fails for any private dependency.

`local-git-mcp` solves this for explicit git operations the agent performs on its working repo, but it does not help when the Go toolchain needs to resolve a transitive private dependency during module graph resolution (`go build`, `go test`, `go mod tidy`).

local-gomod-proxy solves this by running a minimal HTTP server on the host that implements the Go module proxy protocol. Public modules are forwarded to `proxy.golang.org`. Private modules are resolved using the host's git credentials and served back to the sandbox. The sandbox holds only a short-lived bearer token — no git credentials.

This follows the same pattern as `mcp-broker`, `local-git-mcp`, and `local-gh-mcp`: the host holds credentials, the sandbox holds only a scoped token.

## Architecture

local-gomod-proxy is a single HTTP binary. No config file, no persistent state beyond the auth token file.

```
                         ┌────────────────────────────────┐
                         │  host: local-gomod-proxy       │
 sandbox (Lima VM)       │                                │
 ┌────────────┐          │   ┌─ router (GOPRIVATE) ─┐     │
 │  go build  │ ──HTTP──►│   │                      ▼     │
 │            │          │   │           PrivateFetcher   │
 │ GOPROXY=   │          │   │           (go mod download)│──► github.com (host git creds)
 │ http://... │          │   │                            │
 │            │          │   │           PublicFetcher    │
 │            │          │   │           (httputil        │──► proxy.golang.org
 │            │          │   │            .ReverseProxy)  │
 └────────────┘          │   └────────────────────────────┘
                         └────────────────────────────────┘
```

### Request flow

1. The sandbox's `go` tool makes a Go module proxy protocol request (`GET /<module>/@v/...`).
2. The HTTP server validates HTTP Basic auth against the stored bearer token (constant-time compare).
3. The router checks the module path against the configured `GOPRIVATE` glob patterns using `golang.org/x/mod/module.MatchPrefixPatterns` — the same function Go's own toolchain uses.
4. **Private match** — `PrivateFetcher` shells out to `go mod download -json <module>@<version>` in the server's working directory, inheriting the host's git credentials via its environment. It parses the JSON result for absolute paths to the `.info`, `.mod`, and `.zip` files inside the host's `GOMODCACHE` and streams those files back.
5. **No private match** — `PublicFetcher` reverse-proxies the request unchanged to `https://proxy.golang.org/<same-path>`.
6. The response flows back to the sandbox's `go` tool.

## Protocol endpoints

Standard Go module proxy protocol:

| Endpoint                          | Purpose                 |
| --------------------------------- | ----------------------- |
| `GET /<module>/@v/list`           | List available versions |
| `GET /<module>/@v/<version>.info` | Version metadata (JSON) |
| `GET /<module>/@v/<version>.mod`  | The `go.mod` file       |
| `GET /<module>/@v/<version>.zip`  | Module source zip       |
| `GET /<module>/@latest`           | Latest version info     |

For private modules, `/list` and `/@latest` are implemented via `go list -m -json -versions <module>@latest` with output transformed to the proxy protocol's expected shape. For public modules, all endpoints are forwarded to `proxy.golang.org` unchanged.

## Project structure

```
local-gomod-proxy/
├── cmd/
│   └── local-gomod-proxy/
│       ├── main.go              # Entry point
│       ├── root.go              # Cobra root command, DI wiring
│       ├── serve.go             # `serve` subcommand
│       └── token.go             # `token` subcommand — prints current token to stdout
├── internal/
│   ├── auth/
│   │   ├── token.go             # Token gen/store/load (XDG path, 0600)
│   │   ├── token_test.go
│   │   ├── middleware.go        # HTTP Basic auth middleware (constant-time compare)
│   │   └── middleware_test.go
│   ├── exec/
│   │   ├── exec.go              # Runner interface (same pattern as siblings)
│   │   └── exec_test.go
│   ├── goenv/
│   │   ├── goenv.go             # Reads GOPRIVATE / GOMODCACHE / GOVERSION via `go env -json`
│   │   └── goenv_test.go
│   ├── router/
│   │   ├── router.go            # GOPRIVATE glob matching via module.MatchPrefixPatterns
│   │   └── router_test.go
│   ├── private/
│   │   ├── fetcher.go           # PrivateFetcher — go mod download + file streaming
│   │   ├── fetcher_test.go
│   │   ├── parse.go             # URL path parser for module/version/endpoint
│   │   ├── parse_test.go
│   │   └── integration_test.go  # Integration tests (go:build integration)
│   ├── public/
│   │   ├── public.go            # PublicFetcher — httputil.ReverseProxy to proxy.golang.org
│   │   └── public_test.go
│   └── server/
│       ├── server.go            # HTTP handler wiring router + fetchers + auth
│       └── server_test.go
├── test/
│   └── e2e/
│       └── e2e_test.go          # E2E tests (go:build e2e; currently skipped — see Security)
├── go.mod
├── Makefile
├── CLAUDE.md
├── DESIGN.md
└── README.md
```

## Validation and errors

Every request validates the module path and version before any shell-out:

1. **URL parse** — path must match the Go module proxy protocol pattern.
2. **Module path** — URL-unescaped via `module.UnescapePath` before being passed as argv to `go mod download`. No shell interpolation — argv slice only. `go mod download` rejects malformed module paths itself.
3. **Version** — URL-unescaped via `module.UnescapeVersion` before being passed as argv to `go mod download`; `go mod download` rejects malformed versions itself.
4. **Auth** — HTTP 401 for missing or invalid token; checked before routing.

Errors from `go mod download` include the command's stderr so callers get actionable output (e.g., "repository not found", "permission denied").

Startup validation:

| Variable     | If empty or missing                                                            |
| ------------ | ------------------------------------------------------------------------------ |
| `GOPRIVATE`  | Fail startup; suggest `go env -w GOPRIVATE=...` or `--private` flag            |
| `GOMODCACHE` | Fail startup (defensive — Go always defaults this)                             |
| `GOVERSION`  | Warn if `< 1.21` (too strict to fail, but modules using `toolchain` may break) |

## Security

- **Auth gate** — HTTP Basic bearer token, constant-time comparison (`crypto/subtle.ConstantTimeCompare`). The username field is ignored; only the password (token) is checked.
- **Token storage** — `0600` file, parent directory `0750`, under `$XDG_CONFIG_HOME/local-gomod-proxy/auth-token`.
- **No shell interpolation** — `go mod download` is invoked via `exec.Command` with an argv slice. Module paths and versions are URL-unescaped via `module.UnescapePath` / `module.UnescapeVersion` before use; `go mod download` rejects malformed inputs itself.
- **Plain HTTP** — traffic stays on the Lima bridge and never leaves the host. See Design decisions below.
- **Auth-over-HTTP limitation** — Go >= 1.22 refuses to send URL-embedded Basic Auth credentials over plain HTTP (`cmd/go/internal/web/http.go:244`, Go issue #42135). Production use requires TLS termination or an alternative auth transport. See README for details.
- **Request logging** — module path, version, private/public verdict, cache hit/miss, and latency logged via `log/slog`. The token is never logged.

## Tech stack

| Component    | Library                                                               |
| ------------ | --------------------------------------------------------------------- |
| CLI          | [cobra](https://github.com/spf13/cobra)                               |
| Module paths | [golang.org/x/mod/module](https://pkg.go.dev/golang.org/x/mod/module) |
| Public proxy | stdlib `net/http/httputil.ReverseProxy`                               |
| Logging      | `log/slog` (stdlib)                                                   |
| Testing      | [testify](https://github.com/stretchr/testify)                        |

No Athens, no `golang.org/x/mod/zip` — `go mod download` hands us finished artifacts.

## Design decisions

**Shell out to `go mod download` instead of implementing the module protocol natively.** Go's own tooling already knows how to clone a git repo, resolve pseudo-versions, build canonical source zips, and populate a content-addressed cache. Re-implementing that in-process duplicates a moving target. Shell-out inherits the user's `GOPRIVATE`, git credential helpers, SSH keys, and toolchain settings for free.

**Reverse-proxy public modules to `proxy.golang.org`.** Leverages the upstream CDN and existing cache. Zero host CPU for the common case. The sandbox doesn't need direct egress to `proxy.golang.org` — only to the host.

**Bearer token auth via HTTP Basic.** Mirrors `mcp-broker`'s token model. 32 random bytes, hex-encoded. The token's only job is to block other machines on the Lima bridge from reaching the proxy. A compromised sandbox already has access to whatever private source it downloaded.

**Plain HTTP, no TLS.** Traffic stays on the Lima bridge and never reaches the public internet. TLS adds cert-provisioning complexity for no real-world benefit at this trust boundary. However, Go >= 1.22 refuses URL-embedded Basic Auth over plain HTTP — so until TLS termination is added, production deployments must use an alternative auth transport. See README.

**Read `GOPRIVATE` and `GOMODCACHE` via `go env -json`, not `os.Getenv`.** Users commonly set these via `go env -w`, which persists to `~/.config/go/env` and is invisible to `os.Getenv`. Reading via `go env` gives a single source of truth matching what the host toolchain actually uses.

**Fail startup if `GOPRIVATE` is unset.** With no private patterns, every request passes through to `proxy.golang.org` and the proxy adds no value. Fail loud rather than degrade silently.

**Rely on the host's `GOMODCACHE`, no separate proxy cache.** `go mod download` populates the shared host cache. Subsequent requests for the same `<module>@<version>` hit the same cache entry. Zero extra code, automatic cleanup via `go clean -modcache`, no cache-coherence bugs.

**Graceful shutdown.** On SIGINT/SIGTERM, the HTTP server is given 5 s to drain in-flight requests via `Server.Shutdown`. In-flight `go mod download` subprocesses do not receive context cancellation — they run to completion (this is a known limitation; upgrading `exec.Runner` to accept a `context.Context` is future work).

## Testing

| Layer                             | What it covers                                                                                                                                                                                              |
| --------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Unit (`make test`)                | Mock `exec.Runner` for PrivateFetcher; mock upstream HTTP for PublicFetcher; unit-test GOPRIVATE glob matching; unit-test token gen + constant-time compare                                                 |
| Integration (`-tags=integration`) | Real `go mod download` against a local file:// git repo; PrivateFetcher serves the correct bytes                                                                                                            |
| E2E (`-tags=e2e`)                 | Build real binary, start it, run `go mod download` as subprocess — exercises the full wire protocol. **Currently skipped** pending TLS support (Go >= 1.22 refuses URL-embedded Basic Auth over plain HTTP) |
