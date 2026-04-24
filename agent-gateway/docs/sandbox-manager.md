# Integrating with sandbox-manager

`sandbox-manager` (`sb`) provisions a Lima-based Linux sandbox for running AI coding agents. This document walks through wiring an `sb` sandbox to talk to `agent-gateway` running on the host.

The integration has two pieces:

1. Install the gateway's root CA into the sandbox trust store.
2. Point `HTTPS_PROXY` / `HTTP_PROXY` inside the sandbox at the host-side gateway.

Neither step is specific to `sb` — the generic commands in the [README quickstart](../README.md#quickstart) cover any Linux sandbox. This doc covers the `sb`-specific provisioning ergonomics.

## Prerequisites

- `agent-gateway serve` is running on the host.
- An agent token exists (`agent-gateway agent add <name>`).
- An `sb` sandbox is created (`sb create`).

## Networking

Lima exposes the host to the guest as `host.lima.internal`. From inside an `sb` sandbox, the gateway is reachable at `http://host.lima.internal:8220` (proxy) and `http://host.lima.internal:8221` (dashboard / CA distribution endpoint).

The gateway binds to `127.0.0.1` by default. Lima's networking makes `host.lima.internal` resolve to the host's loopback-reachable interface, so the default binding works without changes.

## One-shot provisioning

Run these inside the sandbox via `sb shell --`:

```bash
# 1. Fetch and trust the CA.
sb shell -- bash -c '
  curl -fsS http://host.lima.internal:8221/ca.pem |
    sudo tee /usr/local/share/ca-certificates/agent-gateway.crt > /dev/null
  sudo update-ca-certificates
'

# 2. Export proxy env vars for the current shell.
sb shell -- bash -c '
  export HTTPS_PROXY=http://x:agw_YOUR_TOKEN@host.lima.internal:8220
  export HTTP_PROXY=http://x:agw_YOUR_TOKEN@host.lima.internal:8220
  curl https://api.github.com/  # flows through the gateway
'
```

The `x:` username is a convention — HTTP clients require a username when a password is present. The token is the password.

## Persisting proxy env vars

To make the proxy env vars survive across shells, append them to the sandbox user's shell profile. This is a one-time step per sandbox (per token):

```bash
sb shell -- bash -c '
  cat >> ~/.bashrc <<EOF
export HTTPS_PROXY=http://x:agw_YOUR_TOKEN@host.lima.internal:8220
export HTTP_PROXY=http://x:agw_YOUR_TOKEN@host.lima.internal:8220
export NO_PROXY=localhost,127.0.0.1,host.lima.internal
EOF
'
```

`NO_PROXY` avoids sending loopback traffic through the gateway, including the CA-fetch request itself.

## Automating with `sb` provisioning scripts

`sb` supports user-supplied provisioning scripts that run after `sb create` or `sb provision`. A typical setup puts CA trust and proxy env vars into a provisioning script so every `sb create` produces a sandbox that's gateway-ready:

```bash
# ~/.config/sb/provision/10-agent-gateway.sh
#!/bin/bash
set -euo pipefail

# Fail fast if the gateway isn't running on the host.
if ! curl -fsS http://host.lima.internal:8221/ca.pem -o /tmp/agent-gateway-ca.pem; then
  echo "agent-gateway unreachable on host; skipping CA trust" >&2
  exit 0
fi

sudo mv /tmp/agent-gateway-ca.pem /usr/local/share/ca-certificates/agent-gateway.crt
sudo update-ca-certificates

# Proxy env vars. Token must be provisioned separately (see below).
cat > /etc/profile.d/agent-gateway.sh <<'EOF'
if [[ -f "$HOME/.agent-gateway-token" ]]; then
  token=$(cat "$HOME/.agent-gateway-token")
  export HTTPS_PROXY="http://x:${token}@host.lima.internal:8220"
  export HTTP_PROXY="http://x:${token}@host.lima.internal:8220"
  export NO_PROXY="localhost,127.0.0.1,host.lima.internal"
fi
EOF
```

Configure `sb` to copy the token file into the sandbox during provisioning (consult `sb config` for the exact config key — typically a `files` entry mapping `~/.config/sb/agent-gateway-token` on the host to `~/.agent-gateway-token` in the sandbox).

Keeping the token out of the provisioning script itself lets you rotate the agent token (`agent-gateway agent rotate <name>`) and re-provision (`sb provision`) without editing committed scripts.

## Rotating credentials

When the agent token is rotated on the host:

```bash
# On the host:
agent-gateway agent rotate my-agent | awk '/^token:/ {print $2}' > ~/.config/sb/agent-gateway-token
sb provision
```

The new token file is copied into the sandbox during provisioning; the next shell picks it up from `/etc/profile.d/agent-gateway.sh`.

When the CA is rotated on the host (`agent-gateway ca rotate`) the sandbox must re-trust. Re-running the provisioning script above is sufficient — `update-ca-certificates` replaces the old root.

## Verifying the integration

From inside the sandbox:

```bash
sb shell -- bash -c '
  # Proxy env vars visible?
  env | grep -i proxy

  # CA trusted?
  openssl verify -CAfile /etc/ssl/certs/agent-gateway.pem \
    <(openssl s_client -connect api.github.com:443 -servername api.github.com </dev/null 2>/dev/null)

  # End-to-end — should show up on the dashboard live feed.
  curl -s https://api.github.com/zen
'
```

Then check the dashboard at `http://127.0.0.1:8221/dashboard/` on the host — the request should appear on the live feed with the sandbox's agent name attached.

## Non-cooperative sandbox

The sections above assume a cooperative sandbox: one configured to route its HTTP(S) traffic through the gateway voluntarily via `HTTPS_PROXY` / `HTTP_PROXY`. A sandbox that doesn't set those env vars (or whose agent process bypasses them) can still reach the internet directly.

For stronger containment, pin the sandbox's egress at the kernel level using iptables. The rules below accept traffic to `host.lima.internal:8220` (the gateway's proxy port) and DNS (required for name resolution, including resolving `host.lima.internal` itself), and reject everything else.

