# agent-gateway Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use Skill(executing-plans) to implement this plan task-by-task.

**Goal:** Build `agent-gateway`, a host-native Go MITM proxy that matches sandboxed-agent HTTP(S) traffic against declarative HCL rules and swaps dummy credentials for real ones at request time.

**Architecture:** Single Go binary with Cobra CLI. One daemon process (`agent-gateway serve`) binds `:8220` (proxy) and `:8221` (dashboard). State lives in a single SQLite database under XDG paths; master key in the OS keychain (file fallback). CLI state-mutating commands (`secret set`, `agent add`, `rules reload`, etc.) write DB/files and signal the daemon via `SIGHUP` to reload the `atomic.Pointer` snapshots of rules, agents, and secrets. Rules live in `~/.config/agent-gateway/rules.d/*.hcl`, loaded in filename order, first-match-wins. MITM decision (`tunnel | mitm | reject`) is made at CONNECT time; `rule verdict` dispatch (`allow | deny | require-approval`) happens inside the MITM'd session. Dashboard is an embedded vanilla-JS SPA with an SSE live feed; approvers decide via `/api/decide`. Design doc: `.designs/2026-04-16-agent-gateway.md`.

**Tech Stack:** Go 1.25 (no CGO), Cobra CLI, `log/slog`, `hashicorp/hcl/v2` for rule parsing, `ncruces/go-sqlite3` (WASM; matches `mcp-broker`), `zalando/go-keyring` for OS keychain, `golang.org/x/crypto/argon2` for token hashing, `golang.org/x/crypto/chacha20poly1305` _or_ `crypto/aes` + `crypto/cipher` for AES-256-GCM, `oklog/ulid/v2` for request IDs, `PaesslerAG/jsonpath` (or equivalent) for JSON body matchers. Testing: `testify` (unit + integration), build-tag-gated `//go:build integration` / `//go:build e2e`, mock backend via `httptest`.

---

## Conventions used throughout this plan

- All paths are **relative to the repo root**. The new tool lives under `agent-gateway/`.
- Each task ends with a commit. Commit messages use conventional-commits (`feat(agent-gateway): …`, `test(agent-gateway): …`, `chore(agent-gateway): …`).
- Unit tests run with `go test ./...` from the tool directory. Integration and e2e tests use build tags `integration` and `e2e` respectively (matches `mcp-broker`).
- SQLite driver is `github.com/ncruces/go-sqlite3/driver` + blank `github.com/ncruces/go-sqlite3/embed` import (CGO-free). All tables use `WAL` journal mode via `PRAGMA journal_mode=WAL` at open time.
- Constructors return interfaces; all interfaces are listed in §13 of the design. No package-level globals, no `init()` side effects.
- Logging: `log/slog` dependency-injected. Request IDs threaded via `context.Context`.
- Every task lists a failing-test step before implementation. Run the failing test to confirm the red phase, then implement, then re-run. If the test turns out wrong during implementation, fix the test and document why in the commit body.

---

## Task 0: Module bootstrap

**Files:**

- Create: `agent-gateway/go.mod`
- Create: `agent-gateway/go.sum` (via `go mod tidy`)
- Create: `agent-gateway/Makefile`
- Create: `agent-gateway/cmd/agent-gateway/main.go`
- Create: `agent-gateway/CLAUDE.md`
- Modify: `go.work` (add `./agent-gateway`)
- Modify: `Makefile` (root — add `agent-gateway` to `TOOLS`)

**Step 1: Write the bootstrap test**

Create `agent-gateway/cmd/agent-gateway/main_test.go`:

```go
package main

import "testing"

func TestMainPackageCompiles(t *testing.T) {
    // Placeholder so `go test ./...` has at least one target while the
    // package is empty. Real tests come with subsequent tasks.
}
```

**Step 2: Run the test**

From `agent-gateway/`: `go test ./...`

Expected: FAIL — `go.mod` missing / package has no files.

**Step 3: Minimal implementation**

`agent-gateway/go.mod` (module path matches `mcp-broker` convention):

```
module github.com/averycrespi/agent-tools/agent-gateway

go 1.25.8
```

`agent-gateway/cmd/agent-gateway/main.go`:

```go
package main

func main() {}
```

`agent-gateway/Makefile` (mirror `mcp-broker/Makefile`):

```makefile
.PHONY: build install test test-integration test-e2e lint fmt tidy audit

build:
	go build -o agent-gateway ./cmd/agent-gateway

install:
	GOBIN=$(shell go env GOPATH)/bin go install ./cmd/agent-gateway

test:
	go test -race ./...

test-integration:
	go test -race -tags=integration ./...

test-e2e:
	go test -race -tags=e2e -timeout=120s ./test/e2e/...

lint:
	go tool golangci-lint run ./...

fmt:
	go tool goimports -w .

tidy:
	go mod tidy && go mod verify

audit: tidy fmt lint test
	go tool govulncheck ./... || echo "govulncheck: review above (stdlib vulns require Go upgrade)"
```

Edit `go.work` — add `./agent-gateway` to the `use (...)` block (keep entries sorted).

Edit root `Makefile` — add `agent-gateway` to `TOOLS := …`.

`agent-gateway/CLAUDE.md` — brief. Write only the headings now: `# agent-gateway`, `## Development`, `## Architecture`, `## Conventions`. Leave bodies short; they fill out as tasks complete.

**Step 4: Verify**

From `agent-gateway/`: `go mod tidy && go test ./... && go build ./...`. From repo root: `make build` (expect `agent-gateway` target to run alongside others).

Expected: PASS / clean build.

**Step 5: Commit**

```bash
git add agent-gateway/ go.work Makefile
git commit -m "feat(agent-gateway): scaffold Go module and Makefile"
```

---

## Task 1: XDG paths + OS-abstraction helper

**Files:**

- Create: `agent-gateway/internal/paths/paths.go`
- Create: `agent-gateway/internal/paths/paths_test.go`

**Step 1: Write the failing test**

```go
package paths_test

import (
    "path/filepath"
    "testing"

    "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
)

func TestConfigDir_XDGOverride(t *testing.T) {
    t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
    got := paths.ConfigDir()
    want := filepath.Join("/tmp/xdg", "agent-gateway")
    if got != want {
        t.Fatalf("ConfigDir() = %q, want %q", got, want)
    }
}

func TestDataDir_XDGOverride(t *testing.T) {
    t.Setenv("XDG_DATA_HOME", "/tmp/xdg-data")
    got := paths.DataDir()
    want := filepath.Join("/tmp/xdg-data", "agent-gateway")
    if got != want {
        t.Fatalf("DataDir() = %q, want %q", got, want)
    }
}

func TestNamedPaths(t *testing.T) {
    t.Setenv("XDG_CONFIG_HOME", "/c")
    t.Setenv("XDG_DATA_HOME", "/d")
    cases := []struct{ got, want string }{
        {paths.ConfigFile(), "/c/agent-gateway/config.hcl"},
        {paths.RulesDir(), "/c/agent-gateway/rules.d"},
        {paths.AdminTokenFile(), "/c/agent-gateway/admin-token"},
        {paths.MasterKeyFile(), "/c/agent-gateway/master.key"},
        {paths.PIDFile(), "/c/agent-gateway/agent-gateway.pid"},
        {paths.StateDB(), "/d/agent-gateway/state.db"},
        {paths.CAKey(), "/d/agent-gateway/ca.key"},
        {paths.CACert(), "/d/agent-gateway/ca.pem"},
    }
    for _, tc := range cases {
        if tc.got != tc.want {
            t.Errorf("got %q, want %q", tc.got, tc.want)
        }
    }
}
```

**Step 2: Run** — `go test ./internal/paths/...` — Expected: FAIL (package missing).

**Step 3: Implement**

Implement `paths.ConfigDir()`, `DataDir()`, and the named path functions. Replicate the XDG helpers from `mcp-broker/internal/config/config.go` but as a dedicated package because both `cmd/` and several `internal/` packages will call them.

Example shape (keep it minimal, no extra exports):

```go
package paths

import (
    "os"
    "path/filepath"
)

const appName = "agent-gateway"

func configHome() string {
    if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
        return v
    }
    h, _ := os.UserHomeDir()
    return filepath.Join(h, ".config")
}

func dataHome() string {
    if v := os.Getenv("XDG_DATA_HOME"); v != "" {
        return v
    }
    h, _ := os.UserHomeDir()
    return filepath.Join(h, ".local", "share")
}

func ConfigDir() string    { return filepath.Join(configHome(), appName) }
func DataDir() string      { return filepath.Join(dataHome(), appName) }
func ConfigFile() string   { return filepath.Join(ConfigDir(), "config.hcl") }
func RulesDir() string     { return filepath.Join(ConfigDir(), "rules.d") }
func AdminTokenFile() string { return filepath.Join(ConfigDir(), "admin-token") }
func MasterKeyFile() string  { return filepath.Join(ConfigDir(), "master.key") }
func PIDFile() string      { return filepath.Join(ConfigDir(), appName+".pid") }
func StateDB() string      { return filepath.Join(DataDir(), "state.db") }
func CAKey() string        { return filepath.Join(DataDir(), "ca.key") }
func CACert() string       { return filepath.Join(DataDir(), "ca.pem") }
```

**Step 4: Verify** — `go test ./internal/paths/...` — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/internal/paths/
git commit -m "feat(agent-gateway): add XDG paths helper"
```

---

## Task 2: HCL config loader with defaults

**Files:**

- Create: `agent-gateway/internal/config/config.go`
- Create: `agent-gateway/internal/config/config_test.go`
- Create: `agent-gateway/internal/config/default.hcl` (embedded default)

**Step 1: Write the failing tests**

Two test cases:

1. Loading a missing config file writes the default and returns it.
2. Loading a partial config file (e.g. only `proxy.listen = "…"`) preserves user overrides while backfilling every other default.

```go
func TestLoad_MissingWritesDefault(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "config.hcl")

    cfg, err := config.Load(path)
    require.NoError(t, err)

    assert.Equal(t, "127.0.0.1:8220", cfg.Proxy.Listen)
    assert.Equal(t, "127.0.0.1:8221", cfg.Dashboard.Listen)
    assert.Equal(t, 90, cfg.Audit.RetentionDays)
    assert.Equal(t, 5*time.Minute, cfg.Approval.Timeout)
    assert.Equal(t, 50, cfg.Approval.MaxPending)
    assert.Equal(t, int64(1<<20), cfg.ProxyBehavior.MaxBodyBuffer)

    // File was created with 0600.
    st, err := os.Stat(path)
    require.NoError(t, err)
    assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())
}

func TestLoad_PartialOverridesKept(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "config.hcl")
    require.NoError(t, os.WriteFile(path, []byte(`
proxy { listen = "127.0.0.1:9999" }
`), 0o600))

    cfg, err := config.Load(path)
    require.NoError(t, err)
    assert.Equal(t, "127.0.0.1:9999", cfg.Proxy.Listen)
    // Unspecified fields retain defaults.
    assert.Equal(t, "127.0.0.1:8221", cfg.Dashboard.Listen)
    assert.Equal(t, 5*time.Minute, cfg.Approval.Timeout)
}

