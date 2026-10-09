# Run Agent Gateway as a launchd agent (macOS)

Audience: Gateway operators using a logged-in macOS desktop

Purpose: Install, verify, and manage a per-user LaunchAgent with raw launchctl procedures and an operator-owned plist.

Gateway supplies foreground `serve`, not a native service manager. Nothing in a binary upgrade unloads a job, rewrites a plist, removes logs or changes installation data. Existing launchd jobs invoking supported `serve` remain valid. Linux operators use the [systemd guide](systemd.md).

## Existing installations and command-line transition

The former Gateway `service` command group is removed, including its JSON output. Replace automation with the explicit supervisor procedures below. `doctor` no longer inspects native jobs or returns the optional `service` member or `installed service` check; other diagnostic and public API schemas are unchanged. Inspect configuration and logs through the supervisor instead.

No plist rewrite is required merely because management was removed. Retain the actual executable, data root, listener, allowed hosts, traffic budget, diagnostics/output flags and log destinations. `serve --output human|json` and `--json` remain supported. The exact macOS `XPC_SERVICE_NAME=dev.agent-tools.agent-gateway` compatibility hint still preserves omitted HTTP-listener intent as disabled; it grants no identity or credential authority. For a custom label or a manually edited definition, explicitly select `--http-proxy-listen 127.0.0.1:8212` or `--clear-http-proxy-listen`. Bare foreground `serve` otherwise enables HTTP by default. No automatic migration occurs.

Before any separately authorized change, record the installed definition, loaded program/arguments and process identity. Preserve a backup outside automatic-load directories. Do not rename labels, initialize a new root, delete locks or force-kill a process to resolve ambiguity. Retain archived plists, logs and recovery artifacts under [installation safety](installation-safety.md).

## Prepare a new definition

Run as the intended logged-in non-root macOS account, without `sudo`. This guide uses the concrete example account `alice`, native executable `/Users/alice/.local/bin/agent-gateway`, data root `/Users/alice/.local/share/agent-gateway` and logs `/Users/alice/Library/Logs/agent-gateway`. **Replace these with the actual absolute paths for your account before installation.** Do not copy example paths unchanged onto another account. launchd does not expand `~`, shell variables or shell profiles in plist arguments. Use an owner-controlled executable, not a version-manager shim; keep executable and parent directories protected from other users' writes.

The [maintained plist](../../examples/launchd/agent-gateway.plist) invokes `serve` directly. There is no shell wrapper, installer or runtime template dependency. Its explicit HTTP listener requires existing CA signing material; MCP-only configurations must replace the two HTTP address arguments with `--clear-http-proxy-listen`. Preserve allowed hosts only when required for trusted forwarding; loopback binding is not authorization. See [HTTP/client trust setup](http-proxy.md).

