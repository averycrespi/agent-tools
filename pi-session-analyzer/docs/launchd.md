# Run as a launchd agent (macOS)

To keep the dashboard available in the background whenever you're logged in, install two per-user LaunchAgents: one that serves the dashboard on a fixed loopback port, and one that periodically ingests new or changed Pi sessions so the index stays current. The example plists live in [`../examples/launchd/`](../examples/launchd/). All shell commands below assume you're in the `pi-session-analyzer/` subdirectory of the repo.

## Why two agents

The dashboard is deliberately read-only: it never creates, migrates, or writes the database. It exits at startup if the database is missing or its schema is stale, and it serves whatever the index contained at the last ingest. The ingest agent covers both needs — it runs at login (creating the database and applying any pending migrations before the dashboard needs them) and then every 15 minutes (indexing new sessions). Ingest skips unchanged `(path, size, mtime)` source files, so each run is cheap.

Until the first ingest completes, launchd restart-throttles the dashboard agent; it comes up on its own once the database exists.

## Privacy

The dashboard binds literal `127.0.0.1` only and has no TLS or remote-bind option. Every request additionally requires the auth token at `~/.config/pi-session-analyzer/auth-token` (created on first run, mode `0600`; rotate with `pi-session-analyzer token rotate` and kickstart the dashboard agent to apply). The dashboard renders scrubbed-but-identifying session content (prompts, responses, code, paths, hosts) and is **private and not safe to share, screenshot, or port-forward**. The token is defense-in-depth; the loopback bind remains the load-bearing boundary.

Note that the dashboard prints its tokened startup URL to stdout on every launch, so `~/Library/Logs/pi-session-analyzer-dashboard.out.log` contains the current token — treat that log like the token file.

## State paths

Both agents run as your user with launchd's minimal environment. `XDG_DATA_HOME` and `XDG_CONFIG_HOME` are usually unset under launchd (launchd does not source your shell profile), so the database lands at the fallback path `~/.local/share/pi-session-analyzer/sessions.db`, the auth token at `~/.config/pi-session-analyzer/auth-token` (64-char hex bearer token, mode `0600`), and sessions are read from `~/.pi/agent/sessions`. If you override the database path on the command line in one plist, override it identically in the other so both agents use the same database.

## Install

```bash
# 1. Build and install the binary.
make install   # drops it at $(go env GOPATH)/bin, typically ~/go/bin.

# 2. Render the example plists with your username and drop them into
#    ~/Library/LaunchAgents/.
sed "s/USERNAME/$USER/g" examples/launchd/pi-session-analyzer-ingest.plist \
    > ~/Library/LaunchAgents/dev.agent-tools.pi-session-analyzer-ingest.plist
sed "s/USERNAME/$USER/g" examples/launchd/pi-session-analyzer-dashboard.plist \
    > ~/Library/LaunchAgents/dev.agent-tools.pi-session-analyzer-dashboard.plist

# 3. Load them: ingest first so the database exists, then the dashboard.
launchctl bootstrap gui/$UID ~/Library/LaunchAgents/dev.agent-tools.pi-session-analyzer-ingest.plist
launchctl kickstart -k gui/$UID/dev.agent-tools.pi-session-analyzer-ingest
launchctl bootstrap gui/$UID ~/Library/LaunchAgents/dev.agent-tools.pi-session-analyzer-dashboard.plist
launchctl kickstart -k gui/$UID/dev.agent-tools.pi-session-analyzer-dashboard
```

The dashboard plist pins `--port 31415` so the URL stays stable across restarts (the default port `0` would choose a new ephemeral port every launch) and passes `--no-open` so launchd doesn't open a browser tab at every login and restart. Any free loopback port works — edit the plist if 31415 is taken.

## Verify

```bash
# Both states should be "running" (ingest shows "not running" between
# its 15-minute intervals; that's normal for a StartInterval job).
launchctl print gui/$UID/dev.agent-tools.pi-session-analyzer-dashboard | grep -E '^\s+state'

# The dashboard should answer on the pinned port — expect 200. (Use GET:
# the dashboard rejects every other method, including HEAD, with 405.
# Without the bearer token you get a 302 to /unauthorized instead.)
token=$(cat ~/.config/pi-session-analyzer/auth-token)
curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $token" http://127.0.0.1:31415/

# Tail logs. The dashboard prints its tokened URL to stdout (.out.log) —
# open that URL to authenticate a browser; startup errors such as a
# missing database go to stderr (.err.log). Ingest writes a JSON summary
# of each run to stdout.
tail -f ~/Library/Logs/pi-session-analyzer-{dashboard,ingest}.{out,err}.log
```

## Manage

```bash
# Restart after upgrading the binary or editing a plist.
launchctl kickstart -k gui/$UID/dev.agent-tools.pi-session-analyzer-ingest
launchctl kickstart -k gui/$UID/dev.agent-tools.pi-session-analyzer-dashboard

# Stop and unload.
launchctl bootout gui/$UID/dev.agent-tools.pi-session-analyzer-dashboard
launchctl bootout gui/$UID/dev.agent-tools.pi-session-analyzer-ingest
```

After upgrading to a binary that adds detectors or derived columns, run `pi-session-analyzer detect` once by hand: periodic ingest recomputes findings only for changed sessions, so already-indexed sessions keep their old detector coverage until an explicit detect pass. (Schema migrations need no extra step — the next ingest applies them.)

Logs at `~/Library/Logs/pi-session-analyzer-{dashboard,ingest}.{out,err}.log` are not rotated automatically — prune them yourself if they grow.