func TestLoad_ParseError(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "config.hcl")
    require.NoError(t, os.WriteFile(path, []byte(`proxy { listen = }`), 0o600))
    _, err := config.Load(path)
    require.Error(t, err)
}
```

**Step 2: Run** — `go test ./internal/config/...` — Expected: FAIL.

**Step 3: Implement**

- Define the `Config` struct matching the `config.hcl` surface from design §9 (`Proxy`, `Dashboard`, `Rules`, `Secrets`, `Audit`, `Approval`, `ProxyBehavior`, `Timeouts`, `Log`).
- Use `github.com/hashicorp/hcl/v2/gohcl` + `hclparse.NewParser()` for decoding. Durations: decode as string, parse with `time.ParseDuration`. Size values (`max_body_buffer`): decode as string, parse via a small helper that accepts `"1MiB"`, `"500KiB"`, bytes.
- `Load(path)` flow: `defaults := DefaultConfig()`; if file missing, write defaults via `Save(defaults, path)` and return; otherwise parse file into a partial struct shape, then merge fields into `defaults` (only overwrite non-zero specified fields — the simplest approach is to decode into `defaults` directly since HCL only sets attributes that exist in the file).
- `Save(cfg, path)` — serialize back to HCL using a template (embed a `default.hcl` text file and templated substitutions, or hand-render since the schema is fixed and small). Write at `0o600` under `0o750` dirs.
- `Refresh(path)` — load then save, to backfill new defaults on upgrade.

Implementation note: decoding HCL into a struct where some fields are durations requires a two-step parse. Pattern: a "wire" struct with `string` durations and a typed `Config` with `time.Duration`. Convert in a helper. Keep it tidy — the mcp-broker JSON config covers a smaller surface, so we're net-new here.

**Step 4: Verify** — `go test ./internal/config/...` — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/internal/config/ agent-gateway/go.mod agent-gateway/go.sum
git commit -m "feat(agent-gateway): add HCL config loader with defaults"
```

---

## Task 3: SQLite migrations framework

**Files:**

- Create: `agent-gateway/internal/store/store.go`
- Create: `agent-gateway/internal/store/migrations.go`
- Create: `agent-gateway/internal/store/store_test.go`

**Step 1: Write the failing tests**

```go
func TestOpen_CreatesWAL(t *testing.T) {
    path := filepath.Join(t.TempDir(), "state.db")
    db, err := store.Open(path)
    require.NoError(t, err)
    defer db.Close()

    var mode string
    require.NoError(t, db.QueryRow("PRAGMA journal_mode").Scan(&mode))
    assert.Equal(t, "wal", mode)

    var busy int
    require.NoError(t, db.QueryRow("PRAGMA busy_timeout").Scan(&busy))
    assert.Equal(t, 5000, busy)
}

func TestMigrations_AreIdempotent(t *testing.T) {
    path := filepath.Join(t.TempDir(), "state.db")

    db, err := store.Open(path)
    require.NoError(t, err)
    v1, err := store.UserVersion(db)
    require.NoError(t, err)
    require.NoError(t, db.Close())

    // Reopen — should not re-run migrations.
    db, err = store.Open(path)
    require.NoError(t, err)
    v2, err := store.UserVersion(db)
    require.NoError(t, err)
    require.NoError(t, db.Close())

    assert.Equal(t, v1, v2)
    assert.Greater(t, v1, 0)
}
```

**Step 2: Run** — `go test ./internal/store/...` — Expected: FAIL.

**Step 3: Implement**

- `Open(path string) (*sql.DB, error)` — ensures parent dir at `0o750`, opens via `ncruces/go-sqlite3/driver` (`sql.Open("sqlite3", path)`), sets `PRAGMA journal_mode=WAL`, `PRAGMA busy_timeout=5000`, `PRAGMA foreign_keys=ON`, runs migrations.
- `migrations.go` — a slice `var migrations = []func(*sql.DB) error{...}`. `runMigrations()` reads `PRAGMA user_version`, runs pending functions in order, updates the version after each success (single transaction per migration). Migration 1 is empty for now; later tasks append new ones.
- `UserVersion(db)` helper returns the current pragma value.
- Tests use `t.TempDir()` (no env var needed).

**Step 4: Verify** — `go test ./internal/store/...` — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/internal/store/
git commit -m "feat(agent-gateway): add SQLite open + migration framework"
```

---

## Task 4: PID file + daemon-comm check helper

**Files:**

- Create: `agent-gateway/internal/daemon/pidfile.go`
- Create: `agent-gateway/internal/daemon/pidfile_test.go`

**Step 1: Write the failing tests**

Three cases:

1. `Acquire` creates the PID file and returns a handle.
2. A second `Acquire` against the same path fails because a live daemon exists.
3. `Release` removes the file; a fresh `Acquire` then succeeds.
4. A PID file pointing at a non-`agent-gateway` process is treated as stale and overwritten (use our own PID with a bogus-comm stub by injecting the comm-check as a function).

```go
func TestAcquire_ReleaseCycle(t *testing.T) {
    path := filepath.Join(t.TempDir(), "pidfile")
    h, err := daemon.Acquire(path)
    require.NoError(t, err)
    _, err = daemon.Acquire(path)
    require.Error(t, err) // second acquire fails while first holds it
    require.NoError(t, h.Release())
    h2, err := daemon.Acquire(path)
    require.NoError(t, err)
    require.NoError(t, h2.Release())
}

func TestAcquire_StaleFileIsOverwritten(t *testing.T) {
    path := filepath.Join(t.TempDir(), "pidfile")
    // Write a PID that definitely isn't us and isn't our binary.
    require.NoError(t, os.WriteFile(path, []byte("999999"), 0o600))
    h, err := daemon.Acquire(path)
    require.NoError(t, err)
    defer h.Release()
    // Verify the file now holds our PID.
    got, _ := os.ReadFile(path)
    assert.Equal(t, strconv.Itoa(os.Getpid()), strings.TrimSpace(string(got)))
}
```

Also add a test for `SignalDaemon(path string) error` — reads PID, verifies comm is `agent-gateway` (use an injected verifier func for testability: `SignalDaemonWithVerifier(path, verify func(pid int) (bool, error), signal func(int, os.Signal) error)`), sends SIGHUP.

**Step 2: Run** — Expected: FAIL.

**Step 3: Implement**

- `type Handle struct{ path string }` + `Release()`.
- `Acquire(path)` — read-then-check semantics:
  1. If file exists: read PID, verify process lives (`os.FindProcess` + `Signal(syscall.Signal(0))`), verify its comm is `agent-gateway` (see below). If both hold, return `ErrAlreadyRunning`. Else, treat as stale, continue.
  2. Write our PID with `O_WRONLY|O_CREATE|O_TRUNC`, perm `0o600`.
- `verifyComm(pid int) (bool, error)` — on Linux reads `/proc/<pid>/comm`; on macOS runs `ps -p <pid> -o comm=`. Compare trimmed output to `agent-gateway`. Expose a public `SignalDaemon(path string) error` that uses this check before sending SIGHUP.
- Build-tag per-OS files (`pidfile_linux.go`, `pidfile_darwin.go`) for the comm lookup.
- Make the comm and signal functions swappable (package-level vars with test hook) so unit tests can inject fakes.

**Step 4: Verify** — `go test ./internal/daemon/...` — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/internal/daemon/
git commit -m "feat(agent-gateway): add PID file + comm-check daemon signalling"
```

---

## Task 5: Cobra root + `config {path,edit,refresh}` CLI

**Files:**

- Create: `agent-gateway/cmd/agent-gateway/root.go`
- Create: `agent-gateway/cmd/agent-gateway/config.go`
- Modify: `agent-gateway/cmd/agent-gateway/main.go`
- Create: `agent-gateway/cmd/agent-gateway/config_test.go`

**Step 1: Write the failing test**

`config path` prints the resolved config path to stdout; `config refresh` creates the file with defaults if missing.

```go
func TestCmdConfigPath(t *testing.T) {
    t.Setenv("XDG_CONFIG_HOME", t.TempDir())
    var out bytes.Buffer
    cmd := newRootCmd()
    cmd.SetArgs([]string{"config", "path"})
    cmd.SetOut(&out)
    require.NoError(t, cmd.Execute())
    assert.Contains(t, out.String(), "config.hcl")
}

func TestCmdConfigRefresh(t *testing.T) {
    dir := t.TempDir()
    t.Setenv("XDG_CONFIG_HOME", dir)
    cmd := newRootCmd()
    cmd.SetArgs([]string{"config", "refresh"})
    require.NoError(t, cmd.Execute())
    _, err := os.Stat(filepath.Join(dir, "agent-gateway", "config.hcl"))
    require.NoError(t, err)
}
```

**Step 2: Run** — Expected: FAIL.

**Step 3: Implement**

- `root.go` — `newRootCmd()` returns `*cobra.Command`. Accepts `--config <path>` persistent flag (defaults to `paths.ConfigFile()`). `main()` calls `newRootCmd().Execute()` with stderr as the error stream.
- `config.go` — subcommands:
  - `config path` → prints effective path to cmd.OutOrStdout().
  - `config edit` → execs `$EDITOR` (or `vi`) with the config path. If env var missing, returns an error.
  - `config refresh` → calls `config.Refresh()`.
- Use the `config` flag from the root command so tests can override it.

**Step 4: Verify** — `go test ./cmd/agent-gateway/...` — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/cmd/
git commit -m "feat(agent-gateway): add Cobra root and config subcommands"
```

---

## Task 6: `serve` skeleton — binds both ports, handles shutdown

**Files:**

- Create: `agent-gateway/cmd/agent-gateway/serve.go`
- Create: `agent-gateway/cmd/agent-gateway/serve_test.go`

**Step 1: Write the failing test**

Spawn serve in-process with a short-lived context, confirm both TCP ports accept connections, then cancel and verify clean shutdown.

```go
func TestServe_BindsAndShutsDown(t *testing.T) {
    dir := t.TempDir()
    t.Setenv("XDG_CONFIG_HOME", dir)
    t.Setenv("XDG_DATA_HOME", dir)

    cfg := config.DefaultConfig()
    cfg.Proxy.Listen = "127.0.0.1:0"
    cfg.Dashboard.Listen = "127.0.0.1:0"
    cfg.Dashboard.OpenBrowser = false
    require.NoError(t, config.Save(cfg, paths.ConfigFile()))

    ctx, cancel := context.WithCancel(context.Background())
    done := make(chan error, 1)
    go func() { done <- RunServe(ctx, newServeDeps()) }()

    // RunServe must accept a callback or expose the bound ports. Wait for ready.
    // (Implementation exposes a "ready" channel; test waits on it.)
    // ...

    cancel()
    require.NoError(t, <-done)
}
```

**Step 2: Run** — Expected: FAIL.

**Step 3: Implement**

- `serveCmd` calls `RunServe(ctx, deps)`. Responsibilities:
  1. Load config.
  2. Call `paths.ConfigDir()`/`DataDir()` and ensure dirs exist (`0o750`).
  3. Open `state.db` via `store.Open`.
  4. Acquire PID file. Release on exit.
  5. Bind proxy listener (`net.Listen("tcp", cfg.Proxy.Listen)`) and dashboard listener.
  6. Placeholder HTTP servers — proxy returns 501 for now, dashboard returns 200 "hello".
  7. Install SIGHUP (no-op handler that calls the injected `Reload` func, swapped in later tasks), SIGTERM/SIGINT shutdown with 30s grace.
  8. Emit a `ready` signal on a channel exposed via deps, for tests.
- No open-browser logic yet — that comes in the dashboard task. `OpenBrowser = false` is honoured as a pass-through flag stored for later.

**Step 4: Verify** — `go test ./cmd/agent-gateway/...` — Expected: PASS; two listeners bound; clean shutdown on cancel.

**Step 5: Commit**

```bash
git add agent-gateway/cmd/
git commit -m "feat(agent-gateway): add serve skeleton with graceful shutdown"
```

**End of Milestone 1 acceptance:** `agent-gateway serve` binds both ports, `config {path,edit,refresh}` works, `state.db` opens with WAL + `busy_timeout=5s`, a second `serve` fails due to the PID file.

---

## Task 7: Root CA generate + load

**Files:**

- Create: `agent-gateway/internal/ca/root.go`
- Create: `agent-gateway/internal/ca/root_test.go`

**Step 1: Write the failing tests**

```go
func TestLoadOrGenerate_CreatesOnFirstRun(t *testing.T) {
    dir := t.TempDir()
    keyPath := filepath.Join(dir, "ca.key")
    certPath := filepath.Join(dir, "ca.pem")

    a, err := ca.LoadOrGenerate(keyPath, certPath)
    require.NoError(t, err)

    // Check file perms.
    st, _ := os.Stat(keyPath)
    assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())
    st, _ = os.Stat(certPath)
    assert.Equal(t, os.FileMode(0o644), st.Mode().Perm())

    // Cert is self-signed, 10-year validity, P-256 ECDSA.
    block, _ := pem.Decode(a.RootPEM())
    cert, err := x509.ParseCertificate(block.Bytes)
    require.NoError(t, err)
    assert.Equal(t, "agent-gateway local CA", cert.Subject.CommonName)
    assert.True(t, cert.IsCA)
    assert.InDelta(t, 10*365*24, cert.NotAfter.Sub(cert.NotBefore).Hours(), 48)
}