For a **new unused data root only**, perform ordinary [initialization](administration.md#installation-root) with the same account and explicit data path before loading. For an existing root, do not rerun initialization as service repair. The account must own the `0700` data directory, `0600` installation master key and administrator bearer. Retain encrypted backups and separately protected key custody; never put secrets in the plist, argv, environment or logs.

Prepare the intended private log directory (`0700`) and new stdout/stderr files (`0600`) with `umask 077`, without following symlinks or truncating existing files. Inspect existing paths and ownership before writing; do not broadly chmod/chown an existing installation. Save the reviewed plist as `/Users/alice/Library/LaunchAgents/dev.agent-tools.agent-gateway.plist`, owned by alice, mode `0600`. The LaunchAgents directory must be owner-controlled. Refuse an unexpected existing destination rather than overwriting it.

`RunAtLoad` and `KeepAlive` provide supervision. `ExitTimeOut=30` leaves room for Gateway's ten-second drain plus best-effort diagnostics; forced expiry is not clean-shutdown evidence. The fixed utility PATH is not a login shell environment. Managed stdio servers retain their own explicit clean environments.

## Load and inspect

Only run these commands after separately authorizing the installed-resource change. First inspect the intended GUI domain and ensure there is no existing job or other launcher for this data root. An inspection failure is not proof of absence.

```bash
/bin/launchctl print "gui/$(id -u)"
/usr/bin/plutil -lint /Users/alice/Library/LaunchAgents/dev.agent-tools.agent-gateway.plist
/bin/launchctl bootstrap "gui/$(id -u)" /Users/alice/Library/LaunchAgents/dev.agent-tools.agent-gateway.plist
/bin/launchctl print "gui/$(id -u)/dev.agent-tools.agent-gateway"
/Users/alice/.local/bin/agent-gateway --data-dir /Users/alice/.local/share/agent-gateway doctor --address http://127.0.0.1:8210
```

Run bootstrap once. On error or uncertainty, inspect before deciding another action; do not loop or automatically retry. A valid XML plist is not proof of native acceptance. Loaded/running is not readiness. `doctor` uses a bounded unauthenticated readiness check; an unrelated listener can answer, so match the loaded program, literal arguments and process identity separately. `doctor --online` deliberately uses the selected administrator bearer to inspect authenticated status. Neither proves upstream credential health. Never copy a bearer into curl arguments.

Inspect only the necessary bounded interval of the configured stdout/stderr logs locally. [Serve diagnostics](administration.md#safe-serve-diagnostics) are lossy and not audit evidence or permission to replay an uncertain call. launchd does not rotate these files; the operator owns retention and safe stopped log rotation.

## Stop, change settings, and perform maintenance

Disable every other launcher for this installation and coordinate against concurrent manual starts. Record the loaded job's PID and identity **before** unloading, then request graceful termination once:

```bash
/bin/launchctl print "gui/$(id -u)/dev.agent-tools.agent-gateway"
/bin/launchctl bootout "gui/$(id -u)/dev.agent-tools.agent-gateway"
```

Allow at most 30 seconds for graceful termination. Verify the job is absent from the same valid GUI domain and the previously identified process has exited; a zombie remains a blocker until reaped. Check for another owner of the selected installation. An unreachable listener or free lock alone is not process-exit proof. Unknown inspection, PID reuse, surviving children, timeout or a bootout error stops the procedure: retain the existing definition/data and investigate, rather than issuing bootstrap, repeated bootout or `kickstart -k`. Do not signal a PID merely from its basename. launchd may enforce its termination deadline; inspect diagnostics and retain any unclean marker, WAL or recovery files.

Only after confirmed stop may you deliberately edit the plist, replace the binary through its authorized installation method, rotate logs, or run [stopped maintenance](backup-and-recovery.md). Keep all automatic launchers disabled during maintenance. Gateway's stopped-ownership checks still apply and do not authorize deleting a lock or marker. Keep the same data root and account; backups and offline key rotation retain their separate safety requirements.

For restart, retain the unchanged definition and bootstrap it once using the earlier command, then inspect identity/readiness again. For changed settings, preserve a private backup outside LaunchAgents, review every literal argument and lint the edited plist before that one bootstrap. A failed start leaves the selected settings in place: no automatic rollback or mutation replay. If retiring a job permanently, leave it unloaded; moving/removing a plist requires a separate deliberate operator decision, never deletion of installation data or logs.

Process-local sessions, streams and runtime handles disappear on restart. In-flight effects can remain unknown; follow [invocation evidence](invocation-evidence.md).

## Troubleshooting and qualification

- **GUI domain unavailable:** use the intended logged-in account. Do not substitute root, a LaunchDaemon or another user's domain.
- **Bootstrap rejection:** inspect the exact file, permissions, native error and logs. Historical malformed XML may need a separately authorized stopped `plutil -convert xml1` conversion after preserving the original; do not assume lint success proves launchd acceptance.
- **Repeated exits or unready:** inspect executable/data paths, private log destinations, listener conflicts and CA/key custody. Do not initialize another root or reset credentials.
- **Ownership or recovery refusal:** preserve files and use the [recovery guide](backup-and-recovery.md); restarting is not generic repair.

Consult the installed macOS `man launchctl` and `man launchd.plist` for the host version's supervisor semantics. Source fixtures verify CLI removal, explicit foreground behavior and retained compatibility hints; they do not qualify installed resources or native launchd adoption. Any native lifecycle exercise needs separately authorized disposable resources. This guide does not authorize changing the current host installation.

Return to the [documentation map](../README.md) or [Gateway README](../../README.md).
