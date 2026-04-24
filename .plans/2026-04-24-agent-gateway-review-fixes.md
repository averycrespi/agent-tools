# agent-gateway Review Fixes Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use Skill(executing-plans) to implement this plan task-by-task.

**Goal:** Land the 4 P0, 8 P1, and 10 P2 changes from the 2026-04-24 review of `agent-gateway` so the tool moves from "safe by default if you know the conventions" to "hard to accidentally misuse."

**Architecture:** Six work batches, each a self-contained PR. Batches land in order (1 → 6). Within a batch, tasks are TDD with small commits. Source of truth for decisions is `.designs/2026-04-24-agent-gateway-review-fixes.md`.

**Tech Stack:** Go, SQLite (`ncruces/go-sqlite3`, WASM), `spf13/cobra`, `stretchr/testify`, HCL config. See `agent-gateway/CLAUDE.md` for tool conventions (error wrapping, slog, SIGHUP flow, master key, admin token). All work is under `agent-gateway/`.

---

## Batch 1 — Pipeline security (P0-1, P0-3, P1-3, P1-4)

Single PR. All changes in `agent-gateway/internal/proxy/pipeline.go` and test file. Establishes the `X-Agent-Gateway-Reason` header convention used by later batches.

### Task 1.1 — Reason-code constants + helper

**Files:**

- Modify: `agent-gateway/internal/proxy/pipeline.go`
- Test: `agent-gateway/internal/proxy/pipeline_test.go`

**Step 1: Write a failing test for the helper.**

Add to `pipeline_test.go`:

```go
func TestHTTPErrorWithReason_SetsHeaderAndBody(t *testing.T) {
	rec := httptest.NewRecorder()
	httpErrorWithReason(rec, "boom", http.StatusForbidden, ReasonRuleDeny)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, "rule-deny", rec.Header().Get("X-Agent-Gateway-Reason"))
	require.Contains(t, rec.Body.String(), "boom")
}
```

**Step 2: Run test to verify it fails.**

```
go test ./internal/proxy/ -run TestHTTPErrorWithReason -v
```

Expected: FAIL — `httpErrorWithReason` and `ReasonRuleDeny` undefined.

**Step 3: Add reason constants and helper.**

At the top of `pipeline.go` (below imports, near other package-level declarations):

```go
// Reason codes for X-Agent-Gateway-Reason. Stable strings documented in
// docs/security-model.md.
const (
	ReasonBodyMatcherBypassed = "body-matcher-bypassed"
	ReasonRuleDeny            = "rule-deny"
	ReasonUnknownVerdict      = "unknown-verdict"
	ReasonApprovalDenied      = "approval-denied"
	ReasonApprovalTimeout     = "approval-timeout"
	ReasonQueueFull           = "queue-full"
	ReasonNoApprovalBroker    = "no-approval-broker"
	ReasonSecretUnresolved    = "secret-unresolved"
	ReasonForbiddenHost       = "forbidden-host"
	ReasonBodyReadError       = "body-read-error"
)

// httpErrorWithReason writes an HTTP error with an X-Agent-Gateway-Reason
// header. Header must be set before http.Error writes headers.
func httpErrorWithReason(w http.ResponseWriter, body string, code int, reason string) {
	w.Header().Set("X-Agent-Gateway-Reason", reason)
	http.Error(w, body, code)
}
```

**Step 4: Run tests to verify pass.**

```
go test ./internal/proxy/ -run TestHTTPErrorWithReason -v
```

Expected: PASS.

**Step 5: Commit.**

```
git add agent-gateway/internal/proxy/pipeline.go agent-gateway/internal/proxy/pipeline_test.go
git commit -m "feat(agent-gateway): add X-Agent-Gateway-Reason header helper"
```

---

### Task 1.2 — P1-4: body-matcher 403 names the cap

**Files:**

- Modify: `agent-gateway/internal/proxy/pipeline.go:274-281`
- Test: `agent-gateway/internal/proxy/pipeline_test.go`

**Step 1: Write the failing test.**

Add (or extend an existing body-matcher-bypass test):

```go
func TestPipeline_BodyMatcherBypassed_NamesCap(t *testing.T) {
	// Drive a request through the pipeline with a body exceeding maxBodyBuffer
	// where a rule has a body matcher. Set up the existing test harness that
	// other pipeline tests use (TestPipeline_RuleDeny_..., etc.) and mirror it.
	rec, _ := runPipelineWithOversizedBody(t) // helper pattern per existing tests
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, "body-matcher-bypassed", rec.Header().Get("X-Agent-Gateway-Reason"))
	require.Contains(t, rec.Body.String(), "max_body_buffer")
	require.Contains(t, rec.Body.String(), "proxy_behavior.max_body_buffer")
}
```

If a harness helper like `runPipelineWithOversizedBody` doesn't yet exist, use the pattern from nearby tests (look for tests that set `maxBodyBuffer` and drive through `ServeHTTP`). The test body can be inline rather than a helper.

**Step 2: Run test — FAIL on body content check.**

```
go test ./internal/proxy/ -run TestPipeline_BodyMatcherBypassed_NamesCap -v
```

**Step 3: Update pipeline.go:278-280.**

Replace:

```go
w.Header().Set("X-Request-ID", reqID)
http.Error(w, "Forbidden: rule body matcher bypassed", http.StatusForbidden)
return
```

With:

```go
w.Header().Set("X-Request-ID", reqID)
msg := fmt.Sprintf(
    "Forbidden: body exceeds max_body_buffer (%s); raise proxy_behavior.max_body_buffer in config.hcl",
    humanSize(p.maxBodyBuffer),
)
httpErrorWithReason(w, msg, http.StatusForbidden, ReasonBodyMatcherBypassed)
return
```

If `humanSize` does not exist on the package, add:

```go
// humanSize returns "1 MiB", "512 KiB", etc.
func humanSize(n int64) string {
	const (
		KiB = 1024
		MiB = 1024 * KiB
		GiB = 1024 * MiB
	)
	switch {
	case n >= GiB:
		return fmt.Sprintf("%d GiB", n/GiB)
	case n >= MiB:
		return fmt.Sprintf("%d MiB", n/MiB)
	case n >= KiB:
		return fmt.Sprintf("%d KiB", n/KiB)
	default:
		return fmt.Sprintf("%d B", n)
	}
}
```

Add `"fmt"` to imports if not already present.

**Step 4: Run tests — PASS.**

```
go test ./internal/proxy/ -v
```

**Step 5: Commit.**

```
git add agent-gateway/internal/proxy/pipeline.go agent-gateway/internal/proxy/pipeline_test.go
git commit -m "feat(agent-gateway): body-matcher 403 names max_body_buffer cap"
```

---

### Task 1.3 — P0-1: unknown verdict → deny

**Files:**

- Modify: `agent-gateway/internal/proxy/pipeline.go:335-339`
- Test: `agent-gateway/internal/proxy/pipeline_test.go`

**Step 1: Write the failing test.**

```go
func TestPipeline_UnknownVerdict_Denies(t *testing.T) {
	// Construct a *rules.Engine that returns a Match with Rule.Verdict = "future-verdict".
	// Use the test utilities already in this package (look for how other tests fake
	// or wire up the rules engine).
	rec, auditEntries := runPipelineWithUnknownVerdict(t, "future-verdict")
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, "unknown-verdict", rec.Header().Get("X-Agent-Gateway-Reason"))
	require.Len(t, auditEntries, 1)
	require.Equal(t, "unknown_verdict", auditEntries[0].Error)
}
```

