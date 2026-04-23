# agent-gateway

Host-native HTTP/HTTPS proxy for sandboxed AI agents. Sandboxes receive dummy credentials and point `HTTPS_PROXY` at the gateway; rules match outgoing requests and swap in real credentials at request time.

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

Run `make audit` before committing. Integration tests use `//go:build integration`. E2E tests use `//go:build e2e` and live in `test/e2e/`. They build and run the real binary as a subprocess.

## Architecture

Single binary, two ports. `:8220` for the proxy (HTTP CONNECT + plain HTTP); `:8221` for the dashboard SPA, `/ca.pem`, and SSE feed. Both bound to `127.0.0.1` by default.

Pipeline (HTTPS MITM path): CONNECT → agent auth → intercept decision → TLS handshake → rule match → verdict dispatch (allow / deny / require-approval) → inject credentials → upstream dial → stream response → audit.

```
cmd/agent-gateway/      CLI entry point (Cobra): serve, agent {add,list,rm,rotate},
                        secret {add,list,update,rm},
                        rules {check,reload}, admin-token rotate,
                        master-key rotate, ca {export,rotate},
                        config {path,edit,refresh}

internal/proxy/         MITM HTTP/HTTPS proxy — CONNECT handler, per-host
                        *tls.Config cache, ALPN (h1+h2), body buffering,
                        pipeline (rules → inject → upstream → audit),
                        ConnectDecision filter (DecisionTunnel / DecisionMITM /
                        DecisionReject)
internal/ca/            Root CA load/generate; leaf issuance (24h, 1h refresh
                        buffer); shared sync.Map cert cache
internal/rules/         HCL directory loader (filepath.Glob), first-match-wins
                        ordered evaluation, hot reload via SIGHUP
internal/inject/        Header verbs (replace_header, remove_header),
                        ${secrets.X} / ${agent.X} template expansion
internal/secrets/       SQLite-backed AES-256-GCM store, master key via
                        go-keyring (file fallback when keychain unavailable)
internal/audit/         SQLite WAL logger, metadata-only rows (no request
                        bodies), indexed by (agent, host, ts) and
                        (matched_rule, ts); 90-day prune on startup
internal/agents/        Agent registry — name, token_hash, prefix, last_seen;
                        last_seen_at written through on every authenticated request
internal/approval/      In-memory Broker with configurable MaxPending cap and
                        Timeout; OnEvent callback for SSE fan-out; dashboard is
                        the only approver in v1
internal/dashboard/     Embedded SPA (vanilla JS + SSE): live feed, audit,
                        rules, agents, secrets, inline approve/deny
internal/config/        XDG-aware HCL config (config.hcl), default backfill on load
internal/store/         Single SQLite file, WAL mode, shared across audit /
                        agents / secrets; migrations via embedded SQL
internal/daemon/        PID file write/read/delete for CLI→daemon SIGHUP signalling
internal/paths/         XDG-conformant path helpers (config, data, runtime dirs)
```

### CONNECT decision filter

`internal/proxy.Decide` (in `decide.go`) maps (agent auth result, target host, `no_intercept_hosts` list) → `ConnectDecision`:

- `DecisionReject` — no valid agent token in `Proxy-Authorization`
- `DecisionTunnel` — host matches `no_intercept_hosts` glob, or host is an IP literal, or no agent-scoped rule targets this host
- `DecisionMITM` — all other MITM-eligible connections

Tunnel rows are audited with `interception='tunnel'`, bytes in/out, and duration only (no method/path/headers visible at CONNECT time).

### ApprovalGuard pattern

`internal/approval.Broker.Request` parks the in-flight `proxy.ApprovalRequest` in an in-memory map, fires the `OnEvent` callback (which the dashboard SSE handler uses to push a card), then blocks on a per-request channel until `Broker.Decide` is called, the context is cancelled, or `Timeout` elapses. The calling goroutine has already buffered the request body via `internal/proxy.bufferBody` (capped by `proxy_behavior.body_peek_bytes`) so the upstream can still receive all original bytes once injection runs; on decision the goroutine either proceeds with injection or synthesises a `403` / `504`.

### CLI → daemon coordination

