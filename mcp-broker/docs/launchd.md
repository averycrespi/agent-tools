# Run as a launchd agent (macOS)

To keep the broker running in the background whenever you're logged in, install it as a per-user LaunchAgent. The example plist lives in [`../examples/launchd/`](../examples/launchd/). All shell commands below assume you're in the `mcp-broker/` subdirectory of the repo.

## Authenticating backends without a shell

launchd does not source your shell profile, so backend MCP servers configured with `$VAR`-style env references (e.g. `{"env": {"GITHUB_TOKEN": "$GITHUB_TOKEN"}}`) won't see those variables out of the box.

The path of least resistance is to use backends that read host credentials from the macOS Keychain rather than from process env:

- **GitHub** — use [`local-gh-mcp`](../../local-gh-mcp/) instead of the upstream npx server. It shells out to `gh`, which reads its token from the Keychain (unlocked at login) — no env vars needed.
- **Git operations** — use [`local-git-mcp`](../../local-git-mcp/) with `gh auth setup-git` configured on the host.

If you do need to inject a secret, uncomment the relevant block in `examples/launchd/mcp-broker.plist` and paste the value. The plist file should stay `chmod 600` (which `launchctl bootstrap` enforces). Shell exports in `.zshrc`/`.bashrc` won't reach launchd.

## State paths

The broker creates and reads from these on first launch, running as the same user as the launchd job — no extra plist wiring is needed to ensure the directories exist.

| Path                                 | Contents                                |
| ------------------------------------ | --------------------------------------- |
| `~/.config/mcp-broker/config.json`   | Backend servers, rules, port, log level |
| `~/.config/mcp-broker/auth-token`    | 64-char hex bearer token (mode `0600`)  |
| `~/.local/share/mcp-broker/audit.db` | SQLite audit log of every tool call     |

OAuth refresh tokens (for backends that use OAuth) are stored in the macOS Keychain via `go-keyring`, not on disk.

## Install

```bash
# 1. Build and install the binary.
make install   # drops it at $(go env GOPATH)/bin, typically ~/go/bin.

# 2. Render the example plist with your username and drop it into
#    ~/Library/LaunchAgents/.
sed "s/USERNAME/$USER/g" examples/launchd/mcp-broker.plist \
    > ~/Library/LaunchAgents/dev.agent-tools.mcp-broker.plist

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
# Restart after upgrading the binary, editing the plist, or editing
# ~/.config/mcp-broker/config.json.
launchctl kickstart -k gui/$UID/dev.agent-tools.mcp-broker

# Stop and unload.
launchctl bootout gui/$UID/dev.agent-tools.mcp-broker
```

Logs at `~/Library/Logs/mcp-broker.{out,err}.log` are not rotated automatically — prune them yourself if they grow.
