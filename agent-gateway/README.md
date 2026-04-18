# agent-gateway

A host-native HTTP/HTTPS proxy for sandboxed AI agents. Sandboxes receive dummy credentials and point `HTTPS_PROXY` at the gateway. The gateway matches outgoing requests against declarative rules, swaps dummy credentials for real ones at request time, and logs every intercepted request to SQLite. Sensitive requests can be held for human approval via an embedded web dashboard.

## How it works

```
Agent ─HTTPS_PROXY─▶ agent-gateway ─▶ upstream API
                          │
                          ├─ MITM TLS (local root CA, ALPN h1+h2)
                          ├─ HCL rules (host / method / path / header / body)
                          ├─ Credential injection (set_header / remove_header)
                          ├─ Human approval via web dashboard
                          └─ SQLite audit log
```

Every HTTPS request from the sandbox flows through a CONNECT tunnel. The gateway either relays the connection as a raw TCP tunnel (no interception) or performs TLS MITM using a locally-issued leaf certificate. For MITM'd hosts, rules are evaluated in filename × line order; the first match wins. Three outcomes are possible: `allow` (inject credentials and forward), `deny` (return 403), or `require-approval` (block until a human approves via the dashboard). Unmatched requests on MITM'd hosts pass through untouched — the dummy credential reaches the upstream and fails there, which is the safe default.

## Install

```bash
go install github.com/averycrespi/agent-tools/agent-gateway/cmd/agent-gateway@latest
```

Or build from source:

```bash
git clone https://github.com/averycrespi/agent-tools
cd agent-tools/agent-gateway
make build    # builds ./agent-gateway
make install  # installs to $GOPATH/bin
```

Requires Go 1.25+.

**CA trust:** The gateway generates a root CA on first run. Every sandbox must trust this CA for HTTPS interception to work. Export the CA certificate and install it:

```bash
# Export the CA certificate
agent-gateway ca export > /tmp/agent-gateway-ca.pem

# macOS host
sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain /tmp/agent-gateway-ca.pem

# Lima / Ubuntu sandbox — run inside the sandbox
sudo cp /tmp/agent-gateway-ca.pem /usr/local/share/ca-certificates/agent-gateway.crt
sudo update-ca-certificates
```

The gateway also serves the CA certificate at `http://localhost:8221/ca.pem` for easy sandbox retrieval.

## First run

```bash
# 1. Start the gateway (creates default config on first run)
agent-gateway serve

# The dashboard URL (with admin token) is printed to stderr on startup:
#   Dashboard: http://127.0.0.1:8221/dashboard/?token=<admin-token>

# 2. Create an agent token (in a second terminal)
agent-gateway agent add myagent
# Prints: agw_a1b2c3...

# 3. Add a secret
agent-gateway secret set gh_token
# Prompts for the value; stored encrypted in SQLite

# 4. Write a rule file
mkdir -p ~/.config/agent-gateway/rules.d
cat > ~/.config/agent-gateway/rules.d/github.hcl <<'EOF'
rule "github-issues" {
  host   = "api.github.com"
  method = ["POST"]
  path   = "/repos/*/issues"

  inject {
    set_header "Authorization" "Bearer ${secrets.gh_token}"
    remove_header "Authorization"  # strips the dummy credential first
  }
}
EOF

# 5. Reload rules without restarting
agent-gateway rules reload

# 6. Trust the CA in the sandbox (Lima example)
agent-gateway ca export | limactl shell default sudo tee /usr/local/share/ca-certificates/agent-gateway.crt
limactl shell default sudo update-ca-certificates

# 7. Set the proxy env vars in the sandbox
#    Replace <host-ip> with the gateway's address from the sandbox's perspective
limactl shell default -- bash -c '
  export HTTPS_PROXY=http://x:agw_a1b2c3...@<host-ip>:8220
  export HTTP_PROXY=http://x:agw_a1b2c3...@<host-ip>:8220
  gh issue create --repo owner/repo --title "Test" --body "Hello"
'
```

## Concepts

**Agents** are sandbox identities. Each agent has a name and a token (`agw_…`). The token travels in the proxy URL's userinfo (`http://x:<token>@host:8220`) and is sent as `Proxy-Authorization: Basic` on every CONNECT request. Rules and secrets can be scoped to specific agents.

**Rules** are HCL files in the rules directory (`~/.config/agent-gateway/rules.d/`). Each rule matches on any combination of host, method, path, headers, and body fields. Three verdicts: `allow` (with optional `inject` block), `deny`, `require-approval`. Rules reload hot — edit a file and run `agent-gateway rules reload`; invalid edits leave the previous rule-set live.

