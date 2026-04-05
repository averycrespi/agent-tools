# Mobile Approval via Telegram Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use Skill(executing-plans) to implement this plan task-by-task.

**Goal:** Extend mcp-broker with opt-in Telegram-based approval so any configured approver (dashboard or Telegram) can resolve a pending request — first response wins.

**Architecture:** Add `DenialReason string` to the audit model and propagate it through the `Approver` interface. A new `MultiApprover` fans requests out to all approvers concurrently with a shared timeout. A new `TelegramApprover` uses Telegram Bot API polling (no inbound connections) to send notifications with inline Approve/Deny buttons.

**Tech Stack:** Go 1.25, Telegram Bot API (plain HTTP, no SDK), `net/http`, `database/sql`, SQLite, `stretchr/testify`

---

## Task 1: Add DenialReason to audit package

**Files:**
- Modify: `mcp-broker/internal/audit/audit.go`
- Modify: `mcp-broker/internal/audit/audit_test.go`

**Step 1: Write the failing test**

Add to `mcp-broker/internal/audit/audit_test.go`:

```go
func TestLogger_RecordWithDenialReason(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	l, err := NewLogger(path)
	require.NoError(t, err)
	defer func() { _ = l.Close(context.Background()) }()

	denied := false
	err = l.Record(context.Background(), Record{
		Timestamp:    time.Now(),
		Tool:         "fs.write",
		Verdict:      "require-approval",
		Approved:     &denied,
		DenialReason: "timeout",
	})
	require.NoError(t, err)

	records, _, err := l.Query(context.Background(), QueryOpts{Limit: 10})
	require.NoError(t, err)
	require.Equal(t, "timeout", records[0].DenialReason)
}
```

**Step 2: Run test to verify it fails**

```bash
cd mcp-broker && go test ./internal/audit/... -run TestLogger_RecordWithDenialReason -v
```
Expected: FAIL — `Record` struct has no `DenialReason` field.

**Step 3: Add DenialReason to Record struct**

In `mcp-broker/internal/audit/audit.go`, update the `Record` struct (lines 18–25):

```go
type Record struct {
	Timestamp    time.Time      `json:"timestamp"`
	Tool         string         `json:"tool"`
	Args         map[string]any `json:"args,omitempty"`
	Verdict      string         `json:"verdict"`
	Approved     *bool          `json:"approved,omitempty"`
	DenialReason string         `json:"denial_reason,omitempty"`
	Error        string         `json:"error,omitempty"`
}
```

**Step 4: Add schema migration and update INSERT/SELECT**

In `mcp-broker/internal/audit/audit.go`:

Replace `createSQL` (lines 34–46) and `insertSQL` (lines 48–49):