Use the existing test harness pattern. If there isn't a rules-engine fake, construct a minimal `*rules.Rule` with `Verdict = "future-verdict"` and wire it into the pipeline's evaluator (follow the pattern already used by deny/approval tests).

**Step 2: Run — FAIL.**

```
go test ./internal/proxy/ -run TestPipeline_UnknownVerdict -v
```

**Step 3: Update pipeline.go:335-339.**

Replace:

```go
default:
    // Unknown verdict: treat as allow to be forward-compatible.
    p.log.Warn("proxy: unknown rule verdict; treating as allow",
        "verdict", m.Rule.Verdict, "request_id", reqID)
}
```

With:

```go
default:
    // Unknown verdict: fail closed. Any new verdict must be added at parse
    // time (internal/rules/parse.go). Reaching this branch means version
    // skew or in-memory corruption; treat as deny to avoid bypass.
    a.Error = "unknown_verdict"
    p.log.Error("proxy: unknown rule verdict; denying",
        "verdict", m.Rule.Verdict, "request_id", reqID)
    w.Header().Set("X-Request-ID", reqID)
    httpErrorWithReason(w, "Forbidden", http.StatusForbidden, ReasonUnknownVerdict)
    return
}
```

**Step 4: Run — PASS.**

```
go test ./internal/proxy/ -v
```

**Step 5: Commit.**

```
git add agent-gateway/internal/proxy/pipeline.go agent-gateway/internal/proxy/pipeline_test.go
git commit -m "fix(agent-gateway): deny on unknown rule verdict"
```

---

### Task 1.4 — P0-3: redact sensitive headers in approval payload

**Files:**

- Modify: `agent-gateway/internal/proxy/pipeline.go` (likely near `assertedHeaders` — grep for it)
- Test: `agent-gateway/internal/proxy/pipeline_test.go`

**Step 1: Locate `assertedHeaders`.**

```
grep -n "assertedHeaders" agent-gateway/internal/proxy/*.go
```

**Step 2: Write the failing test.**

```go
func TestAssertedHeaders_RedactsSensitive(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer secret-token-123")
	h.Set("X-API-Key", "apk_xxx")
	h.Set("Cookie", "session=abc")
	h.Set("X-Custom-Tenant", "acme")
	got := assertedHeaders(h, []string{"Authorization", "X-API-Key", "Cookie", "X-Custom-Tenant"})
	require.Equal(t, "<redacted>", got.Get("Authorization"))
	require.Equal(t, "<redacted>", got.Get("X-API-Key"))
	require.Equal(t, "<redacted>", got.Get("Cookie"))
	require.Equal(t, "acme", got.Get("X-Custom-Tenant"))
}
```

**Step 3: Run — FAIL.**

```
go test ./internal/proxy/ -run TestAssertedHeaders_RedactsSensitive -v
```

**Step 4: Update `assertedHeaders`.**

Above the function, add:

```go
// sensitiveHeaders are redacted in approval payloads to keep credentials
// out of the approval UI and any captured SSE stream. Comparison is
// case-insensitive via http.Header.Get/Set canonicalisation.
var sensitiveHeaders = map[string]struct{}{
	"Authorization":       {},
	"Proxy-Authorization": {},
	"Cookie":              {},
	"Set-Cookie":          {},
	"X-Api-Key":           {},
	"X-Auth-Token":        {},
}
```

Inside `assertedHeaders`, when copying each header, check canonical name membership and substitute:

```go
for _, name := range asserted {
    canonical := http.CanonicalHeaderKey(name)
    if _, sensitive := sensitiveHeaders[canonical]; sensitive {
        out.Set(canonical, "<redacted>")
        continue
    }
    if v := src.Get(canonical); v != "" {
        out.Set(canonical, v)
    }
}
```

(Adapt to the actual body of `assertedHeaders` — preserve its existing loop shape.)

**Step 5: Run — PASS.**

```
go test ./internal/proxy/ -v
```

**Step 6: Commit.**

```
git add agent-gateway/internal/proxy/pipeline.go agent-gateway/internal/proxy/pipeline_test.go
git commit -m "fix(agent-gateway): redact sensitive headers in approval payload"
```

---

### Task 1.5 — P1-3: queue-full → 503 + Retry-After

**Files:**

- Modify: `agent-gateway/internal/proxy/pipeline.go:310-315`
- Test: `agent-gateway/internal/proxy/pipeline_test.go`

**Step 1: Write the failing test.**

```go
func TestPipeline_ApprovalQueueFull_Returns503(t *testing.T) {
	rec, _ := runPipelineWithApprovalError(t, approval.ErrQueueFull)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, "30", rec.Header().Get("Retry-After"))
	require.Equal(t, "queue-full", rec.Header().Get("X-Agent-Gateway-Reason"))
}

func TestPipeline_ApprovalBrokerError_Returns502(t *testing.T) {
	rec, _ := runPipelineWithApprovalError(t, errors.New("some other failure"))
	require.Equal(t, http.StatusBadGateway, rec.Code)
}
```

Use the existing approval-broker fake pattern. If one doesn't exist, build an approval stub that returns the configured error from `Request`.

**Step 2: Run — FAIL on queue-full test.**

```
go test ./internal/proxy/ -run TestPipeline_Approval -v
```

**Step 3: Update pipeline.go:310-315.**

Replace:

```go
if apErr != nil {
    p.log.Error("proxy: approval broker error", "request_id", reqID, "err", apErr)
    w.Header().Set("X-Request-ID", reqID)
    http.Error(w, "approval error", http.StatusBadGateway)
    return
}
```

With:

```go
if apErr != nil {
    p.log.Error("proxy: approval broker error", "request_id", reqID, "err", apErr)
    w.Header().Set("X-Request-ID", reqID)
    if errors.Is(apErr, approval.ErrQueueFull) {
        w.Header().Set("Retry-After", "30")
        httpErrorWithReason(w, "approval queue full", http.StatusServiceUnavailable, ReasonQueueFull)
        return
    }
    httpErrorWithReason(w, "approval error", http.StatusBadGateway, "approval-broker-error")
    return
}
```

Add `"errors"` and the `approval` import if not present.

**Step 4: Run — PASS.**

```
go test ./internal/proxy/ -v
```

**Step 5: Commit.**

```
git add agent-gateway/internal/proxy/pipeline.go agent-gateway/internal/proxy/pipeline_test.go
git commit -m "fix(agent-gateway): return 503 + Retry-After on approval queue full"
```

---

### Task 1.6 — Sweep: every 4xx/5xx sets a reason header

**Files:**

- Modify: `agent-gateway/internal/proxy/pipeline.go` (all `http.Error` call sites)
- Test: `agent-gateway/internal/proxy/pipeline_test.go`

**Step 1: Find remaining bare `http.Error` calls.**

```
grep -n "http.Error" agent-gateway/internal/proxy/pipeline.go
```

Expected sites not yet converted (update each): rule-deny at line ~291, approval-denied at ~320, approval-timeout at ~325, no-approval-broker at ~299, secret-unresolved path (search for `secret_unresolved`), body-read-error at line ~260, forbidden-host path (grep `Forbidden`).

