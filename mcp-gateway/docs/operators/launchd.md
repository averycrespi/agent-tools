# Run Gateway as a launchd agent (macOS)

Audience: Gateway operators using a logged-in macOS desktop

Purpose: Install, verify, and manage a per-user LaunchAgent with the [example plist](../../examples/launchd/mcp-gateway.plist). This supervises the foreground `mcp-gateway serve` process; it is not a system LaunchDaemon or an unattended credential-access solution.

## GUI session and credentials

Run these commands in Terminal as the intended logged-in user, without `sudo`. Use `~/Library/LaunchAgents` and the `gui/<uid>` domain, not `/Library/LaunchDaemons` or a root service. The GUI session supplies the user's native Keychain context; login does not guarantee that every credential operation can succeed without interaction. Logout ends that session's service; the installed agent starts again at GUI login.

Gateway's startup keyring capability probe is secret-free and does not request credential access. Authenticated `status` exposes the capability snapshot, not secret values. A `ready` snapshot does not guarantee that a later native read, write, or delete will avoid a macOS permission prompt. Such operations can fail or outlive cancellation. Attend any expected prompt in the same GUI session and verify the requesting program before approving it; investigate unexpected prompts rather than granting blanket access. Gateway has no plaintext fallback and is currently unsuitable for unattended credential access. See the [native-keyring contract](../design/downstream-servers.md#keyring-capability-and-generation-cutover).