```go
const createSQL = `
CREATE TABLE IF NOT EXISTS audit_records (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp     TEXT    NOT NULL,
    tool          TEXT    NOT NULL,
    args          TEXT,
    verdict       TEXT    NOT NULL,
    approved      INTEGER,
    denial_reason TEXT    NOT NULL DEFAULT '',
    error         TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_audit_timestamp ON audit_records(timestamp);
CREATE INDEX IF NOT EXISTS idx_audit_tool ON audit_records(tool);
`

const migrateSQL = `ALTER TABLE audit_records ADD COLUMN denial_reason TEXT NOT NULL DEFAULT ''`

const insertSQL = `INSERT INTO audit_records (timestamp, tool, args, verdict, approved, denial_reason, error)
VALUES (?, ?, ?, ?, ?, ?, ?)`
```

In `NewLogger`, after `db.Exec(createSQL)`, add the migration (lines 74–77):

```go
if _, err := db.Exec(createSQL); err != nil {
    _ = db.Close()
    return nil, fmt.Errorf("create audit table: %w", err)
}

// Migrate: add denial_reason column if it doesn't exist yet.
_, _ = db.Exec(migrateSQL)
```

In `Record()`, add `rec.DenialReason` to the `stmt.Exec` call (line 107):

```go
_, err = l.stmt.Exec(
    rec.Timestamp.Format(time.RFC3339),
    rec.Tool,
    argsJSON,
    rec.Verdict,
    approved,
    rec.DenialReason,
    rec.Error,
)
```

In `Query()`, update `selectSQL` (line 145) and the `rows.Scan` call (line 164):

```go
selectSQL := "SELECT timestamp, tool, args, verdict, approved, denial_reason, error FROM audit_records" +
    where + " ORDER BY id DESC LIMIT ? OFFSET ?"
```

```go
var (
    ts, tool, verdict, denialReason, errStr string
    argsJSON                                 sql.NullString
    approved                                 sql.NullInt64
)
if err := rows.Scan(&ts, &tool, &argsJSON, &verdict, &approved, &denialReason, &errStr); err != nil {
    return nil, 0, fmt.Errorf("scan audit record: %w", err)
}
```

Then set `rec.DenialReason = denialReason` when building the `rec` (after line 174):

```go
rec := Record{
    Timestamp:    timestamp,
    Tool:         tool,
    Verdict:      verdict,
    DenialReason: denialReason,
    Error:        errStr,
}
```

**Step 5: Run test to verify it passes**

```bash
cd mcp-broker && go test ./internal/audit/... -v
```
Expected: all PASS.

**Step 6: Commit**

```bash
cd mcp-broker && git add internal/audit/audit.go internal/audit/audit_test.go
git commit -m "feat(audit): add denial_reason field with schema migration"
```

---

## Task 2: Update Approver interface and broker pipeline

**Files:**
- Modify: `mcp-broker/internal/broker/broker.go`
- Modify: `mcp-broker/internal/broker/broker_test.go`

**Step 1: Write the failing test**

In `mcp-broker/internal/broker/broker_test.go`, update `mockApprover` (lines 40–45) and add a test for DenialReason propagation.

Replace `mockApprover`:

```go
type mockApprover struct{ mock.Mock }

func (m *mockApprover) Review(ctx context.Context, tool string, args map[string]any) (bool, string, error) {
	a := m.Called(ctx, tool, args)
	return a.Bool(0), a.String(1), a.Error(2)
}
```

Add a new test:

```go
func TestBroker_Handle_ApprovalRequired_DenialReasonPropagated(t *testing.T) {
	al := new(mockAuditLogger)
	al.On("Record", mock.Anything, mock.MatchedBy(func(r audit.Record) bool {
		return r.DenialReason == "timeout"
	})).Return(nil)

	ap := new(mockApprover)
	ap.On("Review", mock.Anything, "fs.write", mock.Anything).Return(false, "timeout", nil)

	engine := rules.New([]config.RuleConfig{{Tool: "*", Verdict: "require-approval"}})

	b := &Broker{
		servers:  new(mockServerManager),
		rules:    engine,
		auditor:  al,
		approver: ap,
	}

	_, err := b.Handle(context.Background(), "fs.write", nil)
	require.Error(t, err)
	al.AssertExpectations(t)
}
```

Also update existing mock calls that use `Return(true, nil)` / `Return(false, nil)` to `Return(true, "", nil)` / `Return(false, "", nil)` — they won't compile until the interface changes.

**Step 2: Run test to verify it fails**

```bash
cd mcp-broker && go test ./internal/broker/... -v
```
Expected: FAIL — compile error: wrong number of return values.

**Step 3: Update the Approver interface and Handle()**

In `mcp-broker/internal/broker/broker.go`, update the `Approver` interface (lines 30–32):

```go
// Approver handles human approval decisions.
// It returns (approved, denialReason, err). denialReason is "user" for explicit
// denials, "timeout" for timeouts, and "" when approved or not applicable.
type Approver interface {
	Review(ctx context.Context, tool string, args map[string]any) (bool, string, error)
}
```

Update `Handle()` where `b.approver.Review` is called (lines 83–94):

```go
approved, denialReason, err := b.approver.Review(ctx, tool, args)
rec.Approved = &approved
rec.DenialReason = denialReason
if err != nil {
    rec.Error = fmt.Sprintf("approver error: %v", err)
    _ = b.auditor.Record(ctx, rec)
    return nil, fmt.Errorf("approver error for %s: %w", tool, err)
}
if !approved {
    rec.Error = fmt.Sprintf("denied by approver: %s", tool)
    _ = b.auditor.Record(ctx, rec)
    return nil, fmt.Errorf("%w (by approver): %s", ErrDenied, tool)
}
```

**Step 4: Run tests to verify they pass**

```bash
cd mcp-broker && go test ./internal/broker/... -v
```
Expected: all PASS (note: dashboard won't compile until Task 3).

**Step 5: Commit**

```bash
cd mcp-broker && git add internal/broker/broker.go internal/broker/broker_test.go
git commit -m "feat(broker): update Approver interface to return denial reason"
```

---

## Task 3: Update Dashboard to implement new Approver interface

**Files:**
- Modify: `mcp-broker/internal/dashboard/dashboard.go`
- Modify: `mcp-broker/internal/dashboard/dashboard_test.go`

**Step 1: Write failing tests**

In `mcp-broker/internal/dashboard/dashboard_test.go`, update all `Review` call sites and add a denial reason test.

Update the signature of `Review` in all existing tests from:
```go
approved, err := d.Review(...)
```
to:
```go
approved, reason, err := d.Review(...)
```

Update `TestDashboard_Review_DeniesViaAPI` to assert `reason == "user"`:
```go
func TestDashboard_Review_DeniesViaAPI(t *testing.T) {
	d := New(nil, nil, nil)
	mux := d.Handler()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	type result struct {
		approved bool
		reason   string
	}
	done := make(chan result, 1)
	go func() {
		approved, reason, err := d.Review(context.Background(), "github.push", map[string]any{})
		require.NoError(t, err)
		done <- result{approved, reason}
	}()

	time.Sleep(50 * time.Millisecond)

	resp, err := http.Get(srv.URL + "/api/pending")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var pending []pendingRequest
	err = json.NewDecoder(resp.Body).Decode(&pending)
	require.NoError(t, err)

	body := `{"id":"` + pending[0].ID + `","decision":"deny"}`
	resp2, err := http.Post(srv.URL+"/api/decide", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()

	r := <-done
	require.False(t, r.approved)
	require.Equal(t, "user", r.reason)
}
```

**Step 2: Run test to verify it fails**

```bash
cd mcp-broker && go test ./internal/dashboard/... -v
```
Expected: FAIL — compile error: `Review` returns 2 values but 3 expected.

**Step 3: Update Dashboard**

In `mcp-broker/internal/dashboard/dashboard.go`:

Add `DenialReason string` to `decidedRequest` (after line 35):

```go
type decidedRequest struct {
	ID           string         `json:"id"`
	Tool         string         `json:"tool"`
	Args         map[string]any `json:"args"`
	Decision     string         `json:"decision"`
	DenialReason string         `json:"denial_reason,omitempty"`
	Timestamp    time.Time      `json:"timestamp"`
	DecidedAt    time.Time      `json:"decided_at"`
}
```

Update `Review()` signature and return (lines 83–114):

```go
// Review blocks until a human approves or denies the request via the web UI.
// Returns (approved, denialReason, err). On explicit denial: denialReason="user".
// On context cancellation: returns (false, "", ctx.Err()).
func (d *Dashboard) Review(ctx context.Context, tool string, args map[string]any) (bool, string, error) {
	id := generateID()
	ch := make(chan string, 1) // sends denial reason or "" for approval

	pr := &pendingRequest{
		ID:        id,
		Tool:      tool,
		Args:      args,
		Timestamp: time.Now(),
		decision:  ch,
	}

	d.mu.Lock()
	d.pending[id] = pr
	d.mu.Unlock()

	if d.logger != nil {
		d.logger.Info("approval requested", "tool", tool, "request_id", id)
	}
	d.broadcast(newRequestEvent(pr))

	select {
	case denialReason := <-ch:
		approved := denialReason == ""
		return approved, denialReason, nil
	case <-ctx.Done():
		d.mu.Lock()
		delete(d.pending, id)
		d.mu.Unlock()
		d.broadcast(removedEvent(id))
		return false, "", ctx.Err()
	}
}
```

Update `pendingRequest.decision` channel type from `chan bool` to `chan string`:

```go
type pendingRequest struct {
	ID        string         `json:"id"`
	Tool      string         `json:"tool"`
	Args      map[string]any `json:"args"`
	Timestamp time.Time      `json:"timestamp"`
	decision  chan string
}
```

Update `handleDecide()` to send denial reason on the channel and populate `DenialReason` in `decidedRequest` (lines 116–159):

```go
func (d *Dashboard) handleDecide(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		ID       string `json:"id"`
		Decision string `json:"decision"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	d.mu.Lock()
	pr, ok := d.pending[payload.ID]
	if ok {
		delete(d.pending, payload.ID)
	}
	d.mu.Unlock()

	if !ok {
		http.Error(w, "unknown request ID", http.StatusNotFound)
		return
	}

	approved := payload.Decision == "approve"
	denialReason := ""
	if !approved {
		denialReason = "user"
	}

	pr.decision <- denialReason

	decision := "denied"
	if approved {
		decision = "approved"
	}
	dr := decidedRequest{
		ID:           pr.ID,
		Tool:         pr.Tool,
		Args:         pr.Args,
		Decision:     decision,
		DenialReason: denialReason,
		Timestamp:    pr.Timestamp,
		DecidedAt:    time.Now(),
	}
	d.mu.Lock()
	d.decided = append(d.decided, dr)
	d.mu.Unlock()

	d.broadcast(decidedEvent(dr))
	w.WriteHeader(http.StatusOK)
}
```

Also update `Review()` in `New()` — the channel creation line uses `make(chan bool, 1)`, change to `make(chan string, 1)`.

**Step 4: Run tests to verify they pass**

```bash
cd mcp-broker && go test ./internal/dashboard/... -v
```
Expected: all PASS.

**Step 5: Commit**

```bash
cd mcp-broker && git add internal/dashboard/dashboard.go internal/dashboard/dashboard_test.go
git commit -m "feat(dashboard): update Approver impl to return denial reason"
```

---

## Task 4: Add config fields

**Files:**
- Modify: `mcp-broker/internal/config/config.go`
- Modify: `mcp-broker/internal/config/config_test.go`

**Step 1: Write failing tests**

Add to `mcp-broker/internal/config/config_test.go`:

```go
func TestLoad_TelegramConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	data := `{
		"approval_timeout_seconds": 300,
		"telegram": {
			"enabled": true,
			"token": "mytoken",
			"chat_id": "123456"
		}
	}`
	err := os.WriteFile(path, []byte(data), 0o600)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, 300, cfg.ApprovalTimeoutSeconds)
	require.True(t, cfg.Telegram.Enabled)
	require.Equal(t, "mytoken", cfg.Telegram.Token)
	require.Equal(t, "123456", cfg.Telegram.ChatID)
}