**Step 2: Add one test per site asserting header.**

```go
func TestPipeline_RuleDeny_SetsReason(t *testing.T) {
	rec, _ := runPipelineWithRuleVerdict(t, "deny")
	require.Equal(t, "rule-deny", rec.Header().Get("X-Agent-Gateway-Reason"))
}
func TestPipeline_ApprovalDenied_SetsReason(t *testing.T) { /* ... approval-denied ... */ }
func TestPipeline_ApprovalTimeout_SetsReason(t *testing.T) { /* ... approval-timeout ... */ }
func TestPipeline_NoApprovalBroker_SetsReason(t *testing.T) { /* ... no-approval-broker ... */ }
// ... etc for every reason constant added in Task 1.1
```

**Step 3: Run — FAIL (some pass by accident from prior tasks).**

```
go test ./internal/proxy/ -v
```

**Step 4: Convert each remaining `http.Error` to `httpErrorWithReason`.**

For each call site, replace `http.Error(w, msg, code)` with `httpErrorWithReason(w, msg, code, ReasonX)` using the appropriate constant.

**Step 5: Run — PASS.**

```
go test ./internal/proxy/ -v
```

**Step 6: Commit.**

```
git add agent-gateway/internal/proxy/pipeline.go agent-gateway/internal/proxy/pipeline_test.go
git commit -m "feat(agent-gateway): set X-Agent-Gateway-Reason on every proxy 4xx/5xx"
```

---

## Batch 2 — Startup/fatal (P0-2, P2-9)

Single PR. All changes in `agent-gateway/cmd/agent-gateway/serve.go` plus pipeline nil-injector simplification.

### Task 2.1 — P0-2: secrets store unavailable → fatal

**Files:**

- Modify: `agent-gateway/cmd/agent-gateway/serve.go:171-179`
- Test: `agent-gateway/cmd/agent-gateway/serve_test.go`

**Step 1: Write the failing test.**

Use the pattern from the nearby test file. If there isn't already an `execServe`-returns-error path under test, add:

```go
func TestExecServe_SecretsStoreError_ReturnsError(t *testing.T) {
	// Use a mechanism that forces secrets.NewStore to fail. Easiest path:
	// inject a temp XDG where the master-key file exists but is unreadable
	// (chmod 000) and the keychain is unavailable (t.Setenv of
	// GO_KEYRING_MOCK_KEYCHAIN or the test-friendly env used by
	// isolate secrets tests — see recent commit 29f98e7).
	// Assert execServe returns a non-nil error containing "secrets store".
}
```

If the existing test infrastructure makes this infeasible, add an injectable `newSecretsStore` function variable in `serve.go` with default `secrets.NewStore` and override it in the test to return `(nil, errors.New("boom"))`.

**Step 2: Run — FAIL.**

```
go test ./cmd/agent-gateway/ -run TestExecServe_SecretsStoreError -v
```

**Step 3: Update serve.go:168-189.**

Replace the soft-fail block with:

```go
// 3b. Initialise the secrets store and header injector. Failure is fatal:
// running with no injector silently leaks sandbox dummy tokens through rules
// that were meant to swap in real credentials, indistinguishable from "no
// rule matched" in the audit log.
secretsStore, err := secrets.NewStore(db, log)
if err != nil {
    fmt.Fprintf(os.Stderr,
        "agent-gateway: secrets store unavailable: %v\n"+
            "  The daemon requires a working secrets store to inject credentials.\n"+
            "  If the keychain is unavailable, ensure the file fallback path is readable.\n",
        err,
    )
    return fmt.Errorf("secrets store unavailable: %w", err)
}
inj := inject.NewInjector(secretsStore, cfg.Secrets.CacheTTL)
proxyInjector := &injectAdapter{inj: inj}

// 3b.1. Surface coverage warnings.
for _, w := range warnSecretCoverage(ctx, engine, secretsStore) {
    log.Warn(w)
}
```

**Step 4: Run — PASS.**

```
go test ./cmd/agent-gateway/ -v
```

**Step 5: Commit.**

```
git add agent-gateway/cmd/agent-gateway/serve.go agent-gateway/cmd/agent-gateway/serve_test.go
git commit -m "fix(agent-gateway): fail fatally when secrets store unavailable"
```

---

### Task 2.2 — Drop nil-injector branches

**Files:**

- Modify: `agent-gateway/internal/proxy/pipeline.go` (wherever `p.injector == nil` is checked — grep for it)

**Step 1: Find the checks.**

```
grep -n "proxyInjector\|Injector == nil\|p.injector" agent-gateway/internal/proxy/*.go
```

**Step 2: Remove the dead branches.**

With P0-2 fatal, `proxyInjector` is always non-nil at daemon start. Delete any `if injector == nil` guards in the pipeline (including the fail-soft path for secret_unresolved that assumes the injector might be absent). Ensure the `secret-unresolved` path is still reachable via the injector itself returning an error — the reason header must still fire.

**Step 3: Run existing tests — all PASS.**

```
go test ./internal/proxy/ -v
```

If a test previously relied on `injector == nil` behavior, update it to use a stub injector that returns the specific error the test targets.

**Step 4: Commit.**

```
git add agent-gateway/internal/proxy/pipeline.go agent-gateway/internal/proxy/pipeline_test.go
git commit -m "refactor(agent-gateway): drop nil-injector branches (injector is always present)"
```

---

### Task 2.3 — P2-9: log startup paths

**Files:**

- Modify: `agent-gateway/cmd/agent-gateway/serve.go` (near the existing `"agent-gateway started"` line at ~341-345)
- Test: `agent-gateway/cmd/agent-gateway/serve_test.go`

**Step 1: Write the failing test.**

```go
func TestExecServe_PrintsPaths(t *testing.T) {
	// Arrange: set XDG to tempdirs, run serve for a short window (ctx cancelled
	// after listeners are up and the startup lines have been written), capture
	// stdout. Follow the pattern already used for the "Dashboard:" / "Proxy:"
	// startup lines.
	stdout := runServeUntilStarted(t)
	require.Contains(t, stdout, "config:")
	require.Contains(t, stdout, "state_db:")
	require.Contains(t, stdout, "ca_cert:")
	require.Contains(t, stdout, "pid_file:")
	require.NotContains(t, stdout, "ca_key:") // deliberately omitted
}
```

**Step 2: Run — FAIL.**

```
go test ./cmd/agent-gateway/ -run TestExecServe_PrintsPaths -v
```

**Step 3: Add the print block.**

Above the existing `"agent-gateway started"` slog line, add:

```go
// Paths for operator debugging (systemd/launchd stdout picks these up).
fmt.Fprintf(cmd.OutOrStdout(), "config:    %s\n", paths.ConfigFile())
fmt.Fprintf(cmd.OutOrStdout(), "state_db:  %s\n", paths.StateDB())
fmt.Fprintf(cmd.OutOrStdout(), "ca_cert:   %s\n", paths.CACert())
fmt.Fprintf(cmd.OutOrStdout(), "pid_file:  %s\n", paths.PIDFile())
log.Info("paths",
    "config", paths.ConfigFile(),
    "state_db", paths.StateDB(),
    "ca_cert", paths.CACert(),
    "pid_file", paths.PIDFile(),
)
```

Confirm `paths` package has the accessors — add thin wrappers if `CACert()` or `StateDB()` don't exist.

