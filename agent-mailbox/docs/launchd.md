# Run Agent Mailbox Dashboard as a launchd agent (macOS)

To keep `agent-mailbox dashboard` available in the background whenever you're logged in, install it as a per-user LaunchAgent. The example plist lives in [`../examples/launchd/`](../examples/launchd/). All shell commands below assume you're in the `agent-mailbox/` subdirectory of the repo.

This only runs the local dashboard server. It does not create a central mailbox daemon: CLI and MCP sends continue to write directly to the SQLite store, and the dashboard process only serves local triage UI/API/SSE over the same store.

## State paths

The dashboard creates or reads these paths as the same user as the launchd job:

| Path                                          | Contents                                      |
| --------------------------------------------- | --------------------------------------------- |
| `~/.config/agent-mailbox/auth-token`          | 64-char dashboard token (mode `0600`)         |
| `~/.local/state/agent-mailbox/mailbox.db`     | Default SQLite mailbox database (mode `0600`) |
| `~/.local/state/agent-mailbox/mailbox.db-wal` | SQLite WAL sidecar (mode `0600`)              |
| `~/.local/state/agent-mailbox/mailbox.db-shm` | SQLite shared-memory sidecar (mode `0600`)    |

`$XDG_CONFIG_HOME` and `$XDG_STATE_HOME` are usually unset under launchd because launchd does not source your shell profile, so the fallback paths above are what you'll normally see.

If you use a custom database location, add `--db-path /path/to/mailbox.db` to the `ProgramArguments` array in the plist. Use the same `--db-path` for CLI commands and any `mcp-broker` backend configuration that should share the dashboard mailbox.

## Install

```bash
# 1. Build and install the binary.
make install   # drops it at $(go env GOPATH)/bin, typically ~/go/bin.

# 2. Render the example plist with your username and drop it into
#    ~/Library/LaunchAgents/.
sed "s/USERNAME/$USER/g" examples/launchd/agent-mailbox-dashboard.plist \
    > ~/Library/LaunchAgents/dev.agent-tools.agent-mailbox-dashboard.plist
chmod 600 ~/Library/LaunchAgents/dev.agent-tools.agent-mailbox-dashboard.plist

# 3. Load and start it.
launchctl bootstrap gui/$UID ~/Library/LaunchAgents/dev.agent-tools.agent-mailbox-dashboard.plist
launchctl kickstart -k gui/$UID/dev.agent-tools.agent-mailbox-dashboard
```

The example runs `agent-mailbox dashboard --no-open` so launchd restarts do not pop browser tabs. The authenticated dashboard URL is printed to `~/Library/Logs/agent-mailbox-dashboard.out.log` on each start.

## Verify

```bash
# State should be "running".
launchctl print gui/$UID/dev.agent-tools.agent-mailbox-dashboard | grep -E '^\s+state'

# Hit the dashboard API with the bearer token — should print HTTP/1.1 200 OK.
token=$(cat ~/.config/agent-mailbox/auth-token)
curl -sI -H "Authorization: Bearer $token" http://127.0.0.1:8500/dashboard/api/messages

# Tail logs. The authenticated URL is written to stdout; startup diagnostics
# and failed request logs are written to stderr.
tail -f ~/Library/Logs/agent-mailbox-dashboard.{out,err}.log
```

Open the printed URL, or visit `http://127.0.0.1:8500/dashboard/?token=$(cat ~/.config/agent-mailbox/auth-token)` once to set the dashboard cookie.

## Manage

```bash
# Restart after upgrading the binary or rotating the dashboard auth token.
launchctl kickstart -k gui/$UID/dev.agent-tools.agent-mailbox-dashboard

# Stop and unload.
launchctl bootout gui/$UID/dev.agent-tools.agent-mailbox-dashboard
```

Logs at `~/Library/Logs/agent-mailbox-dashboard.{out,err}.log` are not rotated automatically — prune them yourself if they grow.