func TestDefaultConfig_TelegramDisabledByDefault(t *testing.T) {
	cfg := DefaultConfig()
	require.False(t, cfg.Telegram.Enabled)
	require.Equal(t, 600, cfg.ApprovalTimeoutSeconds)
}
```

**Step 2: Run test to verify it fails**

```bash
cd mcp-broker && go test ./internal/config/... -run "TestLoad_TelegramConfig|TestDefaultConfig_TelegramDisabledByDefault" -v
```
Expected: FAIL — compile error: no `ApprovalTimeoutSeconds` or `Telegram` field.

**Step 3: Add new types and fields**

In `mcp-broker/internal/config/config.go`, add `TelegramConfig` after `LogConfig`:

```go
// TelegramConfig configures the optional Telegram approval notifier.
// Token and ChatID support $VAR / ${VAR} environment variable expansion.
type TelegramConfig struct {
	Enabled bool   `json:"enabled"`
	Token   string `json:"token"`
	ChatID  string `json:"chat_id"`
}
```

Add `ApprovalTimeoutSeconds` and `Telegram` to `Config`:

```go
type Config struct {
	Servers                map[string]ServerConfig `json:"servers"`
	Rules                  []RuleConfig            `json:"rules"`
	Port                   int                     `json:"port"`
	OpenBrowser            bool                    `json:"open_browser"`
	Audit                  AuditConfig             `json:"audit"`
	Log                    LogConfig               `json:"log"`
	ApprovalTimeoutSeconds int                     `json:"approval_timeout_seconds"`
	Telegram               TelegramConfig          `json:"telegram"`
}
```

Update `DefaultConfig()` to set defaults:

```go
func DefaultConfig() Config {
	return Config{
		Servers: map[string]ServerConfig{},
		Rules: []RuleConfig{
			{Tool: "*", Verdict: "require-approval"},
		},
		Port:                   8200,
		OpenBrowser:            true,
		ApprovalTimeoutSeconds: 600,
		Audit: AuditConfig{
			Path: filepath.Join(xdgDataHome(), "mcp-broker", "audit.db"),
		},
		Log:      LogConfig{Level: "info"},
		Telegram: TelegramConfig{Enabled: false},
	}
}
```

**Step 4: Run tests to verify they pass**

```bash
cd mcp-broker && go test ./internal/config/... -v
```
Expected: all PASS.

**Step 5: Commit**

```bash
cd mcp-broker && git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add ApprovalTimeoutSeconds and TelegramConfig"
```

---

## Task 5: Implement MultiApprover

**Files:**
- Create: `mcp-broker/internal/broker/multi.go`
- Create: `mcp-broker/internal/broker/multi_test.go`

**Step 1: Write failing tests**

Create `mcp-broker/internal/broker/multi_test.go`:

```go
package broker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// stubApprover is a simple approver for MultiApprover tests (no testify mock needed).
type stubApprover struct {
	approved     bool
	denialReason string
	err          error
	delay        time.Duration
}

