# http-broker

MITM HTTP/HTTPS forward proxy that injects credentials for sandboxed agents.

## Development

```bash
make build              # go build -o http-broker ./cmd/http-broker
make install            # go install ./cmd/http-broker
make test               # go test -race ./...
make test-e2e           # go test -race -tags=e2e -timeout=60s ./test/e2e/...
make lint               # go tool golangci-lint run ./...
make audit              # tidy + fmt + lint + test + govulncheck
```

Run `make audit` before committing. E2E tests use `//go:build e2e`, live in
`test/e2e/`, and drive the real binary as a subprocess.

## Architecture

Two loopback listeners: `:8220` for the proxy, `:8221` for the dashboard.
Separate ports on purpose — sharing would make the dashboard reachable
_through_ the proxy.

```
cmd/http-broker/     Cobra CLI; serve.go is the composition root
internal/
  paths/               XDG config/data split
  config/              config.json + rules-loading orchestration
  rules/               rule schema, validation, matching engine
  glob/                the ONE glob-to-regexp translation
  hostnorm/            host and host-glob canonicalisation
  hostmatch/           public-suffix detection
  netguard/            SSRF guard shared by both dial paths
  ca/                  root CA and leaf issuance
  credentials/         resolution and host binding
  auth/                token storage + the two auth checks
  proxy/               CONNECT, tunnel relay, MITM, request pipeline
  audit/               SQLite store
  dashboard/           embedded read-only UI
```

Dependency flow is one-way: `proxy` depends on `rules`, `credentials`, `ca`,
`netguard`, `auth`; `audit` adapts to `proxy`'s own `AuditSink` interface, so
`proxy` never imports `audit`.

## Invariants

These are load-bearing. Changing one needs a matching change to `DESIGN.md`.

- **`internal/glob` is the only glob-to-regexp translation.** Rule matching and
  credential host-scope matching must never diverge. The predecessor shipped
  three copies and documented the drift risk in a code comment.
- **`glob.ToRegexp` walks by rune, not by byte.** Quoting a raw byte of a
  multi-byte UTF-8 sequence re-encodes it, producing a valid regexp that never
  matches — a deny rule that loads cleanly and silently never fires.
- **Both dial paths go through `netguard`.** The tunnel relay and the MITM
  transport. The predecessor guarded only its transport and called a bare
  `net.Dial` on the tunnel, leaving cloud metadata reachable. A rule's
  `allow_private` grant must reach both, or one path refuses what the other
  permits.
- **`allow_private` relaxes one address class and is decided at CONNECT.** RFC
  1918 and RFC 4193 only; loopback, link-local, multicast, unspecified,
  reserved and IMDS addresses stay refused, and `checkAddr` still recurses into
  embedded IPv4 with the grant applied. It is taken over _every_ rule matching
  the host and port, not from the rule that won the action decision, because
  `http.Transport` pools connections by host and port — a per-request grant
  would bind only whichever request dialled first. Rules carrying it are
  required to be host-only for the same reason.
- **`ExpandAll` resolves and host-checks every credential before writing any
  header.** A caller that writes headers only from a successful return cannot
  dispatch a partially injected request.
- **Deny beats intercept, and deny beats tunnel among overlapping host-only
  rules.** Rule order must never change enforcement. Deny therefore defaults to
  _any_ port, like intercept: a narrower default silently reinstated the
  ordering dependency off 443. Only `tunnel` defaults to 443.
- **Absolute-form request lines are `http` only.** The scheme is checked before
  anything derives from it. An `https://` request line has an empty
  `URL.Port()`, so treating it as plain HTTP forwarded a credential-injected
  request in cleartext on port 80.
- **The rules engine is taken once per connection.** `mitmHandler` carries the
  snapshot its CONNECT decision was made against, so a SIGHUP mid-connection
  cannot make the connect-time and per-request decisions disagree. The README
  documents what this means for the kill switch.
- **No audit column may carry a body, a header value, or a credential.**
  `TestNoBodyOrHeaderColumns` fails if a field named like one is added.
- **The dashboard is read-only.** Every route is registered `GET <path>`.
  `docs/dashboard.md` is the source of truth the AC-12 sweep parses; add a
  route there or it is never swept.