**Step 4: Run — PASS.**

**Step 5: Commit.**

```
git add agent-gateway/cmd/agent-gateway/serve.go agent-gateway/cmd/agent-gateway/serve_test.go agent-gateway/internal/paths/
git commit -m "feat(agent-gateway): log resolved config/state/ca/pid paths on startup"
```

---

## Batch 3 — Validation centralization (P0-4, P1-2, P1-7, P1-8)

Single PR. Extracts coverage checks and plugs them into every caller.

### Task 3.1 — store.OpenReadOnly helper

**Files:**

- Modify: `agent-gateway/internal/store/store.go` (or wherever `Open` lives — grep for `func Open`)
- Test: `agent-gateway/internal/store/store_test.go`

**Step 1: Check for existing helper.**

```
grep -n "func Open" agent-gateway/internal/store/*.go
```

**Step 2: Write the failing test.**

```go
func TestOpenReadOnly_RejectsWrites(t *testing.T) {
	dir := t.TempDir()
	// First create a regular DB and close it.
	db, err := store.Open(filepath.Join(dir, "state.db"))
	require.NoError(t, err)
	require.NoError(t, db.Close())

	ro, err := store.OpenReadOnly(filepath.Join(dir, "state.db"))
	require.NoError(t, err)
	defer ro.Close()

	_, err = ro.Exec("CREATE TABLE x (y TEXT)")
	require.Error(t, err)
}

func TestOpenReadOnly_AbsentFile_ErrNotExist(t *testing.T) {
	_, err := store.OpenReadOnly(filepath.Join(t.TempDir(), "missing.db"))
	require.ErrorIs(t, err, os.ErrNotExist)
}
```

**Step 3: Run — FAIL.**

**Step 4: Add the helper.**

```go
// OpenReadOnly opens a SQLite file read-only. Returns os.ErrNotExist if path
// is missing (caller can skip optional checks in that case).
func OpenReadOnly(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	// ncruces/go-sqlite3 honours ?mode=ro.
	db, err := sql.Open("sqlite3", "file:"+path+"?mode=ro&_txlock=deferred")
	if err != nil {
		return nil, fmt.Errorf("open %s read-only: %w", path, err)
	}
	return db, nil
}
```

Imports: `database/sql`, `errors`, `fmt`, `os`.

**Step 5: Run — PASS.**

**Step 6: Commit.**

```
git add agent-gateway/internal/store/
git commit -m "feat(agent-gateway): add store.OpenReadOnly helper"
```

---

### Task 3.2 — P0-4: rules check runs secret coverage, --strict flag

**Files:**

- Modify: `agent-gateway/cmd/agent-gateway/rules.go:110-174`
- Test: `agent-gateway/cmd/agent-gateway/rules_test.go`

**Step 1: Write failing tests.**

```go
func TestRulesCheck_WithCoverageWarnings_PrintsThem(t *testing.T) {
	// Arrange: XDG tempdirs, state DB with a secret allowed only for "api.github.com",
	// a rule matching "*.example.com" referencing ${secrets.that_one}. Run rules check.
	buf := &bytes.Buffer{}
	err := execRulesCheck(cmd, rulesDir, stubLister, buf /* out */)
	require.NoError(t, err)
	require.Contains(t, buf.String(), "coverage")
}

func TestRulesCheck_Strict_FailsOnWarnings(t *testing.T) {
	// Same fixture, but pass --strict. Expect non-nil error.
}

func TestRulesCheck_NoStateDB_SkipsCoverage(t *testing.T) {
	// XDG with no state.db file. Expect note "skipping secret coverage check",
	// no error, no panic.
}
```

**Step 2: Run — FAIL.**

**Step 3: Extend `execRulesCheck` to open DB read-only and call coverage.**

Update the command to accept a `--strict` flag; after the existing parse-and-syntax checks, call:

```go
dbPath := paths.StateDB()
db, err := store.OpenReadOnly(dbPath)
switch {
case errors.Is(err, os.ErrNotExist):
    fmt.Fprintln(out, "note: state DB not found; skipping secret coverage check")
case err != nil:
    return fmt.Errorf("rules check: open state db: %w", err)
default:
    defer db.Close()
    secretsStore, err := secrets.OpenForRead(db, log)
    if err != nil {
        return fmt.Errorf("rules check: load secrets: %w", err)
    }
    engine, err := rules.NewEngineFromRules(parsed)
    if err != nil {
        return fmt.Errorf("rules check: build engine: %w", err)
    }
    warns := warnSecretCoverage(ctx, engine, secretsStore)
    for _, w := range warns {
        fmt.Fprintln(out, "warning:", w)
    }
    if strict && len(warns) > 0 {
        return fmt.Errorf("rules check: %d coverage warning(s) (--strict)", len(warns))
    }
}
```

Wire `--strict` into the Cobra command:

```go
var strict bool
cmd := &cobra.Command{
    Use:   "check",
    Short: "Validate rules files for syntax and coverage",
    RunE: func(c *cobra.Command, _ []string) error {
        return execRulesCheck(c, paths.RulesDir(), loadKnownSecrets(c.ErrOrStderr()), strict)
    },
}
cmd.Flags().BoolVar(&strict, "strict", false, "Exit non-zero on any warning")
```

If `secrets.OpenForRead` doesn't exist, add it alongside `secrets.NewStore` — a constructor that opens without initialising write state. Alternatively, `secrets.NewStore` may be usable with a read-only DB; try it first.

**Step 4: Run — PASS.**

**Step 5: Commit.**

```
git add agent-gateway/cmd/agent-gateway/rules.go agent-gateway/cmd/agent-gateway/rules_test.go agent-gateway/internal/secrets/
git commit -m "feat(agent-gateway): rules check runs secret coverage, adds --strict"
```

---

### Task 3.3 — P1-2: warnNoInterceptOverlap

**Files:**

- Modify: `agent-gateway/cmd/agent-gateway/secret_coverage.go`
- Test: `agent-gateway/cmd/agent-gateway/secret_coverage_test.go`
- Modify: `agent-gateway/cmd/agent-gateway/serve.go` (add callers at startup + SIGHUP)
- Modify: `agent-gateway/cmd/agent-gateway/rules.go` (add caller in `execRulesCheck`)

**Step 1: Write failing test.**

```go
func TestWarnNoInterceptOverlap_GroupsByEntry(t *testing.T) {
	// Two no_intercept_hosts entries, three rules, overlap as:
	//   - "api.github.com" shadows rules A and B
	//   - "*.example.com" shadows rule C
	// Expect exactly two warnings, each listing its shadowed rules.
	engine := buildEngineWithRules(t,
		ruleSpec{name: "A", file: "10-gh.hcl", host: "api.github.com"},
		ruleSpec{name: "B", file: "10-gh.hcl", host: "*.github.com"},
		ruleSpec{name: "C", file: "20-ex.hcl", host: "foo.example.com"},
	)
	warns := warnNoInterceptOverlap(engine, []string{"api.github.com", "*.example.com"})
	require.Len(t, warns, 2)
	require.Contains(t, warns[0], "api.github.com")
	require.Contains(t, warns[0], `"A"`)
	require.Contains(t, warns[0], `"B"`)
	require.Contains(t, warns[1], "*.example.com")
	require.Contains(t, warns[1], `"C"`)
}
```

Use the existing rules-engine construction from other tests in the same file.