func TestLoadOrGenerate_Reuses(t *testing.T) {
    dir := t.TempDir()
    a1, _ := ca.LoadOrGenerate(filepath.Join(dir, "ca.key"), filepath.Join(dir, "ca.pem"))
    a2, _ := ca.LoadOrGenerate(filepath.Join(dir, "ca.key"), filepath.Join(dir, "ca.pem"))
    assert.Equal(t, a1.RootPEM(), a2.RootPEM())
}
```

**Step 2: Run** — Expected: FAIL.

**Step 3: Implement**

- `ca.LoadOrGenerate(keyPath, certPath string) (*Authority, error)` — if both files exist, load; otherwise generate a P-256 ECDSA root with `CommonName: "agent-gateway local CA"`, `IsCA: true`, `KeyUsage: CertSign | CRLSign`, 10 y validity. PEM-encode key (mode `0o600`) and cert (mode `0o644`).
- `Authority` struct holds parsed cert + key + cached PEM bytes.
- `Rotate()` method: atomic replacement — write to `ca.key.new` / `ca.pem.new`, rename. Leaf cache is invalidated by the caller (dashboard already lives on a separate cycle).

**Step 4: Verify** — `go test ./internal/ca/...` — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/internal/ca/
git commit -m "feat(agent-gateway): add root CA load/generate"
```

---

## Task 8: Leaf cert issuance + cache + sweeper

**Files:**

- Create: `agent-gateway/internal/ca/leaf.go`
- Create: `agent-gateway/internal/ca/leaf_test.go`
- Modify: `agent-gateway/internal/ca/root.go` (add `ServerConfig` method)

**Step 1: Write the failing test**

```go
func TestServerConfig_CacheHitReusesConfig(t *testing.T) {
    a := newTestAuthority(t)
    c1, err := a.ServerConfig("example.com")
    require.NoError(t, err)
    c2, err := a.ServerConfig("example.com")
    require.NoError(t, err)
    assert.Same(t, c1, c2)
}

func TestServerConfig_DifferentHostsDifferentCerts(t *testing.T) {
    a := newTestAuthority(t)
    c1, _ := a.ServerConfig("a.example.com")
    c2, _ := a.ServerConfig("b.example.com")
    assert.NotSame(t, c1, c2)
    assert.Equal(t, []string{"h2", "http/1.1"}, c1.NextProtos)
}

func TestServerConfig_LeafVerifiesAgainstRoot(t *testing.T) {
    a := newTestAuthority(t)
    c, err := a.ServerConfig("example.com")
    require.NoError(t, err)

    pool := x509.NewCertPool()
    pool.AppendCertsFromPEM(a.RootPEM())

    leaf, err := x509.ParseCertificate(c.Certificates[0].Certificate[0])
    require.NoError(t, err)
    _, err = leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "example.com"})
    require.NoError(t, err)
}

func TestSweeper_RemovesExpired(t *testing.T) {
    // Shorten the refresh buffer for the test.
    a := newTestAuthorityWithBuffer(t, 1*time.Millisecond)
    _, _ = a.ServerConfig("example.com")
    time.Sleep(20 * time.Millisecond)
    a.SweepOnce() // exposed only for tests via a build tag or method
    _, has := a.cacheLookup("example.com")
    assert.False(t, has)
}
```

**Step 2: Run** — Expected: FAIL.

**Step 3: Implement**

- `Authority.ServerConfig(host string) (*tls.Config, error)`:
  - Cache lookup in `sync.Map`. Miss → issue a leaf with `SANs: [host]`, `KeyUsage: DigitalSignature | KeyEncipherment`, `ExtKeyUsage: ServerAuth`, 24h validity. Sign with root. Build `*tls.Config` with `Certificates: []tls.Certificate{...}`, `NextProtos: []string{"h2", "http/1.1"}`, `MinVersion: VersionTLS12`.
  - Cache store; return pointer.
- Background sweep: `Authority.Start(ctx)` spawns a goroutine that every 5 min walks the cache and removes entries where `leaf.NotAfter.Before(time.Now().Add(1*time.Hour))`.
- IP literals (v4 / v6): if the host parses as `net.IP`, put it in `IPAddresses` rather than `DNSNames`. _Note:_ CONNECT-time we'll route IP literals to tunnel anyway (§6); this branch exists defensively.

**Step 4: Verify** — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/internal/ca/
git commit -m "feat(agent-gateway): add leaf cert issuance with TLS config cache"
```

---

## Task 9: `ca {export,rotate}` CLI

**Files:**

- Create: `agent-gateway/cmd/agent-gateway/ca.go`
- Create: `agent-gateway/cmd/agent-gateway/ca_test.go`

**Step 1: Write the failing test**

`ca export` prints the PEM from disk to stdout. `ca rotate` replaces the CA files atomically and prints a reminder that sandboxes must re-trust.

**Step 2: Run** — Expected: FAIL.

**Step 3: Implement**

- `ca export` reads `paths.CACert()` and writes to `cmd.OutOrStdout()`. No rebuild — this is a file read.
- `ca rotate` calls `ca.Rotate(keyPath, certPath)` which generates a new root into `*.new` files and renames atomically. Emits a SIGHUP to the running daemon afterwards via `daemon.SignalDaemon(paths.PIDFile())` (no-op if daemon is down). Prints `rotated: ~/.local/share/agent-gateway/ca.pem — every sandbox must re-trust.`

**Step 4: Verify** — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/cmd/
git commit -m "feat(agent-gateway): add ca export/rotate CLI"
```

---

## Task 10: CONNECT handler + basic MITM plumbing

**Files:**

- Create: `agent-gateway/internal/proxy/connect.go`
- Create: `agent-gateway/internal/proxy/pipeline.go`
- Create: `agent-gateway/internal/proxy/proxy.go`
- Create: `agent-gateway/internal/proxy/connect_test.go`

**Step 1: Write the failing test**

_This milestone's test is end-to-end (§12 M2), so unit-level tests here focus on the CONNECT parsing and handler lifecycle. The real acceptance is the e2e in Task 12._

```go
func TestCONNECT_ParsesAndHandshakes(t *testing.T) {
    auth := newTestAuthority(t)
    p := proxy.New(proxy.Deps{CA: auth, UpstreamRoundTripper: roundTripperFunc(testEchoHandler)})
    ln, err := net.Listen("tcp", "127.0.0.1:0")
    require.NoError(t, err)
    go p.Serve(ln)
    defer ln.Close()

    // Dial the proxy, send CONNECT, handshake TLS, make a GET.
    // Custom http.Transport.Proxy pointing at ln.Addr(), RootCAs = auth.RootPEM().
    // ... assert response body == "echo"
}
```

**Step 2: Run** — Expected: FAIL.

**Step 3: Implement**

- `Proxy.Serve(ln net.Listener)` loop → per-connection goroutine.
- Per connection:
  1. Read request line (HTTP/1.1). Accept `CONNECT host:port HTTP/1.1`. (Plain HTTP support deferred; v1 is mostly HTTPS. Return `400` for non-CONNECT for now.)
  2. Parse `Proxy-Authorization`. For this milestone, accept anything (agent auth comes in Task 24). TODO comment referencing the auth task.
  3. For this milestone: always MITM. (Tunnel logic comes in Task 24.)
  4. Send `HTTP/1.1 200 Connection Established\r\n\r\n`.
  5. Wrap `conn` in `tls.Server(conn, auth.ServerConfig(host))`. Force handshake.
  6. Inspect negotiated ALPN: `"h2"` → `http2.Server.ServeConn`, else treat as HTTP/1.
  7. For each request: build an `http.Request` with `Host = host`, `URL.Scheme = "https"`, dial upstream via `http.Transport{ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{ServerName: host}}`, stream response.
- Streaming correctness: `io.Copy(w, resp.Body)` wrapped with `http.Flusher.Flush()` after each write if the response handler knows it's an SSE/streaming response. The simplest robust pattern is to always `Flush()` whenever `Write` returns; that adds a syscall but matches Go proxy conventions.
- Use `http.NewRequestWithContext(agentReq.Context(), ...)` — **required pattern** from design §13.
- `Proxy.Deps` struct: `CA Authority`, `UpstreamRoundTripper http.RoundTripper` (injectable for tests), `Logger *slog.Logger`. No rules/audit/injection yet — all `TODO: rules` comments.

Expect this package to grow; keep the file boundaries honest (`connect.go` parses CONNECT and drives handshake, `pipeline.go` handles the decoded-request → upstream flow, `proxy.go` exports `Proxy` and wires deps).

**Step 4: Verify** — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/internal/proxy/
git commit -m "feat(agent-gateway): add CONNECT handler with MITM TLS pipeline"
```

---

## Task 11: Wire proxy into `serve`

**Files:**

- Modify: `agent-gateway/cmd/agent-gateway/serve.go`

**Step 1: Write the failing test**

A minimal assertion inside `TestServe_BindsAndShutsDown` (from Task 6) upgraded: after bind, open a raw TCP connection to the proxy port, send `CONNECT a.invalid:443 HTTP/1.1\r\n\r\n`, confirm the proxy replies `HTTP/1.1 200 Connection Established`.

**Step 2: Run** — Expected: FAIL (`serve` still returns 501 placeholder).

**Step 3: Implement**

- In `RunServe`, construct `authority := ca.LoadOrGenerate(paths.CAKey(), paths.CACert())`, then `proxy.New(…)`, then replace the placeholder proxy HTTP handler with `proxy.Serve(listener)` on a goroutine.
- Start the CA sweeper (`authority.Start(ctx)`).

**Step 4: Verify** — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/cmd/ agent-gateway/internal/
git commit -m "feat(agent-gateway): wire CA and proxy into serve"
```

---

## Task 12: E2E harness + TestMITMEndToEnd

**Files:**

- Create: `agent-gateway/test/e2e/teststack_test.go`
- Create: `agent-gateway/test/e2e/mitm_test.go`

**Step 1: Write the failing test**

Port the pattern from `mcp-broker/test/e2e/teststack_test.go`. `newTestStack()`:

- Builds the `agent-gateway` binary into `t.TempDir()` (once via `TestMain`).
- Writes a minimal `config.hcl` pointing proxy+dashboard at ephemeral ports.
- Starts the binary with `XDG_CONFIG_HOME` and `XDG_DATA_HOME` pointing at the temp dir.
- Reads the generated CA PEM.
- Starts a mock upstream `httptest.NewTLSServer` whose cert is added to a trust bundle used by the proxy's upstream RoundTripper — but since upstream TLS uses the system trust store, the cleaner path is to give the test server a cert signed by a second CA which the test adds to the binary's trust store via `SSL_CERT_FILE` env var (Go honours it on Linux/Darwin).
- Sets a test `http.Client` with `Proxy: url.URL{Host: proxyAddr}` and `RootCAs` set to the generated `ca.pem`.