Never put administrator, agent, server, or OAuth secrets in the plist, command arguments, environment variables, URLs, or logs. The service uses native keyring storage for server credentials; the online CLI reads an owner-only administrator bearer file. No bearer value needs to be printed, copied into a shell variable, or passed to `curl` for this procedure. Follow [administrator authentication](administration.md#administrator-authentication) and [upstream server configuration](upstream-servers.md) for credential workflows.

## Install and select paths

Prerequisites: the [Gateway build requirements](../../README.md#installation), Python 3 for the XML-safe renderer below, and macOS `launchctl` and `plutil`. Commands use one shell session. Replace `GATEWAY_SOURCE` with the absolute path to this repository's `mcp-gateway` directory:

```bash
GATEWAY_SOURCE='/absolute/path/to/agent-tools/mcp-gateway'
make -C "$GATEWAY_SOURCE" install
GATEWAY_BIN="$(go env GOPATH)/bin/mcp-gateway"
ACCOUNT_HOME="$(python3 -c 'import os, pwd; print(pwd.getpwuid(os.getuid()).pw_dir)')"
DATA_DIR="$ACCOUNT_HOME/.local/share/mcp-gateway"
LOG_DIR="$ACCOUNT_HOME/Library/Logs/mcp-gateway"
LABEL='dev.agent-tools.mcp-gateway'
DOMAIN="gui/$(id -u)"
SERVICE="$DOMAIN/$LABEL"
PLIST="$ACCOUNT_HOME/Library/LaunchAgents/$LABEL.plist"
LISTEN='127.0.0.1:8210'
ADDRESS="http://$LISTEN"

test -x "$GATEWAY_BIN" &&
  umask 077 &&
  mkdir -p "$ACCOUNT_HOME/Library/LaunchAgents" "$LOG_DIR"
```

Stop on any error before proceeding. Verify `GATEWAY_BIN` names the installed native macOS executable, not a shell shim or an `e2e`-provider build. `make install` uses `$(go env GOPATH)/bin`; adjust the absolute path if installing by another method. Keep logs in a user-owned private directory (mode `0700`) and files private (mode `0600`); inspect existing directories before changing permissions.

**Existing installation:** set `DATA_DIR` to its actual absolute root and skip initialization entirely. If it was created with shell `XDG_DATA_HOME`, select that existing root here; do not silently create a second installation at the example default. Stop any existing foreground or supervised Gateway owning that root or listen address before loading this agent. Do not overwrite an existing plist or take over an unrelated service label.

**New installation only:** after confirming this is a fresh, unused root, run:

```bash
if [ -e "$DATA_DIR" ] || [ -L "$DATA_DIR" ]; then
  printf '%s\n' 'Path already exists; inspect it and do not reinitialize.' >&2
else
  "$GATEWAY_BIN" initialize --data-dir "$DATA_DIR"
fi
```

Initialization creates owner-only state and the new `<data-dir>/admin-bearer` file; it never prints the bearer or overwrites an existing secret output. If initialization fails or is interrupted, stop and use [administration](administration.md#installation-root) and [recovery](backup-and-recovery.md), not repeated initialization or deletion of existing state.

The same explicit `DATA_DIR` is used for initialization, the rendered service argv, and all verification commands. Thus differences between shell and launchd XDG settings cannot select another root. In a new terminal, restore these exact selections before running management commands. Gateway itself resolves defaults from the operating-system account home, not the `HOME` environment variable; see [installation root precedence](administration.md#installation-root).

## Render, validate, and load

The template runs the executable directly, with each argument as its own array element. It has no shell wrapper. `~`, `$HOME`, `$PATH`, command substitutions, and shell quoting inside plist strings are literal, not expansions. The renderer substitutes complete absolute paths and XML-escapes them, including spaces and ampersands; no username-based `sed` replacement is needed.

```bash
python3 - "$GATEWAY_SOURCE/examples/launchd/mcp-gateway.plist" \
  "$PLIST" "$GATEWAY_BIN" "$DATA_DIR" "$LOG_DIR" "$LABEL" "$LISTEN" <<'PY'
import os
import plistlib
import sys

source, target, binary, data, logs, label, listen = sys.argv[1:]
for path in (source, target, binary, data, logs):
    if not os.path.isabs(path):
        raise SystemExit("All selected paths must be absolute")
with open(source, "rb") as stream:
    job = plistlib.load(stream)
job["Label"] = label
job["ProgramArguments"] = [binary, "serve", "--data-dir", data, "--listen", listen]
job["StandardOutPath"] = os.path.join(logs, "stdout.log")
job["StandardErrorPath"] = os.path.join(logs, "stderr.log")
with open(target, "xb") as stream:
    plistlib.dump(job, stream, sort_keys=False)
PY
```

The renderer refuses to overwrite an existing plist. For an existing agent, use the [plist-change procedure](#manage) instead. After successful rendering:

```bash
chmod 600 "$PLIST" &&
  plutil -lint "$PLIST" &&
  launchctl bootstrap "$DOMAIN" "$PLIST"
```

`RunAtLoad` starts the job on loading; no immediate forced kickstart is needed. `KeepAlive` restarts the process after exit, including clean exit. It is not a health check: a hung or unready process can remain running. A readiness failure is a reason to investigate, not an automatic restart or mutation-retry instruction.

The minimal `PATH` is for Gateway's own system utilities, including native keyring support. launchd does not source `.zshrc`, `.bashrc`, or version-manager setup. Do not add secrets to compensate. Gateway-managed stdio servers receive a separate clean environment of declared non-secret values and runtime-resolved secret slots, not the LaunchAgent environment. Their absolute executables, literal arguments, working directories, and environment are configured through [upstream server configuration](upstream-servers.md).

## Verify

```bash
launchctl print "$SERVICE"
curl --noproxy '*' --fail --silent --show-error \
  --connect-timeout 2 --max-time 5 "$ADDRESS/livez"
curl --noproxy '*' --fail --silent --show-error \
  --connect-timeout 2 --max-time 5 "$ADDRESS/readyz"
"$GATEWAY_BIN" --data-dir "$DATA_DIR" status --address "$ADDRESS"
tail -n 50 "$LOG_DIR/stdout.log" "$LOG_DIR/stderr.log"
```

Check launchd's state, program arguments, PID, and last exit status. Both HTTP probes are unauthenticated, use the same exact numeric loopback authority as `--listen`, bypass shell proxies, and have finite deadlines. `/livez` proves process liveness only; `/readyz` reports Gateway readiness, not that every upstream is usable. During startup or drain the service may not be ready. Inspect status and logs before deliberately repeating a read.

`status` authenticates through the public loopback API using `$DATA_DIR/admin-bearer` without displaying its value. If rotation, reset, or restore gave you a replacement file, add `--admin-bearer-file /absolute/path/to/replacement` as described in [administration](administration.md#administrator-authentication); never `cat` the bearer into a header argument. Check keyring capability and storage posture separately from readiness. Native capability status is not proof of successful credential access across restarts.

Stdout carries the safe startup acknowledgement; stderr carries bounded failure diagnostics. For continued observation use `tail -f` on those same files and Ctrl-C to stop tailing, not Gateway. Review logs locally and share only necessary non-secret diagnostics.

## Manage

### Graceful stop and restart

Unload to stop without KeepAlive immediately relaunching the process:

```bash
launchctl bootout "$SERVICE"
```

bootout requests termination and removes the job from the domain. Gateway handles the first `SIGTERM` by making readiness false and draining owned work within its ten-second shutdown bound. The plist sets `ExitTimeOut` to 30 seconds so launchd's deadline leaves room for that drain. A second signal or forced termination can interrupt cleanup. Confirm the job is absent with `launchctl print "$SERVICE"` (expected service-not-found), inspect stderr, and confirm its old process has exited before offline maintenance or replacement. If removal fails or shutdown remains uncertain, investigate rather than sending repeated signals, deleting locks, or assuming the data directory is free.

For a binary upgrade, first bootout and confirm stop, then install the replacement at the same absolute executable path and reload:

```bash
make -C "$GATEWAY_SOURCE" install &&
  plutil -lint "$PLIST" &&
  launchctl bootstrap "$DOMAIN" "$PLIST"
```

For a restart without an upgrade, bootstrap the unchanged plist after the same stop checks. Rerun [verification](#verify). Avoid `launchctl kickstart -k` for routine maintenance: it forcibly replaces the running process rather than preserving Gateway's graceful drain. Restart discards browser sessions and process-local runtime state; in-flight effects can remain unknown and must not be replayed automatically. See [invocation evidence](invocation-evidence.md).

### Plist changes

bootout and confirm stop first. Edit the installed plist's literal values using a plist-aware editor, preserving owner-only permissions and never including secrets. Update the matching shell selections too (`DATA_DIR`, `GATEWAY_BIN`, log paths, listen/address, and label/service as applicable). Validate with `plutil -lint "$PLIST"`, then `launchctl bootstrap "$DOMAIN" "$PLIST"` and verify. A kickstart alone does not reread plist changes. If changing the label, unload the old service before selecting the new label and plist filename. Gateway has no Broker-style signal reload procedure; use its online administration commands for supported runtime changes.

### Logs and uninstall

launchd does not rotate these files. Arrange owner-controlled size limits/rotation; stop and confirm Gateway has exited before moving logs, then bootstrap so it opens the new files. Moving an open log alone leaves the process writing to the old inode. Do not log credentials or add a token-bearing watchdog.

To uninstall the agent, bootout and confirm stop as above, then remove only the selected plist:

```bash
rm -i "$PLIST"
```

This intentionally preserves the executable, data directory, SQLite state, administrator bearer files, native-keyring credentials, and logs. Credential retirement and destructive state removal are separate deliberate operations; consult [backup, restore, and recovery](backup-and-recovery.md) before changing installation state.

## Troubleshooting

- **GUI domain or Keychain unavailable:** run as the installation owner in that user's logged-in macOS GUI session, not through `sudo`, a LaunchDaemon, or a headless session. Inspect authenticated keyring capability and safe remediation codes. Unlock the intended keychain through macOS UI when appropriate; do not put its password in commands or environment. A startup snapshot does not rule out a later prompt or blocked native call.
- **Bootstrap fails or repeated exits:** inspect `plutil -lint`, launchd's last exit status, and stderr. Check the absolute executable exists, is executable for this user and architecture, and is not a version-manager shim requiring a shell profile. All parent directories must exist and permit owner access; logs must be writable. Do not broaden state permissions to fix a path error.
- **Works in Terminal but not launchd:** compare the actual rendered argv and explicit data root, not shell defaults. Check the minimal system `PATH`; configure managed stdio server dependencies separately rather than relying on inherited shell exports.
- **Listener or data ownership conflict:** do not run two Gateway processes on the same root or address. Identify the existing service and its owner before stopping it. Keep Gateway and Broker independent. If intentionally using another installation, select a distinct root, label, log directory, and canonical numeric loopback port consistently for both service and CLI; never remove ownership files to bypass a running owner.
- **Live but unready, authentication failure, or storage latch:** use status and logs, then [administration](administration.md) or [stopped-process recovery](backup-and-recovery.md). A successful `bootstrap` or `/livez` does not prove readiness, administrator authority, upstream health, or clean previous shutdown. Do not reinitialize, reset authority, or force a restart as a generic fix.

Return to the [documentation map](../README.md) or [Gateway README](../../README.md).
