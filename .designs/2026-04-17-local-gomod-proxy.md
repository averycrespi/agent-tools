# local-gomod-proxy

Design for a host-side Go module proxy that lets sandboxed agents resolve private Go dependencies without ever holding the host's git credentials.

## Problem

Some of the user's Go projects depend on private modules hosted in private GitHub repositories. On the host, these resolve via the user's normal git credentials (SSH keys, credential helpers). Inside a sandbox (Lima VM managed by `sb`), those credentials are intentionally absent — so `go mod download` fails for anything private.

The existing `local-git-mcp` solves this for explicit git operations (push/pull/fetch on a repo the agent is working in), but it does not help when the agent's `go build` needs to resolve a transitive private dependency during module graph resolution.

## Goal

A new tool, `local-gomod-proxy`, that runs on the host, holds no secrets itself beyond what the host user already has (git credentials, Go toolchain), and exposes the Go module proxy protocol to the sandbox. Public modules pass through to `proxy.golang.org`. Private modules are resolved using the host's git credentials and served back to the sandbox.

This fits the same pattern as `mcp-broker`, `local-git-mcp`, and `local-gh-mcp`: host holds credentials, sandbox holds only a scoped bearer token.

## Non-goals

- Not a general-purpose public Go module proxy (no Athens-style persistent cache, no multi-tenant auth).
- Not a sandbox provisioner. Configuring `GOPROXY` / `GOSUMDB` inside the sandbox is `sandbox-manager`'s responsibility.
- Not a credential manager. The proxy inherits the host user's existing git setup.

## Architecture

Single Go binary, single HTTP port, no persistent state beyond an auth token file.

```
                             ┌────────────────────────────────┐
                             │  host: local-gomod-proxy       │
   sandbox (Lima VM)         │                                │
   ┌────────────┐            │   ┌─ router (GOPRIVATE) ─┐     │
   │  go build  │ ──HTTP──►  │   │                      ▼     │
   │            │            │   │           PrivateFetcher   │
   │ GOPROXY=   │            │   │           (go mod download)│──► github.com (host git creds)
   │ http://_:T@│            │   │                            │
   │ host.lima  │            │   │           PublicFetcher    │
   │ .internal  │            │   │           (httputil        │──► proxy.golang.org
   │ :7070/     │            │   │            .ReverseProxy)  │
   └────────────┘            │   └────────────────────────────┘
                             └────────────────────────────────┘
```

### Request flow