func (s *stubApprover) Review(ctx context.Context, _ string, _ map[string]any) (bool, string, error) {
	select {
	case <-time.After(s.delay):
		return s.approved, s.denialReason, s.err
	case <-ctx.Done():
		return false, "", ctx.Err()
	}
}

func TestMultiApprover_FirstApproverWins(t *testing.T) {
	fast := &stubApprover{approved: true, delay: 0}
	slow := &stubApprover{approved: false, denialReason: "user", delay: 10 * time.Second}

	m := NewMultiApprover(30*time.Second, fast, slow)
	approved, reason, err := m.Review(context.Background(), "tool", nil)

	require.NoError(t, err)
	require.True(t, approved)
	require.Empty(t, reason)
}

func TestMultiApprover_ExplicitDenyPropagated(t *testing.T) {
	denier := &stubApprover{approved: false, denialReason: "user", delay: 0}

	m := NewMultiApprover(30*time.Second, denier)
	approved, reason, err := m.Review(context.Background(), "tool", nil)

	require.NoError(t, err)
	require.False(t, approved)
	require.Equal(t, "user", reason)
}

func TestMultiApprover_TimeoutReturnsDeniedWithReason(t *testing.T) {
	never := &stubApprover{delay: 10 * time.Second}

	m := NewMultiApprover(50*time.Millisecond, never)
	approved, reason, err := m.Review(context.Background(), "tool", nil)

	require.NoError(t, err)
	require.False(t, approved)
	require.Equal(t, "timeout", reason)
}

func TestMultiApprover_ContextCancelledApproverSkipped(t *testing.T) {
	// Approver returns a context error (simulates being cancelled by another approver winning)
	ctxErr := &stubApprover{err: errors.New("context canceled"), delay: 0}
	good := &stubApprover{approved: true, delay: 5 * time.Millisecond}

	m := NewMultiApprover(30*time.Second, ctxErr, good)
	approved, reason, err := m.Review(context.Background(), "tool", nil)

	require.NoError(t, err)
	require.True(t, approved)
	require.Empty(t, reason)
}
```

**Step 2: Run test to verify it fails**

```bash
cd mcp-broker && go test ./internal/broker/... -run "TestMultiApprover" -v
```
Expected: FAIL — `NewMultiApprover` undefined.

**Step 3: Implement MultiApprover**

Create `mcp-broker/internal/broker/multi.go`:

```go
package broker

import (
	"context"
	"time"
)

// MultiApprover fans a Review call to all approvers concurrently.
// The first non-error response wins; all others are cancelled via context.
// A timeout is applied at this level — if it fires, the call is denied
// with reason "timeout".
type MultiApprover struct {
	approvers []Approver
	timeout   time.Duration
}

// NewMultiApprover creates a MultiApprover with the given timeout and approvers.
func NewMultiApprover(timeout time.Duration, approvers ...Approver) *MultiApprover {
	return &MultiApprover{approvers: approvers, timeout: timeout}
}

func (m *MultiApprover) Review(ctx context.Context, tool string, args map[string]any) (bool, string, error) {
	ctx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	type result struct {
		approved     bool
		denialReason string
		err          error
	}

	ch := make(chan result, len(m.approvers))

	for _, a := range m.approvers {
		a := a
		go func() {
			approved, reason, err := a.Review(ctx, tool, args)
			ch <- result{approved, reason, err}
		}()
	}

	// Collect results; skip errors (cancelled approvers) and return first clean result.
	for range len(m.approvers) {
		select {
		case r := <-ch:
			if r.err == nil {
				cancel()
				return r.approved, r.denialReason, nil
			}
			// approver was cancelled or errored — try the next one
		case <-ctx.Done():
			return false, "timeout", nil
		}
	}
	return false, "timeout", nil
}
```

**Step 4: Run tests to verify they pass**

```bash
cd mcp-broker && go test ./internal/broker/... -v
```
Expected: all PASS.

**Step 5: Commit**

```bash
cd mcp-broker && git add internal/broker/multi.go internal/broker/multi_test.go
git commit -m "feat(broker): add MultiApprover with timeout and fan-out"
```

---

## Task 6: Wire MultiApprover in serve.go

**Files:**
- Modify: `mcp-broker/cmd/mcp-broker/serve.go`

**Step 1: Update serve.go to create MultiApprover**

In `mcp-broker/cmd/mcp-broker/serve.go`, replace the broker creation block (lines 107–111). Currently:

```go
// Create broker
b := broker.New(mgr, engine, auditor, dash, logger.With("component", "broker"))
```

Replace with:

```go
// Create multi-approver (currently wraps only dashboard; Telegram added in later task)
timeout := time.Duration(cfg.ApprovalTimeoutSeconds) * time.Second
multi := broker.NewMultiApprover(timeout, dash)

