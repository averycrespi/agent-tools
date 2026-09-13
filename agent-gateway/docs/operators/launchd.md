# Run Agent Gateway as a launchd agent (macOS)

Audience: Gateway operators using a logged-in macOS desktop

Purpose: Install, verify, and manage a per-user LaunchAgent with the [example plist](../../examples/launchd/agent-gateway.plist). This supervises the foreground `agent-gateway serve` process; it is not a system LaunchDaemon or an unattended credential-access solution.

## Executable naming compatibility

`make install` provides only `agent-gateway`. It leaves stale legacy binaries untouched; follow [retirement and operator cleanup](installation-migration.md#retired-executable-and-operator-cleanup). New installations use canonical Agent Gateway paths and service naming. Existing explicit-root commands remain supported; implicit selection refuses unmigrated legacy roots rather than initializing another installation. Follow the [stopped installation migration](installation-migration.md) procedure for an existing host. Code delivery does not perform or authorize host adoption. Native-keyring identifiers remain unchanged; old service/process detection remains a safety boundary, not supported legacy launch selection.

For a **new** LaunchAgent, the recommended name is selected by default (an explicit path is also supported):

```bash
./scripts/install-launchd-agent.sh --binary "$(go env GOPATH)/bin/agent-gateway"
```

Follow the prerequisites and stop/verification procedures below before installation. Either name accepts the same CLI grammar and explicit installation root. Custom build/install destinations require an explicit absolute `--binary` path. Native keyring permission prompts can depend on the selected executable; attend and verify expected prompts rather than changing credential identifiers or granting blanket access.

## GUI session and credentials

Run these commands in Terminal as the intended logged-in user, without `sudo`. Use `~/Library/LaunchAgents` and the `gui/<uid>` domain, not `/Library/LaunchDaemons` or a root service. The GUI session supplies the user's native Keychain context; login does not guarantee that every credential operation can succeed without interaction. Logout ends that session's service; the installed agent starts again at GUI login.

Gateway's startup keyring capability probe is secret-free and does not request credential access. Authenticated `status` exposes the capability snapshot, not secret values. A `ready` snapshot does not guarantee that a later native read, write, or delete will avoid a macOS permission prompt. Such operations can fail or outlive cancellation. Attend any expected prompt in the same GUI session and verify the requesting program before approving it; investigate unexpected prompts rather than granting blanket access. Gateway has no plaintext fallback and is currently unsuitable for unattended credential access. See the [native-keyring contract](../design/downstream-servers.md#keyring-capability-and-generation-cutover).

Never put administrator, agent, server, or OAuth secrets in the plist, command arguments, environment variables, URLs, or logs. The service uses native keyring storage for server credentials; the online CLI reads an owner-only administrator bearer file. No bearer value needs to be printed, copied into a shell variable, or passed to `curl` for this procedure. Follow [administrator authentication](administration.md#administrator-authentication) and [upstream server configuration](upstream-servers.md) for credential workflows.

## Quick start

Prerequisites: the [Gateway build requirements](../../README.md#installation), Python 3 (standard library only), and the macOS tools `dscl`, `plutil`, `ps`, and `launchctl`. Python owns bounded subprocess inspection and plist serialization; no package installation or manual template editing is needed. Run from this repository's `agent-gateway/` directory in one Terminal session as the intended logged-in user, without `sudo`.

**Existing installation:** skip initialization. Stop any existing foreground or supervised Gateway owning the data root or port before installing a replacement binary or loading the agent. For an already installed agent, follow [management](#manage) instead; the installer refuses to overwrite its plist. If the existing data root or executable differs from the defaults below, use [custom paths](#custom-paths).

```bash
make install
```

**New installation only:** after confirming the default root is fresh and unused:

```bash
"$(go env GOPATH)/bin/agent-gateway" initialize
```

Stop on any error. Initialization creates owner-only state and `<data-dir>/admin-bearer` without printing the bearer. If it fails or is interrupted, use [administration](administration.md#installation-root) and [recovery](backup-and-recovery.md), not repeated initialization or deletion of existing state.

Install the plist and load it:

```bash
./scripts/install-launchd-agent.sh &&
  launchctl bootstrap "gui/$(id -u)" \
    "$HOME/Library/LaunchAgents/dev.agent-tools.agent-gateway.plist"

"$(go env GOPATH)/bin/agent-gateway" status
```

The short commands assume your normal account `HOME` and unchanged `XDG_DATA_HOME`. The installer uses the **OS-account home**, not the `HOME` environment variable. It prints the exact installed paths; recover and verify those selections below before an explicit load, especially if your shell overrides `HOME` or you selected custom paths. A successful bootstrap alone is not a readiness check; continue with [verification](#verify).

### Defaults and installer behavior

| Setting        | Default                                                                                            |
| -------------- | -------------------------------------------------------------------------------------------------- |
| Executable     | `$(go env GOPATH)/bin/agent-gateway`, matching `make install`                                      |
| Data directory | Absolute `$XDG_DATA_HOME/agent-gateway`, otherwise OS-account home + `/.local/share/agent-gateway` |
| Listener       | `127.0.0.1:8210`                                                                                   |
| Label          | `dev.agent-tools.agent-gateway`                                                                    |
| Plist          | OS-account home + `/Library/LaunchAgents/dev.agent-tools.agent-gateway.plist`                      |
| Logs           | OS-account home + `/Library/Logs/agent-gateway/{stdout,stderr}.log`                                |

The [installer](../../scripts/install-launchd-agent.sh) delegates to the shared [LaunchAgent helper](../../scripts/launchd-agent.py). It resolves the [template](../../examples/launchd/agent-gateway.plist) relative to itself, serializes the complete literal `ProgramArguments` array with Python's plist library, and exclusively creates a synced `0600` plist. It creates a private log directory (`0700`) and log files (`0600`), refuses unsafe existing permissions or symlinks rather than changing them, and never overwrites an existing plist. It checks both legacy and canonical service identities and refuses loaded jobs, legacy plists and unknown inspection results before publication. Inspect conflicting paths before changing permissions. Run `./scripts/install-launchd-agent.sh --help` for its options.

It does **not** initialize or open the private database, access credentials, start/stop services, or run the selected Gateway binary. It inspects root presence/permissions and service ownership only. Every external command has a five-second deadline and 1 MiB output cap; timeout cleanup retains and reaps the owned command process before reporting failure. Verify that binary is the intended native macOS executable, not a shell shim or an `e2e`-provider build. On failure, no service is loaded; newly created log directories/files may remain. Fix the reported cause rather than deleting existing state.

The selected data root is written explicitly into the service argv, so launchd does not depend on your shell's XDG settings. A relative `XDG_DATA_HOME` is rejected unless `--data-dir` overrides it, matching [installation root precedence](administration.md#installation-root). The template runs the executable directly with separate argv elements and no shell wrapper; launchd does not expand `~`, `$HOME`, or shell expressions inside plist strings.

### Custom paths

For an existing installation or a binary installed another way:

```bash
./scripts/install-launchd-agent.sh \
  --binary /absolute/path/to/agent-gateway \
  --data-dir /absolute/path/to/existing-data
```

Omitting either flag uses its default. Both flags require absolute paths. Use the installer's printed paths to load and verify these selections; do not initialize another root. Multiple GOPATH entries require an explicit `--binary`. New services may select `--listen` and repeatable `--allowed-host`. Existing installations should use `--from-plist /absolute/private/archive/old.plist` with explicit `--binary` and `--data-dir`; this preserves every other literal argument and refuses simultaneous listener/host overrides. Unknown argument layouts require explicit reconciliation. Custom labels and other supervisors require an advanced [plist-change procedure](#plist-changes); the automated scripts manage one canonical service only.

## Service behavior

`RunAtLoad` starts the job on loading; no immediate forced kickstart is needed. `KeepAlive` restarts the process after exit, including clean exit. It is not a health check: a hung or unready process can remain running. A readiness failure is a reason to investigate, not an automatic restart or mutation-retry instruction.

The minimal `PATH` is for Gateway's own system utilities, including native keyring support. launchd does not source `.zshrc`, `.bashrc`, or version-manager setup. Do not add secrets to compensate. Gateway-managed stdio servers receive a separate clean environment of declared non-secret values and runtime-resolved secret slots, not the LaunchAgent environment. Their absolute executables, literal arguments, working directories, and environment are configured through [upstream server configuration](upstream-servers.md).

## Verify

For detailed verification and later management, recover the installed selections in each new Terminal session. These commands use the default label; adjust `LABEL` only if you deliberately changed it:

```bash
ACCOUNT_HOME="$(dscl -plist . -read "/Users/$(id -un)" NFSHomeDirectory |
  plutil -extract dsAttrTypeStandard:NFSHomeDirectory.0 raw -o - -)"
LABEL='dev.agent-tools.agent-gateway'
DOMAIN="gui/$(id -u)"
SERVICE="$DOMAIN/$LABEL"
PLIST="$ACCOUNT_HOME/Library/LaunchAgents/$LABEL.plist"
GATEWAY_BIN="$(plutil -extract ProgramArguments.0 raw -o - "$PLIST")"
DATA_DIR="$(plutil -extract ProgramArguments.3 raw -o - "$PLIST")"
LISTEN="$(plutil -extract ProgramArguments.5 raw -o - "$PLIST")"
LOG_DIR="$(dirname "$(plutil -extract StandardOutPath raw -o - "$PLIST")")"
ADDRESS="http://$LISTEN"
```

Stop if any selection cannot be read. Then:

```bash
launchctl print "$SERVICE"
curl --noproxy '*' --fail --silent --show-error \
  --connect-timeout 2 --max-time 5 "$ADDRESS/livez"
curl --noproxy '*' --fail --silent --show-error \
  --connect-timeout 2 --max-time 5 "$ADDRESS/readyz"
"$GATEWAY_BIN" --data-dir "$DATA_DIR" status --address "$ADDRESS"
tail -n 50 "$LOG_DIR/stdout.log" "$LOG_DIR/stderr.log"
```

Check launchd's state, program arguments, PID, and last exit status. `plutil -p "$PLIST"` should show exactly six `ProgramArguments` entries in this order: the selected executable, `serve`, `--data-dir`, the selected data root, `--listen`, and `127.0.0.1:8210` (or your deliberately selected listener). No `/ABSOLUTE/PATH/TO/` template placeholders should remain. `plutil -lint` checks syntax, not argument correctness. Both HTTP probes are unauthenticated, use the same exact numeric loopback authority as `--listen`, bypass shell proxies, and have finite deadlines. `/livez` proves process liveness only; `/readyz` reports Gateway readiness, not that every upstream is usable. During startup or drain the service may not be ready. Inspect status and logs before deliberately repeating a read.

`status` authenticates through the public loopback API using `$DATA_DIR/admin-bearer` without displaying its value. If rotation, reset, or restore gave you a replacement file, add `--admin-bearer-file /absolute/path/to/replacement` as described in [administration](administration.md#administrator-authentication); never `cat` the bearer into a header argument. Check keyring capability and storage posture separately from readiness. Native capability status is not proof of successful credential access across restarts.

Stdout carries the safe startup acknowledgement; stderr carries JSON diagnostic lines and the existing terminal-problem representation. The unchanged plist uses the default `warn` diagnostic level: actionable upstream/authentication failures and uncertain cleanup are visible without changing it. Select `--log-level info` when foreground OAuth completion and upstream recovery are needed, or `--log-level debug` for actual attempts, capped retry delays/resets, OAuth stages, and contention timing. Follow [safe serve diagnostics](administration.md#safe-serve-diagnostics) for correlation examples, expected failure/retry/OAuth/recovery sequences, unknown facts, and loss limits. Adding either two-argument flag/value pair deliberately changes the six-entry default argument inventory above and requires an authorized configuration/restart; do not restart a live job merely to inspect existing logs. For continued observation use `tail -f` on those same files and Ctrl-C to stop tailing, not Gateway. Review logs locally and share only necessary non-secret diagnostics. Correlate opaque references only within the logged `process_id`; a reference is not a server ID, durable audit evidence, or proof of execution, and no direct reference-to-resource lookup exists. Gateway and this plist provide no rotation or retention service: the OS-account owner must control log permissions, retention, rotation, and deletion. Broken or blocked destinations can lose even warnings; an absent line is not evidence that an attempt never ran.

## Manage

### Graceful stop and restart

For a routine restart of the default LaunchAgent, run from `agent-gateway/` as the intended logged-in user, without `sudo`:

```bash
./scripts/restart-launchd-agent.sh
```

The [restart script](../../scripts/restart-launchd-agent.sh) resolves the OS-account home, validates the private installed plist and canonical label, and refuses any legacy plist/job. It loads the canonical service if already absent. For a loaded service it requires the loaded path, program and complete argv to match the installed selections, and checks the process UID, start time and command before one `bootout`. It then observes job absence and process exit within a 30-second wait budget before one `bootstrap`; each inspection has its own five-second subprocess bound. Changed/reused or unrecognized process identities refuse rather than authorizing a signal or replacement. The job can remain visible briefly after `bootout` returns; this is not an immediate failure. Unexpected inspection errors or an unconfirmed stop abort without launching a replacement; it never force-kills or retries a mutation. Run it serially, not alongside other service management. It does not install binaries, edit the plist, or support custom labels. Successful launch is not proof of readiness: rerun [verification](#verify). Restart loses browser sessions and process-local state; do not automatically replay interrupted mutations.

For upgrades, plist changes, custom labels, or a manual restart, use the steps below.

Unload to stop without KeepAlive immediately relaunching the process:

```bash
launchctl bootout "$SERVICE"
```

bootout requests termination and removes the job from the domain. Gateway handles the first `SIGTERM` by making readiness false and draining owned work within its ten-second shutdown bound, followed by at most one second of best-effort diagnostic flush. A blocked log write may remain pending until process exit without changing settled storage cleanliness. The plist sets `ExitTimeOut` to 30 seconds so launchd's deadline leaves room for that drain. A second signal or forced termination can interrupt cleanup. Confirm the job is absent with `launchctl print "$SERVICE"` (expected service-not-found), inspect stderr, and confirm its old process has exited before offline maintenance or replacement. If removal fails or shutdown remains uncertain, investigate rather than sending repeated signals, deleting locks, or assuming the data directory is free.

For a binary upgrade, first bootout and confirm stop, then install the replacement at the same absolute executable path and reload. Run the following from this repository's `agent-gateway/` directory; `make install` targets `$(go env GOPATH)/bin`, so use your original installation method instead for a custom binary path:

```bash
make install &&
  plutil -lint "$PLIST" &&
  launchctl bootstrap "$DOMAIN" "$PLIST"
```

For a restart without an upgrade, bootstrap the unchanged plist after the same stop checks. Rerun [verification](#verify). Avoid `launchctl kickstart -k` for routine maintenance: it forcibly replaces the running process rather than preserving Gateway's graceful drain. Restart discards browser sessions and process-local runtime state; in-flight effects can remain unknown and must not be replayed automatically. See [invocation evidence](invocation-evidence.md).

### Plist changes

bootout and confirm stop first. Edit the installed plist's literal values using a plist-aware editor, preserving owner-only permissions and never including secrets. Update the matching shell selections too (`DATA_DIR`, `GATEWAY_BIN`, log paths, listen/address, and label/service as applicable). Validate with `plutil -lint "$PLIST"`, then `launchctl bootstrap "$DOMAIN" "$PLIST"` and verify. A kickstart alone does not reread plist changes. If changing the label, unload the old service before selecting the new label and plist filename. Gateway has no Broker-style signal reload procedure; use its online administration commands for supported runtime changes.

If an older installer generated extra arguments or retained template placeholders, do not load that plist. If already loaded, bootout and confirm stop using the procedure above. Preserve the incorrect plist outside `LaunchAgents`, then rerun the corrected installer from `agent-gateway/` (with the same explicit path overrides, if any):

```bash
mv -i "$PLIST" "$LOG_DIR/launch-agent.before-fix.plist" &&
  ./scripts/install-launchd-agent.sh
```

Inspect the regenerated arguments before following its printed load and verification commands. Do not reinitialize Gateway or remove any data, bearer, or keyring state.

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