1. Sandbox's `go` tool makes a Go module proxy protocol request (`GET /<module>/@v/...`).
2. HTTP server validates HTTP Basic auth against the stored bearer token.
3. Router checks the module path against the configured `GOPRIVATE` glob patterns (via `golang.org/x/mod/module.MatchPrefixPatterns`, the same function Go's own toolchain uses).
4. **Private match** → `PrivateFetcher` shells out to `go mod download -json <module>@<version>` in a scratch dir, with the host's git credentials inherited. Parses the JSON result for absolute paths to the `.info`, `.mod`, `.zip` files inside the host's `GOMODCACHE`. Streams those files back.
5. **No private match** → `PublicFetcher` reverse-proxies the request unchanged to `https://proxy.golang.org/<same-path>`.
6. The response flows back to the sandbox's `go` tool.

### Endpoints

Standard Go module proxy protocol:

| Endpoint                          | Purpose                 |
| --------------------------------- | ----------------------- |
| `GET /<module>/@v/list`           | List available versions |
| `GET /<module>/@v/<version>.info` | Version metadata (JSON) |
| `GET /<module>/@v/<version>.mod`  | The `go.mod` file       |
| `GET /<module>/@v/<version>.zip`  | Module source zip       |
| `GET /<module>/@latest`           | Latest version info     |

For private modules, `/list` and `/@latest` are implemented via `go list -m -json -versions <module>@latest` with output transformed to the proxy protocol's expected shape.

## Key design decisions

### Shell out to `go mod download` instead of implementing the module protocol natively

**Decision:** For private modules, invoke the host's `go` binary (`go mod download -json`) and serve the resulting files.

**Why:** Go's own tooling already knows how to clone a git repo, resolve pseudo-versions, build canonical source zips (via `golang.org/x/mod/zip`), and populate a content-addressed cache. Re-implementing that in-process duplicates a moving target. Shell-out inherits the user's `GOPRIVATE`, git credential helpers, SSH keys, and toolchain auto-upgrade (for the `toolchain` directive) for free.

### Reverse-proxy public modules to proxy.golang.org

**Decision:** For anything that doesn't match `GOPRIVATE`, `httputil.ReverseProxy` streams bytes from `proxy.golang.org` through unchanged.

**Why:** Leverages the upstream CDN and existing cache. Zero host CPU for the common case. Sandbox doesn't need egress to `proxy.golang.org` directly — only to the host.

### Bearer token auth, token embedded in GOPROXY URL via HTTP Basic

**Decision:** 32 random bytes, hex-encoded, stored at `$XDG_CONFIG_HOME/local-gomod-proxy/token` with `0600`. Sandbox sets `GOPROXY=http://_:<token>@host.lima.internal:7070/`. Proxy parses HTTP Basic and compares with `crypto/subtle.ConstantTimeCompare`.

**Why:** Mirrors `mcp-broker`'s token model exactly — one more reason to reuse `internal/auth` verbatim. HTTP Basic in the URL is what Go's module tooling natively supports (no `.netrc` wrangling).

**Trade-off accepted:** Token is visible via `env` inside the sandbox. Acceptable — the token's only job is to block other machines on the Lima bridge from reaching the proxy. A compromised sandbox already has the credentials to exfiltrate private source anyway.

### Plain HTTP, no TLS

**Decision:** Listen on `0.0.0.0:7070` (configurable) with plain HTTP.

**Why:** Traffic never leaves the Lima bridge on the host. TLS adds cert-provisioning complexity for zero real-world benefit at this trust boundary. Go's module tooling accepts `http://` URLs.

### Read `GOPRIVATE` and `GOMODCACHE` via `go env`, not process env

**Decision:** On startup, shell out to `go env -json GOPRIVATE GOMODCACHE GOVERSION`.

**Why:** Users commonly set `GOPRIVATE` via `go env -w GOPRIVATE=...`, which persists in `~/.config/go/env` and is not visible via `os.Getenv`. Reading through `go env` gives a single source of truth matching what the host toolchain actually uses.

**Startup behavior:**

| Var          | If empty                                                                                                                | Rationale                                                                                                              |
| ------------ | ----------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------- |
| `GOPRIVATE`  | **Fail startup** with actionable error (suggest `go env -w GOPRIVATE=...`). Overridable via `--private <pattern>` flag. | With no private patterns, every request passes through and the proxy adds no value. Fail loud, don't degrade silently. |
| `GOMODCACHE` | Fail startup (defensive — Go always defaults this).                                                                     | Required to locate downloaded artifacts.                                                                               |
| `GOVERSION`  | Warn if `< 1.21`.                                                                                                       | Pre-1.21 Go can't parse modules using the `toolchain` directive, but works fine otherwise — too strict to fail.        |

### Rely on the host's `GOMODCACHE`, no separate proxy cache

**Decision:** `go mod download` populates the host's shared module cache. Subsequent requests for the same `<module>@<version>` hit the same cache entry.

**Why:** Zero extra code, automatic cleanup via `go clean -modcache`, no cache-coherence bugs between the proxy and the host Go toolchain.

### Scope: serve only, no sandbox-side config

**Decision:** `local-gomod-proxy` is a server. A sibling `token` subcommand prints the current token to stdout so that `sandbox-manager` provisioning can read it and construct the `GOPROXY` URL inside the VM.

**Why:** Matches the separation of concerns between `mcp-broker` and `sandbox-manager` today. Keeps this tool focused and independently testable.

## Sandbox configuration

Documented in the tool's README and implemented by `sandbox-manager` provisioning (not by this tool):

```sh
export GOPROXY=http://_:<token>@host.lima.internal:7070/
export GOSUMDB=off
# Deliberately DO NOT set GOPRIVATE in the sandbox — that would bypass the proxy.
```

`GOSUMDB=off` inside the sandbox is acceptable because `go.sum` (committed to the repo) is the primary integrity check. `GOSUMDB` is a secondary trust layer guarding against a compromised proxy; here the proxy is the trusted component, and tampering would surface immediately via `go.sum` mismatch.

## Project structure

Follows `local-git-mcp` / `mcp-broker` conventions.

```
local-gomod-proxy/
├── CLAUDE.md
├── DESIGN.md                       # written during implementation
├── Makefile
├── README.md
├── go.mod
├── cmd/local-gomod-proxy/
│   ├── main.go                     # Cobra entry
│   ├── root.go                     # DI wiring
│   ├── serve.go                    # `serve` subcommand
│   └── token.go                    # `token` subcommand — prints current token
└── internal/
    ├── exec/                       # Runner interface (same pattern as siblings)
    ├── auth/                       # Bearer token gen/store/verify (mirrors mcp-broker)
    ├── goenv/                      # Wraps `go env -json` for GOPRIVATE/GOMODCACHE/GOVERSION
    ├── router/                     # GOPRIVATE glob matching, selects fetcher
    ├── private/                    # PrivateFetcher — shells out to `go mod download`
    ├── public/                     # PublicFetcher — reverse-proxy to proxy.golang.org
    └── server/                     # HTTP handlers wiring router + fetchers + auth
```

## Dependencies

| Dep                               | Purpose                                                         |
| --------------------------------- | --------------------------------------------------------------- |
| `github.com/spf13/cobra`          | CLI framework (same as siblings)                                |
| `golang.org/x/mod/module`         | `MatchPrefixPatterns` for GOPRIVATE globs, path escape/unescape |
| stdlib `net/http/httputil`        | `ReverseProxy` for public path                                  |
| stdlib `encoding/json`, `os/exec` | Shell-out + JSON parsing from `go mod download -json`           |
| `github.com/stretchr/testify`     | Test assertions (same as siblings)                              |

Explicitly not pulling in Athens or reimplementing `golang.org/x/mod/zip` — `go mod download` hands us finished artifacts.

## Security model

- **Trust boundary:** host trusts sandbox only enough to respond to module fetches; sandbox trusts host with git credentials and module resolution.
- **Auth gate:** bearer token, constant-time comparison. No rate-limiting (single-user deployment).
- **No arbitrary execution:** `go mod download` is invoked with a validated `<module>@<version>` pair parsed from the URL path. Module paths are validated via `module.CheckPath` before being passed to the shell. No shell interpolation — `exec.Command` with an argv slice.
- **File permissions:** token file `0600`, parent dir `0750` — matches `mcp-broker`.
- **Audit:** request logs include module path, version, private/public verdict, cache hit/miss, latency. Via `log/slog`, same as siblings.

## Testing

| Layer                             | What it covers                                                                                                                                                |
| --------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Unit (`make test`)                | Mock `exec.Runner` for PrivateFetcher; mock upstream HTTP for PublicFetcher; unit-test `GOPRIVATE` glob matching; unit-test token gen + constant-time compare |
| Integration (`-tags=integration`) | Test HTTP server impersonating `proxy.golang.org` → PublicFetcher; local file:// git repo → real `go mod download` → PrivateFetcher serves the correct bytes  |
| E2E (`-tags=e2e`)                 | Build real binary, start it, run `GOPROXY=http://localhost:... go mod download <real-module>` as subprocess — exercises the full wire protocol                |

All following the same pattern as `mcp-broker/test/e2e/` and `local-git-mcp/`.

## Operational concerns

- Graceful shutdown on `SIGTERM` / `SIGINT`: in-flight `go mod download` subprocesses receive context cancellation.
- Structured request logs via `log/slog` at info level; debug level adds command lines for `go mod download` invocations.
- Startup log line records host Go version, resolved `GOPRIVATE`, `GOMODCACHE` path, listen address, and a token-presence indicator (never the token itself).

## Open questions / deferred

- **Version discovery for private modules**: `go list -m -versions` requires network access to the VCS and can be slow for repos with many tags. Leaving as shell-out for now; revisit if it proves too slow.
- **Module deprecation / retraction**: not handled explicitly. `go mod download` surfaces these naturally via its own output; defer to Go's behavior.
- **Host toolchain drift**: if the host's `go` binary is updated mid-run, in-flight subprocesses still use the old one. Acceptable — operator restarts the proxy after toolchain upgrades.