// Create broker
b := broker.New(mgr, engine, auditor, multi, logger.With("component", "broker"))
```

Add `"time"` to imports if not already present (it is, at line ~10).

**Step 2: Build to verify it compiles**

```bash
cd mcp-broker && go build ./...
```
Expected: success.

**Step 3: Run all tests**

```bash
cd mcp-broker && go test -race ./...
```
Expected: all PASS.

**Step 4: Commit**

```bash
cd mcp-broker && git add cmd/mcp-broker/serve.go
git commit -m "feat(serve): wire MultiApprover with configurable timeout"
```

---

## Task 7: Dashboard UI — countdown timer and denial reason badges

**Files:**
- Modify: `mcp-broker/internal/dashboard/dashboard.go`
- Modify: `mcp-broker/internal/dashboard/index.html`
- Modify: `mcp-broker/internal/dashboard/dashboard_test.go`

**Step 1: Write failing test for Deadline in SSE payload**

Add to `mcp-broker/internal/dashboard/dashboard_test.go`:

```go
func TestDashboard_PendingRequest_HasDeadline(t *testing.T) {
	d := New(nil, nil, nil)

	deadline := time.Now().Add(10 * time.Minute)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = d.Review(ctx, "test.tool", nil)
	}()

	time.Sleep(50 * time.Millisecond)

	d.mu.Lock()
	var pr *pendingRequest
	for _, p := range d.pending {
		pr = p
		break
	}
	d.mu.Unlock()

	require.NotNil(t, pr)
	require.WithinDuration(t, deadline, pr.Deadline, time.Second)

	cancel()
	<-done
}
```

**Step 2: Run test to verify it fails**

```bash
cd mcp-broker && go test ./internal/dashboard/... -run TestDashboard_PendingRequest_HasDeadline -v
```
Expected: FAIL — `pendingRequest` has no `Deadline` field.

**Step 3: Add Deadline to pendingRequest and populate from context**

In `mcp-broker/internal/dashboard/dashboard.go`, update `pendingRequest`:

```go
type pendingRequest struct {
	ID        string         `json:"id"`
	Tool      string         `json:"tool"`
	Args      map[string]any `json:"args"`
	Timestamp time.Time      `json:"timestamp"`
	Deadline  time.Time      `json:"deadline,omitempty"`
	decision  chan string
}
```

In `Review()`, populate `Deadline` from context:

```go
pr := &pendingRequest{
	ID:        id,
	Tool:      tool,
	Args:      args,
	Timestamp: time.Now(),
	decision:  ch,
}
if deadline, ok := ctx.Deadline(); ok {
	pr.Deadline = deadline
}
```

**Step 4: Run test to verify it passes**

```bash
cd mcp-broker && go test ./internal/dashboard/... -v
```
Expected: all PASS.

**Step 5: Update index.html — countdown timer and denial reason badges**

In `mcp-broker/internal/dashboard/index.html`, find the JavaScript section that handles SSE events and pending/decided requests. Make these changes:

1. In the CSS `<style>` section, add styles for the countdown and denial badge (add after the existing `.badge` or status styles):

```css
  .countdown {
    font-family: var(--font-mono);
    font-size: 0.75rem;
    color: var(--text-secondary);
  }
  .countdown.urgent { color: var(--red); }
  .badge-timeout { background: #78350f; color: #fbbf24; }
```

2. In the JavaScript, find where pending request cards are rendered (look for `renderPending` or similar function). Add countdown rendering using the `deadline` field from the event:

```js
function formatCountdown(deadlineISO) {
  if (!deadlineISO) return '';
  const ms = new Date(deadlineISO) - Date.now();
  if (ms <= 0) return '0:00';
  const totalSecs = Math.ceil(ms / 1000);
  const mins = Math.floor(totalSecs / 60);
  const secs = totalSecs % 60;
  return `${mins}:${secs.toString().padStart(2, '0')}`;
}
```

Add a live countdown update loop that calls `renderPending()` (or equivalent) every second when there are pending requests with deadlines.

3. In the decided requests rendering, update the decision badge logic to use `denial_reason`:

```js
function decisionBadge(req) {
  if (req.decision === 'approved') {
    return '<span class="badge badge-green">✅ Approved</span>';
  }
  if (req.denial_reason === 'timeout') {
    return '<span class="badge badge-timeout">⏱️ Timed out</span>';
  }
  return '<span class="badge badge-red">❌ Denied</span>';
}
```

> Note: The exact edits depend on the current HTML structure. Read the full `index.html` first, find the relevant JS rendering functions, and apply minimal targeted changes. Preserve all existing formatting and style. Do not rewrite the entire file.

**Step 6: Build to verify embedding works**

```bash
cd mcp-broker && go build ./...
```
Expected: success.

**Step 7: Commit**

```bash
cd mcp-broker && git add internal/dashboard/dashboard.go internal/dashboard/dashboard_test.go internal/dashboard/index.html
git commit -m "feat(dashboard): add countdown timer and denial reason badges"
```

---

## Task 8: Implement TelegramApprover

**Files:**
- Create: `mcp-broker/internal/telegram/telegram.go`
- Create: `mcp-broker/internal/telegram/telegram_test.go`

**Step 1: Write failing tests**

Create `mcp-broker/internal/telegram/telegram_test.go`:

```go
package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func fakeTelegramServer(t *testing.T, callbackData string) (*httptest.Server, *int32) {
	t.Helper()
	var messageID int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			atomic.StoreInt32(&messageID, 42)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":     true,
				"result": map[string]any{"message_id": 42},
			})
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			id := int(atomic.LoadInt32(&messageID))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"result": []map[string]any{{
					"update_id": 1,
					"callback_query": map[string]any{
						"id":   "cq1",
						"data": callbackData,
						"message": map[string]any{
							"message_id": id,
						},
					},
				}},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		}
	}))
	return srv, &messageID
}

