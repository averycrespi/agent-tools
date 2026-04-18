# sandbox-manager Design

## Motivation

Running AI coding agents with full host access is a non-starter for autonomous work. The agent needs to install packages, run arbitrary code, and modify files — but doing that on the host means one bad command can trash your environment. Containers help, but they're optimized for application isolation, not interactive development workflows. What you really want is a full VM that looks like a real development machine, is cheap to create and destroy, and can be provisioned to match your host environment.

Lima provides lightweight Linux VMs on macOS with near-native performance via Apple's Virtualization.framework. `sb` wraps Lima's lifecycle into a small set of commands: `sb create` spins up a provisioned VM, `sb destroy` tears it down, and `sb shell` drops you in. Configuration drives what gets copied in and what scripts run during provisioning, so the sandbox can be tailored to any workflow.

The key design goal: **the sandbox should feel like a fresh development machine, not a container.** Full systemd, real user accounts with matching UID/GID, writable mounts for shared directories, and provisioning scripts that install the same tools you'd install on a real box.

## Architecture

```
cmd/
  sb/             CLI entry point (main.go)
  root.go         Service construction, flags, completion
  create.go       sb create
  start.go        sb start
  stop.go         sb stop
  destroy.go      sb destroy
  provision.go    sb provision
  status.go       sb status
  shell.go        sb shell
  config.go       sb config {path,edit,refresh}

internal/
  exec/           Runner interface abstracting os/exec
  config/         Config loading + XDG path functions
  lima/           Lima VM operations (limactl wrapper)
  sandbox/        Orchestration tying lima + config + template together
```

### Dependency Flow

```
cmd → sandbox.Service → lima.Client → exec.Runner
                      → config.Config
                      → template (embedded lima.yaml)
```

All external commands flow through `exec.Runner`, an interface with `Run` and `RunInteractive` methods. Tests inject mock runners to verify command arguments without executing real processes.

### Key Design Decisions

**exec.Runner interface.** Every shell command goes through this interface. This makes the Lima client fully testable with mock runners — tests verify exact argument construction without touching limactl.

**Interface segregation in sandbox.** The sandbox package defines its own `LimaClient` interface containing only the methods it needs, rather than depending on the concrete type. This keeps coupling minimal and tests focused.

**Config-driven provisioning.** What to copy in, which scripts to run, resource allocation — all driven by `~/.config/sb/config.json`. The tool has no hardcoded knowledge of any specific agent or development workflow. The repo ships example provisioning scripts under `examples/provision/` as reference material, but `sb` itself has no awareness of them — they're plain files referenced by absolute path from user configs.

**Smart create.** `sb create` is the primary entry point and handles all states: if the VM doesn't exist, it creates and provisions it; if it's stopped, it starts and provisions it; if it's running, it re-provisions it. This makes it safe to re-run without thinking about current state.

**Template rendering.** The Lima YAML template is embedded in the binary via `//go:embed`. At create time, it's rendered with host user information (username, UID, GID, home directory) and config values (CPUs, memory, disk, mounts). This ensures the VM user matches the host user, which makes shared mounts work without permission issues.

**UID/GID preservation.** The VM user is created with the same UID and GID as the host user. This is critical for writable mounts — files created in the VM have the correct ownership on the host.

**XDG base directories.** Config in `$XDG_CONFIG_HOME/sb`. Falls back to `~/.config` per the XDG spec.

### VM Lifecycle

**Create (`sb create`):**

```
Render lima.yaml template with host user info + config
  → limactl start --name=sb <template>
  → Provision:
    → For each copy_paths entry:
      → Expand ~/ to home directory, detect directories (trailing /)
      → mkdir -p <parent> (or <dst> for directories) in VM
      → limactl cp [-r] <local> sb:<guest>
    → For each script:
      → limactl cp <script> sb:/tmp/sb-provision-script
      → chmod +x
      → execute
      → clean up temp file
```

**Start (`sb start`):**

```
limactl start sb
```

**Stop (`sb stop`):**

```
limactl stop sb
```

**Destroy (`sb destroy`):**

```
If running → stop first
limactl delete --force sb
```

**Shell (`sb shell [-- command]`):**

```
limactl shell --workdir / sb [-- command]
```

### State Machine

```
NotCreated ──create──▶ Running
Running    ──stop────▶ Stopped
Stopped    ──start───▶ Running
Running    ──destroy─▶ NotCreated
Stopped    ──destroy─▶ NotCreated
```

`create` on a running VM re-provisions. `create` on a stopped VM starts and re-provisions. `stop` on a stopped/non-existent VM is a no-op. `destroy` on a non-existent VM is a no-op.

### Error Handling

- Lima operation failures propagate as errors and halt the operation
- Provisioning script cleanup failures log warnings but don't halt — best-effort
- `destroy` prompts for confirmation unless `--force` is passed
- State checks prevent invalid transitions (e.g. `start` on a non-existent VM returns an error)
