# mcp-broker Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use Skill(executing-plans) to implement this plan task-by-task.

**Goal:** Build a generic MCP proxy that connects to backend MCP servers, discovers their tools, and exposes them through a single frontend MCP endpoint with policy rules, human approval, and audit logging.

**Architecture:** A single Go binary that acts as both an MCP client (connecting to backends) and an MCP server (serving agents). The pipeline is: tool call → rules check → optional approval → proxy to backend → audit. Configuration is a single JSON file. A web dashboard provides human approval UI and audit viewing.

**Tech Stack:** Go 1.25, Cobra (CLI), mcp-go (MCP protocol), ncruces/go-sqlite3 (audit), slog (logging), testify (testing)

**Design doc:** `.plans/2026-03-20-mcp-broker-design.md`

**Reference implementation:** `~/Workspace/brocade/` — adapt patterns from here but simplify heavily. The key files to reference are listed per-task below.

---

### Task 1: Project scaffolding

Sets up the Go module, tooling config, and empty main.

**Files:**
- Create: `mcp-broker/go.mod`
- Create: `mcp-broker/.tool-versions`
- Create: `mcp-broker/.golangci.yml`
- Create: `mcp-broker/Makefile`
- Create: `mcp-broker/cmd/mcp-broker/main.go`

**Step 1: Create the Go module**

```bash
cd mcp-broker && go mod init github.com/averycrespi/agent-tools/mcp-broker
```

Then edit `go.mod` to set Go version:

```
module github.com/averycrespi/agent-tools/mcp-broker

go 1.25.0
```

**Step 2: Create .tool-versions**

```
golang 1.25.0
```

**Step 3: Create .golangci.yml**

Reference: `~/Workspace/brocade/.golangci.yml` — use v2 format with standard defaults.

```yaml
version: "2"
linters:
  default: standard
  enable:
    - errorlint
    - gocritic
    - gosec
  settings:
    gosec:
      excludes:
        - G304 # file path from variable (we need this for config paths)
  exclusions:
    rules:
      - path: _test\.go
        linters:
          - gosec
formatters:
  enable:
    - goimports
```

**Step 4: Create Makefile**

```makefile
.PHONY: build test lint fmt tidy audit

build:
	go build -o /tmp/bin/mcp-broker ./cmd/mcp-broker

test:
	go test -race ./...

lint:
	go tool golangci-lint run

fmt:
	go tool goimports -w .

tidy:
	go mod tidy && go mod verify

audit: tidy fmt lint test
	go tool govulncheck ./...
```

**Step 5: Create minimal main.go**

```go
package main

import (
	"log/slog"
	"os"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	return nil
}
```

**Step 6: Verify it builds**

Run: `cd mcp-broker && make build`
Expected: Binary at `/tmp/bin/mcp-broker`, exits 0 with no output.

**Step 7: Commit**

```bash
git add mcp-broker/
git commit -m "feat: scaffold mcp-broker project with Go module and tooling"
```

---

### Task 2: Config package

Loads, saves, and refreshes the JSON config file. Adapted from `~/Workspace/brocade/internal/config/config.go` but with the simplified schema from the design doc.

**Files:**
- Create: `mcp-broker/internal/config/config.go`
- Create: `mcp-broker/internal/config/config_test.go`

**Step 1: Write the test**

```go
package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoad_CreatesDefaultOnFirstRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, 8200, cfg.Port)
	require.Equal(t, "info", cfg.Log.Level)
	require.FileExists(t, path)
}

func TestLoad_ReadsExistingConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	err := os.WriteFile(path, []byte(`{"port": 9000}`), 0o600)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, 9000, cfg.Port)
}

func TestRefresh_BackfillsNewDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	// Write minimal config without log section
	err := os.WriteFile(path, []byte(`{"port": 9000}`), 0o600)
	require.NoError(t, err)

	written, err := Refresh(path)
	require.NoError(t, err)
	require.Equal(t, path, written)

	// Reload — log level should be backfilled
	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, 9000, cfg.Port)
	require.Equal(t, "info", cfg.Log.Level)
}

func TestConfig_ServerTypes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	json := `{
		"servers": [
			{"name": "echo", "command": "echo", "args": ["hello"]},
			{"name": "remote", "type": "http", "url": "http://localhost:3000/mcp"}
		]
	}`
	err := os.WriteFile(path, []byte(json), 0o600)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Servers, 2)
	require.Equal(t, "echo", cfg.Servers[0].Name)
	require.Equal(t, "echo", cfg.Servers[0].Command)
	require.Equal(t, "http", cfg.Servers[1].Type)
	require.Equal(t, "http://localhost:3000/mcp", cfg.Servers[1].URL)
}

func TestConfigPath_ReturnsXDGPath(t *testing.T) {
	path := ConfigPath()
	require.Contains(t, path, "mcp-broker")
	require.Contains(t, path, "config.json")
}
```

**Step 2: Run tests to verify they fail**

Run: `cd mcp-broker && go test ./internal/config/ -v`
Expected: FAIL — package doesn't exist yet.

**Step 3: Implement config.go**

Reference: `~/Workspace/brocade/internal/config/config.go`

```go
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config is the top-level configuration for mcp-broker.
type Config struct {
	Servers []ServerConfig `json:"servers"`
	Rules   []RuleConfig   `json:"rules"`
	Port    int            `json:"port"`
	Audit   AuditConfig    `json:"audit"`
	Log     LogConfig      `json:"log"`
}

// ServerConfig defines a backend MCP server.
type ServerConfig struct {
	Name    string            `json:"name"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Type    string            `json:"type,omitempty"`
	URL     string            `json:"url,omitempty"`
}

// RuleConfig defines a policy rule mapping a tool glob to a verdict.
type RuleConfig struct {
	Tool    string `json:"tool"`
	Verdict string `json:"verdict"`
}

// AuditConfig controls the SQLite audit log.
type AuditConfig struct {
	Path string `json:"path"`
}

// LogConfig controls logging behavior.
type LogConfig struct {
	Level string `json:"level"`
}

func xdgConfigHome() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config")
}

func xdgDataHome() string {
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share")
}

// ConfigPath returns the default config file path.
func ConfigPath() string {
	return filepath.Join(xdgConfigHome(), "mcp-broker", "config.json")
}

// DefaultConfig returns a Config with all default values.
func DefaultConfig() Config {
	return Config{
		Servers: []ServerConfig{},
		Rules: []RuleConfig{
			{Tool: "*", Verdict: "require-approval"},
		},
		Port: 8200,
		Audit: AuditConfig{
			Path: filepath.Join(xdgDataHome(), "mcp-broker", "audit.db"),
		},
		Log: LogConfig{Level: "info"},
	}
}

// Load reads config from the given path.
// If the file does not exist, it writes DefaultConfig() and returns it.
func Load(path string) (Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if _, err := Save(cfg, path); err != nil {
			return cfg, err
		}
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// Save writes cfg to path. Creates parent directories as needed.
// Returns the path written.
func Save(cfg Config, path string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// Refresh loads the config (with defaults overlay), then writes it back.
// This fills in any new default values. Returns the path written.
func Refresh(path string) (string, error) {
	cfg, err := Load(path)
	if err != nil {
		return "", err
	}
	return Save(cfg, path)
}
```

**Step 4: Run tests**

Run: `cd mcp-broker && go test ./internal/config/ -v -race`
Expected: All PASS.

**Step 5: Commit**

```bash
git add mcp-broker/internal/config/
git commit -m "feat: add config package with load, save, refresh"
```

---

### Task 3: CLI commands (root, serve, config)

Sets up the Cobra CLI with `serve`, `config path`, `config refresh`, and `config edit` subcommands. The `serve` command is a placeholder that just loads config and exits.

**Files:**
- Create: `mcp-broker/cmd/mcp-broker/root.go`
- Create: `mcp-broker/cmd/mcp-broker/serve.go`
- Create: `mcp-broker/cmd/mcp-broker/config.go`
- Modify: `mcp-broker/cmd/mcp-broker/main.go`

**Reference:** `~/Workspace/brocade/cmd/brocade/main.go`, `serve.go`, `config.go`

**Step 1: Install cobra dependency**

Run: `cd mcp-broker && go get github.com/spf13/cobra`

**Step 2: Rewrite main.go**

```go
package main

import (
	"log/slog"
	"os"
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		slog.Error("fatal error", "error", err)
		os.Exit(1)
	}
}
```

**Step 3: Create root.go**

```go
package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)

