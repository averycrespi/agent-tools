# Run Pi Orchestrator Dashboard as a launchd agent (macOS)

To keep `po dashboard` available in the background whenever you're logged in, install it as a per-user LaunchAgent. The example plist lives in [`../examples/launchd/`](../examples/launchd/). All shell commands below assume you're in the `pi-orchestrator/` subdirectory of the repo.

This only runs the read-only dashboard server. It does not create a central `po` daemon, start workflow runs, stop workflow runs, remove worktrees, or expose mutation APIs.

## State paths

The dashboard creates or reads these paths as the same user as the launchd job:

| Path                                   | Contents                              |
| -------------------------------------- | ------------------------------------- |
| `~/.config/po/workflows`               | Default workflow definition directory |
| `~/.config/po/auth-token`              | 64-char dashboard token (mode `0600`) |
| `~/.local/state/po/po.db`              | Default SQLite workflow database      |
| `~/.local/state/po/runs/<run-id>`      | Workflow supervisor logs              |
| `~/.local/state/po/artifacts/<run-id>` | Workflow artifacts                    |

`$XDG_CONFIG_HOME` and `$XDG_STATE_HOME` are usually unset under launchd because launchd does not source your shell profile, so the fallback paths above are what you'll normally see.

## Install

```bash
# 1. Build and install the binary.
make install   # drops it at $(go env GOPATH)/bin, typically ~/go/bin.

# 2. Render the example plist with your username and drop it into
#    ~/Library/LaunchAgents/.
sed "s/USERNAME/$USER/g" examples/launchd/po-dashboard.plist \
    > ~/Library/LaunchAgents/dev.agent-tools.po-dashboard.plist
chmod 600 ~/Library/LaunchAgents/dev.agent-tools.po-dashboard.plist

# 3. Load and start it.
launchctl bootstrap gui/$UID ~/Library/LaunchAgents/dev.agent-tools.po-dashboard.plist
launchctl kickstart -k gui/$UID/dev.agent-tools.po-dashboard
```

The example runs `po dashboard --no-open` so launchd restarts do not pop browser tabs. The authenticated dashboard URL is printed to `~/Library/Logs/po-dashboard.out.log` on each start.

## Verify

```bash
# State should be "running".
launchctl print gui/$UID/dev.agent-tools.po-dashboard | grep -E '^\s+state'

# Hit the dashboard API with the bearer token — should print HTTP/1.1 200 OK.
token=$(cat ~/.config/po/auth-token)
curl -sI -H "Authorization: Bearer $token" http://127.0.0.1:8400/dashboard/api/runs

# Tail logs. The authenticated URL is written to stdout; startup diagnostics
# and failed request logs are written to stderr.
tail -f ~/Library/Logs/po-dashboard.{out,err}.log
```

Open the printed URL, or visit `http://127.0.0.1:8400/dashboard/?token=$(cat ~/.config/po/auth-token)` once to set the dashboard cookie.

## Manage

```bash
# Restart after upgrading the binary or rotating the dashboard auth token.
launchctl kickstart -k gui/$UID/dev.agent-tools.po-dashboard

# Stop and unload.
launchctl bootout gui/$UID/dev.agent-tools.po-dashboard
```

Logs at `~/Library/Logs/po-dashboard.{out,err}.log` are not rotated automatically — prune them yourself if they grow.
