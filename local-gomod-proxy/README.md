# local-gomod-proxy

A host-side Go module proxy that lets sandboxed agents resolve private Go dependencies without holding the host's git credentials. Public modules are forwarded to `proxy.golang.org`; private modules (matched by `GOPRIVATE` patterns) are fetched via the host's git credentials and served back to the sandbox.

> **Local-only. Do not expose to the public internet.** The proxy requires TLS + HTTP Basic auth on every request. The credentials file at `$XDG_STATE_HOME/local-gomod-proxy/credentials` is a host-local secret — keep it on the host. The proxy defaults to binding `127.0.0.1:7070`, which the Lima sandbox reaches via `host.lima.internal:7070` (Lima's default user-mode networking forwards the guest's `host.lima.internal` to the host's loopback). Do not override `--addr` to a public interface or `0.0.0.0`.

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

| Flag          | Default                             | Description                                                                                           |
| ------------- | ----------------------------------- | ----------------------------------------------------------------------------------------------------- |
| `--addr`      | `127.0.0.1:7070`                    | Address to listen on. Loopback by default; the Lima sandbox reaches it via `host.lima.internal`.      |
| `--private`   | _(reads `go env GOPRIVATE`)_        | GOPRIVATE-style glob patterns for private modules. Overrides `go env GOPRIVATE`.                      |
| `--state-dir` | `$XDG_STATE_HOME/local-gomod-proxy` | Directory for TLS cert + credentials. Defaults under `~/.local/state/` if `$XDG_STATE_HOME` is unset. |
| `--upstream`  | `https://proxy.golang.org`          | Public upstream proxy URL                                                                             |

`GOPRIVATE` must be set — either via `go env -w GOPRIVATE=github.com/your-org/*` on the host or via `--private`. If neither is set, startup fails with an actionable error.

## How the sandbox consumes it

The sandbox needs two files from the host: the proxy's TLS cert (`cert.pem`) and the credentials file. The sandbox does **not** mount the host's `$HOME` — files must be copied in explicitly.

### With sandbox-manager

Add both files to `copy_paths` and the provisioning script to `scripts` in your `~/.config/sb/config.json`:

```json
{
  "copy_paths": [
    "~/.local/state/local-gomod-proxy/cert.pem",
    "~/.local/state/local-gomod-proxy/credentials"
  ],
  "scripts": [
    "/path/to/agent-tools/local-gomod-proxy/examples/provision/gomod-proxy.sh"
  ]
}
```

Both files land at `~/.local/state/local-gomod-proxy/` inside the sandbox. The provisioning script then installs the cert into the sandbox's system trust store (`sudo update-ca-certificates`) and writes `GOPROXY` to `~/.bashrc`.

**Cert rotation:** re-run `sb provision` after the host regenerates its cert — `copy_paths` re-runs before `scripts`, so the new cert flows through transparently.

### Without sandbox-manager

Copy both files into the sandbox via whatever mechanism your setup uses, then run:

```sh
# Install the cert into the system trust store (Lima sandboxes have passwordless sudo).
sudo cp ~/.local/state/local-gomod-proxy/cert.pem /usr/local/share/ca-certificates/local-gomod-proxy.crt
sudo update-ca-certificates

# Configure GOPROXY. The credentials file contains a single line "x:<token>".
export GOPROXY="https://$(tr -d '\n' < ~/.local/state/local-gomod-proxy/credentials)@host.lima.internal:7070/"
# go.sum (committed to the repo) is the primary integrity check.
export GOSUMDB=off
# Explicitly clear GOPRIVATE so all modules route through GOPROXY.
unset GOPRIVATE
go env -u GOPRIVATE
```

## Run as a launchd agent (macOS)

To keep the proxy running in the background whenever you're logged in, install it as a per-user LaunchAgent. See [docs/launchd.md](docs/launchd.md) for setup (including git auth under launchd), install, verify, and manage steps.

## Security

- **What's blocked:** browser JS, casual `localhost` probes, and any process that doesn't know to read the credentials file. The default `--addr` of `127.0.0.1:7070` binds loopback only; TLS + basic auth layer on top. Do not override `--addr` to a public interface, a VPN-reachable interface, or `0.0.0.0`. If you run a custom Lima network (`networks:` in `lima.yaml`) whose gateway is not the host loopback, bind explicitly to that gateway IP instead.
- **What isn't blocked:** any process running as the same OS user can `cat ~/.local/state/local-gomod-proxy/credentials` and use them. The `0600` mode prevents other OS-level users from reading the file, not other processes of yours.
- **Rotation:** `rm -rf "$XDG_STATE_HOME/local-gomod-proxy"` (or `~/.local/state/local-gomod-proxy/` if `$XDG_STATE_HOME` is unset), restart the proxy, then re-run provisioning in every sandbox that uses it.
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
