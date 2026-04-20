# local-gomod-proxy Design

## Motivation

Sandboxed AI agents often work in Go projects that depend on private modules hosted in private GitHub repositories. On the host, those dependencies resolve transparently via the user's git credentials (SSH keys, credential helpers). Inside a sandbox (Lima VM managed by `sb`), those credentials are intentionally absent — so `go mod download` fails for any private dependency.

`local-git-mcp` solves this for explicit git operations the agent performs on its working repo, but it does not help when the Go toolchain needs to resolve a transitive private dependency during module graph resolution (`go build`, `go test`, `go mod tidy`).

local-gomod-proxy solves this by running a minimal HTTP server on the host that implements the Go module proxy protocol. Public modules are forwarded to `proxy.golang.org`. Private modules are resolved using the host's git credentials and served back to the sandbox.

The proxy is **unauthenticated** and binds to the host loopback (`127.0.0.1:7070`) by default. The Lima sandbox reaches it via `host.lima.internal:7070` — Lima's default user-mode networking forwards the guest's `host.lima.internal` to the host's loopback, so no bridge IP discovery is needed. The host still holds the git credentials; the sandbox reaches the proxy over the bridge and carries nothing. See [Design decisions](#design-decisions) for why there is no auth.

## Architecture

local-gomod-proxy is a single HTTP binary. No config file, no persistent state.

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
2. The router checks the module path against the configured `GOPRIVATE` glob patterns using `golang.org/x/mod/module.MatchPrefixPatterns` — the same function Go's own toolchain uses.
3. **Private match** — `PrivateFetcher` shells out to `go mod download -json <module>@<version>` in the server's working directory, inheriting the host's git credentials via its environment. It parses the JSON result for absolute paths to the `.info`, `.mod`, and `.zip` files inside the host's `GOMODCACHE` and streams those files back.
4. **No private match** — `PublicFetcher` reverse-proxies the request unchanged to `https://proxy.golang.org/<same-path>`.
5. The response flows back to the sandbox's `go` tool.

## Protocol endpoints

Standard Go module proxy protocol:

| Endpoint                          | Purpose                 |
| --------------------------------- | ----------------------- |
| `GET /<module>/@v/list`           | List available versions |
| `GET /<module>/@v/<version>.info` | Version metadata (JSON) |
| `GET /<module>/@v/<version>.mod`  | The `go.mod` file       |
| `GET /<module>/@v/<version>.zip`  | Module source zip       |
| `GET /<module>/@latest`           | Latest version info     |

For private modules, `/list` is implemented via `go list -m -json -versions <module>@latest` (the `-versions` flag populates the full version set), and `/@latest` via `go list -m -json <module>@latest` (no `-versions` — only the resolved latest is needed). Output is transformed to the proxy protocol's expected shape. For public modules, all endpoints are forwarded to `proxy.golang.org` unchanged.

## Project structure