**Step 2: Run — FAIL.**

**Step 3: Implement.**

Add to `secret_coverage.go`:

```go
// warnNoInterceptOverlap returns one warning per no_intercept_hosts entry that
// has any rule whose match.host could plausibly overlap. Conservative overlap
// — false positives are acceptable; false negatives are the real footgun.
func warnNoInterceptOverlap(engine *rules.Engine, patterns []string) []string {
	if engine == nil || len(patterns) == 0 {
		return nil
	}
	var warns []string
	for i, p := range patterns {
		shadowed := engine.RulesOverlappingHost(p) // or iterate engine.Rules() and use hostmatch.Overlap
		if len(shadowed) == 0 {
			continue
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "proxy_behavior.no_intercept_hosts[%d] %q shadows:\n", i, p)
		for _, r := range shadowed {
			fmt.Fprintf(&sb, "  - rule %q (%s) match.host %q\n", r.Name, r.File, r.Match.Host)
		}
		warns = append(warns, strings.TrimSuffix(sb.String(), "\n"))
	}
	return warns
}
```

If `engine.RulesOverlappingHost` doesn't exist, implement it in `internal/rules/engine.go` using `internal/hostmatch.Overlap` (or add `hostmatch.Overlap` if missing — use a conservative approximation, see existing `hostmatch` code for the pattern used by `warnSecretCoverage`).

**Step 4: Run — PASS.**

**Step 5: Wire into `serve.go` startup + SIGHUP and `rules.go execRulesCheck`.**

In `serve.go:185-189` (currently the `warnSecretCoverage` loop), add a parallel block:

```go
for _, w := range warnNoInterceptOverlap(engine, cfg.ProxyBehavior.NoInterceptHosts) {
    log.Warn(w)
}
```

Mirror in the SIGHUP handler (~line 434-438) and in `execRulesCheck` (print to `out`).

**Step 6: Run — PASS all tests, integration tests.**

**Step 7: Commit.**

```
git add agent-gateway/cmd/agent-gateway/ agent-gateway/internal/rules/ agent-gateway/internal/hostmatch/
git commit -m "feat(agent-gateway): warn when no_intercept_hosts shadows rules"
```

---

### Task 3.4 — P1-7: public-suffix no_intercept_hosts → error

**Files:**

- Modify: `agent-gateway/internal/config/validate.go:170-194`
- Test: `agent-gateway/internal/config/validate_test.go`

**Step 1: Update existing warning-path test to error-path.**

In `validate_test.go`, find the test that asserts `warnIfPublicSuffix` produces a warning for `*.com`. Update assertions:

```go
func TestValidateNoInterceptHosts_RejectsPublicSuffix(t *testing.T) {
	_, err := validateNoInterceptHosts([]string{"*.com"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "public suffix")
}
```

**Step 2: Run — FAIL (still returns warnings, not error).**

**Step 3: Update `validateNoInterceptHosts`.**

In `validate.go:127-194`, convert the public-suffix branch from appending a warning to returning an error:

```go
if isPublicSuffix(pattern) {
    return nil, fmt.Errorf(
        "no_intercept_hosts[%d] %q strips to a public suffix; list specific domains instead",
        i, pattern,
    )
}
```

Adjust `validateConfig` callers so the error propagates. Keep any other warnings the function emits; the public-suffix path is the only one promoted.

**Step 4: Run — PASS.**

**Step 5: Commit.**

```
git add agent-gateway/internal/config/
git commit -m "fix(agent-gateway): reject public-suffix no_intercept_hosts"
```

---

### Task 3.5 — P1-8: CLI-side coverage warnings on mutations

**Files:**

- Modify: `agent-gateway/cmd/agent-gateway/secret.go` (all mutation commands)
- Test: `agent-gateway/cmd/agent-gateway/secret_test.go`

**Step 1: Write failing tests.**

One per mutation (`secret add`, `secret update`, `secret rm`, `secret bind`, `secret unbind`):

```go
func TestSecretAdd_PrintsCoverageWarnings(t *testing.T) {
	// Arrange: rules dir with a rule matching "*.example.com" referencing ${secrets.new}.
	// Add a secret "new" with allowed_hosts = ["api.github.com"] (no overlap).
	// Assert stdout contains a coverage warning about "new".
}
```

**Step 2: Run — FAIL.**

**Step 3: Add the post-mutation coverage sweep.**

Factor out a helper in `secret.go`:

```go
// printCoverageAfterMutation loads the rules engine + secrets store and prints
// any coverage warnings to out. Non-fatal — failures to build the engine are
// logged at debug level and the mutation still succeeds.
func printCoverageAfterMutation(ctx context.Context, out io.Writer, store secrets.Store) {
    parsed, _, err := rules.ParseDir(paths.RulesDir())
    if err != nil {
        return
    }
    engine, err := rules.NewEngineFromRules(parsed)
    if err != nil {
        return
    }
    for _, w := range warnSecretCoverage(ctx, engine, store) {
        fmt.Fprintln(out, "warning:", w)
    }
}
```

Call it after the DB mutation and before the best-effort SIGHUP in each of the five commands.

**Step 4: Run — PASS.**

**Step 5: Commit.**

```
git add agent-gateway/cmd/agent-gateway/secret.go agent-gateway/cmd/agent-gateway/secret_test.go
git commit -m "feat(agent-gateway): print secret coverage warnings from mutation CLIs"
```

---

## Batch 4 — Reload / config model (P1-1, P1-10, P2-5)

Single PR. Renames `rules reload` → `reload`, adds config hash check, adds config edit diff.

### Task 4.1 — Create top-level `reload` command

**Files:**

- Create: `agent-gateway/cmd/agent-gateway/reload.go`
- Create: `agent-gateway/cmd/agent-gateway/reload_test.go`
- Modify: `agent-gateway/cmd/agent-gateway/root.go` (wire the new command in)

**Step 1: Write the failing test.**

```go
func TestReload_NoDaemon_Errors(t *testing.T) {
	buf := &bytes.Buffer{}
	err := execReload(nil, filepath.Join(t.TempDir(), "missing.pid"),
		alwaysVerify, failSend, buf)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no daemon running")
}

func TestReload_RunningDaemon_SendsSIGHUP(t *testing.T) {
	// Use the daemon package's test utilities; same pattern as existing
	// execRulesReload tests.
	var sent os.Signal
	sendSpy := func(pid int, sig os.Signal) error { sent = sig; return nil }
	buf := &bytes.Buffer{}
	err := execReload(nil, validPIDFile(t), alwaysVerify, sendSpy, buf)
	require.NoError(t, err)
	require.Equal(t, syscall.SIGHUP, sent)
	require.Contains(t, buf.String(), "reloaded")
}
```

**Step 2: Run — FAIL.**

**Step 3: Implement `reload.go`.**

```go
package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/daemon"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
)

func newReloadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reload",
		Short: "Signal the running daemon to reload rules, agents, secrets, admin token, and CA",
		Long: `Sends SIGHUP to the daemon to re-read all mutable state:
  - Rule files in rules.d/
  - Agent registry (tokens)
  - Secret value cache (re-decrypts on next use)
  - Admin token file
  - CA certificate (invalidates leaf cache)

Does NOT reload config.hcl. Edits to config.hcl require a restart.