func TestApprover_Review_Approves(t *testing.T) {
	srv, _ := fakeTelegramServer(t, "approve")
	defer srv.Close()

	a := newWithBase("token", "123", srv.URL, &http.Client{Timeout: 5 * time.Second}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	approved, reason, err := a.Review(ctx, "github.push", map[string]any{"branch": "main"})
	require.NoError(t, err)
	require.True(t, approved)
	require.Empty(t, reason)
}

func TestApprover_Review_Denies(t *testing.T) {
	srv, _ := fakeTelegramServer(t, "deny")
	defer srv.Close()

	a := newWithBase("token", "123", srv.URL, &http.Client{Timeout: 5 * time.Second}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	approved, reason, err := a.Review(ctx, "github.push", nil)
	require.NoError(t, err)
	require.False(t, approved)
	require.Equal(t, "user", reason)
}

func TestApprover_Review_ContextCancelled(t *testing.T) {
	// Fake server that never returns a callback
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":     true,
				"result": map[string]any{"message_id": 1},
			})
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			// Hold until request context is done
			<-r.Context().Done()
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []any{}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		}
	}))
	defer srv.Close()

	a := newWithBase("token", "123", srv.URL, &http.Client{Timeout: 5 * time.Second}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	approved, reason, err := a.Review(ctx, "test.tool", nil)
	require.NoError(t, err) // context cancel is not returned as an error
	require.False(t, approved)
	require.Equal(t, "timeout", reason)
}

func TestFormatArgs_TruncatesLongJSON(t *testing.T) {
	args := map[string]any{"key": strings.Repeat("x", 300)}
	result := formatArgs(args)
	require.LessOrEqual(t, len([]rune(result)), maxArgLen+len("... (truncated)"))
	require.Contains(t, result, "(truncated)")
}

func TestFormatArgs_EmptyArgs(t *testing.T) {
	require.Equal(t, "(no args)", formatArgs(nil))
	require.Equal(t, "(no args)", formatArgs(map[string]any{}))
}
```

**Step 2: Run test to verify it fails**

```bash
cd mcp-broker && go test ./internal/telegram/... -v
```
Expected: FAIL — package does not exist.

**Step 3: Implement TelegramApprover**

Create `mcp-broker/internal/telegram/telegram.go`:

```go
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
	"unicode/utf8"
)

const (
	defaultAPIBase  = "https://api.telegram.org"
	maxArgLen       = 200
	pollTimeout     = 30
)

// Approver sends approval requests via Telegram and polls for responses.
// It implements broker.Approver and requires no inbound connections — it only
// makes outbound HTTP calls to the Telegram Bot API.
type Approver struct {
	token   string
	chatID  string
	apiBase string
	client  *http.Client
	logger  *slog.Logger
}

// New creates a TelegramApprover for production use.
func New(token, chatID string, logger *slog.Logger) *Approver {
	return newWithBase(token, chatID, defaultAPIBase, &http.Client{Timeout: 40 * time.Second}, logger)
}

// newWithBase creates an Approver with a custom API base URL (used in tests).
func newWithBase(token, chatID, apiBase string, client *http.Client, logger *slog.Logger) *Approver {
	return &Approver{
		token:   token,
		chatID:  chatID,
		apiBase: apiBase,
		client:  client,
		logger:  logger,
	}
}

// Review sends a Telegram notification and blocks until the user taps Approve or
// Deny, the context is cancelled, or the deadline is reached.
// Returns (approved, denialReason, err). On context cancellation/timeout:
// returns (false, "timeout", nil) — the caller should not treat this as an error.
func (a *Approver) Review(ctx context.Context, tool string, args map[string]any) (bool, string, error) {
	remaining := remainingTime(ctx)
	argsStr := formatArgs(args)
	text := fmt.Sprintf("🔧 <code>%s</code>\n\n<pre>%s</pre>\n\n⏳ %s remaining", tool, argsStr, remaining)

	msgID, err := a.sendMessage(ctx, text)
	if err != nil {
		if ctx.Err() != nil {
			return false, "timeout", nil
		}
		return false, "", fmt.Errorf("send telegram message: %w", err)
	}

	approved, denialReason, err := a.pollForDecision(ctx, msgID)

	// Best-effort: update the message to show the outcome.
	outcome := resolvedText(approved, denialReason, err, ctx)
	_ = a.editMessage(context.Background(), msgID, outcome)

	if err != nil {
		return false, "timeout", nil
	}
	return approved, denialReason, nil
}