Tests:

```go
func TestMITMEndToEnd_H1(t *testing.T)            { ... }
func TestMITMEndToEnd_H2(t *testing.T)            { ... }
func TestMITMEndToEnd_StreamingResponse(t *testing.T) { // chunked every 100ms; assert first chunk arrives before last
    ...
}
```

`TestMITMEndToEnd_H2` uses `http.Transport{ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{NextProtos: []string{"h2"}}}` on the agent side.

The streaming test uses a backend that sets `X-Accel-Buffering: no` and flushes every 100 ms; the client records wall-clock timestamps of each chunk arrival and asserts spread > 300 ms.

**Step 2: Run** — Expected: FAIL (test infra missing; proxy likely fails on real GETs because MITM path not complete).

**Step 3: Implement**

- Finish any proxy pipeline gaps that fall out from the failing test (e.g. certain header normalisation for h2, `Host` header propagation).
- `SSL_CERT_FILE` env var set in the subprocess so the daemon accepts the mock upstream's cert.

**Step 4: Verify** — `make test-e2e` from `agent-gateway/` — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/test/e2e/ agent-gateway/internal/proxy/
git commit -m "test(agent-gateway): add e2e MITM tests covering h1, h2, streaming"
```

**End of Milestone 2 acceptance:** `TestMITMEndToEnd` passes (h1↔h1, h1↔h2, streaming).

---

## Task 13: HCL rule grammar + loader

**Files:**

- Create: `agent-gateway/internal/rules/types.go`
- Create: `agent-gateway/internal/rules/parse.go`
- Create: `agent-gateway/internal/rules/parse_test.go`
- Create: `agent-gateway/internal/rules/testdata/simple.hcl`
- Create: `agent-gateway/internal/rules/testdata/labelled-blocks.hcl`

**Step 1: Write the failing tests**

```go
func TestParse_SimpleAllow(t *testing.T) {
    rs, warnings, err := rules.ParseDir("testdata")
    require.NoError(t, err)
    require.Empty(t, warnings)
    require.Len(t, rs, 1)
    assert.Equal(t, "github-issue-create", rs[0].Name)
    assert.Equal(t, []string{"claude-review"}, rs[0].Agents)
    assert.Equal(t, "api.github.com", rs[0].Match.Host)
    assert.Equal(t, "POST", rs[0].Match.Method)
    assert.Equal(t, "/repos/*/*/issues", rs[0].Match.Path)
    assert.Equal(t, "^2022-", rs[0].Match.Headers["X-GitHub-Api-Version"])
    assert.Equal(t, "allow", rs[0].Verdict)
    assert.Equal(t, "Bearer ${secrets.gh_bot}", rs[0].Inject.SetHeaders["Authorization"])
}

func TestParse_JSONBodyMatcher(t *testing.T) {
    // Loads labelled-blocks.hcl, asserts json_body with two jsonpath matchers.
}

func TestParse_EmptyAgentsIsError(t *testing.T) {
    // Write rule with agents = []; expect error.
}

func TestParse_LexicalOrder(t *testing.T) {
    // Files 00-a.hcl has rule A, 10-b.hcl has rule B; loaded order is [A, B].
}

func TestParse_UnknownBodyBlockIsError(t *testing.T) { ... }
```

**Step 2: Run** — Expected: FAIL.

**Step 3: Implement**

- `types.go`:

```go
type Rule struct {
    Name    string
    Agents  []string
    Match   Match
    Verdict string
    Inject  *Inject
    // compiled bits populated at load time
    hostGlob  globMatcher
    pathGlob  globMatcher
    headerREs map[string]*regexp.Regexp
    body      bodyMatcher // nil when rule has no body block
}

type Match struct {
    Host    string
    Method  string
    Path    string
    Headers map[string]string
    // one of JSONBody, FormBody, TextBody
    JSONBody *JSONBodyMatch
    FormBody *FormBodyMatch
    TextBody *TextBodyMatch
}

type JSONBodyMatch struct{ Paths []JSONPathMatcher }
type JSONPathMatcher struct{ Path, Matches string; re *regexp.Regexp }
// ... FormBody with fields, TextBody with raw regex
```

- `parse.go`: use `github.com/hashicorp/hcl/v2/hclparse` to parse each file into an `hcl.File`. For each `rule "name" {...}` block, decode attributes + sub-blocks manually (attribute-map friendliness; `gohcl` struggles with dynamic labels). Implement decoders for `match {}`, `inject {}`, and the three body block variants.
- `ParseDir(dir string) (rules []Rule, warnings []string, err error)` — reads `*.hcl` in `filepath.Glob(dir+"/*.hcl")` sorted by filename, calls `parseFile` on each, concatenates.
- Duplicate rule names across the ruleset are a load error (makes audit rule-name references unambiguous).
- `agents = []` → error. Omitted `agents` → nil slice (means "all agents").
- Two-phase validation: **this task only** validates template **syntax** (via a regex that matches `${identifier.field}` shapes); semantic existence of secrets/agents is NOT checked here.

**Step 4: Verify** — `go test ./internal/rules/...` — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/internal/rules/
git commit -m "feat(agent-gateway): add HCL rule parser"
```

---

## Task 14: Rule matcher — host/path/method/headers

**Files:**

- Create: `agent-gateway/internal/rules/match.go`
- Create: `agent-gateway/internal/rules/match_test.go`
- Modify: `agent-gateway/internal/rules/types.go` (expose `Request` and `compile` helpers)

**Step 1: Write the failing tests**

Cover:

- `host`: exact match, single-segment `*`, multi-segment `**`.
- `path`: same glob rules.
- `method`: exact uppercase match; mismatch fails.
- `headers`: regex must compile (RE2), value must match case-insensitive header lookup.
- `agents` omitted matches every agent; `agents = ["claude"]` doesn't match `codex`.
- First-match-wins ordering.

```go
func TestEvaluate_HostGlob(t *testing.T) {
    rs := parseInline(t, `
rule "a" { match { host = "api.github.com"      path = "/**" } verdict = "allow" }
rule "b" { match { host = "*.github.com"         path = "/**" } verdict = "deny"  }
rule "c" { match { host = "**.enterprise.local"  path = "/**" } verdict = "allow" }
`)
    engine := rules.Compile(rs)
    m := engine.Evaluate(&rules.Request{Agent: "x", Host: "api.github.com", Method: "GET", Path: "/foo"})
    require.NotNil(t, m)
    assert.Equal(t, "a", m.Rule.Name)
    m = engine.Evaluate(&rules.Request{Agent: "x", Host: "git.enterprise.local", Method: "GET", Path: "/foo"})
    assert.Equal(t, "c", m.Rule.Name)
}
```

**Step 2: Run** — Expected: FAIL.

**Step 3: Implement**

- Host/path globs: use a small glob implementation. `*` = `[^.]*` for host (no `.` crossing) or `[^/]*` for path. `**` = `.*`. Translate to anchored regex once at compile.
- `Compile(rules []Rule) *Engine` — compiles all globs/regexes; returns an engine with `Evaluate(*Request) *Match`.
- `Match` carries the rule pointer plus the matched-rule index.
- No body evaluation yet — that's Task 15.
- No atomic snapshot yet — that's Task 16.

**Step 4: Verify** — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/internal/rules/
git commit -m "feat(agent-gateway): add rule matcher for host/path/method/headers"
```

---

## Task 15: Body matchers (JSON, form, text)

**Files:**

- Modify: `agent-gateway/internal/rules/match.go`
- Create: `agent-gateway/internal/rules/body.go`
- Create: `agent-gateway/internal/rules/body_test.go`

**Step 1: Write the failing tests**

```go
func TestBody_JSONPathMatch(t *testing.T)         { ... }
func TestBody_JSONBlockRejectsNonJSONContentType(t *testing.T) { ... }
func TestBody_EmptyBodyNeverMatches(t *testing.T)  {
    // GET with no body; rule has json_body; must not match.
}
func TestBody_FormBodyFieldRegex(t *testing.T)     { ... }
func TestBody_TextBodyRegex(t *testing.T)          { ... }
func TestBody_OverSizeCapBypasses(t *testing.T) {
    // body is > 1 MiB; evaluator returns no match plus a reason tag
    // "body_matcher_bypassed:size".
}
```

**Step 2: Run** — Expected: FAIL.

**Step 3: Implement**

- `bodyMatcher` interface with `Match(ct string, body []byte) (bool, error)`. Three impls.
- `Request` gets a `Body []byte` + `BodyTruncated bool` + `BodyTimedOut bool` set by the proxy buffer layer (Task 17). If `BodyTruncated` or `BodyTimedOut`, body matchers auto-fail and the rule evaluator attaches `error = "body_matcher_bypassed:size"` or `":timeout"` to the `Match` for the audit row.
- JSON impl: `PaesslerAG/jsonpath.Get(jsonBytes, path)` → iterate, regex-match each value's string form. (Normalise numeric → `%v`.) Bracket-wildcards like `$.labels[*]` expected.
- Form impl: `url.ParseQuery`, match each field. Content-Type must start with `application/x-www-form-urlencoded`.
- Text impl: raw regex over body. Content-Type must start with `text/`.
- "No body or Content-Length: 0" → body-block rules never match, regardless of declared content-type.
- Evaluate order inside a rule: check host/path/method first, then headers, then body. Short-circuit on first mismatch.

**Step 4: Verify** — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/internal/rules/
git commit -m "feat(agent-gateway): add json/form/text body matchers with fail-soft caps"
```

---

## Task 16: Rules engine interface + atomic snapshot

**Files:**

- Create: `agent-gateway/internal/rules/engine.go`
- Create: `agent-gateway/internal/rules/engine_test.go`

**Step 1: Write the failing test**

```go
func TestEngine_ReloadSwapsAtomically(t *testing.T) {
    dir := t.TempDir()
    write(t, dir, "00.hcl", `rule "a" { match { host = "a.com" path = "/**" } verdict = "allow" }`)

    e, err := rules.NewEngine(dir)
    require.NoError(t, err)
    assert.Equal(t, "a", e.Evaluate(&rules.Request{Host: "a.com", Path: "/x"}).Rule.Name)

    write(t, dir, "00.hcl", `rule "b" { match { host = "b.com" path = "/**" } verdict = "allow" }`)
    require.NoError(t, e.Reload())

    assert.Equal(t, "b", e.Evaluate(&rules.Request{Host: "b.com", Path: "/x"}).Rule.Name)
    assert.Nil(t, e.Evaluate(&rules.Request{Host: "a.com", Path: "/x"}))
}

func TestEngine_InvalidReloadKeepsPreviousRuleset(t *testing.T) {
    // write valid → load → write invalid HCL → Reload() returns error →
    // Evaluate still uses previous ruleset.
}
```

**Step 2: Run** — Expected: FAIL.

**Step 3: Implement**

- `Engine` struct with `snapshot atomic.Pointer[snapshot]`.
- `NewEngine(dir string)` — `ParseDir`, `Compile`, store snapshot.
- `Reload()` — parse + compile into a new snapshot; on any error, return the error without swapping; on success, `snapshot.Store(new)`.
- `Evaluate(*Request) *Match` — load pointer, walk rules.
- Also maintain a derived map: `HostsForAgent(agent string) map[string]struct{}` — the list of hosts that have at least one MITM-eligible rule applying to this agent. Rebuilt during `Reload`. Used by CONNECT-time decision (Task 24).