Exits non-zero if no daemon is running.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return execReload(cmd, paths.PIDFile(),
				daemon.DefaultVerifyComm, daemon.DefaultSendSignal,
				cmd.OutOrStdout())
		},
	}
}

func execReload(_ interface{}, pidPath string,
	verify func(int) (bool, error), send func(int, os.Signal) error,
	out io.Writer,
) error {
	err := daemon.SignalDaemonWithVerifier(pidPath, verify, send)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("no daemon running; start it with 'agent-gateway serve'")
		}
		return fmt.Errorf("reload: %w", err)
	}
	fmt.Fprintln(out, "reloaded")
	return nil
}
```

Wire it in `root.go` alongside the existing `rulesCmd`:

```go
rootCmd.AddCommand(newReloadCmd())
```

**Step 4: Run — PASS.**

**Step 5: Commit.**

```
git add agent-gateway/cmd/agent-gateway/reload.go agent-gateway/cmd/agent-gateway/reload_test.go agent-gateway/cmd/agent-gateway/root.go
git commit -m "feat(agent-gateway): add top-level reload command"
```

---

### Task 4.2 — Deprecate `rules reload` as hidden alias

**Files:**

- Modify: `agent-gateway/cmd/agent-gateway/rules.go:70-108`
- Test: `agent-gateway/cmd/agent-gateway/rules_reload_test.go`

**Step 1: Update the existing rules reload test to assert the deprecation notice.**

```go
func TestRulesReload_EmitsDeprecationNotice(t *testing.T) {
	buf := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err := execRulesReload(nil, validPIDFile(t), alwaysVerify, noopSend, buf, stderr)
	require.NoError(t, err)
	require.Contains(t, stderr.String(), "deprecated")
	require.Contains(t, stderr.String(), "agent-gateway reload")
}
```

**Step 2: Run — FAIL.**

**Step 3: Update `newRulesReloadCmd` to be hidden and print a notice.**

```go
func newRulesReloadCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "reload",
		Short:  "(deprecated) Use 'agent-gateway reload' instead",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.ErrOrStderr(),
				"'agent-gateway rules reload' is deprecated; use 'agent-gateway reload' instead")
			return execReload(cmd, paths.PIDFile(),
				daemon.DefaultVerifyComm, daemon.DefaultSendSignal,
				cmd.OutOrStdout())
		},
	}
}
```

Delete the old `execRulesReload` body (or have it call `execReload`). Existing tests that drove the old function update to call `execReload`.

**Step 4: Run — PASS.**

**Step 5: Commit.**

```
git add agent-gateway/cmd/agent-gateway/rules.go agent-gateway/cmd/agent-gateway/rules_reload_test.go
git commit -m "chore(agent-gateway): deprecate 'rules reload' in favour of 'reload'"
```

---

### Task 4.3 — Daemon writes config hash to meta table

**Files:**

- Modify: `agent-gateway/cmd/agent-gateway/serve.go`
- Modify: `agent-gateway/internal/store/` migrations (if the `meta` table's column set needs extending — likely already a `(key, value)` shape)
- Test: `agent-gateway/cmd/agent-gateway/serve_test.go`

**Step 1: Check the meta table shape.**

```
grep -rn "meta" agent-gateway/internal/store/migrations*.sql
```

**Step 2: Write the failing test.**

```go
func TestExecServe_WritesConfigHashToMeta(t *testing.T) {
	// Start serve against a known config.hcl, stop it, open state DB,
	// read meta where key='config_hash', assert equal to sha256 of the file.
}
```

**Step 3: Add the write.**

In `serve.go`, after config load but before startup lines:

```go
if err := store.PutMeta(db, "config_hash", sha256File(cfgPath)); err != nil {
    return fmt.Errorf("record config hash: %w", err)
}
```

Add `sha256File` helper (reads file, hex-encodes `sha256.Sum256`). Add `store.PutMeta`/`GetMeta` if they don't exist (simple `INSERT OR REPLACE INTO meta(key, value) VALUES(?, ?)`).

**Step 4: Run — PASS.**

**Step 5: Commit.**

```
git add agent-gateway/cmd/agent-gateway/ agent-gateway/internal/store/
git commit -m "feat(agent-gateway): record config.hcl hash in meta table on start"
```

---

### Task 4.4 — `reload` compares config hash, warns

**Files:**

- Modify: `agent-gateway/cmd/agent-gateway/reload.go`
- Test: `agent-gateway/cmd/agent-gateway/reload_test.go`

**Step 1: Write failing test.**

```go
func TestReload_ConfigHashChanged_Warns(t *testing.T) {
	// Arrange: seed meta.config_hash with stale hash, current config.hcl has different content.
	buf := &bytes.Buffer{}
	err := execReload(...)
	require.NoError(t, err)
	require.Contains(t, buf.String(), "config.hcl has changed")
	require.Contains(t, buf.String(), "restart")
}
```

**Step 2: Run — FAIL.**

**Step 3: Add the check.**

Before sending SIGHUP in `execReload`, open state DB read-only, read `config_hash`, compare against current file's sha256. If mismatch:

```go
fmt.Fprintln(out,
    "warning: config.hcl has changed since the daemon started.",
    "\n  Changes to config.hcl require a daemon restart.",
    "\n  Apply with: kill $(cat "+pidPath+") and re-run 'agent-gateway serve'.",
)
```

Still do the SIGHUP after — the warning is informational, not blocking.

**Step 4: Run — PASS.**

**Step 5: Commit.**

```
git add agent-gateway/cmd/agent-gateway/reload.go agent-gateway/cmd/agent-gateway/reload_test.go
git commit -m "feat(agent-gateway): reload warns when config.hcl has changed"
```

---

### Task 4.5 — `config edit` diffs pre/post

**Files:**

- Modify: `agent-gateway/cmd/agent-gateway/config.go:48-67`
- Test: `agent-gateway/cmd/agent-gateway/config_test.go`

**Step 1: Write failing test.**

```go
func TestConfigEdit_WarnsOnFieldChange(t *testing.T) {
	// Arrange: XDG tempdir with initial config. Mock $EDITOR via a helper
	// script path in t.Setenv that rewrites the file in place (change
	// approval.timeout from "5m" to "30m").
	buf := &bytes.Buffer{}
	err := execConfigEdit(cfgPath, buf /*stdout*/)
	require.NoError(t, err)
	require.Contains(t, buf.String(), "config.hcl has changed")
	require.Contains(t, buf.String(), "approval.timeout")
}
```

**Step 2: Run — FAIL.**

**Step 3: Extend `newConfigEditCmd`.**

Parse config before launching the editor (`config.Load(cfgPath)` → `preCfg`). After the editor exits, parse again (`postCfg`). Diff the two via reflection over struct fields (or explicit field list — probably cleaner). If any fields differ, print the warning with per-field old/new values.

A small diff helper:

```go
func diffConfig(pre, post *config.Config) []string {
    // Walk well-known field names explicitly — no reflection needed.
    var diffs []string
    if pre.Approval.Timeout != post.Approval.Timeout {
        diffs = append(diffs, fmt.Sprintf("approval.timeout: %s -> %s",
            pre.Approval.Timeout, post.Approval.Timeout))
    }
    // ... every top-level restart-required field, matching docs/config.md
    return diffs
}
```

Print after editor returns:

```go
if diffs := diffConfig(preCfg, postCfg); len(diffs) > 0 {
    fmt.Fprintln(cmd.OutOrStdout(), "warning: config.hcl has changed. These edits require a daemon restart:")
    for _, d := range diffs {
        fmt.Fprintln(cmd.OutOrStdout(), "  -", d)
    }
    fmt.Fprintln(cmd.OutOrStdout(), "Apply with: kill <pid> and re-run 'agent-gateway serve'.")
}
```

**Step 4: Run — PASS.**

**Step 5: Commit.**

```
git add agent-gateway/cmd/agent-gateway/config.go agent-gateway/cmd/agent-gateway/config_test.go
git commit -m "feat(agent-gateway): config edit warns on restart-required changes"
```

---

### Task 4.6 — Update CLAUDE.md with restart-only rule

**Files:**

- Modify: `agent-gateway/CLAUDE.md:82` (the existing "config.hcl is not re-parsed" block)

**Step 1: Replace the paragraph.**

Change the existing block to:

```
`config.hcl` is fully restart-only. No field in `config.hcl` is re-parsed on
SIGHUP — edits require a SIGTERM + fresh `serve` invocation. The daemon records
a sha256 of `config.hcl` in the SQLite `meta` table at startup; `agent-gateway
reload` warns if the on-disk file differs. `agent-gateway config edit` also
diffs pre/post and warns on any change.
```

**Step 2: Commit.**

```
git add agent-gateway/CLAUDE.md
git commit -m "docs(agent-gateway): document config.hcl as fully restart-only"
```

---

## Batch 5 — Audit / CLI polish (P1-5, P2-2, P2-3)

Single PR.

### Task 5.1 — P1-5: populate audit Query with truncation

**Files:**

- Modify: `agent-gateway/internal/proxy/pipeline.go:164` (grep for the audit assembly site)
- Modify: `agent-gateway/internal/audit/audit.go` (add `truncate` helper if not already local to pipeline)
- Test: `agent-gateway/internal/proxy/pipeline_test.go`

**Step 1: Write failing test.**

```go
func TestPipeline_StoresQueryStringWithTruncation(t *testing.T) {
	// Drive a request with a long RawQuery (say 3 KB). Assert the audit row
	// has Query populated, length == 2048 + len("…"), and ends with "…".
	long := strings.Repeat("a=1&", 1024)
	rec, entries := runPipelineWithRawQuery(t, long)
	require.Equal(t, http.StatusOK, rec.Code) // or whatever default allow fixture returns
	require.NotNil(t, entries[0].Query)
	require.True(t, strings.HasSuffix(*entries[0].Query, "…"))
	require.LessOrEqual(t, len(*entries[0].Query), 2048+len("…"))
}