func (a *Approver) pollForDecision(ctx context.Context, messageID int) (bool, string, error) {
	offset := 0
	for {
		if ctx.Err() != nil {
			return false, "", ctx.Err()
		}

		updates, err := a.getUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return false, "", ctx.Err()
			}
			if a.logger != nil {
				a.logger.Warn("telegram poll error", "error", err)
			}
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
				return false, "", ctx.Err()
			}
			continue
		}

		for _, update := range updates {
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}
			if update.CallbackQuery == nil {
				continue
			}
			if update.CallbackQuery.Message.MessageID != messageID {
				continue
			}
			_ = a.answerCallbackQuery(context.Background(), update.CallbackQuery.ID)
			if update.CallbackQuery.Data == "approve" {
				return true, "", nil
			}
			return false, "user", nil
		}
	}
}

func (a *Approver) apiURL(method string) string {
	return fmt.Sprintf("%s/bot%s/%s", a.apiBase, a.token, method)
}

type sendMessageReq struct {
	ChatID      string        `json:"chat_id"`
	Text        string        `json:"text"`
	ParseMode   string        `json:"parse_mode"`
	ReplyMarkup inlineKeyboard `json:"reply_markup"`
}

type inlineKeyboard struct {
	InlineKeyboard [][]inlineButton `json:"inline_keyboard"`
}

type inlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type sendMessageResp struct {
	OK     bool `json:"ok"`
	Result struct {
		MessageID int `json:"message_id"`
	} `json:"result"`
}

type getUpdatesResp struct {
	OK     bool     `json:"ok"`
	Result []Update `json:"result"`
}