**Step 4: Verify** — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/internal/rules/
git commit -m "feat(agent-gateway): add rules.Engine with atomic hot-reload snapshot"
```

---

## Task 17: Body buffering in proxy pipeline

**Files:**

- Modify: `agent-gateway/internal/proxy/pipeline.go`
- Create: `agent-gateway/internal/proxy/buffer.go`
- Create: `agent-gateway/internal/proxy/buffer_test.go`

**Step 1: Write the failing test**

```go
func TestBufferBody_UnderCap(t *testing.T)        { ... }
func TestBufferBody_AtCapMarkedTruncated(t *testing.T) { ... }
func TestBufferBody_TimeoutMarks(t *testing.T) {
    // slow reader; context deadline fires; result is (buf, truncated=false, timedOut=true)
}
func TestBufferBody_NoContentLengthNoBody(t *testing.T) {
    // GET with Content-Length: 0 → returns nil body, never peeks.
}
```

**Step 2: Run** — Expected: FAIL.

**Step 3: Implement**

- `bufferBody(ctx context.Context, r io.ReadCloser, cap int64, timeout time.Duration) (body []byte, truncated bool, timedOut bool, rewound io.ReadCloser, err error)`.
- Returns a `rewound` reader that, after the matcher peek, will stream the original body to upstream (even if truncated, we stream the original reader since the matcher already declared itself `_bypassed:size`).
- Implementation: wrap reader; read up to `cap+1` bytes with a context-driven deadline. If > cap, truncated = true, stash the buffered bytes and chain `io.MultiReader(bytes.NewReader(buf), r)` as the rewound stream. If timeout, similar.
- For requests with no body (GET, `Content-Length: 0`), short-circuit and return `(nil, false, false, emptyReader, nil)`.
- Pipeline (`pipeline.go`) is updated to call `bufferBody` only when the request matches any rule that declares a body matcher (determined by a boolean on the `rules.Request`). _Optimisation:_ check per-rule during evaluation — if no rule on this host needs a body, skip buffering entirely.

Task 17 stops short of actually calling the engine — the integration happens in Task 21.

**Step 4: Verify** — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/internal/proxy/
git commit -m "feat(agent-gateway): add bounded body-buffering helper for rule matching"
```

---

## Task 18: `rules check` CLI (syntax validation)

**Files:**

- Create: `agent-gateway/cmd/agent-gateway/rules.go`
- Create: `agent-gateway/cmd/agent-gateway/rules_test.go`

**Step 1: Write the failing test**

```go
func TestRulesCheck_ValidReturnsZero(t *testing.T) { ... }
func TestRulesCheck_InvalidReturnsNonZero(t *testing.T) { ... }
func TestRulesCheck_MissingSecretIsWarningNotError(t *testing.T) {
    // Rule references ${secrets.does_not_exist}; exits 0 but prints warning.
}
```

**Step 2: Run** — Expected: FAIL.

**Step 3: Implement**