func TestPipeline_StoresShortQueryVerbatim(t *testing.T) {
	rec, entries := runPipelineWithRawQuery(t, "sort=updated&page=2")
	require.Equal(t, "sort=updated&page=2", *entries[0].Query)
}
```

**Step 2: Run — FAIL.**

**Step 3: Populate the field.**

Near `pipeline.go:164`, after `a.Path = r.URL.Path`:

```go
if r.URL.RawQuery != "" {
    q := truncateString(r.URL.RawQuery, 2048)
    a.Query = &q
}
```

Add `truncateString`:

```go
// truncateString returns s unchanged if len(s) <= max, otherwise s[:max] + "…".
func truncateString(s string, max int) string {
    if len(s) <= max {
        return s
    }
    return s[:max] + "…"
}
```

**Step 4: Run — PASS.**

**Step 5: Commit.**

```
git add agent-gateway/internal/proxy/pipeline.go agent-gateway/internal/proxy/pipeline_test.go
git commit -m "feat(agent-gateway): populate audit Query field (truncated at 2KiB)"
```

---

### Task 5.2 — P2-2: Long blocks on destructive commands

**Files:**

- Modify: `agent-gateway/cmd/agent-gateway/master_key.go`
- Modify: `agent-gateway/cmd/agent-gateway/admin_token.go`
- Modify: `agent-gateway/cmd/agent-gateway/ca.go`
- Modify: `agent-gateway/cmd/agent-gateway/agent.go` (the `rm` command)
- Modify: `agent-gateway/cmd/agent-gateway/secret.go` (the `rm` command)
- Test: the existing test files for each

**Step 1: Write failing test per file.**

```go
func TestMasterKeyRotateCmd_HasLongHelp(t *testing.T) {
	cmd := newMasterKeyRotateCmd(/*deps*/)
	require.NotEmpty(t, cmd.Long)
	require.Contains(t, cmd.Long, "Immediate consequences")
	require.Contains(t, cmd.Long, "Recovery")
}
```

Repeat for `admin-token rotate` (mention "existing dashboard sessions are invalidated"), `ca rotate` (mention "every sandbox must re-trust the new CA"), `agent rm` (mention "sandbox sees 407 on next request"), `secret rm` (mention "rules referencing this secret will 401").

**Step 2: Run — FAIL.**

**Step 3: Add `Long` strings.**

Example for `master_key.go`:

```go
Long: `Rewrap the data-encryption key under a new master key.

Immediate consequences:
  - A new master key is generated and persisted to keychain or file fallback.
  - All encrypted secrets in SQLite are re-wrapped under the new key.
  - The previous master key is deleted best-effort after a successful rotation.

Recovery:
  If the re-encryption transaction fails after a new key is persisted, both
  master keys will exist. The active key id is tracked in the SQLite 'meta'
  table (active_key_id); recover by pointing it back at the previous id.`,