### How Lima exposes the host

Lima runs its guest kernel with a user-mode network stack. `host.lima.internal` is a stable hostname that always resolves to the host's loopback-reachable interface — typically `192.168.5.2` on the default Lima network (`192.168.5.0/24`). Verify the address inside the sandbox with:

```bash
getent hosts host.lima.internal
```

Substitute that address for `HOST_ADDR` in the rules below.

### iptables rules

Run inside the Lima VM (for example via `sb shell --`) as root. Replace `192.168.5.2` with the actual address of `host.lima.internal` if it differs.

```bash
# Variables — adjust if your Lima network differs.
HOST_ADDR=192.168.5.2   # host.lima.internal
PROXY_PORT=8220

# Flush existing OUTPUT rules (review first if other rules are present).
iptables -F OUTPUT

# Allow loopback so processes communicating via 127.0.0.1 are unaffected.
iptables -A OUTPUT -o lo -j ACCEPT

# Allow already-established and related traffic (stateful accept).
iptables -A OUTPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT

# Allow DNS (UDP and TCP) — required for name resolution, including
# resolving host.lima.internal itself before the gateway connection.
iptables -A OUTPUT -p udp --dport 53 -j ACCEPT
iptables -A OUTPUT -p tcp --dport 53 -j ACCEPT

# Allow egress to the gateway proxy port only.
iptables -A OUTPUT -p tcp -d "$HOST_ADDR" --dport "$PROXY_PORT" -j ACCEPT

# Reject everything else (ICMP unreachable, so failures are fast).
iptables -A OUTPUT -j REJECT --reject-with icmp-net-unreachable
```

DNS is allowed so that the sandbox can resolve hostnames before handing them off to the gateway. Without DNS, tools that resolve before connecting (including curl and most HTTP clients) fail before they can use the proxy. The gateway performs its own independent DNS resolution on the host side, so allowing sandbox DNS does not bypass host-side policy.

After applying these rules, any process inside the sandbox that attempts a direct TCP connection to any destination other than `host.lima.internal:8220` receives an immediate ICMP unreachable. Traffic through the gateway is unaffected.

### Persisting across reboots

iptables rules are not persistent by default. To restore them on boot, install `iptables-persistent` and save:

```bash
apt-get install -y iptables-persistent
netfilter-persistent save
```

Alternatively, add the `iptables` commands to the `sb` provisioning script (see [Automating with `sb` provisioning scripts](#automating-with-sb-provisioning-scripts)) so they run every time a sandbox is created or re-provisioned.

### Verifying containment

From inside the sandbox, direct egress should be rejected immediately:

```bash
# Direct TCP to a public host — should be rejected.
curl --max-time 5 https://api.github.com/zen
# Expected: curl: (7) Failed to connect ... Connection refused / Network unreachable

# Traffic routed through the gateway — should succeed (if a valid token is set).
HTTPS_PROXY=http://x:agw_YOUR_TOKEN@host.lima.internal:8220 \
  curl --max-time 10 https://api.github.com/zen
```

---

> **Prior art.** This iptables recipe follows the non-cooperative sandbox hardening pattern from agent-vault, which uses the same approach of accepting only gateway egress and rejecting all other outbound TCP at the kernel level.

---

## Troubleshooting

- **`curl: (60) SSL certificate problem`** — the CA was not installed, or `update-ca-certificates` was not re-run after install. Verify `/etc/ssl/certs/agent-gateway.pem` exists and is a symlink to your installed cert.
- **`HTTP/1.1 407 Proxy Authentication Required`** — no token, wrong token, or the token has been rotated/revoked on the host. Re-fetch `~/.agent-gateway-token` via `sb provision`.
- **`curl: (7) Failed to connect to host.lima.internal`** — the gateway is not running on the host, or is bound to an interface other than the default. Check `agent-gateway serve` on the host.
- **Traffic doesn't appear on the dashboard** — confirm the request actually went through the proxy (check `env | grep -i proxy` inside the sandbox). Some tools bypass `HTTPS_PROXY` env vars; see the [README](../README.md#limitations) for the known set.