State-mutating CLI commands write to SQLite (with `busy_timeout=5s`), then read the PID file and send `SIGHUP`. The daemon's `SIGHUP` handler performs a coarse reload: re-parse `rules.d/`, invalidate the decrypted-secret LRU, re-run secret-coverage warnings, reload the agent registry from SQLite, reload the admin token file, and reload the CA (clearing the leaf cache). `config.hcl` is **not** re-parsed — edits to `timeouts.*`, `proxy_behavior.*`, `approval.*`, `secrets.cache_ttl`, `audit.retention_days`, and listener addresses require a daemon restart. In-flight requests finish on the pre-reload `atomic.Pointer` snapshot. If no daemon is running, the CLI write is a no-op beyond the DB change — the daemon picks up the new state on next start. Before signalling, the CLI verifies the PID's comm name to guard against PID reuse.

## Conventions

- Errors are wrapped with context: `fmt.Errorf("doing X: %w", err)`
- Audit write errors are intentionally discarded (`_ =`) — the pipeline must not fail because audit failed
- `log/slog` throughout; pass `*slog.Logger` as an explicit parameter (nil-checked in packages that can be constructed without one)
- SQLite driver: `ncruces/go-sqlite3` (WASM, no CGO); always import `_ "github.com/ncruces/go-sqlite3/driver"` alongside `_ "github.com/ncruces/go-sqlite3/embed"`; WAL mode enabled on `store.Open`
- Master key: `go-keyring` (service: `agent-gateway`, account: `master-key-<id>`); file fallback writes to `$XDG_CONFIG_HOME/agent-gateway/master-key-<id>` with `0o600`; warn via slog when falling back. The active id is tracked in the SQLite `meta` table (`active_key_id`); rotation generates a new id, persists the new key BEFORE committing the re-encryption transaction, then deletes the previous id best-effort. A pre-versioning `master-key` keychain account / `master.key` file is migrated to id=1 on first load.
- Config file: HCL (not JSON); path `$XDG_CONFIG_HOME/agent-gateway/config.hcl`; file `0o600`, parent dir `0o750`
- Admin token: 32 random bytes, hex-encoded (64 chars); stored at `$XDG_CONFIG_HOME/agent-gateway/admin-token` with `0o600`; token comparison uses `crypto/subtle.ConstantTimeCompare`
- Agent tokens: `agw_` prefix + 32 random bytes base62-encoded (47 chars total); stored as argon2id hashes with per-agent salt in SQLite
- Rule files: HCL in `$XDG_CONFIG_HOME/agent-gateway/rules.d/`; loaded with `filepath.Glob("*.hcl")` in filename order; `rules check` validates syntax only (no daemon needed)
- `${secrets.X}` and `${agent.X}` expansion happens in `internal/inject` at injection time, not at rule-load time — secrets are always the current live value
- Leaf certs: issued per MITM'd host, 24h validity, regenerated 1h before expiry, cached in `sync.Map`; key is the hostname
- Body matching: `json_body` uses JSON-path, `form_body` and `text_body` use regex; body is buffered up to a configurable cap (default 1 MB) and re-joined to the upstream request stream
- `no_intercept_hosts` in config accepts glob patterns (same `filepath.Match` syntax as rule host fields)
- Dashboard SSE feed: drop-on-full ring buffer (mcp-broker pattern); paginated `/api/audit` covers "what happened while disconnected"
- `internal/proxy.ConnectDecision` constants: `DecisionTunnel` (0), `DecisionMITM` (1), `DecisionReject` (2)
- `internal/approval.ErrQueueFull` is returned synchronously (no block) when `MaxPending` is reached; `ErrUnknownID` is returned by `Decide` for already-resolved or never-created IDs
- PID file at `$XDG_CONFIG_HOME/agent-gateway/agent-gateway.pid`; written on `serve` start, deleted on clean shutdown

## Documentation

User-facing surface changes must be reflected in the corresponding doc. When changing code, update the matching doc in the same change:

- CLI commands, flags, exit codes → `docs/cli.md`
- Rule HCL syntax, matchers, verdicts, `inject` verbs, template expansion, reload semantics → `docs/rules.md`
- Sandbox integration steps, provisioning flow, networking assumptions → `docs/sandbox-manager.md`
- Architecture, package layout, request lifecycle, audit schema, open questions → `DESIGN.md` (and the package list above in this file)
- Install, quickstart, concepts, limitations, links → `README.md`

If a change doesn't fit any of the above, it doesn't need a doc update. Err on the side of updating — drift across the doc set is the failure mode we're guarding against.
