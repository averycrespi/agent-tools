# Run as a launchd agent (macOS)

To keep the broker running whenever you are logged in, install the example per-user LaunchAgent from [`../examples/launchd/`](../examples/launchd/). Commands below run from `mcp-broker/`.

## Authenticating backends without a shell

launchd does not source shell profiles. Prefer backends that use the macOS Keychain, such as the official remote GitHub MCP server via OAuth or [`local-git-mcp`](../../local-git-mcp/) with host Git authentication. If a backend requires environment variables, add them to the example plist and keep the rendered plist mode `0600`; shell exports do not reach launchd.

## State paths

| Path                                  | Contents                                                |
| ------------------------------------- | ------------------------------------------------------- |
| `~/.config/mcp-broker/config.json`    | Backend servers, rules path, listener, and log settings |
| `~/.config/mcp-broker/rules.json`     | Base policy rules (mode `0600`)                         |
| `~/.config/mcp-broker/agent-token`    | Sandbox-facing MCP credential (mode `0600`)             |
| `~/.config/mcp-broker/admin-token`    | Host-only dashboard credential (mode `0600`)            |
| `~/.local/share/mcp-broker/audit.db`  | SQLite audit log                                        |
| `~/.local/share/mcp-broker/grants.db` | SQLite grant authorization state                        |

A legacy `auth-token` is migration input only. Its normalized value becomes `agent-token`, a fresh distinct `admin-token` is created, and the legacy file is retired. Update external sandbox `copy_paths` to `agent-token` before the next provisioning run; never copy `admin-token` into a sandbox.

## Install

```bash
make install
sed "s/USERNAME/$USER/g" examples/launchd/mcp-broker.plist \
    > ~/Library/LaunchAgents/dev.agent-tools.mcp-broker.plist
chmod 600 ~/Library/LaunchAgents/dev.agent-tools.mcp-broker.plist
launchctl bootstrap gui/$UID ~/Library/LaunchAgents/dev.agent-tools.mcp-broker.plist
launchctl kickstart -k gui/$UID/dev.agent-tools.mcp-broker
```

## Verify

```bash
launchctl print gui/$UID/dev.agent-tools.mcp-broker | grep -E '^\s+state'
agent_token=$(mcp-broker token show agent)
curl -sI -H "Authorization: Bearer $agent_token" http://127.0.0.1:8200/mcp
tail -f ~/Library/Logs/mcp-broker.{out,err}.log
```

launchd output is non-interactive, so neither log receives a token-bearing dashboard URL and the broker does not open a browser. From an interactive host terminal, obtain the admin value with `mcp-broker token show admin` and open `http://localhost:8200/dashboard/?token=<admin-token>`.

## Manage

```bash
# Independently reload rules, agent-token, and admin-token.
launchctl kill HUP gui/$UID/dev.agent-tools.mcp-broker

# Restart after binary or startup-only config changes.
launchctl kickstart -k gui/$UID/dev.agent-tools.mcp-broker

# Stop and unload.
launchctl bootout gui/$UID/dev.agent-tools.mcp-broker
```

A bad rules file or bad role candidate cannot block a valid change to another reloadable file. Invalid role values keep that role's prior in-memory credential.

Agent rotation is coordinated: run `mcp-broker token rotate agent`, refresh sandbox `copy_paths` and re-provision while avoiding new agent starts, send `SIGHUP` promptly, then reconnect clients with the old value. New MCP HTTP requests reject the old value after activation; existing streams may drain. For admin rotation, rotate, send `SIGHUP`, and reopen the dashboard. Old cookies fail on new dashboard requests, while an already-open SSE stream may continue.

Restart is still required for backend servers, tool patches, hooks, listener settings, `rules.path`, database paths/settings, Telegram, approval timeout, logging, browser behavior, body limits, or backend rediscovery. Grant mint/revoke changes apply per request.

Downgrading to a one-token binary re-merges agent and dashboard authority. Stop or isolate the broker before deliberately reconstructing legacy shared-token state, and treat every sandbox holder as dashboard-authorized until re-upgrade and rotation.

On restart or stop, shutdown cancels active streams/calls and has a ten-second hard limit. launchd logs are not rotated automatically.
