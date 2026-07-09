# Run as a launchd agent (macOS)

To keep the broker running in the background whenever you're logged in, install it as a per-user LaunchAgent. The example plist lives in [`../examples/launchd/`](../examples/launchd/). All shell commands below assume you're in the `mcp-broker/` subdirectory of the repo.

## Authenticating backends without a shell

launchd does not source your shell profile, so backend MCP servers configured with `$VAR`-style env references (e.g. `{"env": {"GITHUB_TOKEN": "$GITHUB_TOKEN"}}`) won't see those variables out of the box.

The path of least resistance is to use backends that read host credentials from the macOS Keychain rather than from process env:

- **GitHub** — use the official remote GitHub MCP server (`https://api.githubcopilot.com/mcp/`). It authenticates over OAuth, and the broker stores the refresh token in the Keychain — no env vars needed.
- **Git operations** — use [`local-git-mcp`](../../local-git-mcp/) with `gh auth setup-git` configured on the host.

If you do need to inject a secret, uncomment the relevant block in `examples/launchd/mcp-broker.plist` and paste the value. The plist file should stay `chmod 600`; `launchctl bootstrap` rejects group- or world-writable plists, but it does not make secret-bearing files private for you. Shell exports in `.zshrc`/`.bashrc` won't reach launchd.

## State paths

The broker creates and reads from these on first launch, running as the same user as the launchd job — no extra plist wiring is needed to ensure the directories exist.

| Path                                  | Contents                                     |
| ------------------------------------- | -------------------------------------------- |
| `~/.config/mcp-broker/config.json`    | Backend servers, rules_path, port, log level |
| `~/.config/mcp-broker/rules.json`     | Base policy rules (mode `0600`)              |
| `~/.config/mcp-broker/auth-token`     | 64-char hex bearer token (mode `0600`)       |
| `~/.local/share/mcp-broker/audit.db`  | SQLite audit log of every tool call          |
| `~/.local/share/mcp-broker/grants.db` | SQLite grant authorization state             |

OAuth refresh tokens (for backends that use OAuth) are stored in the macOS Keychain via `go-keyring`, not on disk.

## Install

```bash
# 1. Build and install the binary.
make install   # drops it at $(go env GOPATH)/bin, typically ~/go/bin.

# 2. Render the example plist with your username and drop it into
#    ~/Library/LaunchAgents/.
sed "s/USERNAME/$USER/g" examples/launchd/mcp-broker.plist \
    > ~/Library/LaunchAgents/dev.agent-tools.mcp-broker.plist
chmod 600 ~/Library/LaunchAgents/dev.agent-tools.mcp-broker.plist

# 3. Load and start it.
launchctl bootstrap gui/$UID ~/Library/LaunchAgents/dev.agent-tools.mcp-broker.plist
launchctl kickstart -k gui/$UID/dev.agent-tools.mcp-broker
```

## Verify

```bash
# State should be "running".
launchctl print gui/$UID/dev.agent-tools.mcp-broker | grep -E '^\s+state'

# Hit the MCP endpoint with the bearer token — should print HTTP/1.1 200 OK
# (or 405 if the server only accepts POST on /mcp; either confirms auth passed).
token=$(cat ~/.config/mcp-broker/auth-token)
curl -sI -H "Authorization: Bearer $token" http://127.0.0.1:8200/mcp

# Tail logs. slog writes to stderr by default, so .err.log carries
# startup and request logs (including the dashboard URL printed on
# every start); .out.log stays empty unless something unusual hits stdout.
tail -f ~/Library/Logs/mcp-broker.{out,err}.log
```

## Manage

```bash
# Reload policy rules after editing ~/.config/mcp-broker/rules.json.
# Invalid reloads are logged and leave the previous rules active.
launchctl kill HUP gui/$UID/dev.agent-tools.mcp-broker

# Restart after upgrading the binary, editing the plist, changing backend
# servers, tool patches, host/port, rules_path, audit path, grants path/max TTL,
# auth token, Telegram settings, approval timeout, log level, open_browser,
# request body limit, or fixing a backend that exhausted startup retries and
# needs its tools rediscovered. Grant mint/revoke changes are read from grants.db
# on the next MCP request and do not need restart.
launchctl kickstart -k gui/$UID/dev.agent-tools.mcp-broker

# Stop and unload.
launchctl bootout gui/$UID/dev.agent-tools.mcp-broker
```

Logs at `~/Library/Logs/mcp-broker.{out,err}.log` are not rotated automatically — prune them yourself if they grow.