**Secrets** are named values stored encrypted in SQLite (`AES-256-GCM`). The master key is held in the OS keychain (`go-keyring`) with a file fallback. Secrets are referenced in rules as `${secrets.<name>}`. Rotating a secret takes effect on the next request with no restart.

**CA** — The gateway generates a root CA on first run and stores it at `~/.local/share/agent-gateway/ca.pem`. Leaf certificates for intercepted hosts are issued on demand (24-hour validity, regenerated 1 hour before expiry) and cached in memory. The agent token prefix is injected into cert SANs for per-agent attribution.

**Dashboard** — The embedded web UI runs at `:8221`. It shows a live SSE feed of intercepted requests, the audit log, configured rules, registered agents, and stored secrets (names only — values are never shown). Pending `require-approval` requests appear inline with approve/deny buttons.

## Architecture

See [`.designs/2026-04-16-agent-gateway.md`](../.designs/2026-04-16-agent-gateway.md) for the full design document.

```
cmd/agent-gateway/      CLI entry point (Cobra): serve, agent, secret, rules,
                        token, ca, config subcommands

internal/proxy/         MITM HTTP/HTTPS proxy — CONNECT handler, per-host
                        *tls.Config cache, ALPN (h1+h2), body buffering,
                        pipeline (rules → inject → upstream → audit)
internal/ca/            Root CA load/generate; leaf issuance (24h, 1h refresh buffer)
internal/rules/         HCL directory loader, first-match-wins matcher,
                        hot reload via SIGHUP
internal/inject/        Header verbs (set_header, remove_header),
                        ${secrets.X} / ${agent.X} template expansion
internal/secrets/       SQLite-backed AES-256-GCM store, master key via
                        go-keyring (file fallback)
internal/audit/         SQLite WAL logger, metadata-only rows
internal/agents/        Agent registry — name, token_hash, prefix, last_seen
internal/approval/      In-memory pending-request store with timeout,
                        dashboard is the only approver in v1
internal/dashboard/     Embedded SPA (vanilla JS + SSE): live feed, audit,
                        rules, agents, secrets, approve/deny
internal/config/        XDG-aware HCL config, default backfill on load
internal/store/         Single-file SQLite DB, WAL mode, migrations
internal/daemon/        PID file management for CLI→daemon signalling
```

Ports:

- **`:8220`** — proxy (HTTP CONNECT + plain HTTP), bound `127.0.0.1`
- **`:8221`** — dashboard SPA + `/ca.pem` + SSE, bound `127.0.0.1`

## CLI

```
agent-gateway serve                       Start the gateway
agent-gateway serve -v                    Enable debug logging

agent-gateway agent add <name>            Create an agent token
agent-gateway agent list                  List agents
agent-gateway agent rm <name>             Remove an agent
agent-gateway agent rotate <name>         Rotate an agent token
agent-gateway agent show <name>           Show agent details

agent-gateway secret set <name>           Store a secret (prompts for value)
agent-gateway secret list                 List secret names
agent-gateway secret rotate <name>        Re-encrypt a secret with a new value
agent-gateway secret rm <name>            Remove a secret
agent-gateway secret master rotate        Rotate the master encryption key
agent-gateway secret export <name>        Print a secret value (use with care)

agent-gateway rules check                 Validate rule files (syntax only)
agent-gateway rules reload                Signal the daemon to reload rules

agent-gateway token rotate admin          Rotate the admin dashboard token

agent-gateway ca export                   Print the CA certificate to stdout
agent-gateway ca rotate                   Generate a new root CA

agent-gateway config path                 Print config file path
agent-gateway config edit                 Open config in $EDITOR
agent-gateway config refresh              Backfill new defaults into config
```

## Development

```bash
make build              # go build -o agent-gateway ./cmd/agent-gateway
make install            # go install ./cmd/agent-gateway
make test               # go test -race ./...
make test-integration   # go test -race -tags=integration ./...
make test-e2e           # go test -race -tags=e2e -timeout=120s ./test/e2e/...
make lint               # go tool golangci-lint run ./...
make fmt                # go tool goimports -w .
make tidy               # go mod tidy && go mod verify
make audit              # tidy + fmt + lint + test + govulncheck
```

Run `make audit` before committing. Integration tests use `//go:build integration`. E2E tests use `//go:build e2e` and live in `test/e2e/`.

## Prior art

`agent-gateway` is architecturally inspired by [onecli](https://github.com/onecli/onecli), an HTTP proxy that injects credentials into requests from sandboxed agents. The core match-and-swap concept and the `Proxy-Authorization` userinfo convention (`http://x:<token>@host:port`) both originate there.

`agent-gateway` extends the model with HCL rules, content-type-aware body matching, a human-approval flow, a live SSE audit dashboard, and per-agent identity. It is a clean-room Go reimplementation; no code is shared. See `NOTICE` for attribution.
