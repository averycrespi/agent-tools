# agent-gateway — Security Audit

**Scope:** Full review of `agent-gateway` as of commit on branch `agent-gateway` (2026-04-20). Audit covers cryptography, secret storage, authentication, the MITM proxy pipeline, rule engine, injection, dashboard, audit log, file/IPC surface, and configuration defaults.

**Methodology:** Static source review of every package under `internal/` plus the `cmd/` entry points and the default HCL config. Findings were cross-checked against actual code (file:line citations below) — subagent-produced drafts that did not survive verification were dropped.

**Threat model assumed.** The gateway runs as a host daemon. Agents are sandboxed, semi-trusted processes that authenticate with a `agw_` token and talk through the proxy. The dashboard is admin UI behind an admin-token gate, intended to run on loopback. Real credentials live on the host and are injected at request time; agents see only dummy creds. Attackers of concern:

- **A1 — A compromised sandbox agent** (malicious or subverted). Can craft arbitrary HTTP traffic through `HTTPS_PROXY`, submit malformed CONNECT targets, send pathological bodies, and attempt to leak real credentials or fingerprint other agents.
- **A2 — A non-root local user on the host**. Can read anything with permissive file modes; cannot escalate directly but can read the SQLite audit log or config if perms allow.
- **A3 — An attacker who reaches the proxy/dashboard ports over the network** (LAN, port-forwarding, misconfigured listen address).
- **A4 — A misconfiguring operator**. Writes a rules file with a catastrophic regex; sets `retention_days` to a huge value; binds to `0.0.0.0`.

---

## Severity legend

- **Critical** — directly enables credential theft, MITM bypass, or remote compromise in the default configuration.
- **High** — enables compromise in plausible deployments or creates a large blast-radius footgun.
- **Medium** — hardening gap; exploitation requires additional prerequisites.
- **Low** — defence-in-depth weakness, best-practice deviation, or narrow edge case.
- **Informational** — note for future work; not currently exploitable.

---

## Summary table

| #   | Severity      | Area       | Issue                                                                                  |
| --- | ------------- | ---------- | -------------------------------------------------------------------------------------- |
| 1   | High          | Crypto     | AES-256-GCM used without AAD — ciphertext-swap confusion on secrets rows               |
| 2   | High          | Auth       | Argon2id parameters far below OWASP minimum (`time=1, memory=64 KiB`)                  |
| 3   | High          | Filesystem | Config/data/rules/key dirs created with `0o750` — group-readable layout leaks presence |
| 4   | High          | Filesystem | SQLite DB, WAL, and SHM files inherit process umask — no explicit `0o600`              |
| 5   | High          | Proxy      | Hostname normalization failure falls back to raw host — rule bypass surface            |
| 6   | High          | Rules      | No bound on rule-file regex complexity — operator can ReDoS the proxy                  |
| 7   | Medium        | Dashboard  | Admin-token cookie `Secure: false` hardcoded with a TODO                               |
| 8   | Medium        | Dashboard  | No response security headers (CSP, X-Frame-Options, X-Content-Type-Options)            |
| 9   | Medium        | Proxy      | Unbounded leaf-cert cache — memory exhaustion via unique CONNECT targets               |
| 10  | Medium        | Injection  | No CRLF/control-char validation on secret values before header write                   |
| 11  | Medium        | Proxy      | Body-buffer hard I/O error forwards a partial body upstream                            |
| 12  | Medium        | Audit      | Raw query string is persisted verbatim — credentials in URLs land in the DB            |
| 13  | Medium        | Crypto     | Root CA valid for 10 years; no rotation enforcement                                    |
| 14  | Medium        | Auth       | Prefix-map lookup short-circuits on miss — timing-distinguishable agent enumeration    |
| 15  | Medium        | Config     | `retention_days`, `max_body_buffer`, `max_pending` have no upper bound                 |
| 16  | Medium        | Config     | Listen address not validated against non-loopback addresses                            |
| 17  | Low           | TLS        | Leaf and upstream TLS floor is TLS 1.2; could require 1.3                              |
| 18  | Low           | Auth       | Admin token never expires; no rotation cadence                                         |
| 19  | Low           | Crypto     | Root CA has `BasicConstraintsValid=true` but no `MaxPathLenZero`                       |
| 20  | Low           | Daemon     | PID-file TOCTOU between stale check and overwrite                                      |
| 21  | Low           | Audit      | `LIMIT`/`OFFSET` interpolated via `fmt.Sprintf` — parameterised is safer               |
| 22  | Low           | Dashboard  | No rate limit on dashboard endpoints or approval broker per-agent                      |
| 23  | Low           | Proxy      | `Secure: false` cookie TODO also present on `handleUnauthorizedPOST`                   |
| 24  | Low           | Proxy      | Tunnel mode relays raw TCP — agent-spoofable `X-Forwarded-For` pass-through            |
| 25  | Informational | Crypto     | Keychain service name not instance-scoped; multiple installs collide                   |
| 26  | Informational | Dashboard  | SSE broker drops events on full per-subscriber buffer (by design, but silent)          |

---

## Findings

### 1. (High) AES-256-GCM used without AAD

**File:** `internal/secrets/crypto.go:31`, `:48`

```go
ciphertext = gcm.Seal(nil, nonce, plaintext, nil)
…
plain, err := gcm.Open(nil, nonce, ciphertext, nil)
```

GCM is called with a `nil` additional-authenticated-data argument. Each secret row is `(name, scope, ciphertext, nonce)`, and the ciphertext is not cryptographically bound to `(name, scope)`. An attacker (or a buggy migration) with DB write access can swap `ciphertext`/`nonce` between rows: a row labelled `gh_bot` can be made to decrypt to the value of `prod_db_pw`, with the master key still verifying integrity. The proxy then injects the wrong real credential into a request it believed was scoped to a different one.

DB write access is a high bar, but the gateway also exposes CLI paths that mutate rows (`secret add/update/rm`); a compromised CLI operator could stage a swap that survives rotation.

**Fix.** Bind the ciphertext to the row identity:

```go
aad := []byte(name + "\x00" + scope)
ct := gcm.Seal(nil, nonce, plaintext, aad)
// on decrypt, pass the same aad; failure → treat as corrupted row
```

Rotate existing ciphertexts in a migration: decrypt without AAD, re-encrypt with AAD.

---

### 2. (High) Argon2id parameters far below OWASP minimum

**File:** `internal/agents/registry.go:26-32`

```go
const (
    argon2Time    = 1
    argon2Memory  = 64 * 1024  // 64 KiB
    argon2Threads = 4
    argon2KeyLen  = 32
)
```

OWASP's 2023 Argon2id guidance is `m=19 MiB, t=2, p=1` as a floor. 64 KiB is roughly **300× weaker** in memory cost. Tokens carry ~189 bits of entropy (finding not a defect in itself), so brute-forcing a raw hash is infeasible, but the weak KDF means:

- Any _specific_ token value can be verified at ~1000–5000 tries/sec/core. Combined with the prefix-lookup leak (finding #14) an attacker who learns a prefix can brute-force the 8-char base62 suffix (~2²⁹ space) in hours on a laptop.
- Disk snapshots or DB exfil carry low per-token work — one compromised backup = recoverable tokens at GPU throughput.

**Fix.** Raise to `time=2, memory=19*1024, threads=1, keyLen=32`. Make the parameters configurable in `config.hcl` under `auth { argon2 { … } }` so they can be tuned per deployment. Existing hashes continue to verify (argon2id is parameter-aware), but plan a re-hash-on-login migration path.

---

### 3. (High) Config/data/key directory permissions are `0o750`

**File:** `cmd/agent-gateway/serve.go:118-126`, `internal/config/config.go:404-410`, `internal/secrets/masterkey.go:203`, `internal/dashboard/auth.go:38, :56`, `internal/store/store.go:17`

All XDG parent directories are created with `0o750`:

```go
os.MkdirAll(paths.ConfigDir(), 0o750)
os.MkdirAll(paths.DataDir(),   0o750)
os.MkdirAll(paths.RulesDir(),  0o750)
```

Individual files are `0o600`, which is correct, but `0o750` on the parent means any user in the owning group can **list the directory**. This leaks:

- Presence of `master-key-N` files (rotation count, indicates whether key rotation was used).
- Existence of `admin-token`, `agent-gateway.pid`, `state.db` and its WAL sidecar, `ca.crt`, `ca.key`, rule filenames.
- Rule file _names_ specifically — these often encode integrations ("github.hcl", "jira-writes.hcl") and reveal which upstreams are in scope.

Combined with finding #4, on a system with a permissive umask the files themselves could be group-readable.

**Fix.** Use `0o700` for every `os.MkdirAll` in the agent-gateway config/data paths. Add a startup self-check: `os.Stat` each critical dir, reject start if mode is more permissive than `0o700` or owner is not the current user.

---

### 4. (High) SQLite DB files inherit process umask

**File:** `internal/store/store.go:16-47`

```go
db, err := sql.Open("sqlite3", path)
```

The SQLite driver creates `state.db`, `state.db-wal`, and `state.db-shm` with mode `0o644` modified by the process umask. The audit log, agent hashes (Argon2 output), and encrypted secret ciphertexts all live in this DB. A permissive umask (`0o022`, the default on many Linux distributions) leaves these files world-readable.

While the master key is in the OS keychain (or an explicitly `0o600` fallback file), anything encrypted with it sits in a potentially world-readable DB. An attacker with local read access can:

- Read the audit log in full (agent names, hosts, rules matched, timestamps, bytes in/out).
- Extract argon2id hashes for offline attack (compounded by finding #2).
- Extract ciphertexts for future attack if the master key ever leaks.

**Fix.** After opening, `os.Chmod(path, 0o600)` on the DB and both sidecar files. Ideally also set the umask for the process at startup to `0o077` so any file creation is owner-only.

---

### 5. (High) Hostname normalization failure falls back to raw host

**File:** `internal/proxy/connect.go:23-34`

```go
func normalizeHostSilently(host string, log …) string {
    out, err := hostnorm.Normalize(host)
    if err != nil {
        log.Debug("hostnorm failed; falling back to raw host", …)
        return host
    }
    return out
}
```

When IDNA validation fails, the raw user-controlled host is used verbatim as:

- The cache key for leaf certificate issuance (`ca.ServerConfig(host)` in `connect.go:127`).
- The rule-matching input (`Decide` in `connect.go:84`, which calls `hostnorm.Normalize` again and silently falls back — `decide.go:144-146`).
- The audit row's `host` field.

The comment argues this is safe because an un-normalizable host "will not match any rule and will fall through to tunnel or reject." That is true for _matching_, but it has two consequences:

1. **Defaulting to tunnel on malformed hosts is a silent weakening of MITM.** Agent sends a CONNECT for an IDN homograph (e.g. `раypal.com` with Cyrillic `а`), IDNA fails, proxy tunnels it as raw TCP to whatever upstream DNS resolves that string to. Tunnel audit row records the mangled host; operator never sees the real destination.
2. **Cache keys diverge from the rule engine's canonical form.** If a later code path assumes `ca.ServerConfig(x)` and `rules.Evaluate(host=x)` agree on `x`, a malformed host breaks that invariant.

**Fix.** Treat normalization failure as a hard CONNECT reject (`write407` or 400). Malformed hostnames should not transit the gateway at all — the fail-open posture here is a trap. If you want operational flexibility, add a config knob `proxy_behavior.allow_unnormalizable_hosts = false` that defaults to the strict behaviour.

---

### 6. (High) Rule regex has no complexity bound — operator ReDoS

**File:** `internal/rules/parse.go:501-534`, evaluated in `internal/rules/body.go`

Header-match and body-match regexes come from operator-authored `*.hcl` files and are compiled with bare `regexp.Compile`. There is no budget, timeout, or complexity check. A rules file containing an expression like `(a+)+$` or `(.*.*)*x` applied against a 1 MiB buffered body will pin a CPU core.

This is technically a "trusted config" surface — but:

- The config is editable by any operator account with CLI access, not just the daemon owner.
- Rules are loaded on every SIGHUP, so a bad regex can be introduced without restarting the daemon or tripping any validation beyond syntax.
- Go's `regexp` uses RE2 which is already linear in input length, but `regexp2`-style catastrophic backtracking is not the only DoS vector — a simple `.*` against a 1 MiB body that gets evaluated across thousands of rules/requests still costs.

**Fix.**

- Use RE2's existing linear guarantee, but cap rule count per request (refuse to evaluate more than N rules per (agent, host) — already implicit via first-match ordering but worth making explicit).
- Add a max pattern length check in `compileRule` (e.g. 4 KiB).
- Wrap body-match evaluation in a per-request wall-clock budget (e.g. 100 ms) using `context.WithTimeout` threaded into `matchRule`/`matchBody`.

---

### 7. (Medium) Admin-token cookie `Secure: false` hardcoded

**File:** `internal/dashboard/auth.go:116-126`, `internal/dashboard/dashboard.go:193-197`

```go
http.SetCookie(w, &http.Cookie{
    …
    // Secure is false for local dev (127.0.0.1 over HTTP).
    // TODO(TLS): set Secure: true when the gateway is served over HTTPS.
    Secure: false,
})
```

Two code paths (query-token exchange and the unauthorized POST handler) both set `Secure: false` with a TODO. If anyone puts the dashboard behind an HTTPS reverse proxy or runs the gateway on a host where HTTPS is ever possible, the admin token cookie is transmittable in the clear.

`HttpOnly: true` + `SameSite: http.SameSiteStrictMode` are set correctly, so CSRF and JS-read attacks are mitigated — the gap is specifically network eavesdropping.

**Fix.**

```go
secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
http.SetCookie(w, &http.Cookie{ …, Secure: secure })
```

And drop the TODOs.

---

### 8. (Medium) No response security headers

**File:** `internal/dashboard/dashboard.go:107-132` (mux wiring), individual handlers

No global middleware sets `Content-Security-Policy`, `X-Content-Type-Options`, `X-Frame-Options`, or `Referrer-Policy`. The SPA correctly uses `textContent` for untrusted data (verified in `app.js`) and the unauthorized form HTML-escapes its dynamic error string (`dashboard.go:214`), so there's no currently-known stored XSS. But:

- Defence-in-depth is absent: a future change that introduces `innerHTML` would not be caught by CSP.
- `<iframe>` embedding is not blocked (SameSite=Strict mitigates but doesn't prevent).
- The dashboard is a trusted admin surface; tightening is cheap.

**Fix.** Add a middleware around `mux` in `Handler()`:

```go
func secureHeaders(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Security-Policy",
            "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; "+
            "img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'")
        w.Header().Set("X-Content-Type-Options", "nosniff")
        w.Header().Set("X-Frame-Options", "DENY")
        w.Header().Set("Referrer-Policy", "no-referrer")
        next.ServeHTTP(w, r)
    })
}
```

Exclude `/ca.pem` from the middleware or allow `application/x-pem-file` through a scoped policy.

---

### 9. (Medium) Leaf-cert cache is unbounded

**File:** `internal/ca/leaf.go:31-59, :82-99`

```go
type leafCache struct {
    m sync.Map
}
```

`sync.Map` with no eviction policy. A malicious agent with valid credentials can open CONNECT to `a1.evil.tld, a2.evil.tld, …, aN.evil.tld` — each unique host generates a new P-256 keypair, certificate, and `tls.Config`, and the entries sit in memory until `sweepExpired` removes them at `NotAfter - 1h` (23 hours later). A few million unique hostnames is enough to exhaust a modest host's memory.

A sweep runs every 5 minutes (`defaultSweepInterval`) but only drops entries whose cert is near expiry — it does not cap size.

**Fix.** Replace the `sync.Map` with a bounded LRU (e.g. `golang.org/x/sync/…` or `hashicorp/golang-lru/v2`). Cap at a configurable size (default 10 000). Evict on insert when full.

---

### 10. (Medium) No CRLF / control-char validation on secret values

**File:** `internal/inject/template.go:85-111`, `internal/inject/injector.go:125-127`

```go
case secretIdentRE.MatchString(inner):
    …
    value, scope, allowedHosts, err := store.Get(ctx, secretName, agentName)
    …
    return value            // template.go:111
…
for _, m := range mutations {
    req.Header.Set(m.name, m.value)   // injector.go:126
}
```

A secret value is returned verbatim and passed into `req.Header.Set`. Go's `net/http` Transport _does_ reject a subset of control chars when writing the request (since Go 1.20-ish), so plain `\r\n` injection into an outgoing HTTP request is partly mitigated — but:

- `http.Header.Set` itself does **not** validate, so the value sits in memory and may be echoed into logs or a future code path that doesn't go through Transport.
- HTTP/2 (which this proxy prefers) has slightly different validation rules; bugs in that layer have historically let CRLF-adjacent bytes through.
- The "secrets are trusted" argument is the right default, but this is the _last_ defensive layer before credentials hit the network. Assuming secrets are clean is the kind of assumption that gets relaxed later by accident.

**Fix.** In `inject/template.go` at the secret return point, reject values containing `\r`, `\n`, or any byte `< 0x20` other than `\t`. Return `ErrSecretUnresolved` (so the injection fail-softs) or a new `ErrSecretInvalid` (to hard-fail with 403). Log which secret was rejected so an operator notices.

---

### 11. (Medium) Body-buffer hard I/O error forwards partial body

**File:** `internal/proxy/buffer.go:85-89`, used in `internal/proxy/pipeline.go:236-240`

```go
default:
    // Hard read error.
    err = readErr
    rewound = io.NopCloser(io.MultiReader(bytes.NewReader(buf), r))
}
```

On a hard read error, `bufferBody` returns a rewound reader that replays whatever was read so far plus the (likely broken) original reader. The caller in `pipeline.go:238` only logs:

```go
p.log.Warn("proxy: body buffer read error; body matchers will not fire", …)
```

and proceeds as if the body is fine — but body matchers _do not fire_, so a rule that would have `deny`'d based on the body content now doesn't match. If the matched rule was an `allow` that relied on body matching to narrow it, the request still forwards because the body matcher `true`-defaults when there is no body matcher... but when `NeedsBodyBuffer` was true, a matcher existed and we failed to evaluate it.

This is adjacent to the existing "body matcher bypassed" path (which _does_ fail-closed at `pipeline.go:252-259`), but only fires when the body _could_ be read and either exceeded cap or timed out. The hard-I/O-error path skips that check.

**Fix.** Treat a hard body-buffer I/O error the same as `body_matcher_bypassed:timeout`: if the buffer failure happened while a body matcher was expected, synthesise a 403 with `X-Request-ID`. The current fail-soft is a silent way for a broken agent or racy upstream to skip body rules.

---

### 12. (Medium) Raw query string persisted verbatim in audit log

**File:** `internal/audit/audit.go` (insert path, `e.Query`); captured in `internal/proxy/pipeline.go` as `r.URL.RawQuery` via `audit.Entry`.

The audit row stores the full query string. Agents may (incorrectly, but routinely) include credentials in query params — GitHub's legacy `?access_token=…`, analytics/marketing APIs with `?api_key=…`, etc. The audit DB is intended to be readable (via the dashboard and direct SQLite access) and retained for 90 days by default.

**Fix.** Either:

- Redact query params that match a configurable denylist of names (`api_key`, `token`, `password`, `secret`, `access_token`, `auth`, `key`) before insertion. Replace the value with `[REDACTED]`.
- Or elide the query string entirely in audit rows and rely on the dashboard showing method/host/path only.

Finding #4 compounds this: a world-readable `state.db` plus a credentialed query string = pre-packaged credential leak.

---

### 13. (Medium) Root CA is valid for 10 years

**File:** `internal/ca/root.go:171`

```go
NotAfter: now.Add(10 * 365 * 24 * time.Hour),
```

A 10-year local MITM root is unusual for a tool whose threat model involves host-local secret material. The longer the root lives, the higher the chance it leaks (backups, forked sandbox VMs, a developer accidentally committing `ca.key`). `ca rotate` exists but is not mandatory and has no expiry warning.

**Fix.**

- Reduce default to 1 year.
- Add a startup check: if `NotAfter - time.Now() < 30 days`, log a warning and surface on the dashboard banner.
- Document rotation in `docs/cli.md` as a quarterly operator task.

---

### 14. (Medium) Prefix-map lookup short-circuits on miss

**File:** `internal/agents/registry.go:128-141`

```go
prefix := Prefix(token)

r.mu.RLock()
entry, ok := r.prefixMap[prefix]
r.mu.RUnlock()

if !ok {
    return nil, ErrInvalidToken   // early exit — no argon2 work
}

candidate := deriveHash(token, entry.salt)
if subtle.ConstantTimeCompare(candidate, entry.hash) != 1 {
    return nil, ErrInvalidToken
}
```

A valid prefix causes argon2id work (tens of ms); an invalid prefix returns immediately. This is a classic enumeration oracle: an attacker who can submit CONNECT requests (from the sandbox network or an open port) can iterate prefixes and distinguish "valid prefix, wrong suffix" from "no such prefix" by response latency. Prefixes are 12 chars of `agw_` + 8 base62 chars → ~47 bits. A prefix oracle lets you narrow the enumeration space meaningfully before attacking the Argon2 hash (finding #2), which is where the full exploit chain lands.

**Fix.** Always do the Argon2 work:

```go
entry, ok := r.prefixMap[prefix]
if !ok {
    entry = r.dummyEntry   // pre-computed at registry init
}
candidate := deriveHash(token, entry.salt)
valid := subtle.ConstantTimeCompare(candidate, entry.hash) == 1
if !ok || !valid {
    return nil, ErrInvalidToken
}
```

The dummy entry's hash never matches a real token, so correctness is preserved; timing is uniform. Note: this becomes more costly with the stronger Argon2 params from finding #2, so pair this change with per-IP or per-conn rate limiting on the proxy listener.

---

### 15. (Medium) No upper bound on `retention_days`, `max_body_buffer`, `max_pending`

**File:** `internal/config/validate.go` (only validates `no_intercept_hosts`)

The HCL config accepts any positive integer for:

- `audit.retention_days` — no cap; `999999` silently fills disk.
- `proxy_behavior.max_body_buffer` — no cap; `"10GiB"` would allocate on every body-buffered request.
- `approval.max_pending` — zero means "unlimited" (per design), but also "no backpressure."

**Fix.** Add upper bounds in `validate.go`:

```go
if cfg.Audit.RetentionDays < 0 || cfg.Audit.RetentionDays > 3650 { … }
if cfg.ProxyBehavior.MaxBodyBuffer < 0 || cfg.ProxyBehavior.MaxBodyBuffer > 100<<20 { … }  // 100 MiB
if cfg.Approval.MaxPending < 0 || cfg.Approval.MaxPending > 10000 { … }
```

Refuse to start on out-of-range values. Document the caps.

---

### 16. (Medium) Listen address not validated

**File:** `cmd/agent-gateway/serve.go:205, :212`; default in `internal/config/default.hcl:2, :6`

Both listen addresses default to `127.0.0.1`, which is correct. There is no check that prevents an operator from setting `proxy.listen = "0.0.0.0:8220"` or the dashboard to `0.0.0.0:8221`. The `DESIGN.md`/security model explicitly states the dashboard should be loopback-only, but the code does not enforce it.

Consequences of a non-loopback bind:

- Proxy on `0.0.0.0` — any host on the LAN can use the gateway, attempt auth, and potentially enumerate agent prefixes (finding #14).
- Dashboard on `0.0.0.0` — admin UI reachable with only the 256-bit token protecting it. In combination with the `Secure: false` cookie (finding #7), a single cleartext request exposes the token.

**Fix.** In `validateConfig`, parse each listen address and warn (or refuse by default) if the bound IP is not loopback. Add an opt-in flag (`allow_non_loopback_listen = true`) so the rare legitimate case still works.

---

### 17. (Low) TLS minimum version is 1.2

**File:** `internal/ca/leaf.go:191`, `cmd/agent-gateway/serve.go:228`

```go
MinVersion: tls.VersionTLS12
```

TLS 1.2 is still broadly deployed; this isn't a defect. But TLS 1.3 is 7 years old, all major upstreams support it, and the gateway controls both the leaf and the upstream client — no compatibility constraints apply.

**Fix.** Bump both MinVersion to `tls.VersionTLS13`. Leave a config knob for the rare operator who has a legacy upstream.

---

### 18. (Low) Admin token never expires

**File:** `internal/dashboard/auth.go:23-63`

Admin token is a 32-byte hex string stored in a `0o600` file. `admin-token rotate` regenerates it on demand. There is no time-based expiry, no audit trail of rotations, and the cookie `MaxAge` is 1 year (`dashboard.go:194`). A stolen token is valid indefinitely.

**Fix.**

- Persist `(token, issued_at)` instead of just `token`, and reject tokens older than N days (default 90).
- On rotation, keep the previous token valid for a configurable grace period (e.g. 5 minutes) so a live dashboard doesn't log the operator out immediately.
- Surface token age in the dashboard UI as a nag banner.

---

### 19. (Low) Root CA has no path-length constraint

**File:** `internal/ca/root.go:165-175`

```go
template := &x509.Certificate{
    …
    IsCA:                  true,
    KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
    BasicConstraintsValid: true,
}
```

`MaxPathLen` is not set and `MaxPathLenZero` is `false`, so the cert effectively asserts "can sign intermediate CAs." The gateway only ever signs leaves. If the root key ever leaks, an attacker could (with a little effort) use it to mint a rogue intermediate trusted by any host that installed this CA.

**Fix.**

```go
MaxPathLen:     0,
MaxPathLenZero: true,
```

The second flag is needed in Go because `0` is otherwise ambiguous with "unset."

---

### 20. (Low) PID-file acquire has a TOCTOU window

**File:** `internal/daemon/pidfile.go:50-69`

The `Acquire` function reads the PID file, checks liveness + comm, then writes. Between the check and the write, another process can win the race. The impact is modest because `writePID` goes through `atomicfile.Write` (rename-based) and the comm check would catch a genuinely different process on the next startup — but the guarantee here is weaker than it looks.

**Fix.** Use `os.OpenFile(path, O_CREATE|O_EXCL|O_WRONLY, 0o600)` to atomically create-or-fail. On `EEXIST`, read the existing file, do the liveness/comm check, and if stale, `os.Remove` + retry the `O_EXCL` create. This converts the TOCTOU into a fast-fail-retry loop.

---

### 21. (Low) `LIMIT`/`OFFSET` interpolated via `fmt.Sprintf`

**File:** `internal/audit/audit.go:240-248`

```go
if f.Limit != nil {
    q += fmt.Sprintf(" LIMIT %d", *f.Limit)
}
```

`*f.Limit` is an `int` parsed via `strconv.Atoi` at `dashboard.go:305-311`, so there is no _current_ injection path. It's still bad practice — if someone later changes the type or skips the `Atoi` check, the bug becomes exploitable with no visible diff.

**Fix.** Use placeholders: `q += " LIMIT ?"; args = append(args, *f.Limit)`. Same for `OFFSET`. Also add an upper bound in the dashboard handler (`if n > 10000 { n = 10000 }`) so a malicious admin-token holder can't ask for a million rows.

---

### 22. (Low) No rate limiting on dashboard or approval queue per-agent

**File:** `internal/dashboard/dashboard.go`, `internal/approval/broker.go`

Neither surface enforces per-IP or per-agent rate limits. A compromised admin token yields unlimited audit queries; a compromised agent with a `require-approval` rule could enqueue requests up to the global `max_pending` cap without the broker distinguishing one misbehaving agent from many.

**Fix.** For the dashboard, add a small token bucket (e.g. 100 req/min per IP) on API endpoints. For the approval broker, add a per-agent cap alongside the global cap — document the default in `DESIGN.md`.

---

### 23. (Low) `Secure: false` cookie also in `handleUnauthorizedPOST`

**File:** `internal/dashboard/dashboard.go:188-198`

Same TODO as finding #7 but in the form-login code path. Same fix.

---

### 24. (Low) Tunnel mode relays raw TCP — agent-controlled `X-Forwarded-For`

**File:** `internal/proxy/connect.go:179-264`

Tunnel mode is by design raw TCP (`io.Copy` both ways). A malicious agent can prepend arbitrary HTTP headers, including `X-Forwarded-For`/`True-Client-IP`/`Forwarded`, that the upstream may trust. This is inherent to HTTP CONNECT proxies — noted here as a reminder, not a defect. Document in `docs/security-model.md` that upstream services treating these headers as authoritative must not trust traffic routed through an agent-gateway tunnel.

---

### 25. (Informational) Keychain service name not instance-scoped

**File:** `internal/secrets/masterkey.go:18`

```go
const keychainService = "agent-gateway"
```

Multiple installations of agent-gateway for the same OS user (dev + prod workspaces, multiple worktrees) share the same keychain namespace. The key-ID scheme (`master-key-1`, `master-key-2`, …) partially avoids collisions because each install tracks its own active ID in its own SQLite, but a fresh install's rotation could clobber another install's ID.

**Fix.** Hash the config dir into the service name, or accept an explicit instance ID via env/flag:

```go
func keychainService() string {
    return "agent-gateway-" + shortHash(paths.ConfigDir())
}
```

---

### 26. (Informational) SSE broker silently drops events on full buffer

**File:** `internal/dashboard/sse.go` (broadcast logic)

The dashboard live feed uses drop-on-full per subscriber. A slow browser tab or paused debugger will silently lose approval events. The dashboard mitigates by refetching `/api/pending` on reconnect, but new audit rows are lost until the next page load. Not a security issue per se, but has security-observability implications: a flood of events during an incident may erase the signal you needed.

**Fix.** Increase per-subscriber buffer (32 → 256), and emit a synthetic `{"kind":"events_dropped","count":N}` event when slots were overwritten so the UI can prompt a reload.

---

## Unsafe defaults — quick reference

| Default                                       | File                                        | Concern                                                                                                                              | Recommended value                                    |
| --------------------------------------------- | ------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------ | ---------------------------------------------------- |
| `proxy.listen = "127.0.0.1:8220"`             | `default.hcl:2`                             | Safe default; not enforced (finding #16)                                                                                             | keep, enforce                                        |
| `dashboard.listen = "127.0.0.1:8221"`         | `default.hcl:6`                             | Safe default; not enforced (finding #16)                                                                                             | keep, enforce                                        |
| `dashboard.open_browser = true`               | `default.hcl:7`                             | Pops a browser on every start — surprising on servers                                                                                | change to `false`; override via `--open-browser`     |
| `proxy_behavior.no_intercept_hosts = []`      | `default.hcl:29`                            | Every host is MITM-eligible unless a rule targets it; combined with `DecisionTunnel` fallback, unrule'd hosts _do_ tunnel by default | acceptable given the design, but document explicitly |
| `proxy_behavior.max_body_buffer = "1MiB"`     | `default.hcl:30`                            | Per-request allocation; no upper bound on operator override (finding #15)                                                            | keep default, enforce max                            |
| `audit.retention_days = 90`                   | `default.hcl:19`                            | No upper bound (finding #15)                                                                                                         | keep default, enforce max                            |
| `approval.timeout = "5m"`, `max_pending = 50` | `default.hcl:24-25`                         | No per-agent cap; drop-on-full at the global level                                                                                   | add per-agent cap                                    |
| `secrets.cache_ttl = "60s"`                   | `default.hcl:15`                            | Secret value cached in-process 60s after first fetch; a compromised process keeps creds past a rotation                              | consider `0s` default, or explicit opt-in            |
| Leaf TLS `MinVersion = TLS 1.2`               | `ca/leaf.go:191`                            | Finding #17                                                                                                                          | TLS 1.3                                              |
| Upstream TLS `MinVersion = TLS 1.2`           | `serve.go:228`                              | Finding #17                                                                                                                          | TLS 1.3                                              |
| Root CA lifetime 10y                          | `ca/root.go:171`                            | Finding #13                                                                                                                          | 1y                                                   |
| Argon2id `time=1, memory=64 KiB`              | `agents/registry.go:26-32`                  | Finding #2                                                                                                                           | `time=2, memory=19 MiB`                              |
| All XDG dirs created `0o750`                  | multiple                                    | Finding #3                                                                                                                           | `0o700`                                              |
| Cookie `Secure = false`                       | `dashboard/auth.go:125`, `dashboard.go:197` | Finding #7                                                                                                                           | conditional on `r.TLS`                               |

---

## Positive findings (keep doing these)

A security audit that only calls out problems is misleading. These design choices are load-bearing and worth protecting from regression:

- **Agent tokens have 256 bits of entropy** (`agents/token.go`). Combined with per-agent salts and Argon2id hashing, tokens are not guessable even with a weak KDF.
- **Constant-time comparison** (`subtle.ConstantTimeCompare`) is used correctly for admin token (`dashboard/auth.go:152, :160, :187`) and agent-token hash verification (`agents/registry.go:139`).
- **`InsecureSkipVerify` is explicitly `false`** on the upstream Transport (`proxy/proxy.go:220`). The gateway _verifies_ upstream certs against the system trust store — it is a terminator, not a bypass.
- **Hop-by-hop header stripping** follows RFC 7230 §6.1 including Connection-named tokens (`proxy/pipeline.go:99-111`).
- **Request/response invariant enforcement**: the `require-approval` path stashes only _asserted_ headers in the approval view (`pipeline.go:474-482`), so operator review surfaces don't leak body data or non-matched headers.
- **Atomic writes** everywhere sensitive: CA key, admin token, PID file, master-key file, config — all go through `atomicfile.Write` with explicit `0o600`.
- **Fail-closed on secret scope violations**: `ErrSecretHostScopeViolation` synthesises a 403 rather than forwarding with the agent's dummy creds (`pipeline.go:364-375`). This is the right shape — loud over quiet.
- **Approval broker's ApprovalGuard pattern** (`approval/broker.go:105-113`) ensures cancelled contexts don't leak pending entries.
- **Rule matcher requires explicit `match.host`** (`rules/parse.go:233-238`): operators cannot accidentally write a rule that silently applies to every host. The error message even guides them to `host = "**"` for the rare intentional case.
- **SQL elsewhere uses parameterised queries** (`audit.go:210-228`, registry queries in `agents/registry.go`).

---

## Prioritized remediation plan

**P0 — before the next release:**

1. #3, #4 — tighten directory and DB-file permissions to `0o700`/`0o600`; add startup perm-check.
2. #7, #23 — set `Cookie.Secure` dynamically based on request scheme.
3. #2 — raise Argon2id parameters to OWASP minimum; document the migration.
4. #5 — reject CONNECT requests whose host fails normalization.
5. #15, #16 — add config bounds validation and loopback enforcement.

**P1 — follow-up release:**

6. #1 — add AAD to AES-GCM on the secrets table.
7. #8 — add response security headers middleware.
8. #9 — replace unbounded leaf-cert cache with bounded LRU.
9. #10 — validate secret values for CRLF/control bytes.
10. #11 — fail-closed on body-buffer hard errors.
11. #12 — redact known-credential query params in audit rows.
12. #14 — constant-time agent prefix lookup (dummy hash).

**P2 — next quarter:**

13. #13, #18 — shorter CA lifetime, admin-token expiry.
14. #17 — TLS 1.3 minimum.
15. #19 — `MaxPathLenZero` on root CA.
16. #20, #21, #22 — small correctness/hardening cleanups.

**Not urgent, but track:**

17. #25, #26 — keychain instance scoping, SSE drop signalling.

---

## Verification notes

Every finding in this report was verified against the actual source at the cited file:line before being written up. Subagent reports that suggested "race in approval callback outside lock" (the design is intentional and correct — the callback payload is self-contained), "token prefix collision" (2⁴⁸ birthday bound is dominated by the full-token space), and a few smaller claims did not survive verification and were dropped.

---

# Part II — Deeper Audit (additional surface)

The initial review (findings 1–26) covered the core cryptography, proxy pipeline, rules, dashboard, audit, and filesystem surface. This addendum covers the surface that was only partially touched the first time: HTTP/2 server config, CONNECT-read Slowloris, SPA/SSE frontend, CLI command ergonomics, SIGHUP reload coherence, rule-evaluation edge cases, documentation gaps, and the sandbox-manager integration example. Findings are numbered continuing from #26.

## Additional summary table

| #   | Severity      | Area           | Issue                                                                                                                                                                               |
| --- | ------------- | -------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 27  | High          | Stdout leakage | Dashboard URL including admin token printed to stdout at every `serve` start — lands in journald / Docker / k8s logs                                                                |
| 28  | High          | Docs           | `docs/sandbox-manager.md` example exports the agent token into shell env, exposing it via `env`, `/proc/<pid>/environ`, and any log dump                                            |
| 29  | High          | Proxy          | HTTP/2 server has no `MaxConcurrentStreams`, `MaxReadFrameSize`, or `MaxHeaderBytes` — exposed to Rapid Reset (CVE-2023-44487) and CONTINUATION flood (CVE-2024-27316)              |
| 30  | High          | Proxy          | CONNECT read has no deadline before `http.ReadRequest` — Slowloris against goroutine count                                                                                          |
| 31  | Medium        | Dashboard      | `marshalEvent` interpolates `ev.Kind` without validation — SSE frame injection if an untrusted kind is ever broadcast                                                               |
| 32  | Medium        | Dashboard      | `esc()` helper does not escape single quotes, yet is used inside `onclick='decide(\'…\')'` attribute — works today because IDs are ULIDs, breaks silently if ID source ever changes |
| 33  | Medium        | Rules          | Rule path glob matching is case-sensitive and undocumented — `/ADMIN/delete` bypasses a `/admin/*` deny rule                                                                        |
| 34  | Medium        | Reload         | SIGHUP does not reload `config.hcl`; timeouts, listen, `max_body_buffer`, `max_pending`, and `secrets.cache_ttl` silently retain old values even after operator edits the file      |
| 35  | Medium        | CLI            | `config edit` honours `$EDITOR` — well-known UX but worth documenting explicitly as a trust surface                                                                                 |
| 36  | Medium        | Docs           | No operational guidance on audit-log sensitivity, rotation cadence, or backup handling                                                                                              |
| 37  | Low           | Rules          | `allowed_hosts` validation catches `**` and pure-wildcard patterns but not short public-suffix patterns like `*.co` or `*.io`                                                       |
| 38  | Low           | Reload         | Agent-registry reload happens after rules reload; brief window where new agents can fail auth even though their tokens are already in the DB                                        |
| 39  | Low           | CLI            | Secret/agent name inputs have no length or character-class bounds — non-printable names render confusingly in dashboard and CLI listings                                            |
| 40  | Low           | Proxy          | `singleConnListener.Accept` blocks on a nil-conn path without context; minor goroutine-leak risk on abnormal H1 server shutdown                                                     |
| 41  | Informational | Docs           | `docs/security-model.md` lists the loopback-dashboard requirement as operator responsibility — the code does not enforce it (already flagged as #16)                                |

---

## Additional findings

### 27. (High) Admin-token-bearing dashboard URL is printed to stdout

**File:** `cmd/agent-gateway/serve.go:314, :343`

```go
dashURL := fmt.Sprintf("http://%s/dashboard/?token=%s", dashLn.Addr(), dashServer.Token())
log.Info("agent-gateway started", "proxy", proxyLn.Addr(), "dashboard", dashLn.Addr())
…
fmt.Printf("Dashboard: %s\n", dashURL)
```

The admin token sits in the URL's query string and is `Printf`'d to stdout every time `serve` starts. Any process supervisor that captures stdout picks this up:

- **systemd** — `journalctl -u agent-gateway` shows the token to anyone in the `systemd-journal` / `adm` group.
- **launchd** — `StandardOutPath` in the plist persists it to a file.
- **Docker/k8s** — container stdout is the default log sink; `docker logs` / `kubectl logs` show it.
- **supervisord**, **nohup**, `> /var/log/agent-gateway.log` — same.

The token has no expiry (finding #18), so anyone who ever had log access retains dashboard admin forever.

**Fix.** Print the URL without the token, and print the token separately with a clear "handle this like a password" framing:

```go
dashURL := fmt.Sprintf("http://%s/dashboard/", dashLn.Addr())
fmt.Printf("Dashboard: %s\n", dashURL)
fmt.Fprintf(os.Stderr, "Admin token (one-time display): %s\n", dashServer.Token())
```

Better: write the `?token=…` form to a `0o600` file under `$XDG_RUNTIME_DIR` and print the path instead. `open-browser` can read from that file. Operators who need the URL can `cat` it; tailed log files never see the token.

---

### 28. (High) Sandbox-manager example exports the agent token into shell env

**File:** `docs/sandbox-manager.md:80-88`

```bash
cat > /etc/profile.d/agent-gateway.sh <<'EOF'
if [[ -f "$HOME/.agent-gateway-token" ]]; then
  token=$(cat "$HOME/.agent-gateway-token")
  export HTTPS_PROXY="http://x:${token}@host.lima.internal:8220"
  export HTTP_PROXY="http://x:${token}@host.lima.internal:8220"
  export NO_PROXY="localhost,127.0.0.1,host.lima.internal"
fi
EOF
```

The token is embedded in `HTTPS_PROXY` / `HTTP_PROXY`, which places it in every process's environment. Consequences:

- `env` and `printenv` in any shell reveal it.
- `/proc/<pid>/environ` is readable by the same UID — any compromised process inside the sandbox can read the token of any other process.
- Tools that log env (debug dumps, error reporters, crash handlers, `curl -v` prints `HTTPS_PROXY`) leak it.
- The `http://x:TOKEN@host:port` form appears in proxy access logs as `x:TOKEN@...` if any intermediary logs URLs (Lima's network stack doesn't, but an operator's `tcpdump` would see it if the sandbox ever proxied through anything else).

The doc comments "Keeping the token out of the provisioning script itself lets you rotate" — but the script _still_ exports the live token. The rotation concern was file-staleness, not env exposure.

**Fix.** Either:

1. Wrap each agent invocation in a helper that reads the token at exec time and passes it via stdin / env only for the child:

   ```bash
   #!/bin/sh
   # /usr/local/bin/with-proxy
   TOKEN="$(cat "$HOME/.agent-gateway-token")"
   export HTTPS_PROXY="http://x:${TOKEN}@host.lima.internal:8220"
   export HTTP_PROXY="http://x:${TOKEN}@host.lima.internal:8220"
   exec "$@"
   ```

   …then users invoke `with-proxy claude …` instead of getting the env at login.

2. Or use a proxy that reads the token from a file instead of the URL — this is cleaner but requires an auxiliary shim. Document that the current design trades env-exposure for setup simplicity, and flag it in `docs/security-model.md`.

Update the docs either way.

---

### 29. (High) HTTP/2 server has no stream / frame / header limits

**File:** `internal/proxy/connect.go:294-302`

```go
func (p *Proxy) serveH2(conn *tls.Conn, host, agentName string) {
    srv := &http2.Server{
        IdleTimeout: p.idleTimeout,
    }
    srv.ServeConn(conn, …)
}
```

Missing:

- **`MaxConcurrentStreams`** — Go's `http2.Server` defaults to 250, but nothing caps the _rate_ of stream creation. CVE-2023-44487 (HTTP/2 Rapid Reset) exploits the gap between stream creation and the client's RST_STREAM; defensive mitigations require an explicit cap matched to your expected concurrency.
- **`MaxReadFrameSize`** — defaults to 1 MiB in x/net/http2. An attacker can send maximum-size frames to consume memory per stream.
- **`MaxDecoderHeaderTableSize`** / **`MaxEncoderHeaderTableSize`** — HPACK dynamic tables; if an agent sends many large headers, the decoder grows.
- No `MaxHeaderBytes` is set on the HTTP/1 `http.Server` in `serveH1` either.

A malicious agent with a valid token can park thousands of streams, send oversized headers with CONTINUATION frames, or Rapid-Reset to starve the proxy of goroutines. The proxy has to do Argon2 work once per CONNECT but H2 multiplexes many requests per connection — per-request cost is low after the handshake, so the ceiling is much lower than "CPU bound by auth."

**Fix.** Tune both servers and expose as config knobs under `proxy_behavior`:

```go
srv := &http2.Server{
    IdleTimeout:                  p.idleTimeout,
    MaxConcurrentStreams:         p.maxConcurrentStreams,        // e.g. 100
    MaxReadFrameSize:             16 << 10,                       // 16 KiB
    MaxDecoderHeaderTableSize:    4096,
    MaxEncoderHeaderTableSize:    4096,
}
…
srv := &http.Server{
    Handler:           …,
    ReadHeaderTimeout: p.readHeaderTimeout,
    MaxHeaderBytes:    64 << 10,                                  // 64 KiB
    IdleTimeout:       p.idleTimeout,
}
```

Track CVE-2023-44487 and CVE-2024-27316 mitigations in your Go version — the stdlib tries to do the right thing but defence-in-depth at the application level is the expectation.

---

### 30. (High) CONNECT read has no deadline — Slowloris

**File:** `internal/proxy/connect.go:39-53`

```go
func (p *Proxy) serveConn(conn net.Conn) {
    defer func() { _ = conn.Close() }()

    br := bufio.NewReader(conn)
    req, err := http.ReadRequest(br)      // no deadline set
    if err != nil { return }
    …
}
```

Every accepted TCP connection spawns a goroutine (`go p.serveConn(conn)` in `proxy.go:277`). If the client sends `CONNECT ` and then never any bytes, `http.ReadRequest` blocks forever. An attacker from the sandbox LAN (or anywhere, given finding #16) can open thousands of TCP connections, never send a full request line, and exhaust the process's goroutine and FD limits.

The `p.readHeaderTimeout` that exists in `Deps` is only passed to the embedded `http.Server` inside `serveH1` — that runs _after_ the CONNECT has already succeeded and TLS is terminated. The outer CONNECT has no such guard.

**Fix.**

```go
func (p *Proxy) serveConn(conn net.Conn) {
    defer func() { _ = conn.Close() }()

    if p.readHeaderTimeout > 0 {
        _ = conn.SetReadDeadline(time.Now().Add(p.readHeaderTimeout))
    }

    br := bufio.NewReader(conn)
    req, err := http.ReadRequest(br)
    if err != nil { return }

    // Clear the deadline before we hand off to MITM / tunnel.
    _ = conn.SetDeadline(time.Time{})
    …
}
```

Pair with per-client-IP connection rate limiting at the `Serve` accept loop (finding #22 expansion).

---

### 31. (Medium) `marshalEvent` interpolates `ev.Kind` into SSE headers

**File:** `internal/dashboard/sse.go:79-84`

```go
frame := fmt.Sprintf("id: %s\nevent: %s\ndata: %s\n\n",
    ev.ID.String(), ev.Kind, string(dataBytes))
```

Callers today always pass hardcoded kinds (`"approval"`, `"request"`, `"decided"`, `"removed"`). There is no validation that `ev.Kind` is a token (no newlines, no colons). A newline in `Kind` would split a single SSE event into multiple frames; a well-placed `data: ` could inject arbitrary payloads into the event stream that bypass the server's authored structure.

No current code path makes this exploitable — but `Broadcast(kind string, data any)` is a public method (`dashboard.go:89`), and future callers could pass untrusted input.

**Fix.** Validate at the broker boundary:

```go
func isValidEventKind(s string) bool {
    if s == "" || len(s) > 64 { return false }
    for _, r := range s {
        ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_'
        if !ok { return false }
    }
    return true
}

func marshalEvent(ev Event) []byte {
    if !isValidEventKind(ev.Kind) {
        return []byte(": invalid event kind dropped\n\n")
    }
    …
}
```

Or better: make `Kind` a typed enum so the compiler forbids untrusted strings.

---

### 32. (Medium) `esc()` does not escape single quotes, yet is used in `onclick` attributes

**File:** `internal/dashboard/app.js:6-10, :259-264`

```js
function esc(s) {
  var d = document.createElement("div");
  d.textContent = s == null ? "" : String(s);
  return d.innerHTML;
}
…
div.innerHTML =
  '<button class="btn-approve" onclick="decide(\'' + esc(id) + "','approve')\">Approve</button>" +
  '<button class="btn-deny" onclick="decide(\'' + esc(id) + "','deny')\">Deny</button>";
```

The `esc()` helper uses the textContent/innerHTML trick, which encodes `<`, `>`, `&`, `"` — but NOT `'`. A single quote in `id` would terminate the JS string inside the `onclick` attribute and allow arbitrary JS injection. Today, `id` is a ULID (26-character Crockford base32) generated server-side, so the attack is not currently reachable. But this is a maintenance-time landmine: a future refactor that lets a different ID format reach this code path turns into a Critical overnight.

**Fix.** Swap to `addEventListener` with a closure over `id`, and build the card's child elements via DOM APIs:

```js
var approveBtn = document.createElement("button");
approveBtn.className = "btn-approve";
approveBtn.textContent = "Approve";
approveBtn.addEventListener("click", function () {
  decide(id, "approve");
});
var denyBtn = document.createElement("button");
denyBtn.className = "btn-deny";
denyBtn.textContent = "Deny";
denyBtn.addEventListener("click", function () {
  decide(id, "deny");
});
```

No `innerHTML` for user-derived values, no interpolation into HTML event attributes.

---

### 33. (Medium) Path glob matching is case-sensitive and undocumented

**File:** `internal/rules/match.go:152-157`; verified against `internal/rules/parse.go:545` (`compileGlob`).

```go
if r.pathGlob.re != nil {
    if !r.pathGlob.re.MatchString(req.Path) {
        return false, ""
    }
}
```

`regexp.Compile` produces a case-sensitive pattern. A rule intended to `deny` access to `/admin/*` will not match `/Admin/delete` or `/ADMIN/delete`. Many upstream services normalize path case (GitHub's API, for instance), so the _upstream_ accepts both — but the gateway's rule only fires on one.

The method comparison is case-sensitive too (`r.Match.Method != req.Method`, `match.go:161`), with a comment "callers are expected to uppercase" — but that's an undocumented precondition.

Header value regex matching is case-sensitive; `http.Header.Get` canonicalizes the _name_ but not the value. A rule `Authorization ~ "Bearer .*"` does not match `authorization: bearer …` (HTTP/2 forces lowercase header names; values are unchanged but tokens are sometimes lowercased upstream).

**Fix.** Either:

- Normalize by default: lower-case `req.Path` before evaluation and document that path patterns are case-insensitive. This matches the spirit of the host glob (IDNA case-folds).
- Or document the case-sensitivity trap in `docs/rules.md` prominently. Add `(?i)` inline flag examples.
- Pick one and stick with it.

The status quo — silent case-sensitivity with no docs — is the worst of both worlds.

---

### 34. (Medium) SIGHUP does not reload `config.hcl`

**File:** `cmd/agent-gateway/serve.go:370-404`

The SIGHUP handler reloads rules, invalidates the injector cache, reloads the agent registry, reloads the admin token, and reloads the CA. It does **not** re-parse `config.hcl`. So edits to:

- `proxy.listen`, `dashboard.listen` — ignored (expected, listeners are bound).
- `timeouts.*` — ignored.
- `proxy_behavior.max_body_buffer`, `proxy_behavior.no_intercept_hosts` — ignored.
- `approval.max_pending`, `approval.timeout` — ignored.
- `secrets.cache_ttl` — the cache is _cleared_ on SIGHUP, but the next `inject.NewInjector`-time TTL is retained; new entries are inserted with the old TTL.
- `audit.retention_days` — ignored.

Operators have no way to know this short of reading source. The `CLAUDE.md` comment says "SIGHUP handler performs a coarse reload: re-parse config.hcl, re-parse rules.d/" — which is **incorrect** for the actual code.

**Fix.** Either:

1. Make SIGHUP reload the config file and apply whatever is safely reloadable (non-listener knobs, TTLs, caps). Operators would expect nginx-style reload semantics.
2. Or document very clearly what SIGHUP _does_ and _does not_ reload, and fix the `CLAUDE.md` claim so it matches reality.

Option 1 is preferable. It requires refactoring the proxy's config knobs into `atomic.Pointer[config.Config]` rather than fields set at `New` time, but it's straightforward.

---

### 35. (Medium) `config edit` shells out to `$EDITOR`

**File:** `cmd/agent-gateway/config.go:48-67`

```go
editor := os.Getenv("EDITOR")
if editor == "" { editor = "vi" }
c := exec.Command(editor, configPath()) //nolint:gosec
```

`exec.Command` does not invoke a shell, so `EDITOR="bash -c '…'"` is treated as a single argv[0] and fails to exec. But `EDITOR=/path/to/attacker/script` _does_ work, and running the edit command with an attacker-set `EDITOR` is a code execution primitive. This is standard Unix behavior — `git`, `crontab -e`, `visudo` all do the same thing — but worth calling out in the security model doc.

**Fix.** Document the trust surface in `docs/cli.md`:

> `config edit` invokes the program named by `$EDITOR` (or `vi` if unset). `$EDITOR` is trusted; if an attacker controls your environment they can use this to execute code. Prefer `config path` + your editor of choice if you want to audit-trail edits.

No code fix needed.

---

### 36. (Medium) Docs are missing operational security guidance

**File:** `docs/security-model.md`, `docs/cli.md`, `README.md`

The security model doc is unusually clear for a project this size, but it does not cover:

1. **Audit log sensitivity.** The DB contains agent names, hosts, rule matches, raw query strings (finding #12), and timestamps that map credential-using activity. It is readable. No mention of how to protect it.
2. **Rotation cadence.** `ca rotate`, `master-key rotate`, `admin-token rotate`, `agent rotate` all exist; none is suggested on a schedule. Operators will rotate only reactively.
3. **Backup story.** If an operator backs up `~/.config/agent-gateway/` they get the admin-token file, rule files, config, and possibly master-key-file fallback. If they back up `~/.local/share/agent-gateway/` they get encrypted secret ciphertexts and the audit log. No guidance on how to handle these backups or whether the master key should be separately protected.
4. **Compromise response.** If the admin token leaks, what to do? If the CA key leaks? If a single agent token leaks? No runbook.

**Fix.** Add a "Operational security" section to `docs/security-model.md` covering:

- Audit-log treatment (file perms, retention, what not to share).
- Suggested rotation cadences per credential class.
- Backup / restore guidance; flag the master key as the keystone.
- A one-page compromise-response playbook (agent token, admin token, master key, root CA).

---

### 37. (Low) `allowed_hosts` validation does not catch short public suffixes

**File:** `internal/secrets/store.go:345-389`

The sanitizer rejects empty patterns and pure-wildcard patterns (`*`, `**`, `.*`) but accepts `*.co`, `*.io`, `*.com`, etc. The config validator for `no_intercept_hosts` (`internal/config/validate.go:95-108`) _does_ warn on public-suffix matches — but the same check is not applied to secrets' `allowed_hosts`.

An operator writes `secret add gh --host '*.co'` intending `.co` the country TLD but gets a pattern that matches any host ending in `.co` — which includes `evil-site.co`, `twitter.co`, `go.co`. A compromised agent could request `evil.co` and have the real GitHub token injected into the upstream request.

**Fix.** Run `warnIfPublicSuffix` from `config/validate.go` over each `allowed_hosts` entry; reject (not just warn) when the pattern would match an entire ICANN suffix.

---

### 38. (Low) Agent-registry reload ordering

**File:** `cmd/agent-gateway/serve.go:373-394`

SIGHUP reloads in this order: rules → injector cache → secret-coverage warnings → agent registry → admin token → CA. The agent registry reload comes _after_ rules. If an operator `agent add`'s a new agent and then sends SIGHUP, during the narrow window between the rules re-compile (which has already seen the new rule because rules come from files) and the registry reload (which picks up the new agent row), the new agent's CONNECT attempts authenticate against the _old_ prefix map and are rejected.

Small window (milliseconds under normal DB load), but visible under contention.

**Fix.** Reload the agents registry _first_, then rules. The rules only need the registry for `HostsForAgent` lookups at CONNECT time, which is post-auth anyway.

---

### 39. (Low) Secret and agent name inputs are not length- or character-class-bounded

**File:** `cmd/agent-gateway/secret.go:99-142`, `cmd/agent-gateway/agent.go:*`

Secret names, agent names, and secret `--description` flow into the SQLite DB as arbitrary strings. The rules-side does validate rule names indirectly via HCL label parsing, but the CLI accepts anything. Consequences:

- Null bytes, newlines, terminal control sequences, and RTL overrides render in dashboard and CLI output. A secret named `normal‮htappan` can visually reorder characters in listings.
- Names of unbounded length render oddly in tabular output.
- Not an auth bypass — SQLite doesn't care — but the dashboard is the only UI showing these, and it renders via `esc()` for text (safe against XSS but not against visual confusion).

**Fix.** Validate names in `cmd/agent-gateway/args.go` at the CLI boundary: ASCII-printable, `[a-zA-Z0-9._-]{1,64}` or similar. Existing DBs would need a one-time migration check.

---

### 40. (Low) `singleConnListener.Accept` has a nil-conn blocking path

**File:** `internal/proxy/connect.go:355-364`

```go
func (l *singleConnListener) Accept() (net.Conn, error) {
    if l.conn != nil { … return c, nil }
    <-l.ch   // blocks until Close()
    return nil, net.ErrClosed
}
```

If `http.Server.Serve` calls `Accept` a second time (e.g. after a bizarre error path), the goroutine blocks on `<-l.ch` forever unless `Close` fires. In normal operation `srv.Serve(ln)` returns and the defer closes `ln`; I don't see a leak path in practice, but this is the kind of nil-check-then-block idiom that tends to acquire leak paths when code evolves.

**Fix.** Pass a context into `singleConnListener` and select on `ctx.Done()` alongside `l.ch`. Or just let the blocking Accept be cancelled by the server's own `Close`, which the current `_ = srv.Serve(ln)` idiom already arranges — and delete this finding once you confirm that. Keeping this at Low severity as a code-review flag.

---

### 41. (Informational) Code does not enforce the doc-stated loopback dashboard constraint

Already covered as finding #16; noting here for the docs cross-reference.

---

## SIGHUP reload behavior — clarification for operators

Based on the audit of `cmd/agent-gateway/serve.go:370-404`, here's what SIGHUP _actually_ reloads today (vs. what operators likely expect):

| Setting                                    | Reloaded on SIGHUP? | Notes                                        |
| ------------------------------------------ | ------------------- | -------------------------------------------- |
| Rules files (`rules.d/*.hcl`)              | Yes                 | via `engine.Reload()`                        |
| Agent registry (`agents` table)            | Yes                 | `agentsRegistry.ReloadFromDB`                |
| Admin token file                           | Yes                 | `dashServer.ReloadToken`                     |
| CA key/cert files                          | Yes                 | `authority.Reload()` (with leaf-cache clear) |
| Injector's secret value cache              | Yes (cleared)       | But TTL stays at startup value               |
| `config.hcl` — any field                   | **No**              | Requires restart                             |
| `proxy.listen`, `dashboard.listen`         | No                  | Would require re-bind                        |
| `timeouts.*`                               | No                  | Baked into `proxy.New` / `http.Server`       |
| `proxy_behavior.no_intercept_hosts`        | No                  | Baked into `proxy.New`                       |
| `proxy_behavior.max_body_buffer`           | No                  | Same                                         |
| `approval.timeout`, `approval.max_pending` | No                  | Same                                         |
| `secrets.cache_ttl`                        | No                  | Same                                         |
| `audit.retention_days`                     | No                  | Pruner uses startup value                    |

Fix per finding #34, and update `CLAUDE.md` (which currently claims SIGHUP re-parses `config.hcl` — it does not).

---

# Part III — Prior-art comparison: onecli

The user asked for a comparison against [onecli/onecli](https://github.com/onecli/onecli), an open-source credential-mediation gateway. Based on the public material available: a Rust-based HTTP gateway on port 10255, a Next.js dashboard on port 10254, a PostgreSQL backend, AES-256-GCM for credentials at rest, `Proxy-Authorization`-based agent auth, MITM of HTTPS, single-user or Google-OAuth modes, and optional Bitwarden integration for on-demand credential retrieval.

_Caveat._ This comparison is assembled from summaries rather than a direct source review. Specific onecli behaviors asserted below should be verified against upstream before being used as planning input.

## Shared design choices

Both projects agree on the core shape of the problem and the primitives:

- **Dummy-credential-at-agent, real-credential-at-gateway.** Agents never hold the real secret; the gateway swaps in the live value at request time. This is the right core design and both tools converge on it.
- **AES-256-GCM for secret storage.** Both use the same AEAD primitive. agent-gateway has a specific gap around AAD (finding #1) that onecli's docs don't describe either way.
- **Proxy-Authorization for agent auth.** Both use the standard HTTP mechanism; interoperable with every HTTP client's proxy support.
- **HTTPS MITM via a local CA.** Both agents install a local root CA in the sandbox trust store so the gateway can decrypt and re-encrypt.
- **Web dashboard for management.** Both have a separate HTTP surface for admin; both bind separately from the proxy.

## Where agent-gateway is stronger

- **Structured audit log with dashboard surface.** agent-gateway writes every MITM'd request plus every tunnel event to SQLite with WAL and 90-day retention, then renders it in the SPA (tunneled-hosts gap analysis, per-rule matches, etc.). onecli's public description does not mention audit logging, and the review notes explicitly "no explicit mention of audit logging" — a significant operational hole.
- **Approval-broker pattern (`require-approval` verdict).** agent-gateway's rule engine supports a verdict that parks the in-flight request and waits for human click-through on the dashboard (`internal/approval/broker.go`). The onecli description does not surface an equivalent. This is the difference between "automated policy" and "authenticated policy" and matters for gated write rules (PR merges, Jira state transitions).
- **Host-glob-scoped secrets with fail-closed enforcement.** agent-gateway requires every secret to declare `allowed_hosts` and synthesizes a 403 (not fail-soft) when a scope violation is detected (`internal/proxy/pipeline.go:364-375`). This is the main defense against "a misconfigured rule leaks a credential to a wrong host." onecli's docs describe "per-agent access tokens with scoped permissions" — which reads more like per-agent ACLs than per-secret host scopes. The mechanism matters: agent-scoped means _"which agent can use this secret"_; host-scoped means _"which upstream can this secret ever reach"_.
- **No external runtime dependencies.** agent-gateway uses embedded SQLite (WASM driver, no CGO) and OS keyring. onecli requires PostgreSQL — an additional TLS surface, backup surface, and auth surface. For single-user sandbox scenarios this is a meaningful operational difference.
- **Rich HCL rule DSL.** agent-gateway's `match { … }` + `inject { … }` + body matchers (`json_body`, `form_body`, `text_body`) give operators per-request-shape precision. onecli's "credential pattern matching" language is not publicly documented in detail; the depth is unclear.
- **Documented threat model.** `docs/security-model.md` names four attacker profiles and seven guarantees. onecli's README documents the core threat (credential exposure on agent compromise) but does not appear to surface a formal threat model.

## Where onecli is (or may be) stronger

- **Team / multi-user mode via Google OAuth.** agent-gateway is explicitly single-user (admin-token-gated dashboard). onecli's OAuth flow is a genuine feature gap for shared-team deployments. Finding #18's recommendation (admin-token expiry) is the minimum step toward closing this.
- **Bitwarden-backed on-demand credential retrieval.** onecli can fetch credentials from an external vault per-request rather than keeping them encrypted at rest. This reduces the steady-state attack surface: compromising the gateway DB yields nothing because nothing is persisted. agent-gateway's SQLite-at-rest model carries a larger standing blast radius. Consider documenting this trade-off as an "open question" in `DESIGN.md` and adding a vault-backend abstraction when a concrete user need emerges.
- **Rust memory-safety floor.** onecli's gateway is Rust, agent-gateway is Go. Both are memory-safe, but Go's `net/http2` has carried CVEs that required stdlib updates (Rapid Reset, CONTINUATION flood — see finding #29). Rust avoids that specific class. Not a design improvement one way or the other, but a different risk profile.
- **Google OAuth for dashboard auth** implies token-refresh and auditable identity, both of which a shared `admin-token` file cannot offer. agent-gateway's single-token model is fine for a solo developer but awkward for a team.

## Where the comparison is inconclusive

- **Encryption key management.** onecli's docs describe AES-256-GCM but don't publicly detail key storage, rotation, or AAD. agent-gateway's model is explicit (`internal/secrets/masterkey.go`, OS keyring + file fallback, rotation with transactional re-encryption). Without source review I cannot call a winner here.
- **Rate limiting and DoS posture.** Neither project surfaces a clear rate-limit story. Finding #22 recommends adding it; the same gap likely applies to onecli.
- **Supply chain.** Both are third-party code. agent-gateway's Go module graph is small and auditable; onecli's Rust crate graph plus Next.js npm graph is substantially larger. Cargo deny / npm audit disciplines matter.

## Recommendations informed by this comparison

1. **Document the single-user constraint explicitly.** If a user is considering agent-gateway for team use, they should see a pointer to "add OAuth to the dashboard" as an open item, not just discover the constraint by hitting it. Add to `docs/security-model.md` under "What you have to do right": "this is a single-operator tool; multi-user is not supported."
2. **Add a `secrets.backend` abstraction.** onecli's Bitwarden story is compelling for the "don't persist what you don't have to" school. Even without implementing a vault backend, a small interface in `internal/secrets` that lets an external store plug in makes future hardening cheap.
3. **Double down on audit.** This is agent-gateway's biggest edge over onecli. Close findings #4 and #12 so the audit log is _safe_ to rely on (proper file perms, no credential bleed through query strings), and document the audit surface as a product strength.
4. **Ship a rotation CLI.** `agent-gateway rotate-all --quarterly` that rotates admin token, CA, and flags stale agents. onecli doesn't appear to have this either; it's a small lead that matters to the compromise-response story.

---

## Updated remediation priorities

Integrating Part II findings into the existing plan:

**P0 — before next release:**

- Findings #3, #4 (file perms), #7 + #23 (cookie Secure), #2 (Argon2), #5 (hostnorm reject), #15 + #16 (config bounds, loopback), #27 (stdout token leak), #28 (sandbox-manager env leak), #29 (HTTP/2 limits), #30 (CONNECT Slowloris).

**P1 — follow-up release:**

- Findings #1 (AAD), #8 (response headers), #9 (leaf-cache LRU), #10 (CRLF on secrets), #11 (body-buffer hard error), #12 (audit query redaction), #14 (constant-time prefix), #31 (SSE event kind validate), #32 (`esc()` + `onclick`), #33 (path case sensitivity), #34 (SIGHUP config reload), #36 (operational docs).

**P2 — next quarter:**

- Findings #13, #17, #18, #19 (crypto/CA hygiene), #20, #21, #22 (daemon/SQL/rate-limit cleanups), #35 (`$EDITOR` doc), #37 (public-suffix in allowed_hosts), #38 (reload ordering), #39 (name validation).

**Track but not scheduled:**

- Findings #25, #26, #40, #41 (keychain namespacing, SSE drops, listener idiom, docs cross-reference).