var cfgFile string

var rootCmd = &cobra.Command{
	Use:           "mcp-broker",
	Short:         "MCP proxy with policy rules, approval, and audit logging",
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", fmt.Sprintf("config file (default %q)", config.ConfigPath()))
}

func configPath() string {
	if cfgFile != "" {
		return cfgFile
	}
	return config.ConfigPath()
}
```

**Step 4: Create serve.go (placeholder)**

```go
package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)

func init() {
	rootCmd.AddCommand(serveCmd)
	serveCmd.Flags().String("log-level", "", "log level override (debug, info, warn, error)")
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the MCP broker",
	RunE:  runServe,
}

func parseLogLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func runServe(cmd *cobra.Command, _ []string) error {
	cfg, err := config.Load(configPath())
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	if v, _ := cmd.Flags().GetString("log-level"); v != "" {
		cfg.Log.Level = v
	}

	level := parseLogLevel(cfg.Log.Level)
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	logger := slog.New(handler)

	logger.Info("config loaded", "path", configPath())
	logger.Info("serve is not yet implemented")

	return nil
}
```

**Step 5: Create config.go**

Reference: `~/Workspace/brocade/cmd/brocade/config.go` — adapt directly.

```go
package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage mcp-broker configuration",
}

var configPathCmd = &cobra.Command{
	Use:   "path",
	Short: "Print the config file path",
	Args:  cobra.NoArgs,
	Run: func(_ *cobra.Command, _ []string) {
		fmt.Println(configPath())
	},
}

var configRefreshCmd = &cobra.Command{
	Use:   "refresh",
	Short: "Refresh config file with current defaults",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		path, err := config.Refresh(configPath())
		if err != nil {
			return err
		}
		fmt.Println(path)
		return nil
	},
}

var configEditCmd = &cobra.Command{
	Use:   "edit",
	Short: "Open the config file in your editor",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		path, err := config.Refresh(configPath())
		if err != nil {
			return err
		}
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "vi"
		}
		c := exec.Command(editor, path)
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		return c.Run()
	},
}

func init() {
	configCmd.AddCommand(configPathCmd)
	configCmd.AddCommand(configRefreshCmd)
	configCmd.AddCommand(configEditCmd)
	rootCmd.AddCommand(configCmd)
}
```

**Step 6: Verify CLI works**

Run: `cd mcp-broker && make build && /tmp/bin/mcp-broker --help`
Expected: Shows `serve` and `config` subcommands.

Run: `/tmp/bin/mcp-broker config path`
Expected: Prints path like `~/.config/mcp-broker/config.json`.

Run: `/tmp/bin/mcp-broker serve`
Expected: Logs "config loaded" and "serve is not yet implemented", exits 0.

**Step 7: Commit**

```bash
git add mcp-broker/cmd/mcp-broker/
git commit -m "feat: add CLI with serve and config commands"
```

---

### Task 4: Rules engine

Glob-based rule matching with three verdicts: allow, deny, require-approval.

**Files:**
- Create: `mcp-broker/internal/rules/rules.go`
- Create: `mcp-broker/internal/rules/rules_test.go`

**Reference:** `~/Workspace/brocade/internal/gatekeeper/static/static.go` — simplified version.

**Step 1: Write the tests**

```go
package rules

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)

func TestEngine_Evaluate_AllowRule(t *testing.T) {
	e := New([]config.RuleConfig{
		{Tool: "github.*", Verdict: "allow"},
	})
	require.Equal(t, Allow, e.Evaluate("github.get_pr"))
}

func TestEngine_Evaluate_DenyRule(t *testing.T) {
	e := New([]config.RuleConfig{
		{Tool: "*", Verdict: "deny"},
	})
	require.Equal(t, Deny, e.Evaluate("anything"))
}

func TestEngine_Evaluate_RequireApprovalRule(t *testing.T) {
	e := New([]config.RuleConfig{
		{Tool: "fs.write_file", Verdict: "require-approval"},
	})
	require.Equal(t, RequireApproval, e.Evaluate("fs.write_file"))
}

func TestEngine_Evaluate_FirstMatchWins(t *testing.T) {
	e := New([]config.RuleConfig{
		{Tool: "github.push", Verdict: "require-approval"},
		{Tool: "github.*", Verdict: "allow"},
		{Tool: "*", Verdict: "deny"},
	})
	require.Equal(t, RequireApproval, e.Evaluate("github.push"))
	require.Equal(t, Allow, e.Evaluate("github.get_pr"))
	require.Equal(t, Deny, e.Evaluate("linear.search"))
}

func TestEngine_Evaluate_DefaultIsRequireApproval(t *testing.T) {
	e := New(nil)
	require.Equal(t, RequireApproval, e.Evaluate("anything"))
}

func TestVerdict_String(t *testing.T) {
	require.Equal(t, "allow", Allow.String())
	require.Equal(t, "deny", Deny.String())
	require.Equal(t, "require-approval", RequireApproval.String())
}
```

**Step 2: Run tests to verify they fail**

Run: `cd mcp-broker && go test ./internal/rules/ -v`
Expected: FAIL — package doesn't exist yet.

**Step 3: Implement rules.go**

```go
package rules

import (
	"path/filepath"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)

// Verdict represents the outcome of a rule evaluation.
type Verdict int

const (
	Allow Verdict = iota
	Deny
	RequireApproval
)

func (v Verdict) String() string {
	switch v {
	case Allow:
		return "allow"
	case Deny:
		return "deny"
	case RequireApproval:
		return "require-approval"
	default:
		return "unknown"
	}
}

// ParseVerdict converts a string verdict to a Verdict value.
func ParseVerdict(s string) Verdict {
	switch s {
	case "allow":
		return Allow
	case "deny":
		return Deny
	case "require-approval":
		return RequireApproval
	default:
		return RequireApproval
	}
}

// Engine evaluates tool names against a static list of glob rules.
type Engine struct {
	rules []config.RuleConfig
}

// New creates a rules engine with the given rules.
func New(rules []config.RuleConfig) *Engine {
	return &Engine{rules: rules}
}

// Evaluate returns the verdict for the given tool name.
// First matching rule wins. Default is require-approval.
func (e *Engine) Evaluate(tool string) Verdict {
	for _, rule := range e.rules {
		matched, err := filepath.Match(rule.Tool, tool)
		if err != nil {
			continue
		}
		if matched {
			return ParseVerdict(rule.Verdict)
		}
	}
	return RequireApproval
}
```

**Step 4: Run tests**

Run: `cd mcp-broker && go test ./internal/rules/ -v -race`
Expected: All PASS.

**Step 5: Commit**

```bash
git add mcp-broker/internal/rules/
git commit -m "feat: add rules engine with glob-based matching"
```

---

### Task 5: Audit package (SQLite)

SQLite-backed audit log adapted from `~/Workspace/brocade/internal/auditor/sqlite/sqlite.go`. Uses `ncruces/go-sqlite3` instead of `modernc.org/sqlite`.

**Files:**
- Create: `mcp-broker/internal/audit/audit.go`
- Create: `mcp-broker/internal/audit/audit_test.go`

**Step 1: Install sqlite dependency**

Run: `cd mcp-broker && go get github.com/ncruces/go-sqlite3 github.com/ncruces/go-sqlite3/driver`

**Step 2: Write the tests**

```go
package audit

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLogger_RecordAndQuery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	l, err := NewLogger(path)
	require.NoError(t, err)
	defer l.Close(context.Background())

	rec := Record{
		Timestamp: time.Now(),
		Tool:      "github.get_pr",
		Args:      map[string]any{"repo": "test"},
		Verdict:   "allow",
		Error:     "",
	}
	err = l.Record(context.Background(), rec)
	require.NoError(t, err)

	records, total, err := l.Query(context.Background(), QueryOpts{Limit: 10})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, records, 1)
	require.Equal(t, "github.get_pr", records[0].Tool)
}