- **Every authenticated route hangs off `dashboard.Prefix`.** The mux
  registrations, the cookie `Path`, the `dashboardPaths` redirect allowlist,
  the embedded `index.html`/`app.js` URLs and `serve`'s startup URL all derive
  from it. `/healthz` and `/ca.pem` deliberately do not — moving them would
  break liveness probes and provisioning. The root is registered `GET /{$}`,
  not `GET /`: a subtree pattern there would serve the page for every unmatched
  path instead of 404.

## Gotchas

- **`.gitignore` must not match Go source.** A bare `coverage.*` at the repo
  root silently excluded `internal/rules/coverage.go` from a commit; the
  package then failed to compile from a clean checkout while passing locally.
  Verify with `git archive HEAD | tar -x -C <tmp>` and build there.
- **`serveH1` must close its listener.** It hands one connection to an
  `http.Server` through `singleConnListener`; without the `ConnState` hook
  closing it, `Accept` blocks forever, `Serve` never returns, and every
  intercepted HTTP/1.1 connection leaks a goroutine and a TLS connection.
  `internal/proxy/mitm_test.go` guards this.
- **Only the `driver` blank import from `ncruces/go-sqlite3`.** The sibling
  `embed` package is deprecated.
- **`HTTP_BROKER_TEST_ALLOW_ADDRS` is test-only.** Every mock upstream listens
  on loopback, which `netguard` refuses by design, so without it no test could
  exercise an allowed path. Exact `host:port` only, environment variable only,
  and `serve` warns about every exemption. Never set it in production.
- **The e2e suite trusts mock upstreams via `SSL_CERT_FILE`**, which Go's
  system cert pool honours — not by weakening the proxy's verification. D12's
  strict upstream TLS ships exactly as tested.
- **`env_credentials` only, in tests.** The suite must never touch a real
  keychain or prompt for access.
- **Audit records need an ID.** CONNECT-path events once had none, so every row
  shared an empty primary key and SQLite kept only the first. The store assigns
  a fallback, but set one at the source.
- **The dashboard URL is only printed to a terminal.** `announceDashboard`
  returns early unless stdout is an `*os.File` that passes
  `term.IsTerminal`, because the URL carries the token and launchd's stdout is a
  log file. Tests pass a buffer, so they take that path — asserting on printed
  output needs a pty, not a `bytes.Buffer`.
- **`golangci-lint` can OOM at default concurrency here.** Use
  `GOGC=50 go tool golangci-lint run --concurrency=2 ./...`.

## Conventions

- Errors wrapped with context: `fmt.Errorf("doing X: %w", err)`.
- Sentinel errors for anything a caller branches on (`netguard.ErrBlocked`,
  `credentials.ErrHostScope`).
- Audit write failures are logged and discarded; the pipeline never fails a
  request because auditing failed.
- Config files `0600`, directories `0750`, `ca.pem` `0644`.
- Cobra commands use `SilenceUsage` and command-specific `Args` functions that
  name the missing argument.
- Ported code carries a header comment naming its origin and every deviation.

## Dashboard UI

**This dashboard is a deliberate copy of `mcp-broker`'s.** The base tokens, type
scale, header, tab bar, filter row, table treatment and badges in
`assets/styles.css` are lifted from
`mcp-broker/internal/dashboard/index.html`'s `<style>` block so the two read as
one product. **Change one, change the other**, and verify by running both and
comparing computed styles rather than by reading the CSS.

Only these differ on purpose:

- **Product accent.** mcp-broker is green/amber; http-broker is cyan/violet
  (`--product-accent #22d3ee`, `--product-accent-2 #a78bfa`). The body's radial
  background tints and the tab focus ring follow the accent.
- **Sticky table headers.** The traffic view streams without bound, so `th` is
  `position: sticky`. Invisible at rest, so it does not break the match.

Sizes are rem, matching mcp-broker: `h1` 1.25rem/700/0.04em, `th`
0.6875rem/600 uppercase, `td` and badges 0.8125rem/0.6875rem mono, status label
0.75rem mono. Do not reintroduce px sizing or a `body { font-size }`.

`favicon.svg` is the header logo too — the header `<img>` points at it, so they
cannot drift. Keep it in the shared family: a `#0b111a` 64x64 tile with `rx=14`
and an accent-stroked glyph at roughly 4.5 stroke width filling most of the box.

Values from the audit log are attacker-influenced: render them with
`textContent`, never `innerHTML`.
