# Run Agent Gateway with systemd (Linux)

Audience: Gateway operators using a Linux user manager

Purpose: Supervise foreground serve with an operator-owned systemd user unit.

Gateway does not install, inspect or mutate systemd units. The [maintained example](../../examples/systemd/agent-gateway.service) runs as the existing non-root account, with no replacement native-service API. macOS operators use [launchd](launchd.md). The commands below are deliberate operator procedures, not authorization to modify an installed service.

## Paths and custody

The example account is `alice`, executable `/home/alice/.local/bin/agent-gateway`, data root `/home/alice/.local/share/agent-gateway` and unit `/home/alice/.config/systemd/user/agent-gateway.service`. Replace these with your actual absolute paths before installation; use the configured `XDG_CONFIG_HOME` unit directory if different. Protect the executable and its parent directories from other users' writes. Do not use a shell-profile-dependent shim, `~`, shell substitutions, redirects or `&` in `ExecStart`. Systemd has its own quoting, variable and specifier rules; the example needs none of them.

Use the same account and data root for init, serve, online commands and maintenance. That account must own the `0700` data directory, `0600` master key and bearer files. `UMask=0077` protects new files; it does not repair existing ownership or modes. No secrets belong in the unit, command arguments or environment. Preserve [encrypted backups and separately protected key custody](backup-and-recovery.md).

For a new unused root only, run ordinary [initialization](administration.md#installation-root) as that account. Never initialize another root to fix startup. Inspect an existing installation's selections rather than overwriting them. The example explicitly enables HTTP; it requires the existing CA signing key. For MCP-only operation replace the HTTP address pair with `--clear-http-proxy-listen`. Configure [client trust](http-proxy.md) separately. Retain explicit allowed-host selections only for required trusted forwarding.

## Install and start

Save the reviewed unit in the account's owner-controlled user-unit directory, mode `0600`, without overwriting an unexpected existing file or following symlinks. Preserve a private backup outside the unit search path before changing an existing definition. Ensure no other supervisor or foreground process owns this data root.

Run in the intended user's normal login session, **not** with `sudo systemctl --user`:

```bash
systemctl --user daemon-reload
systemctl --user enable --now agent-gateway.service
systemctl --user status agent-gateway.service
/home/alice/.local/bin/agent-gateway --data-dir /home/alice/.local/share/agent-gateway doctor --address http://127.0.0.1:8210
```

Run each mutation once and inspect errors before deciding another action. `Type=exec` verifies execution of the binary, not application readiness. Match the loaded unit, arguments and process identity before interpreting a readiness probe; an unrelated loopback listener can answer. `doctor --online` deliberately uses the protected administrator bearer for authenticated status, not upstream credential qualification. Never put that bearer in curl arguments.

`Restart=on-failure` restarts failed processes after five seconds, subject to systemd's start-rate limits; an explicit stop is not automatically restarted. Repeated failure requires diagnosis, not a reset/restart loop. The example defines no socket or timer activation. Do not enable another unit against the same installation.

The user manager normally follows login-session policy. For boot startup and continued operation after logout, an administrator may separately authorize `sudo loginctl enable-linger alice`. Lingering affects the **whole account's user manager**, not just Gateway, and cannot unlock an unavailable encrypted home. Do not enable it implicitly. A system-level unit is a different operator-owned deployment choice, not something to enable alongside this user unit.

## Stop and maintenance

For an ordinary deliberate restart use `systemctl --user restart agent-gateway.service`, then inspect identity, logs and readiness again. For maintenance, exclude concurrent operators and disable all other launchers first; never combine maintenance with an automatic restart:

```bash
systemctl --user stop agent-gateway.service
systemctl --user show agent-gateway.service --property=ActiveState --property=SubState --property=Result --property=MainPID
journalctl --user-unit=agent-gateway.service --since '10 minutes ago' --lines=100 --no-pager
```

Confirm stopped state, no main process or remaining unit processes, and the previously identified Gateway's exit. A failed inspection is unknown, not absence; an unreachable listener or free lock alone does not prove exit. The unit sends SIGTERM to Gateway first (`KillMode=mixed`), allowing its ten-second drain to supervise stdio children. Remaining cgroup processes receive SIGKILL after the main process exits or the 30-second `TimeoutStopSec` expires. This can force children before 30 seconds if Gateway exits early. Do not change to `KillMode=process` or `none`, which can leave unowned children.

A successful `stop` alone does not prove clean storage shutdown. On timeout, forced termination, surviving children or uncertain status, retain diagnostics, WAL, key and recovery markers and investigate before maintenance or another start. Gateway's own exclusive stopped-ownership checks remain mandatory; never delete a lock to bypass them.

For maintenance spanning logout/reboot, deliberately use `systemctl --user disable --now agent-gateway.service` and confirm stop. Disabling removes enablement but does **not** prevent manual or dependency activation; coordinate all launchers throughout the window. Perform only the indicated [stopped maintenance](backup-and-recovery.md), with its confirmations and recovery conditions. Do not recover, reset or rotate merely because startup failed.

After successful maintenance, use `systemctl --user start agent-gateway.service` (or explicitly restore prior enablement with `enable --now`), then verify. After a stopped unit-file edit, run `daemon-reload` before that start; `reload` is not the unit-definition operation. Preserve settings after a failed start and reconcile the cause rather than automatically rolling back or replaying mutations. Restart discards process-local sessions and handles; in-flight effects remain subject to [unknown-outcome rules](invocation-evidence.md).

## Logs and authoritative references

The example sends stdout/stderr to the journal. Its access controls and retention are host policy, independent of `UMask`; select a suitably private journal policy before adoption and review only necessary nonsecret excerpts. Diagnostics are lossy, not durable audit evidence or proof of nonexecution. The operator owns retention, binary upgrades and all unit changes. Removing a unit never requires deleting data, logs or credentials.

These procedures were checked against the authoritative systemd manuals:

- [systemd.service](https://www.freedesktop.org/software/systemd/man/latest/systemd.service.html): `Type=exec`, `Restart=`, `TimeoutStopSec=` and command-line syntax; execution is not application readiness, explicit stop does not invoke restart policy.
- [systemd.kill](https://www.freedesktop.org/software/systemd/man/latest/systemd.kill.html): `KillMode=mixed`, `KillSignal=` and `SendSIGKILL=`; initial main-process TERM and subsequent cgroup KILL.
- [systemd.exec](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html): user identity and creation-only `UMask=`.
- [systemctl](https://www.freedesktop.org/software/systemd/man/latest/systemctl.html): `--user`, enable/disable, stop/start/restart and `daemon-reload`.
- [loginctl](https://www.freedesktop.org/software/systemd/man/latest/loginctl.html): `enable-linger` keeps the user manager across logouts and starts it at boot.

Check the installed manuals for your host version before adoption. Source/example tests do not qualify installed units, logout/reboot behavior, native process cleanup or filesystem durability. No live systemd or launchd operations are part of source verification; native qualification needs separately authorized disposable resources.

Return to the [documentation map](../README.md) or [Gateway README](../../README.md).