func TestLogger_QueryWithFilter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	l, err := NewLogger(path)
	require.NoError(t, err)
	defer l.Close(context.Background())

	for _, tool := range []string{"github.get_pr", "github.search", "linear.search"} {
		err := l.Record(context.Background(), Record{
			Timestamp: time.Now(),
			Tool:      tool,
			Verdict:   "allow",
		})
		require.NoError(t, err)
	}

	records, total, err := l.Query(context.Background(), QueryOpts{Tool: "github", Limit: 10})
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, records, 2)
}

func TestLogger_QueryPagination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	l, err := NewLogger(path)
	require.NoError(t, err)
	defer l.Close(context.Background())

	for i := range 5 {
		err := l.Record(context.Background(), Record{
			Timestamp: time.Now().Add(time.Duration(i) * time.Second),
			Tool:      "test.tool",
			Verdict:   "allow",
		})
		require.NoError(t, err)
	}

	records, total, err := l.Query(context.Background(), QueryOpts{Limit: 2, Offset: 0})
	require.NoError(t, err)
	require.Equal(t, 5, total)
	require.Len(t, records, 2)

	records2, _, err := l.Query(context.Background(), QueryOpts{Limit: 2, Offset: 2})
	require.NoError(t, err)
	require.Len(t, records2, 2)
}

func TestLogger_RecordWithApproval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	l, err := NewLogger(path)
	require.NoError(t, err)
	defer l.Close(context.Background())

	approved := true
	err = l.Record(context.Background(), Record{
		Timestamp: time.Now(),
		Tool:      "fs.write",
		Verdict:   "require-approval",
		Approved:  &approved,
	})
	require.NoError(t, err)

	records, _, err := l.Query(context.Background(), QueryOpts{Limit: 10})
	require.NoError(t, err)
	require.NotNil(t, records[0].Approved)
	require.True(t, *records[0].Approved)
}
```

**Step 3: Run tests to verify they fail**

Run: `cd mcp-broker && go test ./internal/audit/ -v`
Expected: FAIL — package doesn't exist yet.

**Step 4: Implement audit.go**

Reference: `~/Workspace/brocade/internal/auditor/sqlite/sqlite.go` — adapted to use `ncruces/go-sqlite3` and simplified types.

```go
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

// Record captures the full lifecycle of a tool call.
type Record struct {
	Timestamp time.Time      `json:"timestamp"`
	Tool      string         `json:"tool"`
	Args      map[string]any `json:"args,omitempty"`
	Verdict   string         `json:"verdict"`
	Approved  *bool          `json:"approved,omitempty"`
	Result    any            `json:"result,omitempty"`
	Error     string         `json:"error,omitempty"`
}

// QueryOpts controls filtering and pagination for audit queries.
type QueryOpts struct {
	Tool   string
	Limit  int
	Offset int
}

const createSQL = `
CREATE TABLE IF NOT EXISTS audit_records (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp TEXT    NOT NULL,
    tool      TEXT    NOT NULL,
    args      TEXT,
    verdict   TEXT    NOT NULL,
    approved  INTEGER,
    result    TEXT,
    error     TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_audit_timestamp ON audit_records(timestamp);
CREATE INDEX IF NOT EXISTS idx_audit_tool ON audit_records(tool);
`

const insertSQL = `INSERT INTO audit_records (timestamp, tool, args, verdict, approved, result, error)
VALUES (?, ?, ?, ?, ?, ?, ?)`

// Logger records and queries audit entries in a SQLite database.
type Logger struct {
	mu   sync.Mutex
	db   *sql.DB
	stmt *sql.Stmt
}

// NewLogger creates a Logger that writes to the given database path.
func NewLogger(path string) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create audit dir: %w", err)
	}

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("open audit db: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable WAL mode: %w", err)
	}

	if _, err := db.Exec(createSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create audit table: %w", err)
	}

	stmt, err := db.Prepare(insertSQL)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("prepare insert: %w", err)
	}

	return &Logger{db: db, stmt: stmt}, nil
}

// Record inserts an audit record.
func (l *Logger) Record(_ context.Context, rec Record) error {
	argsJSON, err := marshalNullable(rec.Args)
	if err != nil {
		return fmt.Errorf("marshal args: %w", err)
	}

	resultJSON, err := marshalNullable(rec.Result)
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}

	var approved sql.NullInt64
	if rec.Approved != nil {
		if *rec.Approved {
			approved = sql.NullInt64{Int64: 1, Valid: true}
		} else {
			approved = sql.NullInt64{Int64: 0, Valid: true}
		}
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	_, err = l.stmt.Exec(
		rec.Timestamp.Format(time.RFC3339),
		rec.Tool,
		argsJSON,
		rec.Verdict,
		approved,
		resultJSON,
		rec.Error,
	)
	if err != nil {
		return fmt.Errorf("insert audit record: %w", err)
	}
	return nil
}

// Query returns audit records matching the given filters.
func (l *Logger) Query(_ context.Context, opts QueryOpts) ([]Record, int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	where := ""
	var queryArgs []any

	if opts.Tool != "" {
		where = " WHERE tool LIKE '%' || ? || '%'"
		queryArgs = append(queryArgs, opts.Tool)
	}

	var total int
	countSQL := "SELECT COUNT(*) FROM audit_records" + where
	if err := l.db.QueryRow(countSQL, queryArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count audit records: %w", err)
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}

	selectSQL := "SELECT timestamp, tool, args, verdict, approved, result, error FROM audit_records" +
		where + " ORDER BY id DESC LIMIT ? OFFSET ?"
	selectArgs := append(queryArgs, limit, opts.Offset)

	rows, err := l.db.Query(selectSQL, selectArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("query audit records: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var records []Record
	for rows.Next() {
		var (
			ts, tool, verdict, errStr string
			argsJSON, resultJSON      sql.NullString
			approved                  sql.NullInt64
		)
		if err := rows.Scan(&ts, &tool, &argsJSON, &verdict, &approved, &resultJSON, &errStr); err != nil {
			return nil, 0, fmt.Errorf("scan audit record: %w", err)
		}

		timestamp, _ := time.Parse(time.RFC3339, ts)

		rec := Record{
			Timestamp: timestamp,
			Tool:      tool,
			Verdict:   verdict,
			Error:     errStr,
		}

		if argsJSON.Valid {
			var args map[string]any
			if err := json.Unmarshal([]byte(argsJSON.String), &args); err == nil {
				rec.Args = args
			}
		}

		if resultJSON.Valid {
			var result any
			if err := json.Unmarshal([]byte(resultJSON.String), &result); err == nil {
				rec.Result = result
			}
		}

		if approved.Valid {
			b := approved.Int64 == 1
			rec.Approved = &b
		}

		records = append(records, rec)
	}

	if records == nil {
		records = []Record{}
	}

	return records, total, rows.Err()
}

// Close closes the prepared statement and database.
func (l *Logger) Close(_ context.Context) error {
	_ = l.stmt.Close()
	return l.db.Close()
}

func marshalNullable(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(data), nil
}

// ensure queryArgs slice copies work correctly with append
var _ = strings.Join
```

Note: Remove the unused `strings` import after verifying — it's there for the `WHERE` clause building. Actually it's unused, remove it. The `strings` import was in the Brocade version for `strings.Join` but we don't need it here.

**Step 5: Run tests**

Run: `cd mcp-broker && go test ./internal/audit/ -v -race`
Expected: All PASS.

**Step 6: Commit**

```bash
git add mcp-broker/internal/audit/
git commit -m "feat: add SQLite audit log with record and query"
```

---

### Task 6: Server manager (backend MCP client)

Connects to backend MCP servers (stdio or HTTP), discovers tools via `tools/list`, namespaces them, and proxies `tools/call`. This is the core new functionality that replaces Brocade's custom providers.

**Files:**
- Create: `mcp-broker/internal/server/manager.go`
- Create: `mcp-broker/internal/server/manager_test.go`

**Step 1: Install mcp-go dependency**

Run: `cd mcp-broker && go get github.com/mark3labs/mcp-go`

**Step 2: Write the tests**

Tests use a mock interface since we can't easily spawn real MCP servers in unit tests.

```go
package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)

// mockBackend implements the Backend interface for testing.
type mockBackend struct {
	mock.Mock
}

func (m *mockBackend) ListTools(ctx context.Context) ([]Tool, error) {
	args := m.Called(ctx)
	return args.Get(0).([]Tool), args.Error(1)
}

func (m *mockBackend) CallTool(ctx context.Context, name string, arguments map[string]any) (*ToolResult, error) {
	args := m.Called(ctx, name, arguments)
	return args.Get(0).(*ToolResult), args.Error(1)
}

func (m *mockBackend) Close() error {
	args := m.Called()
	return args.Error(0)
}

func TestManager_DiscoverTools_PrefixesNames(t *testing.T) {
	mb := new(mockBackend)
	mb.On("ListTools", mock.Anything).Return([]Tool{
		{Name: "search", Description: "Search things"},
		{Name: "get_pr", Description: "Get a PR"},
	}, nil)
	mb.On("Close").Return(nil)

	m := &Manager{
		backends: map[string]Backend{"github": mb},
		tools:    make(map[string]toolEntry),
	}

	err := m.discover(context.Background())
	require.NoError(t, err)

	tools := m.Tools()
	require.Len(t, tools, 2)

	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name] = true
	}
	require.True(t, names["github.search"])
	require.True(t, names["github.get_pr"])
}