// Update is a Telegram update object (only callback_query populated here).
type Update struct {
	UpdateID      int            `json:"update_id"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

// CallbackQuery is a Telegram inline keyboard callback.
type CallbackQuery struct {
	ID      string  `json:"id"`
	Data    string  `json:"data"`
	Message Message `json:"message"`
}

// Message holds just the message_id we need for correlation.
type Message struct {
	MessageID int `json:"message_id"`
}

func (a *Approver) sendMessage(ctx context.Context, text string) (int, error) {
	req := sendMessageReq{
		ChatID:    a.chatID,
		Text:      text,
		ParseMode: "HTML",
		ReplyMarkup: inlineKeyboard{
			InlineKeyboard: [][]inlineButton{{
				{Text: "✅ Approve", CallbackData: "approve"},
				{Text: "❌ Deny", CallbackData: "deny"},
			}},
		},
	}
	body, err := json.Marshal(req)
	if err != nil {
		return 0, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.apiURL("sendMessage"), bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	var result sendMessageResp
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}
	if !result.OK {
		return 0, fmt.Errorf("telegram sendMessage failed")
	}
	return result.Result.MessageID, nil
}

func (a *Approver) getUpdates(ctx context.Context, offset int) ([]Update, error) {
	u := fmt.Sprintf("%s?offset=%d&timeout=%d&allowed_updates=[\"callback_query\"]",
		a.apiURL("getUpdates"), offset, pollTimeout)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var result getUpdatesResp
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if !result.OK {
		return nil, fmt.Errorf("telegram getUpdates failed")
	}
	return result.Result, nil
}

func (a *Approver) answerCallbackQuery(ctx context.Context, callbackQueryID string) error {
	body, _ := json.Marshal(map[string]string{"callback_query_id": callbackQueryID})
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, a.apiURL("answerCallbackQuery"), bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func (a *Approver) editMessage(ctx context.Context, messageID int, text string) error {
	body, _ := json.Marshal(map[string]any{
		"chat_id":    a.chatID,
		"message_id": messageID,
		"text":       text,
	})
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, a.apiURL("editMessageText"), bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func remainingTime(ctx context.Context) string {
	deadline, ok := ctx.Deadline()
	if !ok {
		return "unknown"
	}
	d := time.Until(deadline).Round(time.Second)
	if d <= 0 {
		return "0:00"
	}
	mins := int(d.Minutes())
	secs := int(d.Seconds()) % 60
	return fmt.Sprintf("%d:%02d", mins, secs)
}

func formatArgs(args map[string]any) string {
	if len(args) == 0 {
		return "(no args)"
	}
	b, err := json.MarshalIndent(args, "", "  ")
	if err != nil {
		return "(error formatting args)"
	}
	s := string(b)
	if utf8.RuneCountInString(s) > maxArgLen {
		runes := []rune(s)
		return string(runes[:maxArgLen]) + "... (truncated)"
	}
	return s
}

func resolvedText(approved bool, denialReason string, err error, ctx context.Context) string {
	switch {
	case err != nil && ctx.Err() != nil:
		return "⏱️ Timed out"
	case err != nil:
		return "❌ Error"
	case approved:
		return "✅ Approved"
	case denialReason == "user":
		return "❌ Denied"
	default:
		return "↩️ Resolved elsewhere"
	}
}
```

**Step 4: Run tests to verify they pass**

```bash
cd mcp-broker && go test ./internal/telegram/... -v
```
Expected: all PASS.

**Step 5: Run full test suite**

```bash
cd mcp-broker && go test -race ./...
```
Expected: all PASS.

**Step 6: Commit**

```bash
cd mcp-broker && git add internal/telegram/
git commit -m "feat(telegram): implement TelegramApprover"
```

---

## Task 9: Wire TelegramApprover in serve.go

**Files:**
- Modify: `mcp-broker/cmd/mcp-broker/serve.go`

**Step 1: Add Telegram wiring**

In `mcp-broker/cmd/mcp-broker/serve.go`, replace the multi-approver creation block (added in Task 6) with:

```go
// Create multi-approver
timeout := time.Duration(cfg.ApprovalTimeoutSeconds) * time.Second
approvers := []broker.Approver{dash}
if cfg.Telegram.Enabled {
    tgToken := os.ExpandEnv(cfg.Telegram.Token)
    tgChatID := os.ExpandEnv(cfg.Telegram.ChatID)
    tg := telegram.New(tgToken, tgChatID, logger.With("component", "telegram"))
    approvers = append(approvers, tg)
    logger.Info("telegram approver enabled", "chat_id", tgChatID)
}
multi := broker.NewMultiApprover(timeout, approvers...)

// Create broker
b := broker.New(mgr, engine, auditor, multi, logger.With("component", "broker"))
```

Add import: `"github.com/averycrespi/agent-tools/mcp-broker/internal/telegram"` to the import block.

**Step 2: Build to verify it compiles**

```bash
cd mcp-broker && go build ./...
```
Expected: success.

**Step 3: Run all tests**

```bash
cd mcp-broker && go test -race ./...
```
Expected: all PASS.

**Step 4: Commit**

```bash
cd mcp-broker && git add cmd/mcp-broker/serve.go
git commit -m "feat(serve): wire TelegramApprover when telegram.enabled=true"
```

---

## Task 10: Update documentation

**Files:**
- Modify: `mcp-broker/README.md`
- Modify: `mcp-broker/CLAUDE.md`

**Step 1: Update README.md**

In the `## Configuration` section, add a new `### Mobile Approval (Telegram)` subsection after `### OAuth`:

```markdown
### Mobile Approval (Telegram)

To enable approval via Telegram, add a `telegram` block to your config:

```json
{
  "approval_timeout_seconds": 600,
  "telegram": {
    "enabled": true,
    "token": "$TELEGRAM_BOT_TOKEN",
    "chat_id": "$TELEGRAM_CHAT_ID"
  }
}
```

`token` and `chat_id` support `$VAR` / `${VAR}` environment variable expansion.

**Setup:**
1. Create a bot via [@BotFather](https://t.me/BotFather) — it gives you a token.
2. Start a chat with your bot, then get your chat ID from `https://api.telegram.org/bot<TOKEN>/getUpdates`.
3. Set `TELEGRAM_BOT_TOKEN` and `TELEGRAM_CHAT_ID` in your environment and enable in config.

When enabled, approval requests are sent to both the web dashboard and Telegram simultaneously. The first response (from either) resolves the request.
```

Also update the `## How it works` diagram to mention mobile approval and update the step 2 description:

```markdown
2. **Approval** — if the verdict is `require-approval`, the call blocks until a human approves or denies it via the web dashboard (and optionally Telegram). A configurable timeout (default 10 minutes) auto-denies if no response arrives.
```

Update the config example JSON in `## Configuration` to include the new fields:

```json
{
  "approval_timeout_seconds": 600,
  "telegram": {
    "enabled": false,
    "token": "$TELEGRAM_BOT_TOKEN",
    "chat_id": "$TELEGRAM_CHAT_ID"
  }
}
```

**Step 2: Update CLAUDE.md architecture section**

In `mcp-broker/CLAUDE.md`, update the architecture tree to add the `telegram/` package:

```
internal/
  config/               JSON config with XDG paths, default backfill on load
  rules/                Glob matching (filepath.Match), first-match-wins
  audit/                SQLite (ncruces/go-sqlite3, WASM, no CGO), WAL mode
  server/               Backend interface with stdio, HTTP, SSE, and OAuth transports
  dashboard/            Embedded HTML, SSE updates, implements Approver interface
  telegram/             Telegram Bot API polling approver (opt-in, outbound-only)
  auth/                 Bearer token auth: generation, file storage, HTTP middleware
  broker/               Orchestrator with ServerManager, AuditLogger, Approver interfaces;
                        MultiApprover fans requests to all approvers with shared timeout
```

Add a conventions entry:

```
- Telegram approver uses long-polling (`getUpdates?timeout=30`) — no inbound connections needed; correlates responses by Telegram `message_id`
```

**Step 3: Commit**

```bash
cd mcp-broker && git add README.md CLAUDE.md
git commit -m "docs: document Telegram mobile approval and timeout config"
```

---

## Summary

| Task | Description | Key files |
|------|-------------|-----------|
| 1 | Add DenialReason to audit | `audit/audit.go`, `audit_test.go` |
| 2 | Update Approver interface → `(bool, string, error)` | `broker/broker.go`, `broker_test.go` |
| 3 | Update Dashboard to return denial reason | `dashboard/dashboard.go`, `dashboard_test.go` |
| 4 | Add TelegramConfig + timeout to config | `config/config.go`, `config_test.go` |
| 5 | Implement MultiApprover | `broker/multi.go`, `broker/multi_test.go` |
| 6 | Wire MultiApprover in serve.go | `cmd/mcp-broker/serve.go` |
| 7 | Dashboard UI: countdown + denial badges | `dashboard/dashboard.go`, `dashboard/index.html`, `dashboard_test.go` |
| 8 | Implement TelegramApprover | `telegram/telegram.go`, `telegram/telegram_test.go` |
| 9 | Wire TelegramApprover in serve.go | `cmd/mcp-broker/serve.go` |
| 10 | Update docs | `README.md`, `CLAUDE.md` |
