# Security Audit Remediation Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use Skill(executing-plans) to implement this plan task-by-task.

**Goal:** Implement the 28-task security remediation captured in `.designs/2026-04-23-security-audit-remediation.md`, hardening agent-gateway across filesystem perms, network boundary, crypto-at-rest, HTTP server limits, pipeline fail-closed posture, dashboard CSP, rules matching, and new SSRF/IMDS egress protection.

**Architecture:** Each task lands as a minimal tested change. Crypto work (T#1) uses a single one-shot migration guarded by schema version bump — row ciphertexts re-encrypted with AAD, new DEK/KEK hierarchy stored in `meta` table. Dashboard security (T#7) ships CSP middleware and DOM rewrite in the same commit so inline handlers don't break. Comment sweep (T#28) lands first to establish the WHY-comment baseline all new code matches. No backwards-compatibility shims — single-user tool, author is the only deployed user.

**Tech Stack:** Go 1.25, `ncruces/go-sqlite3` (WASM, no CGO), `hashicorp/golang-lru/v2` (new dep for T#8), vanilla JS for dashboard, HCL for config/rules. Tests use standard `go test -race`, integration tests under `//go:build integration`, e2e under `//go:build e2e` in `test/e2e/`. All commands assume working directory is `agent-gateway/` unless stated.

**Conventions:**

- WHY-comments on security code — explain the invariant and consequence of breaking it, not the what or the ticket number. See `.designs/2026-04-23-security-audit-remediation.md` for examples.
- File paths are relative to repo root.
- Every task ends with `make audit` passing before commit.
- Commit messages follow conventional commits (`type(scope): description`, imperative, ≤50 chars, no trailing period).

---

## Pre-flight

### Task 0: Baseline verification

**Files:** none (verification only).

**Step 1:** Confirm working tree is clean.

Run: `git status`
Expected: `nothing to commit, working tree clean` on branch `agent-gateway`.

**Step 2:** Run baseline audit.

Run: `cd agent-gateway && make audit`
Expected: tidy, fmt, lint, test, govulncheck all pass.

**Step 3:** No commit. If either check fails, stop and investigate.

---

## Phase 1 — Comment baseline (T#28)

### Task 1.1: Sweep existing security code for missing WHY-comments

**Files (modify, add comments only):**

- `agent-gateway/internal/proxy/decide.go` — `Decide` function: explain the tunnel-on-no-rule-match invariant (agent has token but no rule targets this host → tunnel, don't MITM) and why that is the right default (prevents accidental MITM of personal traffic).
- `agent-gateway/internal/proxy/pipeline.go` — secret-scope-violation 403 path (`ErrSecretHostScopeViolation`): explain that fail-closed with 403 is load-bearing — forwarding with dummy creds would leak the existence of the real credential to the scoped host, and would let a wrong-host rule silently route.
- `agent-gateway/internal/secrets/masterkey.go` — key rotation ordering: explain that the new key must be persisted BEFORE the re-encryption transaction commits, because crash mid-commit with old-key-only-persisted would be unrecoverable.
- `agent-gateway/internal/ca/leaf.go` — leaf validity window and refresh buffer: explain that the 1h buffer before `NotAfter` is to absorb clock skew and avoid handshake failures on clients with slow clocks.
- `agent-gateway/internal/agents/registry.go` — `subtle.ConstantTimeCompare`: explain that constant-time compare prevents a timing oracle that would let an attacker distinguish near-miss hashes byte-by-byte.
- `agent-gateway/internal/dashboard/auth.go` — bearer token comparison (`subtle.ConstantTimeCompare`): same reason as registry; explain why a naive `==` on the token would be exploitable.

**Step 1:** Read each listed file and identify the existing code site that enforces a security property but is missing a WHY-comment.

**Step 2:** For each site, add a `//` comment above the relevant block. Comment names the invariant and the consequence of breaking it. See design doc's "good example" for tone.

**Step 3:** Run `make audit` from `agent-gateway/`. Expected: PASS (comment-only changes, no logic impact).

**Step 4:** Commit.

```bash
git add agent-gateway/internal/proxy/decide.go agent-gateway/internal/proxy/pipeline.go \
        agent-gateway/internal/secrets/masterkey.go agent-gateway/internal/ca/leaf.go \
        agent-gateway/internal/agents/registry.go agent-gateway/internal/dashboard/auth.go
git commit -m "docs(agent-gateway): add WHY-comments on existing security code"
```

---

## Phase 2 — P0 hardening

### Task 2.1: Tighten XDG dir perms to 0o700 + startup self-check (T#3)

**Files:**

- Modify: `agent-gateway/cmd/agent-gateway/serve.go` (all `os.MkdirAll` calls)
- Modify: `agent-gateway/internal/config/config.go` (MkdirAll for config dir)
- Modify: `agent-gateway/internal/secrets/masterkey.go` (MkdirAll for key file fallback parent)
- Modify: `agent-gateway/internal/dashboard/auth.go` (MkdirAll for admin-token parent)
- Modify: `agent-gateway/internal/store/store.go` (MkdirAll for state.db parent)
- Create: `agent-gateway/internal/paths/permcheck.go` — `CheckOwnerAndMode(path string, maxMode os.FileMode) error`
- Create: `agent-gateway/internal/paths/permcheck_test.go`
- Modify: `agent-gateway/cmd/agent-gateway/serve.go` — call `CheckOwnerAndMode` on each critical dir at startup.

**Step 1:** Write failing test `permcheck_test.go`: table-driven, cases for `0o700` (ok), `0o750` (rejected), `0o755` (rejected), wrong owner (rejected if running as non-root).

```go
func TestCheckOwnerAndMode(t *testing.T) {
    tmp := t.TempDir()
    mustChmod := func(p string, m os.FileMode) { require.NoError(t, os.Chmod(p, m)) }
    cases := []struct {
        mode    os.FileMode
        wantErr bool
    }{
        {0o700, false},
        {0o750, true},
        {0o755, true},
        {0o777, true},
    }
    for _, c := range cases {
        t.Run(c.mode.String(), func(t *testing.T) {
            d := filepath.Join(tmp, c.mode.String())
            require.NoError(t, os.Mkdir(d, 0o700))
            mustChmod(d, c.mode)
            err := paths.CheckOwnerAndMode(d, 0o700)
            if c.wantErr {
                require.Error(t, err)
            } else {
                require.NoError(t, err)
            }
        })
    }
}
```

**Step 2:** Run: `go test ./internal/paths/ -run TestCheckOwnerAndMode -v`
Expected: FAIL (`CheckOwnerAndMode` undefined).

**Step 3:** Implement `permcheck.go`. Use `os.Stat`, check `info.Mode().Perm() <= maxMode`, check owner via `info.Sys().(*syscall.Stat_t).Uid == uint32(os.Getuid())`. WHY-comment: MkdirAll does not narrow permissions on an existing dir — without this check, upgraded installs silently stay at the old wider mode.

**Step 4:** Run test again. Expected: PASS.

**Step 5:** Change every `os.MkdirAll` listed above from `0o750` to `0o700`.

**Step 6:** In `serve.go` startup, call `paths.CheckOwnerAndMode(d, 0o700)` for `ConfigDir`, `DataDir`, `RulesDir`. Exit with actionable error on failure.

**Step 7:** Run `make audit`. Expected: PASS.

**Step 8:** Commit.

```bash
git commit -m "fix(agent-gateway): tighten xdg dir perms to 0o700 with startup check"
```

### Task 2.2: Chmod SQLite files 0o600 + set process umask 0o077 (T#4)

**Files:**

- Modify: `agent-gateway/internal/store/store.go` — after `sql.Open`, explicit WAL pragma + warm-up write + chmod `state.db`, `state.db-wal`, `state.db-shm` to `0o600`.
- Modify: `agent-gateway/cmd/agent-gateway/serve.go` — set `syscall.Umask(0o077)` at very start of `runServe`.
- Modify: `agent-gateway/internal/store/store_test.go` — add test verifying file modes after open.

**Step 1:** Write failing test in `store_test.go`:

```go
func TestOpen_FilesAreChmoddedTo0600(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "state.db")
    s, err := store.Open(path)
    require.NoError(t, err)
    defer s.Close()

    // Force WAL/SHM creation
    _, err = s.DB().Exec("CREATE TABLE IF NOT EXISTS _probe(x INTEGER)")
    require.NoError(t, err)

    for _, suffix := range []string{"", "-wal", "-shm"} {
        info, err := os.Stat(path + suffix)
        require.NoError(t, err, suffix)
        require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), suffix)
    }
}
```

**Step 2:** Run: `go test ./internal/store/ -run TestOpen_FilesAreChmoddedTo0600 -v`
Expected: FAIL (WAL/SHM may be 0o644).

**Step 3:** Implement: in `store.Open`, after `sql.Open`, execute `PRAGMA journal_mode=WAL` and a trivial write to force WAL/SHM creation, then `os.Chmod` all three files. WHY-comment: SQLite creates these files with process-umask defaults; explicit chmod prevents world-readable audit log, hashes, and ciphertexts when operator's umask is 0o022.

**Step 4:** Run test. Expected: PASS.

**Step 5:** In `cmd/agent-gateway/serve.go` add `syscall.Umask(0o077)` at the top of `runServe`. WHY-comment: process-wide umask tightening so future file creation defaults to owner-only even in paths that forget an explicit mode.

**Step 6:** `make audit`. Expected: PASS.

**Step 7:** Commit.

```bash
git commit -m "fix(agent-gateway): chmod sqlite files 0o600 and set umask 0o077"
```

### Task 2.3: Port ValidateLoopbackAddr from mcp-broker (T#13)

**Files:**

- Create: `agent-gateway/internal/paths/loopback.go` OR colocated in `internal/config/validate.go`. Copy the function body verbatim from `mcp-broker/internal/server/addr.go`.
- Create: `agent-gateway/internal/config/loopback_test.go`
- Modify: `agent-gateway/internal/config/validate.go` — call `ValidateLoopbackAddr` on `cfg.Proxy.Listen` and `cfg.Dashboard.Listen`.

**Step 1:** Write failing test `loopback_test.go`:

```go
func TestValidateLoopbackAddr(t *testing.T) {
    cases := []struct{ addr string; wantErr bool }{
        {"127.0.0.1:8220", false},
        {"127.0.0.2:8220", false},
        {"[::1]:8220", false},
        {"localhost:8220", false},
        {"0.0.0.0:8220", true},
        {"8.8.8.8:8220", true},
        {":8220", true},
        {"127.0.0.1", true},
    }
    for _, c := range cases {
        t.Run(c.addr, func(t *testing.T) {
            err := config.ValidateLoopbackAddr(c.addr)
            if c.wantErr { require.Error(t, err) } else { require.NoError(t, err) }
        })
    }
}
```

**Step 2:** Run: `go test ./internal/config/ -run TestValidateLoopbackAddr -v`
Expected: FAIL.

**Step 3:** Port `ValidateLoopbackAddr` from `mcp-broker/internal/server/addr.go` (lines 14-34). Include the WHY-comment from mcp-broker verbatim, noting the bearer-token-is-defense-in-depth posture.

**Step 4:** Wire into `validate.go`: call for both listener addresses, aggregate errors.

**Step 5:** Run `go test ./internal/config/ -v`. Expected: existing tests PASS + new test PASS.

**Step 6:** `make audit`. Expected: PASS.

**Step 7:** Commit.

```bash
git commit -m "feat(agent-gateway): enforce loopback-only listeners"
```

### Task 2.4: Fail closed on hostname normalization failure (T#5)

**Files:**

- Modify: `agent-gateway/internal/proxy/connect.go` — `normalizeHostSilently` (lines 23-34): return `error` instead of swallowing; callers reject CONNECT.
- Modify: `agent-gateway/internal/proxy/decide.go` — at `decide.go:144-146`, same change.
- Modify: `agent-gateway/internal/proxy/connect_test.go` — add test for malformed-host rejection.

**Step 1:** Write failing test:

```go
func TestServeConn_MalformedHostRejected(t *testing.T) {
    // Craft a CONNECT request with an un-normalizable IDN host.
    // Assert proxy returns 400 and does not open an upstream dial.
    ...
}
```

**Step 2:** Run: `go test ./internal/proxy/ -run TestServeConn_MalformedHostRejected -v`
Expected: FAIL (current code silently tunnels).

**Step 3:** Rename `normalizeHostSilently` → `normalizeHost`, change signature to `(string, error)`. In `serveConn`, on error write `400 Bad Request` to the client and close. Same pattern in `Decide`.

**Step 4:** Add WHY-comment: malformed hosts must not transit the gateway — fail-open here would let an IDN homograph bypass rules by falling through to tunnel mode with mangled audit data.

**Step 5:** Run test. Expected: PASS. Run all proxy tests: `go test ./internal/proxy/ -race -v`.

**Step 6:** `make audit`. Expected: PASS.

**Step 7:** Commit.

```bash
git commit -m "fix(agent-gateway): reject connect on hostname normalization failure"
```

### Task 2.5: Cap retention_days, max_body_buffer, max_pending (T#12)

**Files:**

- Modify: `agent-gateway/internal/config/validate.go` — add bounds checks.
- Modify: `agent-gateway/internal/config/validate_test.go` — add cases.

**Step 1:** Write failing test cases for each field:

```go
func TestValidate_Bounds(t *testing.T) {
    cases := []struct{
        name string
        mut  func(*config.Config)
        wantErr bool
    }{
        {"retention ok", func(c *config.Config){ c.Audit.RetentionDays = 90 }, false},
        {"retention too high", func(c *config.Config){ c.Audit.RetentionDays = 999999 }, true},
        {"body buffer ok", func(c *config.Config){ c.ProxyBehavior.MaxBodyBuffer = 1<<20 }, false},
        {"body buffer too high", func(c *config.Config){ c.ProxyBehavior.MaxBodyBuffer = 1<<40 }, true},
        {"max pending ok", func(c *config.Config){ c.Approval.MaxPending = 50 }, false},
        {"max pending too high", func(c *config.Config){ c.Approval.MaxPending = 100000 }, true},
    }
    // run each against a default config with the mutation applied
}
```

**Step 2:** Run: `go test ./internal/config/ -run TestValidate_Bounds -v`
Expected: FAIL.

**Step 3:** Implement bounds in `validate.go`:

```go
const (
    maxRetentionDays = 3650
    maxBodyBuffer    = 100 << 20 // 100 MiB
    maxPendingCap    = 10000
)
```

Reject out-of-range with errors naming the field and the cap. WHY-comment: caps are guardrails against self-inflicted footguns (typo'd `99999` instead of `999`); they are not a security boundary against malicious operator config.

**Step 4:** Run test. Expected: PASS.

**Step 5:** `make audit`. Expected: PASS.

**Step 6:** Commit.

```bash
git commit -m "feat(agent-gateway): cap retention, body buffer, max pending"
```

### Task 2.6: Raise Argon2id params to OWASP floor + re-hash on auth (T#2)

**Files:**

- Modify: `agent-gateway/internal/agents/registry.go:26-32` — new constants.
- Modify: `agent-gateway/internal/agents/registry.go` — verify-success path: if stored hash used old params, re-hash with new params and persist.
- Modify: `agent-gateway/internal/agents/registry_test.go` — test new params, test re-hash-on-auth.

**Step 1:** Write failing tests.

```go
func TestRegister_UsesOWASPParams(t *testing.T) {
    // Register a new agent, decode the stored hash's encoded params,
    // assert m=19*1024, t=2, p=1.
}

func TestAuthenticate_RehashesLegacyHash(t *testing.T) {
    // Seed registry with a hash computed using the OLD params (m=64, t=1, p=4).
    // Authenticate — verify success AND verify the stored hash is now updated
    // to new params by re-reading from SQLite.
}
```

**Step 2:** Run: `go test ./internal/agents/ -run "TestRegister_UsesOWASPParams|TestAuthenticate_RehashesLegacyHash" -v`
Expected: FAIL.

**Step 3:** In `registry.go`, change constants to `argon2Time = 2, argon2Memory = 19 * 1024, argon2Threads = 1`. WHY-comment: OWASP 2023 floor; dropping below re-introduces GPU-feasible offline attack on exfiltrated DB.

**Step 4:** In the verify path, after `ConstantTimeCompare` succeeds, check whether the stored hash's params match current constants. If not, compute a new hash with current params and persist via a parameterized UPDATE. Handle write failure gracefully — don't fail auth.

**Step 5:** Run tests. Expected: PASS.

**Step 6:** `make audit`. Expected: PASS.

**Step 7:** Commit.

```bash
git commit -m "feat(agent-gateway): raise argon2id params and rehash on auth"
```

### Task 2.7: Cookie.Secure from request scheme (T#6)

**Files:**

- Modify: `agent-gateway/internal/dashboard/auth.go:116-126` (query-token exchange cookie).
- Modify: `agent-gateway/internal/dashboard/dashboard.go:193-197` (unauthorized POST cookie).
- Modify: `agent-gateway/internal/dashboard/auth_test.go` — add tests for both cases.

**Step 1:** Write failing tests that drive through both handlers with `r.TLS` set vs unset and with `X-Forwarded-Proto: https` vs absent; assert `Set-Cookie` contains `Secure` only when one of those is true.

**Step 2:** Run tests. Expected: FAIL.

**Step 3:** Extract helper `cookieSecure(r *http.Request) bool` returning `r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"`. Use at both sites. Delete the two `TODO(TLS)` comments.

**Step 4:** Run tests. Expected: PASS.

**Step 5:** `make audit`. Expected: PASS.

**Step 6:** Commit.

```bash
git commit -m "fix(agent-gateway): set cookie secure from request scheme"
```

### Task 2.8: HTTP/2 + HTTP/1 server limits (T#19)

**Files:**

- Modify: `agent-gateway/internal/proxy/connect.go:294-302` (`serveH2`) and `serveH1`.
- Modify: `agent-gateway/internal/proxy/connect_test.go` — add assertion tests.

**Step 1:** Write failing test asserting the configured limits on the `http2.Server` and `http.Server` values (expose via `export_test.go` helpers if needed):

```go
func TestServeH2_LimitsConfigured(t *testing.T) {
    p := proxy.NewForTest(...)
    srv := proxy.H2ServerFor(p)
    require.Equal(t, uint32(100), srv.MaxConcurrentStreams)
    require.Equal(t, uint32(16<<10), srv.MaxReadFrameSize)
    require.Equal(t, uint32(4096), srv.MaxDecoderHeaderTableSize)
    require.Equal(t, uint32(4096), srv.MaxEncoderHeaderTableSize)
}
```

**Step 2:** Run. Expected: FAIL (fields are zero).

**Step 3:** Set limits in both servers per the design. WHY-comment names CVE-2023-44487 (Rapid Reset) and CVE-2024-27316 (CONTINUATION flood), explains why stdlib defaults (tuned for public-internet servers) are too loose for a sandbox proxy.

**Step 4:** Run test. Expected: PASS.

**Step 5:** `make audit`. Expected: PASS.

**Step 6:** Commit.

```bash
git commit -m "fix(agent-gateway): set http2 and http1 server limits"
```

### Task 2.9: Read deadline on CONNECT (T#20)

**Files:**

- Modify: `agent-gateway/internal/proxy/connect.go:39-53` (`serveConn`).
- Modify: `agent-gateway/internal/proxy/connect_test.go` — add Slowloris test.

**Step 1:** Write failing test: open a `net.Pipe` one end, write nothing, pass the other end to `serveConn`; assert the goroutine returns within `readHeaderTimeout + slack`.

**Step 2:** Run. Expected: FAIL (hangs / times out).

**Step 3:** Add `conn.SetReadDeadline(time.Now().Add(p.readHeaderTimeout))` before `http.ReadRequest`; clear with `conn.SetDeadline(time.Time{})` after success. WHY-comment: the CONNECT outer handshake has no stdlib deadline; a slow client without this would pin a goroutine until GC.

**Step 4:** Run test. Expected: PASS.

**Step 5:** `make audit`. Expected: PASS.

**Step 6:** Commit.

```bash
git commit -m "fix(agent-gateway): set read deadline on connect"
```

---

## Phase 3 — P1: Crypto overhaul (T#1)

Multiple sub-tasks because this is the largest single design piece. Each sub-task commits independently.

### Task 3.1: Add `meta` table and schema version

**Files:**

- Modify: `agent-gateway/internal/store/` migrations embedded SQL — add `meta` table (or columns if one exists) with `dek_wrapped BLOB`, `dek_nonce BLOB`, `kek_kdf_salt BLOB`, plus `schema_version INTEGER` if not already tracked.
- Modify: `agent-gateway/internal/store/store_test.go` — assert migration runs on fresh DB.

**Step 1:** Write failing test that opens a fresh store and reads `schema_version`, expecting the new version N.

**Step 2:** Run. Expected: FAIL.

**Step 3:** Add migration SQL bumping to N. Include columns/table. Run migration on open if version < N.

**Step 4:** Run test. Expected: PASS.

**Step 5:** Commit.

```bash
git commit -m "chore(agent-gateway): add meta schema for dek/kek storage"
```

### Task 3.2: DEK generation + wrap/unwrap plumbing

**Files:**

- Modify: `agent-gateway/internal/secrets/crypto.go` — add `wrapDEK(kek, dek) (ct, nonce, error)` and `unwrapDEK(kek, ct, nonce) (dek, error)`, both using AES-256-GCM with no AAD (DEK is single-purpose, not row-keyed).
- Modify: `agent-gateway/internal/secrets/masterkey.go` — add `deriveKEK(masterKey, salt) []byte` using HKDF-SHA256.
- Create: `agent-gateway/internal/secrets/crypto_test.go` if not exists, or add to existing.

**Step 1:** Write failing tests: wrap → unwrap roundtrip, wrong KEK fails, tampered ciphertext fails.

**Step 2:** Run. Expected: FAIL.

**Step 3:** Implement. WHY-comment on `deriveKEK`: HKDF over the keychain master key separates DEK-wrap from row-encryption — rotating the keychain master rewraps only the ~60-byte DEK blob, not every row.

**Step 4:** Run tests. Expected: PASS.

**Step 5:** Commit.

```bash
git commit -m "feat(agent-gateway): add dek wrap/unwrap and kek derivation"
```

### Task 3.3: Row encryption with AAD

**Files:**

- Modify: `agent-gateway/internal/secrets/crypto.go` — add `encryptRow(dek, name, scope, plaintext) (ct, nonce, error)` and `decryptRow(dek, name, scope, ct, nonce) (plaintext, error)`. AAD = `[]byte(name + "\x00" + scope)`.
- Modify: `agent-gateway/internal/secrets/crypto_test.go` — add tests: roundtrip; swap-attack (encrypt with (name1,scope1), decrypt with (name2,scope2) fails).

**Step 1:** Write failing tests including the swap-attack test.

**Step 2:** Run. Expected: FAIL.

**Step 3:** Implement with GCM + AAD. WHY-comment: AAD binds ciphertext to row identity; a DB-write-capable attacker cannot swap ciphertext between rows without producing a decrypt failure, which prevents the "inject wrong credential" attack.

**Step 4:** Run tests. Expected: PASS.

**Step 5:** Commit.

```bash
git commit -m "feat(agent-gateway): row encryption with name||scope aad"
```

### Task 3.4: One-shot migration: re-encrypt all rows under DEK+AAD

**Files:**

- Modify: `agent-gateway/internal/secrets/store.go` — on schema-version bump, run `migrateToDEK(ctx, tx) error`: generate DEK, derive KEK from active master key with fresh salt, wrap DEK, for each secrets row decrypt-under-old-master-key-no-AAD + re-encrypt-under-DEK-with-AAD, write new columns, commit. Then bump schema version.
- Modify: `agent-gateway/internal/secrets/integration_test.go` — end-to-end test: seed DB with old format, open store, assert all rows decrypt correctly under new format.

**Step 1:** Write failing integration test that seeds a pre-migration DB, opens store, reads a secret, asserts plaintext matches expected value.

**Step 2:** Run. Expected: FAIL.

**Step 3:** Implement `migrateToDEK`. Run in a single transaction. WHY-comment: single-transaction guarantees atomicity — on crash, schema version remains unbumped and next start retries from the old format.

**Step 4:** Run test. Expected: PASS.

**Step 5:** Commit.

```bash
git commit -m "feat(agent-gateway): migrate secrets to dek+aad format"
```

### Task 3.5: Simplify master-key rotation to rewrap-only

**Files:**

- Modify: `agent-gateway/internal/secrets/masterkey.go` — `RotateMasterKey` path: derive new KEK from new master, unwrap DEK with old KEK, rewrap under new KEK, update meta, done. No row iteration.
- Modify: `agent-gateway/cmd/agent-gateway/master_key_test.go` — assert rotation touches only the meta row.

**Step 1:** Write failing test: insert 1000 secrets, rotate master key, assert all secrets still decrypt AND assert `UPDATE` count on secrets table during rotation is 0 (only meta was touched).

**Step 2:** Run. Expected: FAIL (old code re-encrypts rows).

**Step 3:** Rewrite rotation to rewrap DEK only. Preserve the "new key persisted before commit" invariant from existing code. WHY-comment: rotation complexity was previously O(rows); now O(1).

**Step 4:** Run test. Expected: PASS.

**Step 5:** `make audit`. Expected: PASS.

**Step 6:** Commit.

```bash
git commit -m "refactor(agent-gateway): rotate master key via dek rewrap only"
```

---

## Phase 4 — P1: Dashboard security (T#7)

### Task 4.1: Rewrite dashboard inline handlers to DOM APIs (prerequisite for CSP)

**Files:**

- Modify: `agent-gateway/internal/dashboard/app.js:259-264` and any other `innerHTML`-with-interpolation site.

**Step 1:** Audit `app.js` for every `onclick=`, every `innerHTML` concatenation. List each site.

**Step 2:** For each, replace with `document.createElement` + `addEventListener` closure. Strings go through `textContent`, not `innerHTML`. No user-derived value reaches an attribute.

**Step 3:** Manually smoke-test the dashboard — approval buttons work, audit pagination works, rules page renders, agents/secrets pages render. Document in the commit message which handlers were converted.

**Step 4:** `make audit`. Expected: PASS (no Go changes but confirms nothing broke).

**Step 5:** Commit.

```bash
git commit -m "refactor(agent-gateway): dashboard handlers via addEventListener"
```

### Task 4.2: CSP + security headers middleware

**Files:**

- Create: `agent-gateway/internal/dashboard/headers.go` — `secureHeaders(next http.Handler) http.Handler`.
- Create: `agent-gateway/internal/dashboard/headers_test.go`.
- Modify: `agent-gateway/internal/dashboard/dashboard.go:107-132` — wrap mux in `secureHeaders`.

**Step 1:** Write failing test: drive a `/api/pending` request through the handler, assert `Content-Security-Policy`, `X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy` are present with expected values. Assert `/ca.pem` returns without CSP interfering with its content-type.

**Step 2:** Run. Expected: FAIL.

**Step 3:** Implement middleware. CSP value per design:
`default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'`
Skip CSP on `/ca.pem` route or scope separately. WHY-comment: `script-src 'self'` is load-bearing because inline `onclick=` handlers would be blocked — this is why Task 4.1 is a prerequisite.

**Step 4:** Run test. Expected: PASS.

**Step 5:** Smoke-test dashboard in a browser, confirm no CSP violations in the console.

**Step 6:** `make audit`. Expected: PASS.

**Step 7:** Commit.

```bash
git commit -m "feat(agent-gateway): add dashboard csp and security headers"
```

---

## Phase 5 — P1: Pipeline hardening

### Task 5.1: Reject CRLF/control chars in secret values (T#9)

**Files:**

- Modify: `agent-gateway/internal/inject/template.go:85-111` — after `store.Get`, validate value.
- Modify: `agent-gateway/internal/inject/injector.go` — new `ErrSecretInvalid`, 403 path.
- Modify: `agent-gateway/internal/inject/injector_test.go` — add cases.

**Step 1:** Write failing tests: secret with `\r\n`, `\x01`, `\x7f` all rejected with `ErrSecretInvalid`; secret with `\t` and printable chars accepted.

**Step 2:** Run. Expected: FAIL.

**Step 3:** Validation loop over bytes. Return `ErrSecretInvalid` on first bad byte. WHY-comment: last defensive layer before credentials hit the network; makes no assumption about `http.Header.Set` validation (which has historically let CRLF-adjacent bytes through HTTP/2).

**Step 4:** In pipeline, map `ErrSecretInvalid` → 403 synthesis.

**Step 5:** Run tests. Expected: PASS.

**Step 6:** Commit.

```bash
git commit -m "fix(agent-gateway): reject control chars in secret values"
```

### Task 5.2: Fail-closed on body-buffer hard I/O error (T#10)

**Files:**

- Modify: `agent-gateway/internal/proxy/buffer.go:85-89`, `pipeline.go:236-240`.
- Modify: `agent-gateway/internal/proxy/buffer_test.go` — add I/O error case.

**Step 1:** Write failing test: request has a matcher requiring body buffering; body reader returns a hard error; assert pipeline synthesises 403 with `X-Request-ID` rather than forwarding.

**Step 2:** Run. Expected: FAIL (current code logs and proceeds).

**Step 3:** In pipeline, when `bufferBody` returns a hard read error AND `NeedsBodyBuffer` was true, synthesise 403 with `X-Request-ID`. Keep fail-soft when no matcher applies. WHY-comment: aligns with the existing `body_matcher_bypassed:timeout` fail-closed path — silently skipping body matchers on I/O error would let a broken agent or racy upstream bypass rules that a human reviewer had explicitly configured.

**Step 4:** Run test. Expected: PASS.

**Step 5:** Commit.

```bash
git commit -m "fix(agent-gateway): fail closed on body buffer io error"
```

### Task 5.3: Case-insensitive path + method matching (T#22)

**Files:**

- Modify: `agent-gateway/internal/rules/match.go:152-161`.
- Modify: `agent-gateway/internal/rules/parse.go:545` (`compileGlob`) if compile-time lowercasing is cleaner.
- Modify: `agent-gateway/internal/rules/match_test.go` — add cases.
- Modify: `agent-gateway/docs/rules.md` — document case behavior.

**Step 1:** Write failing tests: rule path `/admin/*` matches `/ADMIN/foo`; rule method `POST` matches `post`; header value regex stays case-sensitive unless `(?i)` flag.

**Step 2:** Run. Expected: FAIL.

**Step 3:** Lowercase `req.Path` in matcher, uppercase `req.Method`. Document header-value behavior. WHY-comment: upstream services commonly normalize path case; a deny rule that silently misses `/ADMIN/*` is a trap, not a feature.

**Step 4:** Run tests. Expected: PASS.

**Step 5:** Update `docs/rules.md` with a "Case handling" subsection.

**Step 6:** Commit.

```bash
git commit -m "fix(agent-gateway): case-insensitive path and method matching"
```

---

## Phase 6 — P1: Rule and secret scoping

### Task 6.1: Reject public-suffix in secret allowed_hosts (T#24)

**Files:**

- Modify: `agent-gateway/internal/secrets/store.go:345-389` — apply `warnIfPublicSuffix`-style check, but reject rather than warn.
- Modify: `agent-gateway/internal/config/validate.go` — extract the public-suffix helper so both sites share it.
- Modify: `agent-gateway/internal/secrets/store_test.go` — add cases for `*.co`, `*.io`, `*.com` (all reject).

**Step 1:** Write failing tests.

**Step 2:** Run. Expected: FAIL.

**Step 3:** Extract helper `func matchesPublicSuffix(pattern string) (bool, string)` in a shared location (maybe `internal/paths/` or new `internal/hostmatch/`). Reject in secrets with a clear error pointing at the pattern. WHY-comment: allowed_hosts is the credential-scoping layer — unlike no_intercept_hosts where too-broad means "more MITM," a too-broad allowed_hosts routes real credentials to unintended hosts.

**Step 4:** Run tests. Expected: PASS.

**Step 5:** Commit.

```bash
git commit -m "fix(agent-gateway): reject public-suffix patterns in allowed_hosts"
```

### Task 6.2: Per-agent approval cap (T#18)

**Files:**

- Modify: `agent-gateway/internal/approval/broker.go` — track per-agent pending count.
- Modify: `agent-gateway/internal/config/` — add `approval.max_pending_per_agent` (default 10).
- Modify: `agent-gateway/internal/approval/broker_test.go` — add starvation test.

**Step 1:** Write failing test: agent A fills its per-agent cap with 10 pending; agent B requests; B gets through (global cap 50, agent B cap free); A's 11th request returns `ErrQueueFull`.

**Step 2:** Run. Expected: FAIL (current code would reject A's 11th only if global cap hit).

**Step 3:** Add `map[string]int` pending-per-agent counter in broker, increment on Request, decrement on Decide/timeout/cancel. Return `ErrQueueFull` synchronously on per-agent cap breach. WHY-comment: per-agent cap prevents one runaway agent from starving parallel agents when multiple sandboxes share the same gateway.

**Step 4:** Run tests. Expected: PASS.

**Step 5:** `make audit`. Expected: PASS.

**Step 6:** Commit.

```bash
git commit -m "feat(agent-gateway): per-agent approval cap"
```

---

## Phase 7 — P1: New features

### Task 7.1: Leaf-cert LRU + clock-skew buffer (T#8)

**Files:**

- Modify: `agent-gateway/go.mod` + `go.sum` — add `github.com/hashicorp/golang-lru/v2`.
- Modify: `agent-gateway/internal/ca/leaf.go:31-59, 82-99` — replace `sync.Map` with LRU.
- Modify: `agent-gateway/internal/ca/leaf_test.go` — add LRU eviction + skew-handling tests.

**Step 1:** Write failing tests: cache holds up to 10 000 entries; 10 001st insertion evicts oldest; clock-skew buffer configurable (e.g., 5 min) and applied to `NotBefore`/`NotAfter` checks.

**Step 2:** Run. Expected: FAIL.

**Step 3:** `go get github.com/hashicorp/golang-lru/v2@latest`. Implement. WHY-comment: unbounded cache is a DoS vector authenticated agents can trigger by CONNECT-ing to unique hosts; LRU caps worst-case memory at (10 000 × cert+key size).

**Step 4:** Run tests. Expected: PASS.

**Step 5:** `make audit`. Expected: PASS.

**Step 6:** Commit.

```bash
git commit -m "feat(agent-gateway): bounded leaf-cert lru cache"
```

### Task 7.2: SSRF/IMDS netguard (T#26)

**Files:**

- Create: `agent-gateway/internal/netguard/netguard.go` — `BlockPrivate(addr string, allowPrivate bool) error`.
- Create: `agent-gateway/internal/netguard/netguard_test.go`.
- Modify: `agent-gateway/cmd/agent-gateway/serve.go` — wire into upstream `http.Transport`'s `DialContext`.
- Modify: `agent-gateway/internal/config/` — add `proxy_behavior.allow_private_upstream = false` default.
- Modify: `agent-gateway/internal/config/default.hcl` — document the knob.

**Step 1:** Write failing tests: `169.254.169.254` blocked unconditionally; `fd00:ec2::254` blocked unconditionally; `10.0.0.1` blocked when `allowPrivate=false`, allowed when `allowPrivate=true`; `127.0.0.1` blocked when `allowPrivate=false`; public IP `8.8.8.8` always allowed; DNS resolution happens before check (hostname `metadata.google.internal` → whatever IP → blocked if IMDS or RFC1918).

**Step 2:** Run. Expected: FAIL.

**Step 3:** Implement `BlockPrivate`. Use `net.IP.IsLoopback`, `IsLinkLocalUnicast`, `IsPrivate`. Explicitly check IMDS addresses before the `allowPrivate` branch. Wire into upstream dialer. WHY-comment: SSRF to cloud IMDS (169.254.169.254) is the textbook exfil path for cloud-hosted proxies; the unconditional block for IMDS is non-negotiable even under `allowPrivate=true` because legitimate upstreams never need IMDS.

**Step 4:** Run tests. Expected: PASS.

**Step 5:** `make audit`. Expected: PASS.

**Step 6:** Commit.

```bash
git commit -m "feat(agent-gateway): ssrf/imds egress guard"
```

### Task 7.3: Typed SSE EventKind (T#21)

**Files:**

- Modify: `agent-gateway/internal/dashboard/sse.go` — `type EventKind string`, constants, `Broadcast(EventKind, any)`.
- Modify: all callers of `Broadcast` (likely in `dashboard.go` and approval-integration code).
- Modify: `agent-gateway/internal/dashboard/sse_test.go`.

**Step 1:** Write failing test: `Broadcast` signature takes `EventKind`; untyped string cannot be passed (compile-time check).

**Step 2:** Run `go build ./internal/dashboard/...` against a test file using a raw string. Expected: compile fails.

**Step 3:** Introduce `type EventKind string`, exported constants (`EventApproval`, `EventRequest`, `EventDecided`, `EventRemoved` — match existing callers). Update `Broadcast`. Update callers. WHY-comment: type prevents untrusted string from reaching the SSE frame format; no runtime validation needed because compiler enforces it.

**Step 4:** Run tests + build. Expected: PASS.

**Step 5:** `make audit`. Expected: PASS.

**Step 6:** Commit.

```bash
git commit -m "refactor(agent-gateway): type sse event kind"
```

---

## Phase 8 — P2 cleanups

Bundle into a single commit since each is a tiny change.

### Task 8.1: Bundled P2 cleanups (T#14, T#15, T#16, T#17, T#25)

**Files:**

- Modify: `agent-gateway/internal/ca/leaf.go:191` — `MinVersion: tls.VersionTLS13`.
- Modify: `agent-gateway/cmd/agent-gateway/serve.go:228` — same.
- Modify: `agent-gateway/internal/ca/root.go:165-175` — add `MaxPathLen: 0, MaxPathLenZero: true`.
- Modify: `agent-gateway/internal/daemon/pidfile.go:50-69` — `O_CREATE|O_EXCL|O_WRONLY, 0o600` + stale-retry.
- Modify: `agent-gateway/internal/audit/audit.go:240-248` — `?` placeholder for LIMIT/OFFSET.
- Modify: `agent-gateway/internal/dashboard/dashboard.go:305-311` — clamp parsed limit to 10000.
- Modify: `agent-gateway/cmd/agent-gateway/serve.go:373-394` — reorder SIGHUP: agents → rules → injector → secrets → admin → CA.
- Modify: matching `*_test.go` files for each change.

**Step 1:** For each, write a focused failing test (brief).

**Step 2:** Run all. Expected: FAIL across the board.

**Step 3:** Implement each change with WHY-comment.

**Step 4:** Run tests. Expected: all PASS.

**Step 5:** `make audit`. Expected: PASS.

**Step 6:** Commit.

```bash
git commit -m "fix(agent-gateway): p2 hardening cleanups"
```

---

## Phase 9 — Documentation

### Task 9.1: docs/security-model.md additions (T#11)

**Files:**

- Modify: `agent-gateway/docs/security-model.md`.

**Step 1:** Add three sections per the design:

1. "CA trust-store scope" — one paragraph: CA is for sandbox trust, not host trust; importing into host turns `ca.key` leak into arbitrary-MITM against host traffic.
2. "Tunnel-mode XFF" — one paragraph: tunnel relays raw TCP; agents can forge `X-Forwarded-For` / `True-Client-IP` / `Forwarded`; upstreams treating these as authoritative must not trust tunnel-routed traffic.
3. "Operational security" — audit-log sensitivity (perms, retention, don't share); compromise-response playbook for admin token / `ca.key` / agent token / master key leaks.

**Step 2:** Run `make audit` from `agent-gateway/`. Expected: PASS (doc-only).

**Step 3:** Commit.

```bash
git commit -m "docs(agent-gateway): expand security-model.md with ops guidance"
```

### Task 9.2: SIGHUP reload table (T#23)

**Files:**

- Modify: `agent-gateway/docs/cli.md` (or wherever SIGHUP is referenced).

**Step 1:** Lift the table from `SECURITY_AUDIT.md:1082-1097`. Categorize rows reloaded vs restart-required.

**Step 2:** Commit.

```bash
git commit -m "docs(agent-gateway): document sighup reload semantics"
```

### Task 9.3: Non-cooperative sandbox iptables recipe (T#27)

**Files:**

- Modify: `agent-gateway/docs/sandbox-manager.md`.

**Step 1:** Add a "Non-cooperative sandbox" subsection. Show Lima iptables rules pinning egress so only `host.lima.internal:8220` is reachable. Reference agent-vault prior art in a footnote.

**Step 2:** Commit.

```bash
git commit -m "docs(agent-gateway): add non-cooperative sandbox iptables recipe"
```

---

## Final verification

### Task 10.1: Full audit + e2e smoke

**Files:** none.

**Step 1:** From `agent-gateway/` run: `make audit`. Expected: PASS.

**Step 2:** Run: `make test-integration`. Expected: PASS.

**Step 3:** Run: `make test-e2e`. Expected: PASS.

**Step 4:** Manually launch `agent-gateway serve`, open the dashboard in a browser, execute an agent-token-authenticated CONNECT through the proxy, verify audit log row appears in real time, verify approval broker gates correctly, verify secret injection works, verify CA certificate is accepted by a sandbox client after re-importing.

**Step 5:** If everything passes, plan is complete. If anything fails, isolate the regressing task from `git log --oneline` and fix forward on a new commit.

---

## Documentation task gate

Each task in this plan already owns its doc impact (rules.md for T#22, security-model.md for T#11/24, sandbox-manager.md for T#27, cli.md for T#23). No standalone end-of-plan documentation pass is needed beyond Tasks 9.1–9.3 above.

<!-- Plan is complete. Invoke Skill(executing-plans) to implement. -->