func TestManager_Call_ProxiesToCorrectBackend(t *testing.T) {
	mb := new(mockBackend)
	mb.On("ListTools", mock.Anything).Return([]Tool{
		{Name: "search", Description: "Search"},
	}, nil)
	mb.On("CallTool", mock.Anything, "search", map[string]any{"q": "test"}).
		Return(&ToolResult{Content: "found it"}, nil)
	mb.On("Close").Return(nil)

	m := &Manager{
		backends: map[string]Backend{"github": mb},
		tools:    make(map[string]toolEntry),
	}

	err := m.discover(context.Background())
	require.NoError(t, err)

	result, err := m.Call(context.Background(), "github.search", map[string]any{"q": "test"})
	require.NoError(t, err)
	require.Equal(t, "found it", result.Content)

	mb.AssertExpectations(t)
}

func TestManager_Call_UnknownToolReturnsError(t *testing.T) {
	m := &Manager{
		backends: map[string]Backend{},
		tools:    make(map[string]toolEntry),
	}

	_, err := m.Call(context.Background(), "nonexistent.tool", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown tool")
}

func TestExpandEnv_SubstitutesVariables(t *testing.T) {
	t.Setenv("MY_TOKEN", "secret123")
	env := map[string]string{
		"TOKEN":  "$MY_TOKEN",
		"STATIC": "plainvalue",
	}
	result := expandEnv(env)
	require.Equal(t, "secret123", result["TOKEN"])
	require.Equal(t, "plainvalue", result["STATIC"])
}
```

**Step 3: Run tests to verify they fail**

Run: `cd mcp-broker && go test ./internal/server/ -v`
Expected: FAIL — package doesn't exist yet.

**Step 4: Implement manager.go**

This is new code — no direct Brocade equivalent. It's the generic MCP client that replaces all custom providers.

```go
package server

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)

// Tool represents a discovered MCP tool with its schema.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema,omitempty"`
}

// ToolResult holds the result of a tool call.
type ToolResult struct {
	Content any
	IsError bool
}

// Backend is the interface for communicating with an MCP server.
// Implementations handle stdio and HTTP transports.
type Backend interface {
	ListTools(ctx context.Context) ([]Tool, error)
	CallTool(ctx context.Context, name string, arguments map[string]any) (*ToolResult, error)
	Close() error
}

// toolEntry maps a prefixed tool name to its backend and original name.
type toolEntry struct {
	backend      Backend
	originalName string
	tool         Tool
}

// Manager manages connections to backend MCP servers.
type Manager struct {
	backends map[string]Backend
	tools    map[string]toolEntry
	logger   *slog.Logger
}

// NewManager creates a Manager and connects to all configured backends.
func NewManager(ctx context.Context, servers []config.ServerConfig, logger *slog.Logger) (*Manager, error) {
	m := &Manager{
		backends: make(map[string]Backend),
		tools:    make(map[string]toolEntry),
		logger:   logger,
	}

	for _, srv := range servers {
		backend, err := connect(ctx, srv, logger)
		if err != nil {
			// Log and skip failed backends rather than failing entirely
			logger.Error("failed to connect to backend", "name", srv.Name, "error", err)
			continue
		}
		m.backends[srv.Name] = backend
		logger.Info("connected to backend", "name", srv.Name)
	}

	if err := m.discover(ctx); err != nil {
		return nil, fmt.Errorf("discovering tools: %w", err)
	}

	return m, nil
}

// connect creates a Backend for the given server config.
func connect(ctx context.Context, srv config.ServerConfig, logger *slog.Logger) (Backend, error) {
	switch srv.Type {
	case "http":
		return newHTTPBackend(ctx, srv)
	default:
		// stdio is the default
		return newStdioBackend(ctx, srv, logger)
	}
}

// discover calls tools/list on each backend and builds the prefixed tool registry.
func (m *Manager) discover(ctx context.Context) error {
	for name, backend := range m.backends {
		tools, err := backend.ListTools(ctx)
		if err != nil {
			m.logger.Error("failed to list tools", "backend", name, "error", err)
			continue
		}
		for _, tool := range tools {
			prefixed := name + "." + tool.Name
			m.tools[prefixed] = toolEntry{
				backend:      backend,
				originalName: tool.Name,
				tool: Tool{
					Name:        prefixed,
					Description: tool.Description,
					InputSchema: tool.InputSchema,
				},
			}
		}
		m.logger.Info("discovered tools", "backend", name, "count", len(tools))
	}
	return nil
}

// Tools returns all discovered tools across all backends.
func (m *Manager) Tools() []Tool {
	tools := make([]Tool, 0, len(m.tools))
	for _, entry := range m.tools {
		tools = append(tools, entry.tool)
	}
	return tools
}

// Call proxies a tool call to the appropriate backend.
func (m *Manager) Call(ctx context.Context, tool string, args map[string]any) (*ToolResult, error) {
	entry, ok := m.tools[tool]
	if !ok {
		return nil, fmt.Errorf("unknown tool: %s", tool)
	}
	return entry.backend.CallTool(ctx, entry.originalName, args)
}

