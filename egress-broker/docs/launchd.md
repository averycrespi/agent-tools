# Running under launchd

`egress-broker serve` runs in the foreground and has no pidfile. On macOS,
supervise it with a launchd **user agent**.

## Why a user agent, not a daemon

Credentials come from the login keychain, which is only unlocked inside the
logged-in GUI session. A system daemon (`/Library/LaunchDaemons`) runs outside
that session and cannot read them — every injection would fail with a keychain
error. Install into `~/Library/LaunchAgents` and load into `gui/$UID`.

## Install

```bash
mkdir -p ~/Library/LaunchAgents
sed "s/USERNAME/$(whoami)/g" examples/launchd/egress-broker.plist \
  > ~/Library/LaunchAgents/dev.agent-tools.egress-broker.plist

launchctl bootstrap "gui/$(id -u)" ~/Library/LaunchAgents/dev.agent-tools.egress-broker.plist
launchctl kickstart -p "gui/$(id -u)/dev.agent-tools.egress-broker"
```

Verify:

```bash
curl -s http://127.0.0.1:8221/healthz   # => ok
tail -f ~/Library/Logs/egress-broker.err.log
```

## Reload rules without restarting

```bash
launchctl kill HUP "gui/$(id -u)/dev.agent-tools.egress-broker"
```

`SIGHUP` re-reads `config.json`, `rules.json`, the auth token and the CA. Each
step keeps its previous state on failure, so a typo leaves the previous ruleset
serving traffic and logs an error — check the log after every reload. Listener
addresses are not reloadable; changing one needs a restart.

**Prefer `SIGHUP` over a restart.** Proxy variables are baked into every
sandbox's shell, so a restart points them all at a dead socket for its
duration. See the D15 recovery runbook in the README.

## The plist carries PATH and nothing else

launchd does not source shell profiles, so an environment-only credential
design would force every API key into this plist in plaintext. That is exactly
what the keychain design avoids. Store credentials with:

```bash
printf %s "$TOKEN" | egress-broker credential set gh_bot --host api.github.com
```

`env_credentials` in `config.json` is the escape hatch for headless Linux and
CI — not for this plist.

### First keychain read may prompt

The first time the agent reads a credential, macOS may show an "allow access"
dialog. Under launchd nobody is watching, so it looks like a hang: requests
needing that credential fail with a keychain error until someone answers.

Grant **Always Allow** on first use. To trigger the prompt deliberately, run
`egress-broker credential list` from a terminal in the same login session
before relying on the agent.

## Detecting a wedged proxy

`KeepAlive` restarts a **crashed** process. It does not notice a process that
is still listening but no longer serving — and that failure is worse here than
a crash, because every sandbox's network depends on this socket. A wedged proxy
also answers no signals, so the `SIGHUP` kill switch cannot help.

Probe `/healthz` from outside and restart on failure:

```bash
curl -fsS --max-time 5 http://127.0.0.1:8221/healthz >/dev/null \
  || launchctl kickstart -k "gui/$(id -u)/dev.agent-tools.egress-broker"
```

Run that from a `StartInterval` agent, a cron entry, or whatever host
monitoring already exists. `/healthz` needs no token, precisely so a probe can
be configured without handling one.

## Shutdown and in-flight tunnels

On `SIGTERM` the HTTP servers shut down gracefully with a **10-second** drain
window. `http.Server.Shutdown` has no visibility into hijacked CONNECT relays,
so a tunnel with a live transfer is not counted. If the window elapses with
tunnels still open, the process exits anyway and logs

```
shutdown deadline exceeded with tunnels still open; exiting
```

Exiting is deliberate: a process that waits indefinitely for a long-lived
tunnel never stops, and launchd would then never restart it.

## Uninstall

```bash
launchctl bootout "gui/$(id -u)/dev.agent-tools.egress-broker"
rm ~/Library/LaunchAgents/dev.agent-tools.egress-broker.plist
```
