# macOS LaunchAgent

`wts daemon` manages one per-user LaunchAgent with label `dev.agent-tools.worktree-sync`. It never manages another label and does not modify Git or tmux during install/uninstall.

## Install and lifecycle

Install both binaries in the same directory, then install the agent:

```bash
make install
wts daemon install
wts daemon status
wts daemon logs
wts daemon stop
wts daemon start
wts daemon restart
wts daemon uninstall
```

Installation resolves the absolute `wtsd` sibling of the running `wts` executable, atomically writes `~/Library/LaunchAgents/dev.agent-tools.worktree-sync.plist` with mode `0600`, and bootstraps the current GUI user domain. Repeating install updates only this plist and converges on one loaded service. If replacement bootstrap fails, `wts` restores the previous plist and loaded/stopped state; an incomplete rollback is reported as partial success with recovery guidance.

The generated service uses:

- `RunAtLoad` and `KeepAlive`;
- an explicit `/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin` PATH;
- the effective absolute `XDG_CONFIG_HOME`, `XDG_STATE_HOME`, and `XDG_DATA_HOME` from installation time;
- `~/Library/Logs/worktree-sync.log` for stdout; and
- `~/Library/Logs/worktree-sync.error.log` for stderr.

launchd does not load shell profiles. Install Git and tmux into the explicit PATH or run `wtsd` in a terminal to compare environments. Rerun `wts daemon install` after changing XDG homes so the daemon and CLI continue using the same registry. launchd does not rotate logs; configure a separate rotation policy if needed.

## Direct diagnostics

```bash
launchctl print gui/$(id -u)/dev.agent-tools.worktree-sync

wts daemon logs
wts daemon logs --lines 500
wts daemon logs --follow

wts config validate
wts status --json
wtsd --help
wtsd # foreground startup/reconcile diagnostics
```

`wts daemon logs` reads only the two fixed LaunchAgent files, shows the last 100 lines from each by default, and reports files that have not been created yet. `--lines` changes the initial history and `--follow` runs until interrupted. Operational `slog` records are written to stderr, so `worktree-sync.error.log` normally contains most activity. launchd does not rotate either file.

`wts daemon stop` boots out only this label while retaining its plist, so `KeepAlive` cannot relaunch it. `start` bootstraps a stopped installation; `restart` explicitly boots out and bootstraps it. Repeating start or stop reports an accurate no-op state. Uninstall boots out only this label and removes only its plist. Lifecycle changes are serialized by a fixed private per-user lock even when competing CLI processes use different XDG state homes. The state/config registry, Git worktrees, and tmux resources are preserved.

The reviewed [example plist](../examples/launchd/dev.agent-tools.worktree-sync.plist) shows the generated shape with placeholder paths. Use `wts daemon install` rather than copying it verbatim.

## Other platforms

LaunchAgent commands return a clear unsupported-platform error on Linux. Run `wtsd` directly under the foreground process supervisor of your choice; systemd packaging is intentionally out of scope.