// Close shuts down all backend connections.
func (m *Manager) Close() error {
	var errs []error
	for name, backend := range m.backends {
		if err := backend.Close(); err != nil {
			errs = append(errs, fmt.Errorf("closing %s: %w", name, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("errors closing backends: %v", errs)
	}
	return nil
}

// expandEnv substitutes $VAR references in env values from the process environment.
func expandEnv(env map[string]string) map[string]string {
	if env == nil {
		return nil
	}
	result := make(map[string]string, len(env))
	for k, v := range env {
		if strings.HasPrefix(v, "$") {
			result[k] = os.Getenv(v[1:])
		} else {
			result[k] = v
		}
	}
	return result
}
```

**Step 5: Run tests**

Run: `cd mcp-broker && go test ./internal/server/ -v -race`
Expected: All PASS.

**Step 6: Commit**

```bash
git add mcp-broker/internal/server/
git commit -m "feat: add server manager with tool discovery and proxying"
```

---

### Task 7: Backend implementations (stdio + HTTP)

Implement the actual `Backend` interface using mcp-go's client libraries.

**Files:**
- Create: `mcp-broker/internal/server/stdio.go`
- Create: `mcp-broker/internal/server/http.go`

**Step 1: Implement stdio.go**

```go
package server

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)

// stdioBackend communicates with an MCP server via stdio.
type stdioBackend struct {
	client *client.StdioMCPClient
}

func newStdioBackend(ctx context.Context, srv config.ServerConfig, logger *slog.Logger) (*stdioBackend, error) {
	env := expandEnv(srv.Env)
	envSlice := os.Environ()
	for k, v := range env {
		envSlice = append(envSlice, k+"="+v)
	}

	c, err := client.NewStdioMCPClient(srv.Command, envSlice, srv.Args...)
	if err != nil {
		return nil, fmt.Errorf("spawn stdio server %q: %w", srv.Name, err)
	}

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "mcp-broker",
		Version: "0.1.0",
	}

	if _, err := c.Initialize(ctx, initReq); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("initialize stdio server %q: %w", srv.Name, err)
	}

	logger.Debug("stdio backend initialized", "name", srv.Name, "command", srv.Command)

	return &stdioBackend{client: c}, nil
}

func (b *stdioBackend) ListTools(ctx context.Context) ([]Tool, error) {
	req := mcp.ListToolsRequest{}
	resp, err := b.client.ListTools(ctx, req)
	if err != nil {
		return nil, err
	}

	tools := make([]Tool, len(resp.Tools))
	for i, t := range resp.Tools {
		schema := make(map[string]any)
		if t.InputSchema.Properties != nil {
			schema["type"] = t.InputSchema.Type
			schema["properties"] = t.InputSchema.Properties
		}
		if t.InputSchema.Required != nil {
			schema["required"] = t.InputSchema.Required
		}
		tools[i] = Tool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		}
	}
	return tools, nil
}

func (b *stdioBackend) CallTool(ctx context.Context, name string, arguments map[string]any) (*ToolResult, error) {
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = arguments

	resp, err := b.client.CallTool(ctx, req)
	if err != nil {
		return nil, err
	}

	return &ToolResult{
		Content: resp.Content,
		IsError: resp.IsError,
	}, nil
}

func (b *stdioBackend) Close() error {
	return b.client.Close()
}
```

**Step 2: Implement http.go**

```go
package server

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)

// httpBackend communicates with an MCP server via Streamable HTTP.
type httpBackend struct {
	client *client.StreamableHTTPMCPClient
}

func newHTTPBackend(ctx context.Context, srv config.ServerConfig) (*httpBackend, error) {
	c, err := client.NewStreamableHTTPMCPClient(srv.URL)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client for %q: %w", srv.Name, err)
	}

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "mcp-broker",
		Version: "0.1.0",
	}

	if _, err := c.Initialize(ctx, initReq); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("initialize HTTP server %q: %w", srv.Name, err)
	}

	return &httpBackend{client: c}, nil
}

func (b *httpBackend) ListTools(ctx context.Context) ([]Tool, error) {
	req := mcp.ListToolsRequest{}
	resp, err := b.client.ListTools(ctx, req)
	if err != nil {
		return nil, err
	}

	tools := make([]Tool, len(resp.Tools))
	for i, t := range resp.Tools {
		schema := make(map[string]any)
		if t.InputSchema.Properties != nil {
			schema["type"] = t.InputSchema.Type
			schema["properties"] = t.InputSchema.Properties
		}
		if t.InputSchema.Required != nil {
			schema["required"] = t.InputSchema.Required
		}
		tools[i] = Tool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		}
	}
	return tools, nil
}

func (b *httpBackend) CallTool(ctx context.Context, name string, arguments map[string]any) (*ToolResult, error) {
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = arguments

	resp, err := b.client.CallTool(ctx, req)
	if err != nil {
		return nil, err
	}

	return &ToolResult{
		Content: resp.Content,
		IsError: resp.IsError,
	}, nil
}

func (b *httpBackend) Close() error {
	return b.client.Close()
}
```

**Step 3: Verify build**

Run: `cd mcp-broker && make build`
Expected: Compiles successfully.

**Step 4: Commit**

```bash
git add mcp-broker/internal/server/stdio.go mcp-broker/internal/server/http.go
git commit -m "feat: add stdio and HTTP backend implementations"
```

---

### Task 8: Dashboard (web UI + approval logic)

The web dashboard with approval flow, tool listing, and audit viewing. Adapted from `~/Workspace/brocade/internal/approver/web/web.go` and `index.html`.

**Files:**
- Create: `mcp-broker/internal/dashboard/dashboard.go`
- Create: `mcp-broker/internal/dashboard/dashboard_test.go`
- Create: `mcp-broker/internal/dashboard/index.html`

**Step 1: Write tests**

```go
package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDashboard_Review_ApprovesViaAPI(t *testing.T) {
	d := New(nil, nil, nil)
	mux := d.Handler()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Start a review in a goroutine
	done := make(chan bool, 1)
	go func() {
		approved, err := d.Review(context.Background(), "github.push", map[string]any{"branch": "main"})
		require.NoError(t, err)
		done <- approved
	}()

	// Wait for the pending request to appear
	time.Sleep(50 * time.Millisecond)

	// Get pending requests
	resp, err := http.Get(srv.URL + "/api/pending")
	require.NoError(t, err)
	defer resp.Body.Close()

	var pending []pendingRequest
	err = json.NewDecoder(resp.Body).Decode(&pending)
	require.NoError(t, err)
	require.Len(t, pending, 1)

	// Approve it
	body := `{"id":"` + pending[0].ID + `","decision":"approve"}`
	resp2, err := http.Post(srv.URL+"/api/decide", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode)

	approved := <-done
	require.True(t, approved)
}

func TestDashboard_Review_DeniesViaAPI(t *testing.T) {
	d := New(nil, nil, nil)
	mux := d.Handler()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	done := make(chan bool, 1)
	go func() {
		approved, err := d.Review(context.Background(), "github.push", map[string]any{})
		require.NoError(t, err)
		done <- approved
	}()

	time.Sleep(50 * time.Millisecond)

	resp, err := http.Get(srv.URL + "/api/pending")
	require.NoError(t, err)
	defer resp.Body.Close()

	var pending []pendingRequest
	err = json.NewDecoder(resp.Body).Decode(&pending)
	require.NoError(t, err)

	body := `{"id":"` + pending[0].ID + `","decision":"deny"}`
	resp2, err := http.Post(srv.URL+"/api/decide", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp2.Body.Close()

	approved := <-done
	require.False(t, approved)
}

func TestDashboard_Review_CancelsOnContextDone(t *testing.T) {
	d := New(nil, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := d.Review(ctx, "test.tool", nil)
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	err := <-done
	require.Error(t, err)
}
```

**Step 2: Run tests to verify they fail**

Run: `cd mcp-broker && go test ./internal/dashboard/ -v`
Expected: FAIL.

**Step 3: Implement dashboard.go**

Reference: `~/Workspace/brocade/internal/approver/web/web.go` — adapted with merged approval logic, simplified types.

```go
package dashboard

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/server"
)

type pendingRequest struct {
	ID        string         `json:"id"`
	Tool      string         `json:"tool"`
	Args      map[string]any `json:"args"`
	Timestamp time.Time      `json:"timestamp"`
	decision  chan bool
}

type decidedRequest struct {
	ID        string         `json:"id"`
	Tool      string         `json:"tool"`
	Args      map[string]any `json:"args"`
	Decision  string         `json:"decision"`
	Timestamp time.Time      `json:"timestamp"`
	DecidedAt time.Time      `json:"decided_at"`
}

// ToolLister provides the list of discovered tools.
type ToolLister interface {
	Tools() []server.Tool
}

// AuditQuerier provides audit log queries.
type AuditQuerier interface {
	Query(ctx context.Context, opts audit.QueryOpts) ([]audit.Record, int, error)
}

// Dashboard serves the web UI and manages the approval flow.
type Dashboard struct {
	mu      sync.Mutex
	pending map[string]*pendingRequest
	decided []decidedRequest
	clients []chan []byte
	tools   ToolLister
	auditor AuditQuerier
	logger  *slog.Logger
}

// New creates a Dashboard.
func New(tools ToolLister, auditor AuditQuerier, logger *slog.Logger) *Dashboard {
	return &Dashboard{
		pending: make(map[string]*pendingRequest),
		tools:   tools,
		auditor: auditor,
		logger:  logger,
	}
}

// Handler returns the HTTP handler for the dashboard.
func (d *Dashboard) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /events", d.handleEvents)
	mux.HandleFunc("POST /api/decide", d.handleDecide)
	mux.HandleFunc("GET /api/pending", d.handlePending)
	mux.HandleFunc("GET /api/tools", d.handleTools)
	mux.HandleFunc("GET /api/audit", d.handleAudit)
	mux.HandleFunc("GET /", d.handleIndex)
	return mux
}

