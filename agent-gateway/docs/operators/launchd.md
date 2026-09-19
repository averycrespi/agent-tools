# Run Agent Gateway as a launchd agent (macOS)

Audience: Gateway operators using a logged-in macOS desktop

Purpose: Install, verify, and manage a per-user LaunchAgent with the installed `agent-gateway service` commands. The canonical plist is the single persisted service-settings source; launchd supervises a direct foreground `serve` invocation.

## GUI session and credentials

Run as the intended logged-in macOS user, without `sudo`. Management targets only `dev.agent-tools.agent-gateway` in `gui/<uid>`, under the **OS-account home**, not the `HOME` environment variable. Root and non-macOS execution refuse before mutation. Custom labels, LaunchDaemons, other supervisors, binary upgrades, storage repair and installation migration are outside this command group.

No service command initializes or opens the private database, reads a bearer, or accesses the native keyring. Lifecycle commands may inspect the existing installation lock without creating it or changing recovery markers. Readiness is not credential health: GUI login and the secret-free startup capability probe do not guarantee later Keychain access will avoid an attended prompt. Investigate unexpected prompts; never grant blanket access or place passwords in environment variables. See [native-keyring capability](../design/downstream-servers.md#keyring-capability-and-generation-cutover).

Never put secrets in plist values, argv, environment variables or logs. Service management assumes completed canonical naming adoption; it does not inspect dual labels or import archived legacy plists. Existing migration and recovery remain separate [operator procedures](installation-migration.md).

## Quick start

Prerequisites for **installed management**: the native macOS `agent-gateway` executable and macOS system utilities. No Python, Go toolchain or checkout is needed. Building/installing the executable is a separate [installation](../../README.md#installation) step; service commands never upgrade binaries.

For a **new installation only**, initialize the intended unused data root separately with `agent-gateway initialize`. For an existing installation, retain its data and authority; never initialize another root to resolve a service error.

```bash
agent-gateway service install
agent-gateway service start
agent-gateway service status
```

Install creates the private plist and log destinations, but **does not load the service**. Stop on an error and inspect the reported state before another command. Successful launch acceptance does not prove readiness.

### Defaults and installer behavior

| Selection   | Install default                                                                                    |
| ----------- | -------------------------------------------------------------------------------------------------- |
| Executable  | Absolute path of the currently executing binary, not GOPATH                                        |
| Data root   | Absolute `$XDG_DATA_HOME/agent-gateway`, otherwise OS-account home + `/.local/share/agent-gateway` |
| Listener    | `127.0.0.1:8210`                                                                                   |
| Plist       | OS-account home + `/Library/LaunchAgents/dev.agent-tools.agent-gateway.plist`                      |
| Logs        | OS-account home + `/Library/Logs/agent-gateway/{stdout,stderr}.log`                                |
| Diagnostics | `warn`                                                                                             |

Install refuses an existing plist, loaded canonical job, unsafe permissions/ownership, symlinks and unsupported selections rather than overwriting or fixing them. It creates a synced `0600` plist, `0700` log directory and `0600` log files. Newly created directories/logs can remain after a later failure; existing files are not truncated. A retained private management-lock file serializes cooperating commands and is not a second configuration file.

The [example plist](../../examples/launchd/agent-gateway.plist) illustrates the Go-owned definition, not a runtime template dependency. XML-aware serialization preserves literal arguments, including spaces and XML characters. Generated XML includes the standard plist declaration and self-closing boolean elements for launchd compatibility; passing `plutil -lint` alone does not prove launchd will accept a definition. launchd runs the selected executable directly: no shell expansion, profile sourcing, or wrapper. `RunAtLoad` and `KeepAlive` retain launchd supervision; `ExitTimeOut=30` leaves room for Gateway's ten-second drain plus best-effort diagnostic flush. The utility PATH is `/usr/bin:/bin:/usr/sbin:/sbin`, not a shell/version-manager environment. Managed stdio servers have their own clean configured environments.

### Custom paths

```bash
agent-gateway service install \
  --binary /absolute/path/to/agent-gateway \
  --data-dir /absolute/path/to/existing-data \
  --listen 127.0.0.1:8210 \
  --allowed-host gateway.example \
  --log-level info
```

Binary and data paths must be clean absolute paths. Explicit `--data-dir` overrides XDG selection; relative XDG input otherwise fails. The selected binary must be an owner-controlled executable, not a symlink or shell-profile-dependent shim. Install persists the explicit data path so later management does not recompute it from the shell environment. Selecting a different data path does not move or initialize any data.

## Manage

Use `agent-gateway service --help` for the command inventory and each verb's `--help` for its flags.

### Plist changes

```bash
agent-gateway service update --log-level debug
agent-gateway service update --allowed-host first.example --allowed-host second.example
agent-gateway service update --clear-allowed-hosts
```

Update preserves omitted values. Explicit `--allowed-host` values **replace the whole list**; `--clear-allowed-hosts` clears it and cannot be combined with replacement values. There is no separate configure verb. An unchanged update avoids restart. A changed running or loaded-but-exited/restarting job is gracefully unloaded, its stop confirmed, the private plist atomically replaced, and then loaded once. An unloaded job stays unloaded. Settings persist across restarts and GUI logins.

Canonical installer-produced definitions retain supported literal selections, including trailing `--log-level`, `--output human|json`, or `--json`. Conflicting/duplicate singleton flags, unknown keys/arguments, custom environments/log destinations, shell wrappers and ambiguous loaded-versus-installed definitions refuse with reconciliation guidance; nothing is silently discarded. There is no generic argument passthrough or archived `--from-plist` handover. For unsupported custom definitions, separately authorize a manual stopped reconciliation, preserve the original outside LaunchAgents, and retain every intended nonsecret selection. Do not repair by reinitializing or deleting installation state.

### Graceful stop and restart

```bash
agent-gateway service restart
agent-gateway service stop
agent-gateway service start
```

Restart uses unchanged installed settings and accepts **no settings overrides**, including inherited `--data-dir`. It loads an absent job. Start is a no-op for an already loaded valid definition, including a loaded job waiting to restart. Stop is a no-op only when unloaded and no relevant process owner remains.

Management uses one `bootout` and one appropriate `bootstrap`, never `kickstart -k` or signals to Gateway PIDs. It validates loaded path/program/argv, retains observed process identities and checks installation-scoped ownership rather than globally blocking a basename. Unknown or ambiguous process arguments refuse. The management lock protects cooperating canonical CLI commands, not arbitrary external launchctl calls; do not run manual management or another launcher concurrently. Existing installation lock descriptors remain held through plist publication and the final job/process/plist checks, then are released specifically for the one bootstrap handoff so Gateway can acquire its own lock. This handoff does not coordinate noncooperating foreground launches; keep those disabled throughout management.

Each utility invocation has a five-second deadline and 1 MiB combined output cap; the Go runner retains its unreaped child identity while fencing its own utility group and then reaps it. Stop observation has a 30-second deadline; each operation has a 60-second outer bound, with bounded cleanup. No uncertain mutation is automatically retried. Unknown inspection, reused identity or stop timeout prohibits a replacement owner. A free installation lock alone is not proof of process exit. After bootout, an already tracked PID reported explicitly as a zombie with unchanged UID and start time remains a wait-only blocker, even when `ps` replaces its executable with `<defunct>`, a parenthesized unavailable-command representation, or leaves it empty. It must disappear before bootstrap; a persistent zombie times out. This does not adopt an initially observed zombie, accept a different executable path, or treat an unknown/exiting-only state as proof of exit.

Invalid proposals leave the old plist/service unchanged. Publication failure retains the old definition; failure after confirmed stop leaves the service stopped. Publication/sync uncertainty is reported explicitly. If publication succeeds but bootstrap fails, **new settings remain installed** and launch state is unknown until inspected; there is no automatic rollback or restart retry. Launchd can enforce its termination deadline, so confirmed exit is not proof of clean storage shutdown.

Restart discards browser sessions, runtime handles, streams, OAuth transients and other process-local state. In-flight effects can remain unknown: never automatically replay them. See [invocation evidence](invocation-evidence.md).

### Older generated plist rejected by launchd

Older installers emitted noncanonical plist XML that could pass `plutil -lint` but fail bootstrap with launchd error `109: Invalid property list`. If service status confirms the job is unloaded and launchd logs show this error, preserve a backup outside automatic-load paths, then normalize the installed plist with `plutil -convert xml1 /absolute/path/to/dev.agent-tools.agent-gateway.plist` before starting it. This preserves the settings and does not initialize data or credentials. Upgrade the executable before subsequent install/update operations regenerate the definition. An unchanged update does not rewrite an existing plist.

## Verify

```bash
agent-gateway service status
```

Read-only status emits a finite JSON object containing installed selections, plist/log paths, launchd state, and a **separate** readiness observation. The unauthenticated numeric-loopback `/readyz` probe bypasses proxies and redirects, is bounded to two seconds and sends no credential. It reports `ready`, `not-ready`, `unavailable` or `unknown`; an unrelated listener can answer that address, so the probe is neither process-identity proof nor upstream credential health. Launchd inspection errors never become “unloaded.” A job loaded without an installed definition is reported separately.

For authenticated storage/keyring posture, deliberately run the ordinary `agent-gateway --data-dir /installed/data/path status --address http://127.0.0.1:8210` command using the installed selections. That separate command reads an administrator bearer; service status does not. Never copy a bearer into a curl header argument. Inspect the reported stdout/stderr paths locally, retaining only necessary nonsecret evidence. See [safe serve diagnostics](administration.md#safe-serve-diagnostics) for levels, correlation and loss limits. Logs are not durable audit evidence and missing lines do not prove nonexecution.

Native launchd behavior and Keychain access need separately authorized disposable macOS qualification. Linux and injected utility fixtures are deterministic regression evidence, not native launchd proof. Never smoke-test these mutations against the current host installation without explicit authority.

### Logs and uninstall

```bash
agent-gateway service uninstall
```

Uninstall first confirms graceful stop, then removes **only the canonical plist**. It preserves binaries, log files, data, bearer files, keyring state and the stable management-lock inode. It does not perform credential retirement or workspace cleanup.

launchd does not rotate these logs. Arrange owner-controlled retention separately; stop and confirm exit before moving log files so the next start opens the intended destinations. Do not broaden permissions or add a token-bearing watchdog. Binary replacement is likewise separate: stop, confirm exit, install the intended native executable through its original authorized installation method, then start and verify.

## Troubleshooting

- **GUI domain unavailable:** use the intended logged-in account, not root or a headless system daemon. Unknown inspection is not absence.
- **Utility inspection or cleanup failure:** launchd inspection reports the owned utility failure and OS error without copying command output. Preserve that diagnostic when investigating; an installed plist or a separately successful `launchctl print` does not establish that Gateway's utility supervision succeeded. Do not bypass a cleanup refusal with repeated lifecycle mutations or signals to Gateway PIDs.
- **Bootstrap fails or repeated exits:** inspect the persisted selections, executable availability, private log destinations and safe diagnostics. Keep new settings after a failed updated bootstrap; investigate before a deliberate new attempt.
- **Ownership conflict:** identify the other process/launcher. Do not delete lock files, change labels or force-kill to bypass the refusal.
- **Unready, authentication failure or storage latch:** follow [administration](administration.md) and [stopped recovery](backup-and-recovery.md). Restart, reset and initialization are not generic repair operations.

Return to the [documentation map](../README.md) or [Gateway README](../../README.md).