- `rules check` flow: call `rules.ParseDir(paths.RulesDir())`. On parse error, print error + exit code 1. On success: for each rule, walk `Inject.SetHeaders` values and extract `${secrets.*}` refs (via the regex used in Task 13). Cross-check against the secrets store (we need `secrets.Store.List()` — this task anticipates Task 22; if the store isn't available yet, produce warnings off a stub interface). Output format:

  ```
  ok: 3 rules parsed from 2 files
  warning: rule "github-issue-create" references undefined secret "gh_bot"
  ```

  Exits 0 regardless of warnings (per design §4).

- To unblock this task before secrets exist, accept `--secrets-list` for tests and provide a stub implementation that always returns "all resolved". Real wiring comes in Task 22.

**Step 4: Verify** — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/cmd/
git commit -m "feat(agent-gateway): add rules check CLI"
```

---

## Task 19: SIGHUP reload + `rules reload` CLI

**Files:**

- Modify: `agent-gateway/cmd/agent-gateway/serve.go`
- Modify: `agent-gateway/cmd/agent-gateway/rules.go`
- Create: `agent-gateway/cmd/agent-gateway/rules_reload_test.go`

**Step 1: Write the failing test**

Integration-tagged (build-tag `integration`). Start the daemon in-process, then invoke the CLI's `rules reload` logic programmatically. Assert it sends SIGHUP and the daemon re-reads rules.d.

**Step 2: Run** — Expected: FAIL.

**Step 3: Implement**

- In `serve.go`, install a `signal.Notify(..., syscall.SIGHUP)` handler that:
  - Calls `engine.Reload()`. On error, logs at `error` level; previous ruleset stays live (already guaranteed by engine).
  - Calls `secrets.InvalidateCache()` (added in Task 22).
  - Calls `agents.ReloadFromDB()` (added in Task 26).
  - Re-reads config.hcl — log + swap if unchanged semantics allow (defer the hard parts: listener swap is out of scope; SIGHUP changes take effect for _new_ connections + snapshots only).
- `rules reload` calls `daemon.SignalDaemon(paths.PIDFile())`. Print `reloaded` on success.
- `TestRulesReloadSendsSIGHUP` uses an injected signal function to verify the correct PID is signalled; end-to-end reload is covered by the next task.

**Step 4: Verify** — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/
git commit -m "feat(agent-gateway): add SIGHUP coarse-reload handler and rules reload CLI"
```

---

## Task 20: Integration test — TestRuleReloadHotSwap

**Files:**

- Create: `agent-gateway/test/e2e/rules_reload_test.go`

**Step 1: Write the failing test**

Scenario per design §12 M3: start the daemon with a rule referencing `${secrets.x}` (no secret yet). Invoke `rules reload`. Fire a matching request. Assert audit row has `injection='failed', error='secret_unresolved'` (needs Tasks 21+25 wired). Then replace the rule file with invalid HCL; `rules reload` exits non-zero and existing requests still match the previous ruleset.

Because this is the design's M3 acceptance gate, the test cannot fully pass until Tasks 21 (engine wired to proxy), 22–25 (secrets + injection) are in. **Mark this test `t.Skip("requires M4")` for now** and keep the assertions coded; Task 27 unskips it.

**Step 2: Run** — Expected: SKIP.

**Step 3: Implement** — none yet; test is a scaffold.

**Step 4: Verify** — Expected: SKIP (still zero failures).

**Step 5: Commit**

```bash
git add agent-gateway/test/e2e/
git commit -m "test(agent-gateway): scaffold rule-reload hot-swap e2e (skipped)"
```

---

## Task 21: Wire rules engine into proxy pipeline (no injection yet)

**Files:**

- Modify: `agent-gateway/internal/proxy/pipeline.go`
- Modify: `agent-gateway/internal/proxy/proxy.go`
- Modify: `agent-gateway/cmd/agent-gateway/serve.go`

**Step 1: Write the failing test**

Unit test: inject a stub `rules.Engine` into `proxy.Deps`. Fire a request matching a `deny` rule; expect 403 to the agent.

```go
func TestPipeline_DenyRuleReturns403(t *testing.T) {
    p := proxy.New(proxy.Deps{
        CA:    newTestAuthority(t),
        Rules: stubEngineReturning(&rules.Match{Rule: &rules.Rule{Verdict: "deny"}}),
    })
    // send request through p, assert 403 response.
}
```

Plus a test that a `require-approval` verdict blocks until the injected `approval.Broker` returns a decision (use a channel mock).

**Step 2: Run** — Expected: FAIL.

**Step 3: Implement**

- Proxy now accepts a `rules.Engine` and an `approval.Broker` (interface stub — real broker comes in Task 31).
- Pipeline: after request decode, build `rules.Request`, call `engine.Evaluate`:
  - Nil match or `allow` verdict → forward upstream untouched. For now.
  - `deny` → synthesise `403 Forbidden`.
  - `require-approval` → `broker.Request(ctx, ...)` → on `approved`, treat as allow; `denied` → 403; timeout → `504 Gateway Timeout`.
- Synthesised responses include `X-Request-ID: <ulid>` (ULID assigned upfront — Task 24's responsibility; for now, generate one inline and stash in ctx).

**Step 4: Verify** — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/
git commit -m "feat(agent-gateway): wire rules engine into proxy verdict dispatch"
```

**End of Milestone 3 acceptance (partial):** engine reloads hot; proxy obeys `deny` + `require-approval` verdicts; `allow` passes through unmodified. Full acceptance waits on Task 27.

---

## Task 22: Secrets schema + AES-GCM store + master-key resolution

**Files:**

- Create: `agent-gateway/internal/secrets/store.go`
- Create: `agent-gateway/internal/secrets/crypto.go`
- Create: `agent-gateway/internal/secrets/masterkey.go`
- Create: `agent-gateway/internal/secrets/store_test.go`
- Modify: `agent-gateway/internal/store/migrations.go` (add secrets table)

**Step 1: Write the failing tests**

```go
func TestStore_SetThenGet(t *testing.T)    { ... }
func TestStore_ScopeResolution(t *testing.T) {
    // "gh_bot" global + "gh_bot" agent:foo. Get(name="gh_bot", agent="foo") returns the agent-scoped value.
    // Get(name="gh_bot", agent="bar") returns the global value.
    // Get(name="gh_bot", agent="baz") — when only agent-scoped to "foo" exists — returns ErrNotFound.
}
func TestStore_EncryptionAtRest(t *testing.T) {
    // After Set, read the raw ciphertext column from SQLite and assert it
    // does NOT contain the plaintext.
}
func TestStore_MasterRotate(t *testing.T) {
    // Set two secrets, rotate master key, assert Get still returns the
    // same plaintexts under the new key.
}
func TestMasterKey_FileFallbackWhenKeychainUnavailable(t *testing.T) { ... }
```

**Step 2: Run** — Expected: FAIL.

**Step 3: Implement**

- Migration 2: `CREATE TABLE secrets (...)` per §5.
- `masterkey.go`: `Resolve(keychainService, account, filePath string) ([]byte, bool, error)` — bool = `fromFile`. Tries `zalando/go-keyring` first. On error (no Secret Service, Darwin headless, etc.), falls back to reading `master.key` (mode `0o600`). If neither exists, generates a 32-byte random key, stores it in the keychain (or the file, with a loud stderr warning if keychain fails).
- `crypto.go`: `encrypt(key, plaintext []byte) (ciphertext, nonce []byte, err error)` using `crypto/aes` + `cipher.NewGCM` with a 12-byte per-row random nonce. `decrypt` is the inverse.
- `Store` interface:

```go
type Store interface {
    Get(ctx context.Context, name, agent string) (value string, scope string, err error)
    Set(ctx context.Context, name, agent, value, description string) error
    List(ctx context.Context) ([]Metadata, error)
    Rotate(ctx context.Context, name, agent, newValue string) error
    Delete(ctx context.Context, name, agent string) error
    MasterRotate(ctx context.Context) error
    InvalidateCache()
}
```

- `MasterRotate` — generate new key, within one SQLite transaction: for each row, decrypt with old key + re-encrypt with new key + update. Only commit the new key to keychain/file after the SQL transaction commits.
- `NewStore(db *sql.DB, logger *slog.Logger)` — performs key resolution once at construction. Holds `key []byte` in memory only.

**Step 4: Verify** — `go test ./internal/secrets/...` — Expected: PASS. Plus an integration-tagged test `TestStore_KeychainFallback` behind `//go:build integration` since keychain availability depends on the host.

**Step 5: Commit**

```bash
git add agent-gateway/internal/secrets/ agent-gateway/internal/store/
git commit -m "feat(agent-gateway): add encrypted secret store with master-key fallback"
```

---

## Task 23: Secret CLI commands

**Files:**

- Create: `agent-gateway/cmd/agent-gateway/secret.go`
- Create: `agent-gateway/cmd/agent-gateway/secret_test.go`

**Step 1: Write the failing tests**

Cover each subcommand (`set`, `list`, `rotate`, `rm`, `master rotate`, `export`). Use `t.TempDir` + `XDG_*` overrides. Assertions:

- `set <name>` reads stdin when not a TTY; creates the row.
- `set <name> --agent <a>` creates an agent-scoped row; prints a shadow warning if a global row exists.
- `list` prints `(name, scope, created, rotated, last-used, description)` without values.
- `export <name>` refuses with error when stdout is a TTY; works when piped.
- `rm <name>` works; `rm <name> --agent <a>` scoped.

```go
func TestSecretSet_ShadowWarning(t *testing.T) { ... }
func TestSecretExport_RefusesTTY(t *testing.T) {
    // Use isatty fake; assert error.
}
```

**Step 2: Run** — Expected: FAIL.

**Step 3: Implement**

- Open a fresh `secrets.Store` on each invocation (the daemon holds its own store; the CLI uses a short-lived one with the same DB file, busy_timeout covering contention).
- After any state change, send SIGHUP via `daemon.SignalDaemon` (no-op when daemon down).
- `export` uses `golang.org/x/term.IsTerminal(int(os.Stdout.Fd()))`. Refuses if true. Prints only raw value to stdout, no trailing newline.

**Step 4: Verify** — `go test ./cmd/agent-gateway/...` — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/cmd/
git commit -m "feat(agent-gateway): add secret CLI commands with shadow warnings"
```

---

## Task 24: Template expansion + injector + LRU cache

**Files:**

- Create: `agent-gateway/internal/inject/injector.go`
- Create: `agent-gateway/internal/inject/template.go`
- Create: `agent-gateway/internal/inject/cache.go`
- Create: `agent-gateway/internal/inject/injector_test.go`

**Step 1: Write the failing tests**

```go
func TestTemplate_SecretsExpansion(t *testing.T) {
    store := stubSecrets("gh_bot", "agent:x" -> "abc")
    got, scope, err := inject.Expand(ctx, "Bearer ${secrets.gh_bot}", "x", store)
    require.NoError(t, err)
    assert.Equal(t, "Bearer abc", got)
    assert.Equal(t, "agent:x", scope)
}

func TestTemplate_AgentName(t *testing.T) {
    got, _, err := inject.Expand(ctx, "agent=${agent.name}", "x", nil)
    require.NoError(t, err)
    assert.Equal(t, "agent=x", got)
}

func TestTemplate_UnresolvedSecret_ReturnsError(t *testing.T) {
    _, _, err := inject.Expand(ctx, "Bearer ${secrets.missing}", "x", emptyStore)
    assert.ErrorIs(t, err, inject.ErrSecretUnresolved)
}

func TestTemplate_OpaqueValues(t *testing.T) {
    store := stubSecrets("x", "global" -> "has ${nested} and \\ chars")
    got, _, err := inject.Expand(ctx, "${secrets.x}", "agent", store)
    require.NoError(t, err)
    assert.Equal(t, "has ${nested} and \\ chars", got) // no re-expansion
}

func TestInjector_SetHeaderOverwrites(t *testing.T) { ... }
func TestInjector_RemoveHeader(t *testing.T)         { ... }

func TestCache_TTLExpiry(t *testing.T) { ... }
func TestCache_InvalidateClearsAll(t *testing.T) { ... }
```

**Step 2: Run** — Expected: FAIL.

**Step 3: Implement**

- `template.go`: single-pass scan for `${…}` tokens. Two forms: `secrets.<ident>`, `agent.name`/`agent.id`. Anything else → syntax error (at load time; at expand time, return a typed error).
- `ErrSecretUnresolved` — sentinel returned when `store.Get` returns not-found or mismatched scope.
- `cache.go`: `(agent, name) → (value, expiresAt)`. TTL from config. `Invalidate()` clears. Map + mutex is fine at this scale.
- `Injector.Apply(req *http.Request, rule *rules.Rule, agent string) (status InjectionStatus, scope string, err error)`:
  - For each `set_header` value → `Expand` → `req.Header.Set`.
  - For each `remove_header` → `req.Header.Del`.
  - On first unresolved secret, return `StatusFailed`, don't touch headers, let caller short-circuit to the fail-soft path.
- Add a single `ExpandAll` helper that returns the **first** resolved scope (for the audit row's `credential_scope`). If multiple templates expand to different scopes, log a debug line; v1 records the first.

**Step 4: Verify** — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/internal/inject/
git commit -m "feat(agent-gateway): add template expansion and header injector"
```

---

## Task 25: Wire injection + fail-soft into proxy

**Files:**

- Modify: `agent-gateway/internal/proxy/pipeline.go`
- Modify: `agent-gateway/cmd/agent-gateway/serve.go`

**Step 1: Write the failing test**

```go
func TestPipeline_InjectsOnAllow(t *testing.T)                  { ... }
func TestPipeline_FailSoftOnUnresolvedSecret(t *testing.T) {
    // rule matches, template references missing secret → request is
    // forwarded WITHOUT the injected header; audit context (captured via
    // mock auditor) records injection='failed', error='secret_unresolved'.
}
```

**Step 2: Run** — Expected: FAIL.

**Step 3: Implement**

- Pipeline's allow-path:
  1. Clone the original `http.Request` (upstream = separate object).
  2. Call `injector.Apply`. If `err == ErrSecretUnresolved`, mark the request context with `injection="failed"`, `error="secret_unresolved"`, leave the request _as it came in from the agent_ (dummy credential intact), forward upstream.
  3. On success, set `injection="applied"`, `credential_scope` = the returned scope, `credential_ref` = the first referenced secret name.
- `require-approval` approved path reuses the same inject flow after the human decision.
- Audit fields accumulate on a per-request struct threaded through context; the auditor (Task 28) reads it at end-of-request.

**Step 4: Verify** — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/
git commit -m "feat(agent-gateway): wire header injection with fail-soft fallback"
```

---

## Task 26: E2E — TestSecretSubstitution

**Files:**

- Create: `agent-gateway/test/e2e/secret_substitution_test.go`

**Step 1: Write the failing test**

Scenario (M4 acceptance): mock upstream captures incoming `Authorization` header. Test writes a rule file with `inject { set_header = { "Authorization" = "Bearer ${secrets.gh_bot}" } }`. Invokes `agent-gateway secret set gh_bot --from-stdin` with value `realtoken`. Assertions:

- The agent's HTTP client sends `Authorization: Bearer dummy` (it has no idea what the real value is).
- The mock upstream receives `Authorization: Bearer realtoken`.
- Daemon picks up via SIGHUP — no restart.

**Step 2: Run** — Expected: FAIL.

**Step 3: Implement** — fix any remaining glue. Likely involves the `teststack_test.go` helper growing a `writeRule()` and `setSecret()` helper.

**Step 4: Verify** — `make test-e2e` — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/test/e2e/
git commit -m "test(agent-gateway): add e2e secret-substitution test"
```

**End of Milestone 4 acceptance:** `TestSecretSubstitution` passes.

---

## Task 27: Unskip TestRuleReloadHotSwap

**Files:**

- Modify: `agent-gateway/test/e2e/rules_reload_test.go`

**Step 1: Write the failing assertion path**

Now that secrets + injection exist, the scaffold from Task 20 can assert the full behaviour. Remove the `t.Skip`. Assertions as written in Task 20.

**Step 2: Run** — Expected: FAIL (if any glue is missing).

**Step 3: Implement** — fix fallout.

**Step 4: Verify** — PASS.

**Step 5: Commit**

```bash
git add agent-gateway/test/e2e/
git commit -m "test(agent-gateway): enable rule-reload hot-swap assertions"
```

**End of Milestone 3 acceptance:** `TestRuleReloadHotSwap` passes end-to-end.

---

## Task 28: Agents schema + token mint + argon2 auth

**Files:**

- Create: `agent-gateway/internal/agents/registry.go`
- Create: `agent-gateway/internal/agents/token.go`
- Create: `agent-gateway/internal/agents/registry_test.go`
- Modify: `agent-gateway/internal/store/migrations.go`

**Step 1: Write the failing tests**

```go
func TestMintToken_PrefixAndFormat(t *testing.T) {
    tok := agents.MintToken()
    assert.True(t, strings.HasPrefix(tok, "agw_"))
    assert.GreaterOrEqual(t, len(tok), 36)
}

func TestRegistry_AddAndAuthenticate(t *testing.T) {
    r := newRegistry(t)
    tok, err := r.Add(ctx, "claude", "description")
    require.NoError(t, err)
    a, err := r.Authenticate(ctx, tok)
    require.NoError(t, err)
    assert.Equal(t, "claude", a.Name)
}

func TestRegistry_WrongTokenRejected(t *testing.T)         { ... }
func TestRegistry_RotateInvalidatesOld(t *testing.T)       { ... }
func TestRegistry_RmCascadesSecrets(t *testing.T)          {
    // Add agent, set scoped secret, remove agent, assert secrets row is gone.
}
func TestRegistry_AuthenticateUsesPrefixMap(t *testing.T) {
    // Under the hood: 100 agents, only the one whose prefix matches gets argon2-compared.
}
```

**Step 2: Run** — Expected: FAIL.

**Step 3: Implement**

- Migration 3: `CREATE TABLE agents (...)` per §5.
- `token.go`: `agw_` + 32 bytes base62. Use `crypto/rand`. `Prefix(tok)` returns `tok[:12]` (agw\_ + first 8 chars).
- `registry.go`: on `Add`, hash with `argon2id` (use `golang.org/x/crypto/argon2`, params: `time=1, memory=64*1024, threads=4, keyLen=32`). Store `token_hash`, `token_prefix`, `created_at`, `description`.
- `Authenticate(ctx, token)` — compute prefix, query `SELECT name, token_hash FROM agents WHERE token_prefix = ?`, argon2-compare (`subtle.ConstantTimeCompare` after `argon2.IDKey` on the candidate). Return `*Agent` with `Name`. On success, UPDATE `last_seen_at = now()` (write-through; per §7 coalescing is deferred to v1.1).
- In-memory prefix→hash map cached alongside; refreshed on `Add/Rm/Rotate` and on SIGHUP.
- `Rm(ctx, name)` issues the transactional cascade per §5: `DELETE FROM secrets WHERE scope = 'agent:'||?; DELETE FROM agents WHERE name = ?`.

**Step 4: Verify** — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/internal/agents/ agent-gateway/internal/store/
git commit -m "feat(agent-gateway): add agent registry with argon2 token auth"
```

---

## Task 29: `agent {add,list,show,rm,rotate}` CLI

**Files:**

- Create: `agent-gateway/cmd/agent-gateway/agent.go`
- Create: `agent-gateway/cmd/agent-gateway/agent_test.go`

**Step 1: Write the failing tests**

```go
func TestAgentAdd_PrintsTokenOnce(t *testing.T) {
    // stdout contains both the token (once) and a ready-to-paste HTTPS_PROXY URL.
}
func TestAgentList_NeverShowsFullToken(t *testing.T) { ... }
func TestAgentRotate_InvalidatesPrevious(t *testing.T) { ... }
```

**Step 2: Run** — Expected: FAIL.

**Step 3: Implement**

- `agent add <name>` prints the token and the ready-to-paste URL:

  ```
  token: agw_…
  HTTPS_PROXY=http://x:agw_…@127.0.0.1:8220
  HTTP_PROXY=http://x:agw_…@127.0.0.1:8220
  ```

- `agent show <name>` prints metadata only (no token, even the prefix).
- Every state change sends SIGHUP (idempotent, daemon optional).

**Step 4: Verify** — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/cmd/
git commit -m "feat(agent-gateway): add agent CLI commands"
```

---

## Task 30: CONNECT-time intercept decision + agent auth

**Files:**

- Modify: `agent-gateway/internal/proxy/connect.go`
- Create: `agent-gateway/internal/proxy/decide.go`
- Create: `agent-gateway/internal/proxy/decide_test.go`

**Step 1: Write the failing tests**

Cover every row of the §6 decision table:

| Token valid | `no_intercept_hosts` | Rule matches agent | IP literal | Decision     |
| ----------- | -------------------- | ------------------ | ---------- | ------------ |
| no          | —                    | —                  | —          | reject (407) |
| yes         | yes                  | —                  | —          | tunnel       |
| yes         | no                   | no                 | —          | tunnel       |
| yes         | no                   | yes                | yes        | tunnel       |
| yes         | no                   | yes                | no         | MITM         |

Plus: `Proxy-Authorization` missing, malformed, stale.

**Step 2: Run** — Expected: FAIL.

**Step 3: Implement**

- `Decide(ctx, host string, ag *agents.Agent, engine *rules.Engine, noIntercept []string) Decision` returns `DecisionTunnel | DecisionMITM | DecisionReject`.
- Input parsing: `parseAuth(header string)` extracts the token from `Basic base64("x:<tok>")`.
- If token invalid → return `DecisionReject` with `407 Proxy Authentication Required` headers; caller writes the HTTP reply and closes.
- Else, if host in `no_intercept_hosts` glob-matched list → tunnel. Glob impl same as rule host glob.
- Else, consult `engine.HostsForAgent(agent.Name)` (Task 16). Miss → tunnel.
- IP literal (`net.ParseIP(host) != nil`) → tunnel. (Globs are hostname-only per design.)
- Else → MITM.

**Step 4: Verify** — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/internal/proxy/
git commit -m "feat(agent-gateway): add CONNECT-time tunnel/MITM decision"
```

---

## Task 31: Approval broker (in-memory, ApprovalGuard pattern)

**Files:**

- Create: `agent-gateway/internal/approval/broker.go`
- Create: `agent-gateway/internal/approval/broker_test.go`

**Step 1: Write the failing tests**

```go
func TestRequest_ResolvesOnApprove(t *testing.T)  { ... }
func TestRequest_DeniedReturnsDecision(t *testing.T) { ... }
func TestRequest_TimeoutReturnsErrTimeout(t *testing.T) {
    // short timeout; assert the pending entry is removed after timeout.
}
func TestRequest_ContextCancelRemovesPending(t *testing.T) {
    // agent disconnects mid-wait; pending entry is removed immediately.
}
func TestRequest_QueueFullReturnsErrQueueFull(t *testing.T) {
    b := approval.New(approval.Opts{MaxPending: 1, Timeout: time.Hour})
    go func() { _, _ = b.Request(ctx, pending1) }()
    waitForPending(b, 1)
    _, err := b.Request(ctx, pending2)
    assert.ErrorIs(t, err, approval.ErrQueueFull)
}

func TestBroker_Pending_ViewInvariant(t *testing.T) {
    // Per §8 approval view invariant: the broker's PendingForDashboard
    // MUST strip request + response bodies and non-asserted headers.
}
```

**Step 2: Run** — Expected: FAIL.

**Step 3: Implement**

- `PendingRequest` struct: `ID ulid.ULID`, `Agent`, `RuleName`, `Method`, `Host`, `Path`, `Query`, `CreatedAt`, private `decision chan Decision`.
- `Broker.Request` — implements the `ApprovalGuard` pattern from design §13. See the code block in the design document — transcribe exactly.
- `Pending()` returns a slice with sensitive fields already zero-valued; no body, no header values beyond the ones the matched rule asserted (which is a field on `PendingRequest` the caller fills in with _only_ the rule's assertions, per §8 approval view invariant — this is enforced at construction time, not filtration time).
- `Decide(id, decision)` — looks up pending, sends on channel.
- `max_pending` cap — `ErrQueueFull` returned synchronously.

**Step 4: Verify** — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/internal/approval/
git commit -m "feat(agent-gateway): add approval broker with ApprovalGuard pattern"
```

---

## Task 32: Request ID (ULID) propagation + X-Request-ID on synth responses

**Files:**

- Modify: `agent-gateway/internal/proxy/pipeline.go`
- Modify: `agent-gateway/internal/proxy/connect.go`
- Create: `agent-gateway/internal/proxy/requestid.go`
- Create: `agent-gateway/internal/proxy/requestid_test.go`

**Step 1: Write the failing tests**

```go
func TestRequestID_AssignedImmediately(t *testing.T) { ... }
func TestRequestID_OnDenyResponse(t *testing.T) {
    // A deny rule triggers a synthesised 403; response must include X-Request-ID.
}
func TestRequestID_OnTimeout504(t *testing.T) {
    // require-approval times out → 504 response includes X-Request-ID.
}
func TestRequestID_NotOnForwardedResponse(t *testing.T) {
    // Upstream 200 — X-Request-ID is NOT on the forwarded response.
}
```

**Step 2: Run** — Expected: FAIL.

**Step 3: Implement**

- `NewULID()` helper via `oklog/ulid/v2` with an entropy-safe monotonic reader.
- In the pipeline: generate ULID as soon as the MITM handshake completes (or immediately post-CONNECT for tunnel rows). Put in `context.Context` via a typed key. Attach to every `slog` call (`.With("request_id", id)`).
- Synthesised responses (403, 502, 504) set `X-Request-ID: <ulid>` before writing.
- Forwarded responses: do NOT set the header. The Go proxy pipeline already passes through upstream headers unmodified; no action needed beyond _not_ injecting.

**Step 4: Verify** — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/internal/proxy/
git commit -m "feat(agent-gateway): assign ULID to every request; expose on synth responses"
```

---

## Task 33: Audit schema + Logger + writer in pipeline

**Files:**

- Create: `agent-gateway/internal/audit/audit.go`
- Create: `agent-gateway/internal/audit/audit_test.go`
- Modify: `agent-gateway/internal/store/migrations.go` (requests table + indexes)
- Modify: `agent-gateway/internal/proxy/pipeline.go`

**Step 1: Write the failing tests**

Table-driven: for each of the 8 representative rows in §5, fire a pipeline request and assert the audit row matches. Includes:

- Tunnel row (interception='tunnel', method/path NULL).
- MITM no-rule row (matched_rule NULL, forwarded).
- Happy-path allow (injection='applied', credential_ref + scope set).
- Fail-soft allow (injection='failed', error='secret_unresolved').
- Deny (outcome='blocked').
- Approved (approval='approved', injection='applied').
- Denied (approval='denied', outcome='blocked').
- Timed-out (approval='timed-out', outcome='blocked').

```go
func TestAudit_AllScenarios(t *testing.T) {
    tests := []struct{
        name string
        setup func(t *testing.T, s *teststack)
        wantRow audit.Record
    }{
        ...
    }
    for _, tc := range tests { ... }
}
```

**Step 2: Run** — Expected: FAIL.

**Step 3: Implement**

- Migration 4: `CREATE TABLE requests (...)` + four indexes (`idx_req_ts`, `idx_req_agent`, `idx_req_host`, `idx_req_rule`).
- `audit.Logger` interface per design §13. Fields: `Record(ctx, Entry) error`, `Query(ctx, Filter) ([]Entry, int, error)`, `Prune(ctx, before) (int, error)`.
- `NewLogger(db)` — prepared INSERT statement.
- Writer in pipeline: assemble the entry fields across the lifecycle (ts at decode, status + bytes + duration at response end). `defer auditor.Record(...)` so we always write a row, even on transport errors. Audit errors are logged and discarded (mcp-broker pattern).
- `credential_ref IS NOT NULL ⟺ injection = 'applied'` enforced in Go before INSERT (unit-test the invariant).

**Step 4: Verify** — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/internal/audit/ agent-gateway/internal/store/ agent-gateway/internal/proxy/
git commit -m "feat(agent-gateway): add audit logger with 5-column request-story schema"
```

---

## Task 34: Retention pruner

**Files:**

- Create: `agent-gateway/internal/audit/prune.go`
- Create: `agent-gateway/internal/audit/prune_test.go`

**Step 1: Write the failing test**

```go
func TestPrune_RemovesRowsOlderThanRetention(t *testing.T) { ... }
func TestPruneLoop_RunsAt0400(t *testing.T) {
    // Use an injected clock; advance to 03:59, assert no tick; advance to 04:00, assert a tick.
}
```

**Step 2: Run** — Expected: FAIL.

**Step 3: Implement**

- `Prune(ctx, before)` executes `DELETE FROM requests WHERE ts < ?` in a single statement; returns rows affected.
- `RunPruneLoop(ctx, logger, retention, tickAt)` — uses a `clock.Clock` interface (injectable). Fires `Prune(ctx, now-retention)` once at boot and then at the next `tickAt` local time (e.g., `04:00`), then every 24h.
- Hook into `RunServe` as a background goroutine.

**Step 4: Verify** — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/internal/audit/ agent-gateway/cmd/
git commit -m "feat(agent-gateway): add nightly retention pruner"
```

---

## Task 35: Dashboard — admin auth + SSE broker + HTTP handlers

**Files:**

- Create: `agent-gateway/internal/dashboard/dashboard.go`
- Create: `agent-gateway/internal/dashboard/sse.go`
- Create: `agent-gateway/internal/dashboard/auth.go`
- Create: `agent-gateway/internal/dashboard/dashboard_test.go`

**Step 1: Write the failing tests**

- Auth middleware mirrors `mcp-broker/internal/auth/auth.go` but with cookie name `agent-gateway-auth` and a `/dashboard/unauthorized` page that includes a re-auth form (per §8).
- `/api/pending`, `/api/audit`, `/api/events`, `/api/decide`, `/api/agents`, `/api/rules`, `/api/secrets` smoke tests.
- SSE broker: drop-on-full (32-element buffer); 15s keepalive; each subscriber gets `id:` frames containing the ULID.
- `GET /ca.pem` returns the CA cert, unauthenticated.

**Step 2: Run** — Expected: FAIL.

**Step 3: Implement**

- Port `auth.Middleware` from mcp-broker. Rename cookie. Add the re-auth form on `/dashboard/unauthorized`: a `<form method="POST" action="/dashboard/unauthorized">` that promotes a posted token to a cookie (same subtle-compare path as the query-param flow). Allowlist `/ca.pem` as unauthenticated.
- SSE broker: `type Event struct{ Kind string; ID ulid.ULID; Data any }`. `Broadcast(ev Event)` writes to each subscriber's channel with non-blocking send. `Subscribe(ctx)` returns a `<-chan []byte`. `handleEvents` writes `id: <ulid>\n` then `event: <kind>\n` then `data: <json>\n\n`.
- `/api/decide` calls `broker.Decide(id, decision)`. Returns 404 for unknown.
- `/api/pending` returns the `Broker.Pending()` slice (already invariant-safe per Task 31).
- `/api/audit` paginates via `audit.Logger.Query`.
- `/api/rules`, `/api/agents`, `/api/secrets` read from their respective stores. Secrets are metadata-only, never values.

**Step 4: Verify** — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/internal/dashboard/
git commit -m "feat(agent-gateway): add dashboard auth, SSE broker, and JSON API"
```

---

## Task 36: Dashboard embedded SPA (5 tabs)

**Files:**

- Create: `agent-gateway/internal/dashboard/index.html`
- Create: `agent-gateway/internal/dashboard/app.js`
- Create: `agent-gateway/internal/dashboard/styles.css`
- Create: `agent-gateway/internal/dashboard/favicon.svg`
- Modify: `agent-gateway/internal/dashboard/dashboard.go` (embed them)

**Step 1: Write the failing test**

```go
func TestIndexServesHTMLWithEmbeddedBundle(t *testing.T) {
    // GET /dashboard/ → 200; body contains "<title>agent-gateway</title>" and the
    // app.js script tag; also GET /dashboard/app.js → 200 non-empty.
}
```

Plus a Playwright-based e2e (integration-tagged) that the Live Feed tab shows incoming requests — deferred until Task 37 where a real server is running.

**Step 2: Run** — Expected: FAIL.

**Step 3: Implement**

Single-page vanilla JS app with 5 tabs (Live feed, Audit, Rules, Agents, Secrets). Key requirements:

- **Live feed**: subscribes to `/api/events`; appends rows. On page load, fetches last 200 from `/api/audit` to seed. Pinned section at top for pending approvals (filtered from both `/api/pending` on load and subsequent `approval` events). Tunnel rows render dimmer + collapsible. Pending rows never show body / unasserted headers (enforced server-side; UI just renders what the API returns).
- **Audit**: paginated `/api/audit` queries with time-range only in v1. Show full metadata per row.
- **Rules**: renders rules grouped by file, read-only. Adds `Last matched at` + `24h match count` computed from audit query. Missing-secret badge for rules whose `${secrets.*}` doesn't resolve (server-side `/api/rules` response annotates this).
- **Agents**: list; last-seen + 24h outcome breakdown. No plaintext tokens.
- **Secrets**: list by `(name, scope, created, rotated, last-used, refcount)`.

Keep the JS small. Follow `mcp-broker/internal/dashboard/index.html` style (no framework, no build step, `fetch` + `EventSource` only). The `//go:embed *.html *.js *.css *.svg` line goes in `dashboard.go`.

**Step 4: Verify** — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/internal/dashboard/
git commit -m "feat(agent-gateway): add embedded dashboard SPA with 5 tabs"
```

---

## Task 37: Wire dashboard + approval + auth into serve; open-browser logic

**Files:**

- Modify: `agent-gateway/cmd/agent-gateway/serve.go`
- Create: `agent-gateway/cmd/agent-gateway/token.go` (admin token handling + `token rotate admin`)

**Step 1: Write the failing tests**

```go
func TestServe_DashboardServesIndex(t *testing.T)         { ... }
func TestServe_DashboardRequiresAuth(t *testing.T)        { ... }
func TestServe_OpenBrowserSkippedWhenHeadless(t *testing.T) {
    // --headless flag passed; openBrowser should not be invoked.
}
func TestTokenRotateAdmin_InvalidatesCookie(t *testing.T) {
    // CLI: `token rotate admin` writes a new token; the old cookie no longer auths.
}
```

**Step 2: Run** — Expected: FAIL.

**Step 3: Implement**

- Admin token handling: `auth.EnsureAdminToken(path)` (ported from mcp-broker pattern). Prints the URL at startup (first run only — detect "first run" = admin-token file didn't exist before this invocation). Rotation regenerates the file and invalidates any in-flight cookies by swapping the token the middleware compares against.
- `token rotate admin` CLI → overwrites `admin-token`, SIGHUP to daemon.
- `serve` flag `--headless` skips open-browser regardless of config.
- In `RunServe`: construct `dashboard.Server(deps)`, mount at `/`. `GET /ca.pem` is mounted at the root of the dashboard HTTP server (unauthenticated exception).

**Step 4: Verify** — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/cmd/ agent-gateway/internal/
git commit -m "feat(agent-gateway): wire dashboard with admin auth and open-browser"
```

---

## Task 38: E2E — TestDashboardLiveFeed + TestApprovalViewInvariant

**Files:**

- Create: `agent-gateway/test/e2e/dashboard_test.go`

**Step 1: Write the failing tests**

```go
func TestDashboardLiveFeed(t *testing.T) {
    // Subscribe to /api/events. Fire 20 requests. Assert 20 request events arrive.
    // Fetch /api/audit?limit=100 → 20 rows.
}

func TestApprovalViewInvariant(t *testing.T) {
    // Rule with verdict=require-approval. Agent fires a POST with body "secret-body"
    // and header "X-Hint: confidential".
    // Collect the SSE `approval` event; assert it contains neither "secret-body"
    // nor "X-Hint" / "confidential".
    // Also fetch /api/pending; same assertion.
    // Decide approve via /api/decide; request completes.
}

func TestAgentCancelPropagatesToUpstream(t *testing.T) {
    // Start a slow upstream that blocks reading request body.
    // Agent fires, then cancels mid-read.
    // Assert the upstream handler sees ctx.Done().
}
```

**Step 2: Run** — Expected: FAIL.

**Step 3: Implement** — glue as needed. Likely involves ensuring the dashboard's SSE `approval` event payload really is stripped.

**Step 4: Verify** — `make test-e2e` — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/test/e2e/ agent-gateway/internal/
git commit -m "test(agent-gateway): add e2e dashboard + approval-invariant tests"
```

**End of Milestone 5 acceptance:** live feed test + approval-invariant test + agent-cancel test all pass.

---

## Task 39: E2E — TestAgentScopeFilter

**Files:**

- Create: `agent-gateway/test/e2e/agent_scope_test.go`

**Step 1: Write the failing test**

```go
func TestAgentScopeFilter(t *testing.T) {
    // Two agents "a1" and "a2".
    // Rule with agents = ["a1"] on host "scoped.test.local".
    // a1 CONNECT to scoped.test.local:443 → MITM (decrypted, audit row has matched_rule).
    // a2 CONNECT to scoped.test.local:443 → tunnel (audit row: interception=tunnel, matched_rule=NULL).
}
```

**Step 2: Run** — Expected: FAIL if anything is off.

**Step 3: Implement** — any fixup to make `HostsForAgent` correctly filter by agent name.

**Step 4: Verify** — PASS.

**Step 5: Commit**

```bash
git add agent-gateway/test/e2e/
git commit -m "test(agent-gateway): add e2e agent-scope filter test"
```

**End of Milestone 6 acceptance:** `TestAgentScopeFilter` passes.

---

## Task 40: Polish — shadow warnings, startup summary, tunneled-hosts banner

**Files:**

- Modify: `agent-gateway/cmd/agent-gateway/secret.go`
- Modify: `agent-gateway/cmd/agent-gateway/serve.go`
- Modify: `agent-gateway/internal/dashboard/app.js`
- Create: `agent-gateway/internal/dashboard/tunneled_banner_test.go`

**Step 1: Write the failing tests**

- Secret shadow warning already covered in Task 23 — leave alone.
- Startup summary test: capture `RunServe` log output; assert it contains agent count, secret count, rule count, MITM-eligible host list.
- Tunneled-hosts banner: dashboard JS-integration (hand-coded) — `/api/stats/tunneled-hosts?since=24h` returns a list of hosts that were tunneled but have no rule. The banner renders when that list is non-empty.

**Step 2: Run** — Expected: FAIL on the new parts.

**Step 3: Implement** — add the API endpoint, log line, and banner. Keep the banner dismissible via `localStorage` (per §8 discoverability prompt).

**Step 4: Verify** — PASS.

**Step 5: Commit**

```bash
git add agent-gateway/
git commit -m "feat(agent-gateway): add startup summary and tunneled-hosts banner"
```

---

## Task 41: Documentation — README, NOTICE, CLAUDE.md

**Files:**

- Create: `agent-gateway/README.md`
- Create: `agent-gateway/NOTICE`
- Modify: `agent-gateway/CLAUDE.md`
- Modify: `CLAUDE.md` (root — add `agent-gateway/` entry to the structure list)
- Modify: `README.md` (root — add `agent-gateway` row if that README lists tools)

**Step 1: Write the (very short) failing assertions**

```go
func TestNOTICEMentionsOnecli(t *testing.T) {
    data, err := os.ReadFile("NOTICE")
    require.NoError(t, err)
    assert.Contains(t, string(data), "onecli")
    assert.Contains(t, string(data), "no code is incorporated")
}

func TestREADMEHasRequiredSections(t *testing.T) {
    data, err := os.ReadFile("README.md")
    require.NoError(t, err)
    s := string(data)
    for _, h := range []string{"## Install", "## First run", "## Prior art"} {
        assert.Contains(t, s, h)
    }
}
```

**Step 2: Run** — Expected: FAIL.

**Step 3: Implement**

- `README.md` sections:
  - `## Install` — `go install`, CA trust notes.
  - `## First run` — create admin token, add an agent, add a secret, write a rule file, sandbox trusts CA, sandbox sets HTTPS_PROXY.
  - `## Concepts` — agents, rules, secrets, CA, dashboard.
  - `## Architecture` — brief pointer to `.designs/2026-04-16-agent-gateway.md`.
  - `## Prior art` — crediting onecli (per §10).
- `NOTICE` — per §10:
  > agent-gateway is a clean-room Go reimplementation inspired by onecli
  > (https://github.com/onecli/onecli). No code is incorporated; the
  > architectural concepts listed in the design document are the only
  > overlap. This file exists as good-citizen attribution; no license
  > compliance obligation is asserted.
- `agent-gateway/CLAUDE.md` — flesh out to the same depth as `mcp-broker/CLAUDE.md`: dev commands, architecture block, conventions list (SQLite driver, `go-keyring`, `log/slog` pattern, audit write-errors discarded, `ApprovalGuard` pattern, CONNECT decision filter, etc.).
- Root `CLAUDE.md` — add the `agent-gateway/` line to the structure block.
- Root `README.md` — add the `agent-gateway` entry wherever tools are listed (if that pattern exists; inspect and match).

**Step 4: Verify** — Expected: PASS.

**Step 5: Commit**

```bash
git add agent-gateway/README.md agent-gateway/NOTICE agent-gateway/CLAUDE.md CLAUDE.md README.md
git commit -m "docs(agent-gateway): add README, NOTICE, and CLAUDE.md"
```

---

## Task 42: `make audit` pass

**Files:**

- (no files — this is a verification task)

**Step 1: Write the failing check**

From `agent-gateway/`: `make audit`.

Expected initially: FAIL (lint findings, format differences, missing tidy).

**Step 2: Run the failing check** — confirm what breaks.

**Step 3: Fix**

- Run `make fmt` → commit changes.
- Run `make tidy` → commit changes.
- Run `make lint`; fix each finding or add targeted `//nolint:...` comments with one-line justifications (match `mcp-broker` style where relevant).
- Run `make test` + `make test-integration` + `make test-e2e`.

**Step 4: Verify**

From repo root: `make audit`. Expected: PASS for `agent-gateway` and no regression in other tools.

**Step 5: Commit**

```bash
git add agent-gateway/
git commit -m "chore(agent-gateway): pass make audit"
```

**End of Milestone 7 acceptance:** `make audit` passes for the new tool; README has install + first-run + prior-art sections; fresh-machine smoke test (trust CA, add agent, add secret, add rule, agent makes a request) works in under two minutes.

---

## Post-plan sanity checklist

Before declaring the plan complete, verify:

- [ ] Every §12 milestone's acceptance test is covered by a specific task (M1→T6, M2→T12, M3→T27, M4→T26, M5→T38, M6→T39, M7→T42).
- [ ] Every §13 interface is instantiated at least once in `cmd/agent-gateway/serve.go`.
- [ ] Every §2 non-goal is either excluded from the tasks or only implemented to the degree specified in the non-goal bullet.
- [ ] No task hardcodes an absolute repo path.
- [ ] The plan's total task count (42) is reasonable for a ~5 kloc Go binary with MITM, HCL, SQLite, SSE, and CLI surface.

<!-- Documentation updates covered by Task 41. -->