// Review blocks until a human approves or denies the request via the web UI.
func (d *Dashboard) Review(ctx context.Context, tool string, args map[string]any) (bool, error) {
	id := generateID()
	ch := make(chan bool, 1)

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
	case approved := <-ch:
		return approved, nil
	case <-ctx.Done():
		d.mu.Lock()
		delete(d.pending, id)
		d.mu.Unlock()
		d.broadcast(removedEvent(id))
		return false, ctx.Err()
	}
}

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
	pr.decision <- approved

	decision := "denied"
	if approved {
		decision = "approved"
	}
	dr := decidedRequest{
		ID:        pr.ID,
		Tool:      pr.Tool,
		Args:      pr.Args,
		Decision:  decision,
		Timestamp: pr.Timestamp,
		DecidedAt: time.Now(),
	}
	d.mu.Lock()
	d.decided = append(d.decided, dr)
	d.mu.Unlock()

	d.broadcast(decidedEvent(dr))
	w.WriteHeader(http.StatusOK)
}

func (d *Dashboard) handlePending(w http.ResponseWriter, _ *http.Request) {
	d.mu.Lock()
	items := make([]*pendingRequest, 0, len(d.pending))
	for _, pr := range d.pending {
		items = append(items, pr)
	}
	d.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(items)
}

func (d *Dashboard) handleTools(w http.ResponseWriter, _ *http.Request) {
	var tools []server.Tool
	if d.tools != nil {
		tools = d.tools.Tools()
		sort.Slice(tools, func(i, j int) bool {
			return tools[i].Name < tools[j].Name
		})
	}
	if tools == nil {
		tools = []server.Tool{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"tools": tools})
}

func (d *Dashboard) handleAudit(w http.ResponseWriter, r *http.Request) {
	if d.auditor == nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"records": []audit.Record{}, "total": 0})
		return
	}

	opts := audit.QueryOpts{}
	if v := r.URL.Query().Get("tool"); v != "" {
		opts.Tool = v
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			opts.Limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			opts.Offset = n
		}
	}

	records, total, err := d.auditor.Query(r.Context(), opts)
	if err != nil {
		if d.logger != nil {
			d.logger.Error("audit query failed", "error", err)
		}
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"records": records, "total": total})
}

func (d *Dashboard) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan []byte, 16)
	d.mu.Lock()
	d.clients = append(d.clients, ch)
	d.mu.Unlock()

	defer func() {
		d.mu.Lock()
		for i, c := range d.clients {
			if c == ch {
				d.clients = append(d.clients[:i], d.clients[i+1:]...)
				break
			}
		}
		d.mu.Unlock()
	}()

	_, _ = fmt.Fprintf(w, ": keepalive\n\n")
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case msg := <-ch:
			_, _ = fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (d *Dashboard) handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
}

func (d *Dashboard) broadcast(data []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, ch := range d.clients {
		select {
		case ch <- data:
		default:
		}
	}
}

type sseEvent struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

func newRequestEvent(pr *pendingRequest) []byte {
	b, _ := json.Marshal(sseEvent{Type: "new", Data: pr})
	return b
}

func removedEvent(id string) []byte {
	b, _ := json.Marshal(sseEvent{Type: "removed", Data: map[string]string{"id": id}})
	return b
}

func decidedEvent(dr decidedRequest) []byte {
	b, _ := json.Marshal(sseEvent{Type: "decided", Data: dr})
	return b
}

func generateID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

//go:embed index.html
var indexHTML []byte
```

**Step 4: Create index.html**

Copy `~/Workspace/brocade/internal/approver/web/index.html` and make these changes:
1. Replace "Brocade Dashboard" with "mcp-broker" in the `<title>` and `<h1>`
2. Replace "Providers" tab label with "Tools"
3. Replace "Capabilities" with "Tools" throughout
4. Replace `capability` variable names with `tool` where they appear in display text
5. Replace "Registered Capabilities" with "Discovered Tools"
6. Replace `/api/capabilities` with `/api/tools` and adjust the response key from `capabilities` to `tools`
7. Replace `brocade-notify` localStorage key with `mcp-broker-notify`
8. Replace "Brocade — New Request" notification text with "mcp-broker — Approval Required"
9. In the Audit Log tab filters, change "Capability" label to "Tool" and update the corresponding query parameter from `method` to `tool`
10. Remove the "Agent ID" filter from the audit tab (no agent identity tracking)
11. In the audit table headers, change "Capability" to "Tool" and remove "Agent" column

This is a large file (~1180 lines). The implementer should copy the file from the reference and apply the changes above rather than writing it from scratch.

**Step 5: Run tests**

Run: `cd mcp-broker && go test ./internal/dashboard/ -v -race`
Expected: All PASS.

**Step 6: Commit**

```bash
git add mcp-broker/internal/dashboard/
git commit -m "feat: add dashboard with approval flow, tool listing, and audit UI"
```

---

### Task 9: Broker (core orchestrator)

Wires everything together: creates the frontend MCP server, routes tool calls through the pipeline (rules → approval → proxy → audit).

**Files:**
- Create: `mcp-broker/internal/broker/broker.go`
- Create: `mcp-broker/internal/broker/broker_test.go`

**Reference:** `~/Workspace/brocade/internal/kernel/kernel.go` for the pipeline pattern, `~/Workspace/brocade/internal/transporter/mcp/mcp.go` for MCP server integration.

**Step 1: Write the tests**

```go
package broker

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/rules"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/server"
)

type mockServerManager struct{ mock.Mock }

func (m *mockServerManager) Tools() []server.Tool {
	args := m.Called()
	return args.Get(0).([]server.Tool)
}

func (m *mockServerManager) Call(ctx context.Context, tool string, arguments map[string]any) (*server.ToolResult, error) {
	args := m.Called(ctx, tool, arguments)
	return args.Get(0).(*server.ToolResult), args.Error(1)
}

type mockAuditLogger struct{ mock.Mock }

func (m *mockAuditLogger) Record(ctx context.Context, rec audit.Record) error {
	args := m.Called(ctx, rec)
	return args.Error(0)
}

func (m *mockAuditLogger) Query(ctx context.Context, opts audit.QueryOpts) ([]audit.Record, int, error) {
	args := m.Called(ctx, opts)
	return args.Get(0).([]audit.Record), args.Int(1), args.Error(2)
}

type mockApprover struct{ mock.Mock }

func (m *mockApprover) Review(ctx context.Context, tool string, args map[string]any) (bool, error) {
	a := m.Called(ctx, tool, args)
	return a.Bool(0), a.Error(1)
}