```
local-gomod-proxy/
├── cmd/
│   └── local-gomod-proxy/
│       ├── main.go              # Entry point
│       ├── root.go              # Cobra root command, DI wiring
│       └── serve.go             # `serve` subcommand
├── internal/
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
│   │   ├── classify.go          # Maps toolchain error strings to 404 vs 502
│   │   ├── classify_test.go
│   │   └── integration_test.go  # Integration tests (go:build integration)
│   ├── public/
│   │   ├── public.go            # PublicFetcher — httputil.ReverseProxy to proxy.golang.org
│   │   └── public_test.go
│   └── server/
│       ├── server.go            # HTTP handler wiring router + fetchers
│       └── server_test.go
├── test/
│   └── e2e/
│       └── e2e_test.go          # E2E tests (go:build e2e)
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

Errors from `go mod download` include the command's stderr so callers get actionable output (e.g., "repository not found", "permission denied").

Errors are classified before responding. When the toolchain emits a known "module/version does not exist" signal (`unknown revision`, `invalid version`, `repository does not exist`, `repository not found`, `no matching versions`, or an upstream `404 Not Found` / `410 Gone`), the server responds with **HTTP 404** so the Go client surfaces a clean "not found" error and, if multiple GOPROXY sources are configured, falls through to the next. Everything else (auth failures, network errors, unexpected toolchain output) stays **502** so transient issues are not silently masked as missing modules. Classification lives in `internal/private/classify.go`; see that file's comments for the substring list and its sources. Caveat (golang/go#42751): GitHub returns 404 for inaccessible private repos, so an auth problem against GitHub can surface as `unknown revision` and be mapped to 404 — that is a Go tooling limitation we cannot disambiguate from the toolchain's output.

Errors that occur after the response headers are written (mid-stream `io.Copy` failures, client disconnects, disk I/O errors on `GOMODCACHE`) are wrapped with `ErrResponseCommitted`. The server handler logs these at `Warn` and returns without a second `http.Error`, avoiding the "superfluous WriteHeader" warning and the appended error-text bytes that would corrupt the in-flight artifact.

Startup validation:

| Variable     | If empty or missing                                                            |
| ------------ | ------------------------------------------------------------------------------ |
| `GOPRIVATE`  | Fail startup; suggest `go env -w GOPRIVATE=...` or `--private` flag            |
| `GOMODCACHE` | Fail startup (defensive — Go always defaults this)                             |
| `GOVERSION`  | Warn if `< 1.21` (too strict to fail, but modules using `toolchain` may break) |

## Security

- **Local-only deployment** — the proxy is unauthenticated. Its security model relies entirely on being reachable only from the co-located sandbox. The default `--addr` is `127.0.0.1:7070` (host loopback); the Lima sandbox reaches it via `host.lima.internal:7070`. Overriding `--addr` to a public interface or `0.0.0.0` exposes the host's git credentials to anyone who can reach the port.
- **No shell interpolation** — `go mod download` is invoked via `os/exec` with an argv slice. Module paths and versions are URL-unescaped via `module.UnescapePath` / `module.UnescapeVersion` before use; `go mod download` rejects malformed inputs itself.
- **Plain HTTP** — traffic stays on the Lima bridge and never leaves the host.
- **Request logging** — module path, version, and private/public verdict logged via `log/slog`.

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

**No application-level auth.** Go ≥ 1.22 (cf. [Go issue #42135](https://github.com/golang/go/issues/42135)) refuses to send URL-embedded credentials over plain HTTP, and every other auth mechanism the `go` tool supports (`.netrc`, `GOAUTH`) is likewise HTTPS-gated (`cmd/go/internal/auth/auth.go` panics if the request scheme isn't HTTPS). Adding auth therefore requires TLS termination. Given the trust boundary — the sandbox is a co-located peer on a host-local bridge and the host already holds the git credentials — the cost of cert provisioning outweighs the benefit. Security is enforced at the network layer: bind to a local-only interface so no external caller can reach the port. This is the same posture Athens recommends for local dev deployments.

**Plain HTTP, no TLS.** Traffic stays on the Lima bridge and never reaches the public internet. TLS adds cert-provisioning complexity for no real-world benefit at this trust boundary.

**Read `GOPRIVATE` and `GOMODCACHE` via `go env -json`, not `os.Getenv`.** Users commonly set these via `go env -w`, which persists to `~/.config/go/env` and is invisible to `os.Getenv`. Reading via `go env` gives a single source of truth matching what the host toolchain actually uses.

**Fail startup if `GOPRIVATE` is unset.** With no private patterns, every request passes through to `proxy.golang.org` and the proxy adds no value. Fail loud rather than degrade silently.

**Rely on the host's `GOMODCACHE`, no separate proxy cache.** `go mod download` populates the shared host cache. Subsequent requests for the same `<module>@<version>` hit the same cache entry. Zero extra code, automatic cleanup via `go clean -modcache`, no cache-coherence bugs.

**Graceful shutdown.** On SIGINT/SIGTERM, the HTTP server is given 5 s to drain in-flight requests via `Server.Shutdown`. `exec.Runner.Run` takes a `context.Context` and `OSRunner` uses `exec.CommandContext`, so `Server.Shutdown`'s per-request context cancellation also kills any in-flight `go mod download` / `go list` subprocess (SIGKILL). The same mechanism propagates client disconnects: a sandbox client aborting its HTTP request cancels the request context, which terminates the subprocess instead of letting it complete unwanted work.

## Testing

| Layer                             | What it covers                                                                                                                      |
| --------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------- |
| Unit (`make test`)                | Mock `exec.Runner` for PrivateFetcher; mock upstream HTTP for PublicFetcher; unit-test GOPRIVATE glob matching                      |
| Integration (`-tags=integration`) | Real `go mod download` against a local file:// git repo; PrivateFetcher serves the correct bytes                                    |
| E2E (`-tags=e2e`)                 | Build real binary, start it, run `go mod download` against it as a subprocess — exercises the full wire protocol for public modules |