```

Repeat for each destructive command. Keep `Short` unchanged.

**Step 4: Run — PASS.**

**Step 5: Commit.**

```
git add agent-gateway/cmd/agent-gateway/
git commit -m "docs(agent-gateway): add Long help to destructive commands"
```

---

### Task 5.3 — P2-3: `--output json` on agent list

**Files:**

- Modify: `agent-gateway/cmd/agent-gateway/agent.go` (the `list` command)
- Test: `agent-gateway/cmd/agent-gateway/agent_test.go`

**Step 1: Write failing test.**

```go
func TestAgentList_JSONOutput(t *testing.T) {
	// Seed two agents. Run 'agent list -o json'.
	buf := &bytes.Buffer{}
	err := execAgentList(registry, "json", buf)
	require.NoError(t, err)
	var payload struct {
		Agents []struct {
			Name       string `json:"name"`
			Prefix     string `json:"prefix"`
			CreatedAt  string `json:"created_at"`
			LastSeenAt string `json:"last_seen_at"`
		} `json:"agents"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &payload))
	require.Len(t, payload.Agents, 2)
	// Ensure token hashes are not exposed.
	require.NotContains(t, buf.String(), "hash")
	require.NotContains(t, buf.String(), "token")
}

func TestAgentList_TextOutput_Default(t *testing.T) {
	// Unchanged output from prior behavior.
}

func TestAgentList_InvalidOutput_Errors(t *testing.T) {
	err := execAgentList(registry, "yaml", &bytes.Buffer{})
	require.Error(t, err)
}
```

**Step 2: Run — FAIL.**

**Step 3: Implement.**

Add `-o/--output` flag to the Cobra command. In `execAgentList`, branch on value:

```go
switch output {
case "", "text":
    return writeAgentListText(w, agents)
case "json":
    return json.NewEncoder(w).Encode(map[string]any{"agents": agentsForJSON(agents)})
default:
    return fmt.Errorf("--output must be 'json' or 'text'")
}
```

`agentsForJSON` returns a slice of structs with exactly the four fields listed; no token hashes, no salts.

**Step 4: Run — PASS.**

**Step 5: Commit.**

```
git add agent-gateway/cmd/agent-gateway/agent.go agent-gateway/cmd/agent-gateway/agent_test.go
git commit -m "feat(agent-gateway): add --output json to 'agent list'"
```

---

### Task 5.4 — P2-3: `--output json` on secret list

**Files:**

- Modify: `agent-gateway/cmd/agent-gateway/secret.go` (the `list` command)
- Test: `agent-gateway/cmd/agent-gateway/secret_test.go`

**Step 1: Write failing test.**

```go
func TestSecretList_JSONOutput(t *testing.T) {
	// Seed two secrets with allowed_hosts. Run 'secret list -o json'.
	buf := &bytes.Buffer{}
	err := execSecretList(store, "json", buf)
	require.NoError(t, err)
	var payload struct {
		Secrets []struct {
			Name         string   `json:"name"`
			AllowedHosts []string `json:"allowed_hosts"`
			BoundRules   []string `json:"bound_rules"`
			CreatedAt    string   `json:"created_at"`
		} `json:"secrets"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &payload))
	require.Len(t, payload.Secrets, 2)
	// Ensure encrypted values are not exposed.
	require.NotContains(t, buf.String(), "ciphertext")
	require.NotContains(t, buf.String(), "nonce")
}
```

**Step 2: Run — FAIL.**

**Step 3: Implement same pattern as Task 5.3.**

**Step 4: Run — PASS.**

**Step 5: Commit.**

```
git add agent-gateway/cmd/agent-gateway/secret.go agent-gateway/cmd/agent-gateway/secret_test.go
git commit -m "feat(agent-gateway): add --output json to 'secret list'"
```

---

## Batch 6 — Documentation (P1-6, P2-6, P2-7, P2-10, P2-11, P2-12)

Single PR. No TDD — content changes only. Each task lists the exact file and section; commit at the end of each task.

### Task 6.1 — `docs/rules.md` glob table + CONNECT-host callout

**Files:**

- Modify: `agent-gateway/docs/rules.md` (locate the `match.host` section)

Add a subsection "Glob semantics":

```markdown
### Glob semantics

`match.host` uses glob patterns. Single `*` matches within one DNS label
(does not cross `.`); `**` crosses `.` boundaries.

| Pattern           | Matches                                             | Does not match       |
| ----------------- | --------------------------------------------------- | -------------------- |
| `api.example.com` | `api.example.com`                                   | `foo.example.com`    |
| `*.example.com`   | `foo.example.com`                                   | `a.b.example.com`    |
| `**.example.com`  | `example.com`, `foo.example.com`, `a.b.example.com` | (matches everything) |
```

Add a callout near the top of the `match.host` section:

```markdown
> **Note:** `match.host` is matched against the CONNECT target (the hostname
> the sandbox asked the gateway to tunnel to), not the inner `Host:` header
> of the HTTP request. See `docs/security-model.md` for the full TLS
> interception flow.
```

Commit:

```
git add agent-gateway/docs/rules.md
git commit -m "docs(agent-gateway): glob semantics table and CONNECT-host callout"
```

---

### Task 6.2 — `default.hcl` 0s timeout comment

**Files:**

- Modify: `agent-gateway/internal/config/default.hcl:49-50`

Add a comment line above the two `0s` fields:

```hcl
# 0s means no deadline. Set e.g. "5m" to bound extremely long streaming requests.
request_body_read   = "0s"
response_body_read  = "0s"
```

Commit:

```
git add agent-gateway/internal/config/default.hcl
git commit -m "docs(agent-gateway): explain 0s means no deadline in default.hcl"
```

---

### Task 6.3 — Example rules setup comments

**Files:**

- Modify: every file under `agent-gateway/examples/rules.d/*.hcl` that references `${secrets.X}`

For each file, prepend a comment block modelled on `00-github-denylist.hcl:1-63`:

```hcl
# Setup:
#   echo -n "ghp_yourtoken" | agent-gateway secret add github_token --host api.github.com
#
# Without this, rules referencing ${secrets.github_token} will fail with:
#   X-Agent-Gateway-Reason: secret-unresolved
```

Commit:

```
git add agent-gateway/examples/
git commit -m "docs(agent-gateway): add secret setup comments to example rules"
```

---

### Task 6.4 — `docs/config.md`

**Files:**

- Create: `agent-gateway/docs/config.md`

One-page reference. One table per config block. Structure:

```markdown
# Configuration reference

All fields in `config.hcl` require a daemon restart to take effect. SIGHUP
(via `agent-gateway reload`) does not re-parse this file.

Paths: see `agent-gateway paths` or the startup banner.

## `listen` block

| Field   | Type   | Default          | Description                   |
| ------- | ------ | ---------------- | ----------------------------- |
| `proxy` | string | `127.0.0.1:8220` | Proxy listener address.       |
| `admin` | string | `127.0.0.1:8221` | Dashboard/admin API listener. |

## `timeouts` block

...
```

Cover every block in `internal/config/default.hcl`. Each row: field, type, default, short description. Add a footer noting `reload` hash check.

Commit:

```
git add agent-gateway/docs/config.md
git commit -m "docs(agent-gateway): add docs/config.md reference"
```

---

### Task 6.5 — README sections

**Files:**

- Modify: `agent-gateway/README.md`

Add three sections after the existing Quickstart:

```markdown
## Stopping

The daemon shuts down cleanly on SIGTERM:

    kill $(cat $XDG_CONFIG_HOME/agent-gateway/agent-gateway.pid)

In-flight requests finish before the process exits.

## Upgrading

Schema migrations run automatically on `serve` startup. Downgrades are
not supported — the SQLite schema is forward-only. Back up
`$XDG_DATA_HOME/agent-gateway/state.db` before upgrading across major
versions.

## Single instance per user

Only one `agent-gateway serve` may run per user at a time. Concurrent
starts fail at PID-file acquisition or with a listener bind error. To
run multiple instances (e.g., for testing) use different XDG base dirs.
```

Commit:

```
git add agent-gateway/README.md
git commit -m "docs(agent-gateway): add stopping/upgrading/single-instance sections"
```

---

### Task 6.6 — Update `docs/cli.md`

**Files:**

- Modify: `agent-gateway/docs/cli.md`

1. Rename the `rules reload` entry to top-level `reload`; add a "deprecated alias" note for `rules reload`.
2. Add a new section "`X-Agent-Gateway-Reason` codes" listing every constant from Task 1.1 with one-line meaning.
3. Add JSON schema blocks for `agent list -o json` and `secret list -o json` (the shapes from Tasks 5.3 and 5.4).
4. Add a row to the SIGHUP table noting that the inject cache clear is for `secret update` propagation (not TTL changes).

Commit:

```
git add agent-gateway/docs/cli.md
git commit -m "docs(agent-gateway): cli.md reflects reload rename, reason codes, JSON output"
```

---

## Final verification (after all batches)

Run from the repo root:

```
make -C agent-gateway audit
make -C agent-gateway test-integration
make -C agent-gateway test-e2e
```

All three must be green before the last batch merges.

<!-- Documentation updates are covered by Batch 6. -->