func TestBroker_Handle_AllowedTool(t *testing.T) {
	sm := new(mockServerManager)
	sm.On("Call", mock.Anything, "github.search", map[string]any{"q": "test"}).
		Return(&server.ToolResult{Content: "results"}, nil)

	al := new(mockAuditLogger)
	al.On("Record", mock.Anything, mock.MatchedBy(func(r audit.Record) bool {
		return r.Tool == "github.search" && r.Verdict == "allow"
	})).Return(nil)

	engine := rules.New([]config.RuleConfig{{Tool: "github.*", Verdict: "allow"}})

	b := &Broker{
		servers:  sm,
		rules:    engine,
		auditor:  al,
		approver: nil,
	}

	result, err := b.Handle(context.Background(), "github.search", map[string]any{"q": "test"})
	require.NoError(t, err)
	require.Equal(t, "results", result)

	sm.AssertExpectations(t)
	al.AssertExpectations(t)
}

func TestBroker_Handle_DeniedTool(t *testing.T) {
	al := new(mockAuditLogger)
	al.On("Record", mock.Anything, mock.MatchedBy(func(r audit.Record) bool {
		return r.Verdict == "deny" && r.Error != ""
	})).Return(nil)

	engine := rules.New([]config.RuleConfig{{Tool: "*", Verdict: "deny"}})

	b := &Broker{
		servers:  new(mockServerManager),
		rules:    engine,
		auditor:  al,
		approver: nil,
	}

	_, err := b.Handle(context.Background(), "anything", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "denied")
}

func TestBroker_Handle_ApprovalRequired_Approved(t *testing.T) {
	sm := new(mockServerManager)
	sm.On("Call", mock.Anything, "fs.write", map[string]any{"path": "/tmp"}).
		Return(&server.ToolResult{Content: "ok"}, nil)

	al := new(mockAuditLogger)
	al.On("Record", mock.Anything, mock.Anything).Return(nil)

	ap := new(mockApprover)
	ap.On("Review", mock.Anything, "fs.write", map[string]any{"path": "/tmp"}).Return(true, nil)

	engine := rules.New([]config.RuleConfig{{Tool: "*", Verdict: "require-approval"}})

	b := &Broker{
		servers:  sm,
		rules:    engine,
		auditor:  al,
		approver: ap,
	}

	result, err := b.Handle(context.Background(), "fs.write", map[string]any{"path": "/tmp"})
	require.NoError(t, err)
	require.Equal(t, "ok", result)
}

func TestBroker_Handle_ApprovalRequired_Denied(t *testing.T) {
	al := new(mockAuditLogger)
	al.On("Record", mock.Anything, mock.Anything).Return(nil)

	ap := new(mockApprover)
	ap.On("Review", mock.Anything, "fs.write", mock.Anything).Return(false, nil)

	engine := rules.New([]config.RuleConfig{{Tool: "*", Verdict: "require-approval"}})

	b := &Broker{
		servers:  new(mockServerManager),
		rules:    engine,
		auditor:  al,
		approver: ap,
	}

	_, err := b.Handle(context.Background(), "fs.write", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "denied")
}
```

Note: The test file will need to import `config` for `config.RuleConfig`. Add this import:

```go
import "github.com/averycrespi/agent-tools/mcp-broker/internal/config"
```

**Step 2: Run tests to verify they fail**

Run: `cd mcp-broker && go test ./internal/broker/ -v`
Expected: FAIL.

**Step 3: Implement broker.go**

```go
package broker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/rules"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/server"
)

var ErrDenied = errors.New("denied by policy")

// ServerManager proxies tool calls to backend MCP servers.
type ServerManager interface {
	Tools() []server.Tool
	Call(ctx context.Context, tool string, args map[string]any) (*server.ToolResult, error)
}

// AuditLogger records audit entries.
type AuditLogger interface {
	Record(ctx context.Context, rec audit.Record) error
	Query(ctx context.Context, opts audit.QueryOpts) ([]audit.Record, int, error)
}

// Approver handles human approval decisions.
type Approver interface {
	Review(ctx context.Context, tool string, args map[string]any) (bool, error)
}

// Broker orchestrates the tool call pipeline.
type Broker struct {
	servers  ServerManager
	rules    *rules.Engine
	auditor  AuditLogger
	approver Approver
	logger   *slog.Logger
}

// New creates a Broker with the given components.
func New(servers ServerManager, rulesEngine *rules.Engine, auditor AuditLogger, approver Approver, logger *slog.Logger) *Broker {
	return &Broker{
		servers:  servers,
		rules:    rulesEngine,
		auditor:  auditor,
		approver: approver,
		logger:   logger,
	}
}

// Handle drives the full tool call pipeline: rules → approval → proxy → audit.
func (b *Broker) Handle(ctx context.Context, tool string, args map[string]any) (any, error) {
	rec := audit.Record{
		Timestamp: time.Now(),
		Tool:      tool,
		Args:      args,
	}

	// 1. Rules check
	verdict := b.rules.Evaluate(tool)
	rec.Verdict = verdict.String()

	if b.logger != nil {
		b.logger.Debug("rules evaluated", "tool", tool, "verdict", verdict)
	}

	switch verdict {
	case rules.Deny:
		rec.Error = fmt.Sprintf("denied by policy: %s", tool)
		_ = b.auditor.Record(ctx, rec)
		return nil, fmt.Errorf("%w: %s", ErrDenied, tool)

	case rules.RequireApproval:
		if b.approver == nil {
			rec.Error = "no approver configured"
			_ = b.auditor.Record(ctx, rec)
			return nil, fmt.Errorf("approval required but no approver configured for: %s", tool)
		}

		approved, err := b.approver.Review(ctx, tool, args)
		rec.Approved = &approved
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

	case rules.Allow:
		// proceed
	}

	// 2. Proxy to backend
	result, err := b.servers.Call(ctx, tool, args)
	if err != nil {
		rec.Error = err.Error()
		_ = b.auditor.Record(ctx, rec)
		return nil, fmt.Errorf("backend error for %s: %w", tool, err)
	}

	if result.IsError {
		rec.Error = fmt.Sprintf("%v", result.Content)
		rec.Result = result.Content
	} else {
		rec.Result = result.Content
	}

	// 3. Audit
	_ = b.auditor.Record(ctx, rec)

	if b.logger != nil {
		b.logger.Info("tool call handled", "tool", tool, "verdict", verdict)
	}

	return result.Content, nil
}

// Tools returns all discovered tools (delegates to server manager).
func (b *Broker) Tools() []server.Tool {
	return b.servers.Tools()
}
```

**Step 4: Run tests**

Run: `cd mcp-broker && go test ./internal/broker/ -v -race`
Expected: All PASS.

**Step 5: Commit**

```bash
git add mcp-broker/internal/broker/
git commit -m "feat: add broker with rules, approval, proxy, and audit pipeline"
```

---

### Task 10: Wire serve command

Connect all packages together in the serve command. Start the MCP frontend server and the dashboard on a single port.

**Files:**
- Modify: `mcp-broker/cmd/mcp-broker/serve.go`

**Reference:** `~/Workspace/brocade/cmd/brocade/serve.go` for the wiring pattern, `~/Workspace/brocade/internal/transporter/mcp/mcp.go` for MCP server setup.

**Step 1: Rewrite serve.go**

```go
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/broker"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/dashboard"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/rules"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/server"
)

func init() {
	rootCmd.AddCommand(serveCmd)
	serveCmd.Flags().String("log-level", "", "log level override (debug, info, warn, error)")
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the MCP broker",
	RunE:  runServe,
}

func parseLogLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func runServe(cmd *cobra.Command, _ []string) error {
	cfg, err := config.Load(configPath())
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	if v, _ := cmd.Flags().GetString("log-level"); v != "" {
		cfg.Log.Level = v
	}

	level := parseLogLevel(cfg.Log.Level)
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	logger := slog.New(handler)
	logger.Info("config loaded", "path", configPath())

	// Create audit logger
	auditor, err := audit.NewLogger(cfg.Audit.Path)
	if err != nil {
		return fmt.Errorf("creating audit logger: %w", err)
	}
	defer func() { _ = auditor.Close(context.Background()) }()

	// Connect to backend servers
	ctx := context.Background()
	mgr, err := server.NewManager(ctx, cfg.Servers, logger.With("component", "server"))
	if err != nil {
		return fmt.Errorf("creating server manager: %w", err)
	}
	defer func() { _ = mgr.Close() }()

	tools := mgr.Tools()
	logger.Info("tools discovered", "count", len(tools))

	// Create rules engine
	engine := rules.New(cfg.Rules)

	// Create dashboard
	dash := dashboard.New(mgr, auditor, logger.With("component", "dashboard"))

	// Create broker
	b := broker.New(mgr, engine, auditor, dash, logger.With("component", "broker"))

	// Create MCP server
	mcpSrv := mcpserver.NewMCPServer("mcp-broker", "0.1.0")
	for _, tool := range tools {
		mcpTool := toolToMCPTool(tool)
		mcpSrv.AddTool(mcpTool, makeMCPHandler(b))
	}

	// Create combined HTTP server
	mux := http.NewServeMux()

	// Mount MCP at /mcp
	streamHandler := mcpserver.NewStreamableHTTPServer(mcpSrv)
	mux.Handle("/mcp", streamHandler)

	// Mount dashboard at / (everything else)
	dashHandler := dash.Handler()
	mux.Handle("/", dashHandler)

	addr := fmt.Sprintf(":%d", cfg.Port)
	srv := &http.Server{Addr: addr, Handler: mux}

	// Handle shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-stop:
		logger.Info("shutting down, send again to force exit")
		go func() {
			<-stop
			logger.Warn("forced shutdown")
			os.Exit(1)
		}()
		return srv.Shutdown(context.Background())
	case err := <-errCh:
		if err != http.ErrServerClosed {
			return fmt.Errorf("server error: %w", err)
		}
		return nil
	}
}

func toolToMCPTool(t server.Tool) gomcp.Tool {
	props := make(map[string]any)
	var required []string

	if t.InputSchema != nil {
		if p, ok := t.InputSchema["properties"].(map[string]any); ok {
			props = p
		}
		if r, ok := t.InputSchema["required"].([]string); ok {
			required = r
		}
		// Handle []any from JSON unmarshaling
		if r, ok := t.InputSchema["required"].([]any); ok {
			for _, v := range r {
				if s, ok := v.(string); ok {
					required = append(required, s)
				}
			}
		}
	}

	return gomcp.Tool{
		Name:        t.Name,
		Description: t.Description,
		InputSchema: gomcp.ToolInputSchema{
			Type:       "object",
			Properties: props,
			Required:   required,
		},
	}
}

func makeMCPHandler(b *broker.Broker) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		args, _ := req.Params.Arguments.(map[string]any)
		if args == nil {
			args = make(map[string]any)
		}

		result, err := b.Handle(ctx, req.Params.Name, args)
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		// Wrap slice results for MCP compliance
		if _, ok := result.([]any); ok {
			result = map[string]any{"items": result}
		}

		toolResult, err := gomcp.NewToolResultJSON(result)
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		return toolResult, nil
	}
}
```

**Step 2: Verify build**

Run: `cd mcp-broker && make build`
Expected: Compiles successfully.

**Step 3: Smoke test**

Run: `/tmp/bin/mcp-broker serve`
Expected: Logs "config loaded", "tools discovered" (count 0 with empty server list), "listening" on port 8200. Dashboard should be accessible at `http://localhost:8200/`.

**Step 4: Commit**

```bash
git add mcp-broker/cmd/mcp-broker/serve.go
git commit -m "feat: wire serve command with full pipeline"
```

---

### Task 11: Install tool dependencies and run full audit

Add golangci-lint, goimports, and govulncheck as tool dependencies and run the full audit suite.

**Files:**
- Modify: `mcp-broker/go.mod` (tool directives)

**Step 1: Add tool dependencies**

```bash
cd mcp-broker
go get -tool github.com/golangci/golangci-lint/v2/cmd/golangci-lint
go get -tool golang.org/x/tools/cmd/goimports
go get -tool golang.org/x/vuln/cmd/govulncheck
```

**Step 2: Run the full audit**

Run: `cd mcp-broker && make audit`
Expected: All steps pass — tidy, fmt, lint, test, govulncheck.

**Step 3: Fix any lint issues**

If golangci-lint reports issues, fix them. Common issues to expect:
- Unused variables or imports
- Error return values not checked
- `gosec` warnings

**Step 4: Commit**

```bash
git add mcp-broker/
git commit -m "chore: add tool dependencies, pass full audit"
```

---

### Task 12: Integration test with a real MCP server

Test the full pipeline end-to-end using the echo MCP server (or a simple test server).

**Files:**
- Create: `mcp-broker/internal/broker/integration_test.go`

**Step 1: Write the integration test**

This test creates a real in-memory audit logger, real rules engine, and mock server manager to verify the full pipeline.

```go
//go:build integration

package broker

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/rules"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/server"
)

func TestBroker_Integration_FullPipeline(t *testing.T) {
	// Real audit logger
	dbPath := filepath.Join(t.TempDir(), "test-audit.db")
	auditor, err := audit.NewLogger(dbPath)
	require.NoError(t, err)
	defer auditor.Close(context.Background())

	// Real rules engine
	engine := rules.New([]config.RuleConfig{
		{Tool: "echo.*", Verdict: "allow"},
		{Tool: "*", Verdict: "deny"},
	})

	// Mock server manager
	sm := new(mockServerManager)
	sm.On("Call", mock.Anything, "echo.ping", map[string]any{"message": "hello"}).
		Return(&server.ToolResult{Content: map[string]any{"response": "hello"}}, nil)
	sm.On("Tools").Return([]server.Tool{
		{Name: "echo.ping", Description: "Echo a message"},
	})

	b := New(sm, engine, auditor, nil, nil)

	// Allowed call
	result, err := b.Handle(context.Background(), "echo.ping", map[string]any{"message": "hello"})
	require.NoError(t, err)
	require.NotNil(t, result)

	// Denied call
	_, err = b.Handle(context.Background(), "fs.delete", nil)
	require.Error(t, err)

	// Verify audit records
	records, total, err := auditor.Query(context.Background(), audit.QueryOpts{Limit: 10})
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, records, 2)

	// Most recent first (denied)
	require.Equal(t, "deny", records[0].Verdict)
	require.Equal(t, "allow", records[1].Verdict)
}
```

Note: The test needs the `mock` import — add `"github.com/stretchr/testify/mock"` to imports, and reuse the `mockServerManager` from the unit test file. Either move it to a shared test helper or duplicate it with the build tag.

**Step 2: Run the integration test**

Run: `cd mcp-broker && go test -tags=integration ./internal/broker/ -v -race`
Expected: PASS.

**Step 3: Commit**

```bash
git add mcp-broker/internal/broker/integration_test.go
git commit -m "test: add integration test for full broker pipeline"
```

---

### Task 13: Documentation update

Update the project README to document what mcp-broker is and how to use it.

**Files:**
- Modify: `README.md` (repo root — add mcp-broker section)

**Step 1: Update README.md**

Add a section for mcp-broker under the existing content. The README currently says "My tools for working with AI coding agents." Add:

```markdown
## mcp-broker

A generic MCP proxy that connects to backend MCP servers and exposes them through a single endpoint with policy rules, human approval, and audit logging.

See [`mcp-broker/`](mcp-broker/) for details.
```

**Step 2: Commit**

```bash
git add README.md
git commit -m "docs: add mcp-broker to repo README"
```

---

### Task 14: Final audit and cleanup

Run the complete audit, fix any remaining issues, and ensure everything is clean.

**Step 1: Run full audit**

Run: `cd mcp-broker && make audit`
Expected: All steps pass.

**Step 2: Verify build from clean state**

Run:
```bash
cd mcp-broker
rm -rf /tmp/bin/mcp-broker
make build
/tmp/bin/mcp-broker --help
/tmp/bin/mcp-broker serve &
sleep 2
curl -s http://localhost:8200/ | head -5
kill %1
```
Expected: Help output shows commands, server starts, dashboard HTML is served.

**Step 3: Commit any final fixes**

```bash
git add mcp-broker/
git commit -m "chore: final cleanup and audit pass"
```
