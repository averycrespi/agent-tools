# Run as a launchd agent (macOS)

To keep the proxy running in the background whenever you're logged in, install it as a per-user LaunchAgent. The example plists live in [`../examples/launchd/`](../examples/launchd/). All shell commands below assume you're in the `local-gomod-proxy/` subdirectory of the repo.

## Authenticate git without an ssh-agent

launchd does not start `ssh-agent` or export `SSH_AUTH_SOCK`, so the proxy can't reach passphrase-protected SSH keys out of the box. The simplest fix is to have git fetch private repos over HTTPS + a credential helper, which works fine under launchd because the login keychain is unlocked whenever you're signed in:

```bash
# One-time: gh stores a token in its keychain-backed config and installs a
# credential helper that answers git's prompts from that token.
gh auth login
gh auth setup-git

# Rewrite any ssh://git@github.com URLs in existing go.mod files to HTTPS
# so they also flow through the gh credential helper.
git config --global --add url."https://github.com/".insteadOf "git@github.com:"
git config --global --add url."https://github.com/".insteadOf "ssh://git@github.com/"

# Tell go which module paths are private.
go env -w GOPRIVATE='github.com/your-org/*'
```

If your org enforces SAML-SSO and blocks HTTPS PATs, or you need to reach a git host that only speaks SSH, see [SSH fallback](#ssh-fallback) below.

## Install

```bash
# 1. Build and install the binary.
make install   # drops it at $(go env GOPATH)/bin, typically ~/go/bin.

# 2. Render the example plist with your username and drop it into
#    ~/Library/LaunchAgents/.
sed "s/USERNAME/$USER/g" examples/launchd/local-gomod-proxy.plist \
    > ~/Library/LaunchAgents/dev.agent-tools.local-gomod-proxy.plist

# 3. Load and start it.
launchctl bootstrap gui/$UID ~/Library/LaunchAgents/dev.agent-tools.local-gomod-proxy.plist
launchctl kickstart -k gui/$UID/dev.agent-tools.local-gomod-proxy
```

## Verify

```bash
# State should be "running".
launchctl print gui/$UID/dev.agent-tools.local-gomod-proxy | grep -E '^\s+state'

# Exercise the public path end-to-end — should print HTTP/1.1 200 OK.
curl -sI http://127.0.0.1:7070/github.com/stretchr/testify/@latest

# Tail logs. slog writes to stderr by default, so .err.log carries
# startup and request logs; .out.log is reserved for anything on stdout.
tail -f ~/Library/Logs/local-gomod-proxy.{out,err}.log
```

## Manage

```bash
# Restart after upgrading the binary or editing the plist.
launchctl kickstart -k gui/$UID/dev.agent-tools.local-gomod-proxy

# Stop and unload.
launchctl bootout gui/$UID/dev.agent-tools.local-gomod-proxy
```

Logs at `~/Library/Logs/local-gomod-proxy.{out,err}.log` are not rotated automatically — prune them yourself if they grow.

## SSH fallback

Only needed if HTTPS + `gh` isn't viable for you. This adds a second LaunchAgent that runs `ssh-agent` with a fixed socket path so the proxy's LaunchAgent can find it.

```bash
# 1. Install the ssh-agent LaunchAgent.
sed "s/USERNAME/$USER/g" examples/launchd/ssh-agent.plist \
    > ~/Library/LaunchAgents/dev.agent-tools.ssh-agent.plist
launchctl bootstrap gui/$UID ~/Library/LaunchAgents/dev.agent-tools.ssh-agent.plist

# 2. One-time, from Terminal: load your key into this agent via the macOS
#    keychain so it unlocks without prompting after reboots.
SSH_AUTH_SOCK=$HOME/.ssh/agent.sock \
    ssh-add --apple-use-keychain ~/.ssh/id_ed25519

# 3. Uncomment the SSH_AUTH_SOCK block in examples/launchd/local-gomod-proxy.plist,
#    re-render, and reload.
sed "s/USERNAME/$USER/g" examples/launchd/local-gomod-proxy.plist \
    > ~/Library/LaunchAgents/dev.agent-tools.local-gomod-proxy.plist
launchctl bootout gui/$UID/dev.agent-tools.local-gomod-proxy
launchctl bootstrap gui/$UID ~/Library/LaunchAgents/dev.agent-tools.local-gomod-proxy.plist
```

Don't try to reuse macOS's system `com.openssh.ssh-agent` socket — its path is randomized each boot and scoped to the Aqua session type, so it can't be referenced reliably from a LaunchAgent.
