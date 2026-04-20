# local-gomod-proxy

A host-side Go module proxy that lets sandboxed agents resolve private Go dependencies without holding the host's git credentials. Public modules are forwarded to `proxy.golang.org`; private modules (matched by `GOPRIVATE` patterns) are fetched via the host's git credentials and served back to the sandbox.

> **Local-only. Do not expose to the public internet.** The proxy runs unauthenticated over plain HTTP. It defaults to binding `127.0.0.1:7070`, which the Lima sandbox reaches via `host.lima.internal:7070` (Lima's default user-mode networking forwards the guest's `host.lima.internal` to the host's loopback). Do not override `--addr` to a public interface or `0.0.0.0` — doing so lets anyone on the network resolve modules against your git credentials.

## Install

```bash
# From the local-gomod-proxy subdirectory:
make install

# Or from the repo root:
make install
```

### Go version requirements

- **Host** — the proxy shells out to the host's `go` binary (`go mod download`, `go env -json`), so the host must have a Go toolchain installed. Go ≥ 1.21 is recommended; the proxy logs a warning on older versions because modules that use the `toolchain` directive may fail to resolve.
- **Building the proxy** — `go.mod` pins a specific Go release. Any Go ≥ 1.21 can build it via the `toolchain` auto-fetch; you don't need that exact version on the host.
- **Sandbox client** — no specific requirement beyond a `go` binary that speaks the module proxy protocol (Go 1.11+).

## Run

```bash
local-gomod-proxy serve [--addr 127.0.0.1:7070] [--private PATTERN] [--upstream URL]
```

| Flag         | Default                      | Description                                                                                      |
| ------------ | ---------------------------- | ------------------------------------------------------------------------------------------------ |
| `--addr`     | `127.0.0.1:7070`             | Address to listen on. Loopback by default; the Lima sandbox reaches it via `host.lima.internal`. |
| `--private`  | _(reads `go env GOPRIVATE`)_ | GOPRIVATE-style glob patterns for private modules. Overrides `go env GOPRIVATE`.                 |
| `--upstream` | `https://proxy.golang.org`   | Public upstream proxy URL                                                                        |

`GOPRIVATE` must be set — either via `go env -w GOPRIVATE=github.com/your-org/*` on the host or via `--private`. If neither is set, startup fails with an actionable error.

## How the sandbox consumes it

If you use [`sandbox-manager`](../sandbox-manager/), add [`examples/provision/gomod-proxy.sh`](examples/provision/gomod-proxy.sh) to your `~/.config/sb/config.json`:

```json
{
  "scripts": [
    "~/Workspace/agent-tools/local-gomod-proxy/examples/provision/gomod-proxy.sh"
  ]
}
```

Otherwise, apply the equivalent configuration by hand inside the sandbox:

```sh
export GOPROXY=http://host.lima.internal:7070/
export GOSUMDB=off
# Explicitly clear GOPRIVATE in the sandbox. If it is set (inherited from a shell
# profile, image template, or `go env -w`), matching modules bypass GOPROXY and
# try to clone directly from git — which fails because the sandbox has no
# credentials. Routing everything through the proxy is the whole point.
unset GOPRIVATE
go env -u GOPRIVATE  # clears any `go env -w` persisted value
```

`GOSUMDB=off` is acceptable in the sandbox because `go.sum` (committed to the repo) is the primary integrity check.

## Run as a launchd agent (macOS)

To keep the proxy running in the background whenever you're logged in, install it as a per-user LaunchAgent. See [docs/launchd.md](docs/launchd.md) for setup (including git auth under launchd), install, verify, and manage steps.

## Security

- **Run on a local-only interface.** This proxy is unauthenticated. Anyone who can reach its listen address can resolve modules against your host's git credentials. The default `--addr` of `127.0.0.1:7070` binds loopback only; the Lima sandbox still reaches it via `host.lima.internal:7070` because Lima's default user-mode networking forwards the guest's `host.lima.internal` to the host loopback. Do not override `--addr` to a public interface, a VPN-reachable interface, or `0.0.0.0`. If you run a custom Lima network (`networks:` in `lima.yaml`) whose gateway is not the host loopback, bind explicitly to that gateway IP instead.
- Module paths are validated before any shell-out. No shell interpolation — `go mod download` is invoked via `os/exec` with an argv slice.

## Development

```bash
make build              # Build binary to ./local-gomod-proxy
make test               # Run unit tests with race detector
make test-integration   # Run integration tests (-tags=integration)
make test-e2e           # Run E2E tests (-tags=e2e)
make lint               # Run golangci-lint
make fmt                # Format with goimports
make tidy               # go mod tidy + verify
make audit              # tidy + fmt + lint + test + govulncheck
```

See [DESIGN.md](DESIGN.md) for the full architecture and design decisions.
