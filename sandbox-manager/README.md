# Sandbox Manager (sb)

Manage a Lima VM sandbox for running AI coding agents in isolation. One command to create a provisioned VM, one command to tear it down.

## Install

```bash
cd sandbox-manager && make install
```

Requires Go 1.25+ and [Lima](https://lima-vm.io/).

## Quick Start

```bash
# Create, start, and provision a sandbox
sb create

# Check status
sb status

# Open a shell
sb shell

# Run a command in the sandbox
sb shell -- uname -a

# Stop the sandbox
sb stop

# Start it again
sb start

# Re-provision (copy files and run scripts)
sb provision

# Destroy the sandbox
sb destroy
```

## Commands

### `sb create`

Creates, starts, and provisions the sandbox VM. Handles all states:

- **Not created** — renders the Lima template, creates and starts the VM, then provisions it
- **Stopped** — starts the VM and re-provisions it
- **Running** — re-provisions the VM

Safe to re-run at any time.

### `sb start`

Starts a stopped sandbox. Errors if the VM doesn't exist.

### `sb stop`

Stops a running sandbox. No-op if already stopped or not created.

### `sb destroy [--force]`

Destroys the sandbox VM. Stops it first if running. Prompts for confirmation unless `--force` is passed.

### `sb provision`

Re-runs provisioning on a running sandbox: copies files and executes scripts from the config. Useful after changing config without recreating the VM.

### `sb status`

Prints the sandbox status: `running`, `stopped`, or `not created`.

### `sb shell [-- command]`

Opens an interactive shell in the sandbox, or runs a command if arguments are provided after `--`.

### `sb config`

| Subcommand          | Description                                            |
| ------------------- | ------------------------------------------------------ |
| `sb config edit`    | Open config in `$EDITOR` (creates defaults if missing) |
| `sb config path`    | Print config file path                                 |
| `sb config refresh` | Create or update config with latest defaults           |

## Configuration

Config file: `~/.config/sb/config.json` (follows XDG)

```json
{
  "image": "ubuntu-24.04",
  "cpus": 4,
  "memory": "4GiB",
  "disk": "100GiB",
  "mounts": [],
  "copy_paths": ["~/.gitconfig"],
  "scripts": ["~/.config/sb/scripts/provision.sh"]
}
```

| Field        | Type     | Default          | Description                                                                            |
| ------------ | -------- | ---------------- | -------------------------------------------------------------------------------------- |
| `image`      | string   | `"ubuntu-24.04"` | Ubuntu cloud image version                                                             |
| `cpus`       | int      | `4`              | Number of CPUs allocated to the VM                                                     |
| `memory`     | string   | `"4GiB"`         | Memory allocated to the VM                                                             |
| `disk`       | string   | `"100GiB"`       | Disk size for the VM                                                                   |
| `mounts`     | string[] | `[]`             | Host directories to mount (writable) in the VM                                         |
| `copy_paths` | string[] | `[]`             | Files/directories to copy into the VM (format: `"src"` or `"src:dst"`, `~/` supported) |
| `scripts`    | string[] | `[]`             | Provisioning scripts to run in the VM (paths on host, `~/` supported)                  |

### Copy paths

Copy paths support two formats:

- `"path/to/file"` — copies to the same path in the VM
- `"local/path:guest/path"` — copies to a different path in the VM

Paths starting with `~/` are expanded to the user's home directory. Directories are detected automatically and copied recursively.

## Example provisioning scripts

The repo ships drop-in provisioning scripts under [`examples/provision/`](examples/provision/). Most are self-contained (work on a bare sandbox) and idempotent; the `asdf-*` scripts require `asdf.sh` to have run first and fail fast with a clear error otherwise. Reference them by absolute path in the `scripts` field of your config, individually or in combination:

| Script            | What it does                                                | Requires         |
| ----------------- | ----------------------------------------------------------- | ---------------- |
| `apt-packages.sh` | Template for installing a user-defined list of apt packages | —                |
| `docker.sh`       | Install Docker Engine and enable the daemon                 | —                |
| `asdf.sh`         | Install asdf as a prebuilt binary and wire up `$PATH`       | —                |
| `asdf-nodejs.sh`  | Install the latest Node.js via asdf                         | `asdf.sh`        |
| `asdf-golang.sh`  | Install the latest Go via asdf                              | `asdf.sh`        |
| `claude-code.sh`  | Install Claude Code (native binary)                         | —                |
| `pi-agent.sh`     | Install the Pi coding agent via npm                         | `asdf-nodejs.sh` |

See [`examples/provision/README.md`](examples/provision/README.md) for the full catalog and the convention for adding new examples.

## Paths

| Resource | Location                   |
| -------- | -------------------------- |
| Config   | `~/.config/sb/config.json` |
| VM name  | `sb` (in Lima)             |

## Development

```bash
make build    # Build to ./sb
make install  # Install to $GOPATH/bin/sb
make test     # Run tests with race detector
make lint     # Run golangci-lint
make fmt      # Format with goimports
make tidy     # go mod tidy + verify
make audit    # tidy + fmt + lint + test + govulncheck
```

## Architecture

See [DESIGN.md](DESIGN.md) for the full design document.

```
cmd/
  sb/             CLI entry point
  *.go            Cobra commands (thin wrappers)
internal/
  exec/           Runner interface abstracting os/exec
  config/         Config loading + XDG path functions
  lima/           Lima VM client (limactl wrapper)
  sandbox/        Orchestration (lifecycle + provisioning + template)
```
