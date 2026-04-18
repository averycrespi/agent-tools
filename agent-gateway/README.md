# agent-gateway

A host-native HTTP/HTTPS proxy for sandboxed AI agents. Sandboxes receive dummy credentials and point `HTTPS_PROXY` at the gateway. The gateway matches outgoing requests against declarative rules, swaps dummy credentials for real ones at request time, and logs every intercepted request to SQLite. Sensitive requests can be held for human approval via an embedded web dashboard.

## How it works

```
Sandbox ─HTTPS_PROXY─▶ agent-gateway ─▶ upstream API
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

## Quickstart

agent-gateway is designed to run on the host while one or more sandboxed agents point at it. These steps show the generic Linux-sandbox flow. For `sandbox-manager` / Lima specifics, see [docs/sandbox-manager.md](./docs/sandbox-manager.md).

### 1. Start the gateway

```bash
agent-gateway serve
```

The dashboard URL (with admin token) is printed once on first run:

```
Dashboard: http://127.0.0.1:8221/dashboard/?token=<admin-token>
```

Ports bind to `127.0.0.1` by default: `:8220` for the proxy, `:8221` for the dashboard.

### 2. Create an agent token

```bash
agent-gateway agent add my-agent
# token: agw_a1b2c3…
# HTTPS_PROXY=http://x:agw_a1b2c3…@127.0.0.1:8220
# HTTP_PROXY=http://x:agw_a1b2c3…@127.0.0.1:8220
```

### 3. Store a secret

Values are read from stdin — `secret set` refuses a TTY to keep values out of shell history:

```bash
echo -n "ghp_…" | agent-gateway secret set gh_token
```

### 4. Write a rule

```bash
mkdir -p ~/.config/agent-gateway/rules.d
cat > ~/.config/agent-gateway/rules.d/github.hcl <<'EOF'
rule "github-issues" {
  match {
    host   = "api.github.com"
    method = "POST"
    path   = "/repos/*/*/issues"
  }

  verdict = "allow"

  inject {
    set_header = {
      "Authorization" = "Bearer ${secrets.gh_token}"
    }
  }
}
EOF

agent-gateway rules reload
```

See [docs/rules.md](./docs/rules.md) for full rule syntax.

### 5. Trust the CA inside the sandbox

From inside your sandbox (any Linux environment with access to the host):

```bash
# Replace <gateway-host> with the gateway's address from the sandbox's perspective.
curl -fsS http://<gateway-host>:8221/ca.pem | \
  sudo tee /usr/local/share/ca-certificates/agent-gateway.crt > /dev/null
sudo update-ca-certificates
```

### 6. Point the sandbox at the gateway

Inside the sandbox, export the proxy env vars from step 2 (substituting `<gateway-host>` for the address reachable from the sandbox):

```bash
export HTTPS_PROXY=http://x:agw_a1b2c3…@<gateway-host>:8220
export HTTP_PROXY=http://x:agw_a1b2c3…@<gateway-host>:8220
export NO_PROXY=localhost,127.0.0.1

# Traffic now flows through the gateway.
gh issue create --repo owner/repo --title "Test" --body "Hello"
```

The request appears on the dashboard live feed with the rule match, injection status, and outcome.

## Concepts

**Agents** are sandbox identities. Each agent has a name and a token (`agw_…`). The token travels in the proxy URL's userinfo (`http://x:<token>@host:8220`) and is sent as `Proxy-Authorization: Basic` on every CONNECT request. Rules and secrets can be scoped to specific agents.

**Rules** are HCL files in `~/.config/agent-gateway/rules.d/`. Each rule matches on any combination of host, method, path, headers, and body fields. Three verdicts: `allow` (with optional `inject` block), `deny`, `require-approval`. Rules reload hot — edit a file and run `agent-gateway rules reload`; invalid edits leave the previous rule-set live. See [docs/rules.md](./docs/rules.md).

**Secrets** are named values stored encrypted in SQLite (AES-256-GCM). The master key is held in the OS keychain (`go-keyring`) with a file fallback. Secrets are referenced in rules as `${secrets.<name>}`. Rotating a secret takes effect on the next request with no restart.

**CA** — The gateway generates a root CA on first run and stores it at `~/.local/share/agent-gateway/ca.pem`. Leaf certificates for intercepted hosts are issued on demand (24-hour validity, regenerated 1 hour before expiry) and cached in memory. Every sandbox must trust this CA for HTTPS interception to work — the gateway serves it unauthenticated at `http://127.0.0.1:8221/ca.pem`.

**Dashboard** — The embedded web UI runs at `:8221`. It shows a live SSE feed of intercepted requests, the audit log, configured rules, registered agents, and stored secrets (names only — values are never shown). Pending `require-approval` requests appear inline with approve/deny buttons.

## Documentation

- [docs/rules.md](./docs/rules.md) — Rule syntax, matchers, verdicts, injection, template expansion, reload semantics.
- [docs/cli.md](./docs/cli.md) — Every CLI command, flag, and exit code.
- [docs/sandbox-manager.md](./docs/sandbox-manager.md) — Integrating with `sandbox-manager` (Lima-based sandboxes).
- [DESIGN.md](./DESIGN.md) — Full design document: request lifecycle, TLS MITM mechanics, audit schema, open questions.

## Limitations

- **Tools that bypass proxy env vars escape the gateway.** A small number of tools ignore `HTTPS_PROXY` (some Go binaries with `net/http.Transport{Proxy: nil}`, pinned mobile SDKs). Those requests leave the sandbox directly and are not visible to the gateway. iptables-level interception is a possible future extension.
- **Pinned clients reject the MITM certificate.** Upstreams that pin TLS fingerprints will refuse the gateway's leaf cert. Add the hostname to `proxy_behavior.no_intercept_hosts` in `config.hcl` to force pass-through for those hosts.
- **Headless Linux may lack an OS keychain.** If `go-keyring` can't reach a Secret Service daemon, the master key falls back to `~/.config/agent-gateway/master.key` with mode `0600`. A loud startup warning flags this.

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

`agent-gateway` extends the model with HCL rules, content-type-aware body matching, a human-approval flow, a live SSE audit dashboard, and per-agent identity. It is a clean-room Go reimplementation; no code is shared. See [DESIGN.md §10](./DESIGN.md) for the full attribution.
