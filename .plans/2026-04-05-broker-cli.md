# broker-cli Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use Skill(executing-plans) to implement this plan task-by-task.

**Goal:** Build a dynamic CLI frontend for the MCP broker that discovers tools at runtime and generates cobra subcommands from their JSON Schema definitions.

**Architecture:** A new Go module `broker-cli/` connects to the broker's streamable HTTP endpoint, calls `tools/list` on startup, and builds a cobra command tree before `Execute()`. Tool names like `git.push` become `broker-cli git push`; underscores normalize to hyphens. Output is a JSON array on stdout; errors are JSON on stderr.

**Tech Stack:** Go, `github.com/spf13/cobra`, `github.com/mark3labs/mcp-go` (v0.45.0), `github.com/stretchr/testify`, `encoding/json`, `os`, `time`

---

## Task 1: Module scaffold

**Files:**
- Create: `broker-cli/cmd/broker-cli/main.go`
- Create: `broker-cli/cmd/broker-cli/root.go`
- Create: `broker-cli/Makefile`
- Create: `broker-cli/go.mod`
- Modify: `go.work` (add `./broker-cli`)
- Modify: `Makefile` (add `broker-cli` to TOOLS list)

**Step 1: Create the directory structure**

```bash
mkdir -p broker-cli/cmd/broker-cli broker-cli/internal
```

**Step 2: Write `broker-cli/go.mod`**

```
module github.com/averycrespi/agent-tools/broker-cli

go 1.25.7

require (
	github.com/mark3labs/mcp-go v0.45.0
	github.com/spf13/cobra v1.10.2
	github.com/stretchr/testify v1.11.1
)
```

Run `go mod tidy` inside `broker-cli/` to populate indirect deps. Copy the indirect block from `local-gh-mcp/go.mod` as a starting point if needed.

**Step 3: Write `broker-cli/Makefile`**

```makefile
.PHONY: build install test lint fmt tidy audit

build:
	go build -o broker-cli ./cmd/broker-cli

install:
	GOBIN=$(shell go env GOPATH)/bin go install ./cmd/broker-cli

test:
	go test -race ./...

test-integration:
	go test -race -tags=integration ./...

lint:
	go tool golangci-lint run ./...

fmt:
	go tool goimports -w .

tidy:
	go mod tidy && go mod verify

audit: tidy fmt lint test
	go tool govulncheck ./...
```

**Step 4: Write `broker-cli/cmd/broker-cli/main.go`**

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

**Step 5: Write `broker-cli/cmd/broker-cli/root.go` skeleton**

```go
package main

import (
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:          "broker-cli",
	Short:        "CLI frontend for the MCP broker",
	SilenceUsage: true,
	Long: `broker-cli connects to an MCP broker and exposes its tools as CLI subcommands.

Environment:
  MCP_BROKER_ENDPOINT    Broker URL (required)
  MCP_BROKER_AUTH_TOKEN  Bearer token (required)`,
}
```

**Step 6: Add `./broker-cli` to `go.work`**

```
use (
	./broker-cli
	./local-git-mcp
	./local-gh-mcp
	./mcp-broker
	./sandbox-manager
	./worktree-manager
)
```

**Step 7: Add `broker-cli` to root `Makefile`**

```makefile
TOOLS := worktree-manager mcp-broker sandbox-manager local-git-mcp local-gh-mcp broker-cli
```

**Step 8: Verify it builds**

```bash
cd broker-cli && go build ./...
```

Expected: no output, no errors.

**Step 9: Commit**

```bash
git add broker-cli/ go.work Makefile
git commit -m "feat(broker-cli): scaffold module"
```

---

## Task 2: Client package

The client package wraps `mcp-go`'s streamable HTTP client with bearer token auth. It exposes a `Client` interface so the rest of the code can test with a mock.

**Files:**
- Create: `broker-cli/internal/client/client.go`
- Create: `broker-cli/internal/client/client_test.go`

**Step 1: Write `broker-cli/internal/client/client.go`**

```go
package client

import (
	"context"
	"fmt"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// Tool is a discovered MCP tool with its schema.
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
}

// ContentBlock is a single element in a tool result.
type ContentBlock struct {
	Type string
	Text string
}

// ToolResult holds the output of a tool call.
type ToolResult struct {
	Content []ContentBlock
	IsError bool
}

// Client can discover and call tools on the MCP broker.
type Client interface {
	ListTools(ctx context.Context) ([]Tool, error)
	CallTool(ctx context.Context, name string, args map[string]any) (*ToolResult, error)
	Close() error
}

type mcpClientImpl struct {
	c *mcpclient.Client
}

// New connects to the broker at endpoint (e.g. "http://localhost:8200/mcp")
// and authenticates with token.
func New(ctx context.Context, endpoint, token string) (Client, error) {
	opts := []transport.StreamableHTTPCOption{
		transport.WithHTTPHeaders(map[string]string{
			"Authorization": "Bearer " + token,
		}),
	}

	c, err := mcpclient.NewStreamableHttpClient(endpoint, opts...)
	if err != nil {
		return nil, fmt.Errorf("create MCP client: %w", err)
	}

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "broker-cli",
		Version: "0.1.0",
	}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("initialize MCP client: %w", err)
	}

	return &mcpClientImpl{c: c}, nil
}

func (m *mcpClientImpl) ListTools(ctx context.Context) ([]Tool, error) {
	resp, err := m.c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
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

func (m *mcpClientImpl) CallTool(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args

	resp, err := m.c.CallTool(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("call tool %q: %w", name, err)
	}

	result := &ToolResult{IsError: resp.IsError}
	for _, block := range resp.Content {
		switch v := block.(type) {
		case mcp.TextContent:
			result.Content = append(result.Content, ContentBlock{Type: "text", Text: v.Text})
		}
	}
	return result, nil
}

func (m *mcpClientImpl) Close() error {
	return m.c.Close()
}
```

**Step 2: Write `broker-cli/internal/client/client_test.go`**

These are integration tests requiring a running broker. They are tagged `integration` and skipped in normal `make test` runs.

```go
//go:build integration

package client_test

import (
	"context"
	"os"
	"testing"

	"github.com/averycrespi/agent-tools/broker-cli/internal/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func brokerClient(t *testing.T) client.Client {
	t.Helper()
	endpoint := os.Getenv("MCP_BROKER_ENDPOINT")
	token := os.Getenv("MCP_BROKER_AUTH_TOKEN")
	if endpoint == "" || token == "" {
		t.Skip("MCP_BROKER_ENDPOINT and MCP_BROKER_AUTH_TOKEN required")
	}
	c, err := client.New(context.Background(), endpoint+"/mcp", token)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestListTools_returnsTools(t *testing.T) {
	c := brokerClient(t)
	tools, err := c.ListTools(context.Background())
	require.NoError(t, err)
	assert.NotEmpty(t, tools)
}

func TestCallTool_unknownTool(t *testing.T) {
	c := brokerClient(t)
	_, err := c.CallTool(context.Background(), "no.such.tool", nil)
	assert.Error(t, err)
}
```

**Step 3: Run unit tests (integration tests are skipped)**

```bash
cd broker-cli && go test -race ./internal/client/...
```

Expected: `ok` with no tests run (integration tests skipped without tag).

**Step 4: Commit**

```bash
git add broker-cli/internal/client/
git commit -m "feat(broker-cli): add MCP client package"
```

---

## Task 3: Cache package

Caches the tool list to a temp file, keyed by a hash of the broker endpoint, with a 30-second TTL.

**Files:**
- Create: `broker-cli/internal/cache/cache.go`
- Create: `broker-cli/internal/cache/cache_test.go`

**Step 1: Write the failing tests**

```go
// broker-cli/internal/cache/cache_test.go
package cache_test

import (
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/broker-cli/internal/cache"
	"github.com/averycrespi/agent-tools/broker-cli/internal/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func tools() []client.Tool {
	return []client.Tool{
		{Name: "git.push", Description: "Push commits", InputSchema: map[string]any{}},
	}
}

func TestCache_missOnEmpty(t *testing.T) {
	c := cache.New(30 * time.Second)
	got, ok := c.Get("http://localhost:8200")
	assert.False(t, ok)
	assert.Nil(t, got)
}

func TestCache_hitAfterSet(t *testing.T) {
	c := cache.New(30 * time.Second)
	require.NoError(t, c.Set("http://localhost:8200", tools()))
	got, ok := c.Get("http://localhost:8200")
	assert.True(t, ok)
	assert.Equal(t, tools(), got)
}

func TestCache_missAfterExpiry(t *testing.T) {
	c := cache.New(10 * time.Millisecond)
	require.NoError(t, c.Set("http://localhost:8200", tools()))
	time.Sleep(20 * time.Millisecond)
	_, ok := c.Get("http://localhost:8200")
	assert.False(t, ok)
}

func TestCache_differentKeys(t *testing.T) {
	c := cache.New(30 * time.Second)
	require.NoError(t, c.Set("http://localhost:8200", tools()))
	_, ok := c.Get("http://localhost:9999")
	assert.False(t, ok)
}
```

**Step 2: Run tests to verify they fail**

```bash
cd broker-cli && go test ./internal/cache/... 2>&1 | head -5
```

Expected: FAIL — package not found.

**Step 3: Write `broker-cli/internal/cache/cache.go`**

```go
package cache

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/averycrespi/agent-tools/broker-cli/internal/client"
)

// Cache stores the tool list in a temp file with a TTL.
type Cache struct {
	ttl time.Duration
}

// New creates a Cache with the given TTL.
func New(ttl time.Duration) *Cache {
	return &Cache{ttl: ttl}
}

type entry struct {
	Tools     []client.Tool `json:"tools"`
	ExpiresAt time.Time     `json:"expires_at"`
}

// Get returns cached tools for the given endpoint, if still valid.
func (c *Cache) Get(endpoint string) ([]client.Tool, bool) {
	data, err := os.ReadFile(c.path(endpoint))
	if err != nil {
		return nil, false
	}
	var e entry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, false
	}
	if time.Now().After(e.ExpiresAt) {
		return nil, false
	}
	return e.Tools, true
}

// Set writes the tool list to the cache for the given endpoint.
func (c *Cache) Set(endpoint string, tools []client.Tool) error {
	e := entry{Tools: tools, ExpiresAt: time.Now().Add(c.ttl)}
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal cache: %w", err)
	}
	if err := os.WriteFile(c.path(endpoint), data, 0o600); err != nil {
		return fmt.Errorf("write cache: %w", err)
	}
	return nil
}

func (c *Cache) path(endpoint string) string {
	h := sha256.Sum256([]byte(endpoint))
	name := fmt.Sprintf("broker-cli-tools-%x.json", h[:8])
	return filepath.Join(os.TempDir(), name)
}
```

**Step 4: Run tests to verify they pass**

```bash
cd broker-cli && go test -race ./internal/cache/...
```

Expected: PASS.

**Step 5: Commit**

```bash
git add broker-cli/internal/cache/
git commit -m "feat(broker-cli): add tool list cache"
```

---

## Task 4: Output package

Formats MCP content blocks as a JSON array. Each text block is parsed as JSON if possible; otherwise included as a string.

**Files:**
- Create: `broker-cli/internal/output/output.go`
- Create: `broker-cli/internal/output/output_test.go`

**Step 1: Write the failing tests**

```go
// broker-cli/internal/output/output_test.go
package output_test

import (
	"testing"

	"github.com/averycrespi/agent-tools/broker-cli/internal/client"
	"github.com/averycrespi/agent-tools/broker-cli/internal/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormat_jsonObject(t *testing.T) {
	result := &client.ToolResult{
		Content: []client.ContentBlock{{Type: "text", Text: `{"pushed": true, "commits": 3}`}},
	}
	got, err := output.Format(result)
	require.NoError(t, err)
	assert.JSONEq(t, `[{"pushed": true, "commits": 3}]`, got)
}

func TestFormat_plainText(t *testing.T) {
	result := &client.ToolResult{
		Content: []client.ContentBlock{{Type: "text", Text: "Successfully deleted branch"}},
	}
	got, err := output.Format(result)
	require.NoError(t, err)
	assert.JSONEq(t, `["Successfully deleted branch"]`, got)
}

func TestFormat_multipleBlocks(t *testing.T) {
	result := &client.ToolResult{
		Content: []client.ContentBlock{
			{Type: "text", Text: `{"pr": 42}`},
			{Type: "text", Text: `{"checks": "passing"}`},
		},
	}
	got, err := output.Format(result)
	require.NoError(t, err)
	assert.JSONEq(t, `[{"pr": 42}, {"checks": "passing"}]`, got)
}

func TestFormat_emptyContent(t *testing.T) {
	result := &client.ToolResult{Content: nil}
	got, err := output.Format(result)
	require.NoError(t, err)
	assert.JSONEq(t, `[]`, got)
}

func TestFormat_nonTextBlocksIgnored(t *testing.T) {
	// Non-text content blocks (type != "text") are skipped.
	result := &client.ToolResult{
		Content: []client.ContentBlock{
			{Type: "image", Text: ""},
			{Type: "text", Text: "hello"},
		},
	}
	got, err := output.Format(result)
	require.NoError(t, err)
	assert.JSONEq(t, `["hello"]`, got)
}
```

**Step 2: Run tests to verify they fail**

```bash
cd broker-cli && go test ./internal/output/... 2>&1 | head -5
```

Expected: FAIL — package not found.

**Step 3: Write `broker-cli/internal/output/output.go`**

```go
package output

import (
	"encoding/json"
	"fmt"

	"github.com/averycrespi/agent-tools/broker-cli/internal/client"
)

// Format converts a ToolResult's content blocks into a JSON array.
// Each text block is parsed as JSON if valid; otherwise included as a string.
// Non-text blocks are ignored.
func Format(result *client.ToolResult) (string, error) {
	items := make([]any, 0, len(result.Content))
	for _, block := range result.Content {
		if block.Type != "text" {
			continue
		}
		var parsed any
		if err := json.Unmarshal([]byte(block.Text), &parsed); err == nil {
			items = append(items, parsed)
		} else {
			items = append(items, block.Text)
		}
	}
	data, err := json.Marshal(items)
	if err != nil {
		return "", fmt.Errorf("marshal output: %w", err)
	}
	return string(data), nil
}
```

**Step 4: Run tests to verify they pass**

```bash
cd broker-cli && go test -race ./internal/output/...
```

Expected: PASS.

**Step 5: Commit**

```bash
git add broker-cli/internal/output/
git commit -m "feat(broker-cli): add output formatter"
```

---

## Task 5: Flags package

Maps JSON Schema properties to cobra flags and builds a `map[string]any` to send to the broker.

**Files:**
- Create: `broker-cli/internal/flags/flags.go`
- Create: `broker-cli/internal/flags/flags_test.go`

**Step 1: Write the failing tests**

```go
// broker-cli/internal/flags/flags_test.go
package flags_test

import (
	"testing"

	"github.com/averycrespi/agent-tools/broker-cli/internal/flags"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeCmd(schema map[string]any) *cobra.Command {
	cmd := &cobra.Command{Use: "test"}
	flags.AddSchemaFlags(cmd, schema)
	return cmd
}

func parse(cmd *cobra.Command, args ...string) error {
	return cmd.ParseFlags(args)
}

func TestStringFlag_set(t *testing.T) {
	schema := map[string]any{
		"properties": map[string]any{
			"remote": map[string]any{"type": "string", "description": "Remote name"},
		},
	}
	cmd := makeCmd(schema)
	require.NoError(t, parse(cmd, "--remote", "origin"))
	args, err := flags.BuildArgs(cmd, schema)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"remote": "origin"}, args)
}

func TestBoolFlag_set(t *testing.T) {
	schema := map[string]any{
		"properties": map[string]any{
			"force": map[string]any{"type": "boolean", "description": "Force push"},
		},
	}
	cmd := makeCmd(schema)
	require.NoError(t, parse(cmd, "--force"))
	args, err := flags.BuildArgs(cmd, schema)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"force": true}, args)
}

func TestIntFlag_set(t *testing.T) {
	schema := map[string]any{
		"properties": map[string]any{
			"limit": map[string]any{"type": "integer", "description": "Limit"},
		},
	}
	cmd := makeCmd(schema)
	require.NoError(t, parse(cmd, "--limit", "10"))
	args, err := flags.BuildArgs(cmd, schema)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"limit": int64(10)}, args)
}

func TestRequiredValidation_missing(t *testing.T) {
	schema := map[string]any{
		"properties": map[string]any{
			"remote": map[string]any{"type": "string"},
		},
		"required": []any{"remote"},
	}
	cmd := makeCmd(schema)
	require.NoError(t, parse(cmd))
	_, err := flags.BuildArgs(cmd, schema)
	assert.ErrorContains(t, err, "missing required flag: --remote")
}

func TestParamFlag_overridesField(t *testing.T) {
	schema := map[string]any{
		"properties": map[string]any{
			"items": map[string]any{"type": "array"},
		},
	}
	cmd := makeCmd(schema)
	require.NoError(t, parse(cmd, "--param", `items=["a","b"]`))
	args, err := flags.BuildArgs(cmd, schema)
	require.NoError(t, err)
	assert.Equal(t, []any{"a", "b"}, args["items"])
}

func TestRawInput_bypasses(t *testing.T) {
	schema := map[string]any{
		"properties": map[string]any{
			"remote": map[string]any{"type": "string"},
		},
		"required": []any{"remote"},
	}
	cmd := makeCmd(schema)
	// raw-input bypasses required validation entirely
	require.NoError(t, parse(cmd, "--raw-input", `{"remote":"origin"}`))
	args, err := flags.BuildArgs(cmd, schema)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"remote": "origin"}, args)
}

func TestUnsetOptional_omitted(t *testing.T) {
	// Optional string flags that are not set should not appear in args.
	schema := map[string]any{
		"properties": map[string]any{
			"branch": map[string]any{"type": "string"},
		},
	}
	cmd := makeCmd(schema)
	require.NoError(t, parse(cmd))
	args, err := flags.BuildArgs(cmd, schema)
	require.NoError(t, err)
	_, exists := args["branch"]
	assert.False(t, exists)
}
```

**Step 2: Run tests to verify they fail**

```bash
cd broker-cli && go test ./internal/flags/... 2>&1 | head -5
```

Expected: FAIL — package not found.

**Step 3: Write `broker-cli/internal/flags/flags.go`**

```go
package flags

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

const paramFlag = "param"
const rawInputFlag = "raw-input"

// AddSchemaFlags adds cobra flags derived from a JSON Schema to cmd.
// Also adds --param and --raw-input for complex types.
func AddSchemaFlags(cmd *cobra.Command, schema map[string]any) {
	props, _ := schema["properties"].(map[string]any)
	for name, def := range props {
		d, _ := def.(map[string]any)
		typ, _ := d["type"].(string)
		desc, _ := d["description"].(string)
		switch typ {
		case "string":
			cmd.Flags().String(name, "", desc)
		case "boolean":
			cmd.Flags().Bool(name, false, desc)
		case "integer", "number":
			cmd.Flags().Int64(name, 0, desc)
		// object/array/unknown: handled via --param
		}
	}
	cmd.Flags().StringArray(paramFlag, nil, "Set a field as raw JSON: --param 'key=value'")
	cmd.Flags().String(rawInputFlag, "", "Pass entire input as a JSON object, bypassing flags")
}

// BuildArgs reads flag values from cmd and returns a map[string]any for the broker.
// --raw-input takes precedence over all other flags.
// --param overrides individual fields.
// Required fields are validated unless --raw-input is used.
func BuildArgs(cmd *cobra.Command, schema map[string]any) (map[string]any, error) {
	// --raw-input bypasses everything
	if raw, err := cmd.Flags().GetString(rawInputFlag); err == nil && raw != "" {
		var args map[string]any
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			return nil, fmt.Errorf("invalid --raw-input JSON: %w", err)
		}
		return args, nil
	}

	props, _ := schema["properties"].(map[string]any)
	args := make(map[string]any)

	for name, def := range props {
		d, _ := def.(map[string]any)
		typ, _ := d["type"].(string)
		f := cmd.Flags().Lookup(name)
		if f == nil || !f.Changed {
			continue
		}
		switch typ {
		case "string":
			v, _ := cmd.Flags().GetString(name)
			args[name] = v
		case "boolean":
			v, _ := cmd.Flags().GetBool(name)
			args[name] = v
		case "integer", "number":
			v, _ := cmd.Flags().GetInt64(name)
			args[name] = v
		}
	}

	// Apply --param overrides
	params, _ := cmd.Flags().GetStringArray(paramFlag)
	for _, p := range params {
		eq := strings.IndexByte(p, '=')
		if eq < 0 {
			return nil, fmt.Errorf("invalid --param %q: expected key=value", p)
		}
		key := p[:eq]
		val := p[eq+1:]
		var parsed any
		if err := json.Unmarshal([]byte(val), &parsed); err != nil {
			return nil, fmt.Errorf("invalid JSON in --param %q: %w", key, err)
		}
		args[key] = parsed
	}

	// Validate required fields
	required, _ := schema["required"].([]any)
	for _, r := range required {
		name, _ := r.(string)
		if _, ok := args[name]; !ok {
			return nil, fmt.Errorf("missing required flag: --%s", name)
		}
	}

	return args, nil
}
```

**Step 4: Run tests to verify they pass**

```bash
cd broker-cli && go test -race ./internal/flags/...
```

Expected: PASS.

**Step 5: Commit**

```bash
git add broker-cli/internal/flags/
git commit -m "feat(broker-cli): add JSON Schema to flag mapper"
```

---

## Task 6: Tree package

Builds a cobra command tree from a list of discovered tools. Handles dot-separated namespacing and kebab-case normalization.

**Files:**
- Create: `broker-cli/internal/tree/tree.go`
- Create: `broker-cli/internal/tree/tree_test.go`

**Step 1: Write the failing tests**

```go
// broker-cli/internal/tree/tree_test.go
package tree_test

import (
	"testing"

	"github.com/averycrespi/agent-tools/broker-cli/internal/client"
	"github.com/averycrespi/agent-tools/broker-cli/internal/tree"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func findCommand(root *cobra.Command, path ...string) *cobra.Command {
	cmd := root
	for _, name := range path {
		sub, _, err := cmd.Find([]string{name})
		if err != nil || sub == cmd {
			return nil
		}
		cmd = sub
	}
	return cmd
}

func TestBuild_singleTool(t *testing.T) {
	root := &cobra.Command{Use: "broker-cli"}
	tools := []client.Tool{
		{Name: "git.push", Description: "Push commits"},
	}
	tree.Build(root, tools, nil)

	ns := findCommand(root, "git")
	require.NotNil(t, ns, "expected 'git' namespace command")

	cmd := findCommand(root, "git", "push")
	require.NotNil(t, cmd, "expected 'git push' command")
	assert.Equal(t, "Push commits", cmd.Short)
}

func TestBuild_underscoreNormalized(t *testing.T) {
	root := &cobra.Command{Use: "broker-cli"}
	tools := []client.Tool{
		{Name: "github.list_prs", Description: "List PRs"},
	}
	tree.Build(root, tools, nil)

	cmd := findCommand(root, "github", "list-prs")
	require.NotNil(t, cmd, "expected 'github list-prs' command (kebab-case)")
}

func TestBuild_multipleNamespaces(t *testing.T) {
	root := &cobra.Command{Use: "broker-cli"}
	tools := []client.Tool{
		{Name: "git.push", Description: "Push"},
		{Name: "git.pull", Description: "Pull"},
		{Name: "github.list_prs", Description: "List PRs"},
	}
	tree.Build(root, tools, nil)

	assert.NotNil(t, findCommand(root, "git", "push"))
	assert.NotNil(t, findCommand(root, "git", "pull"))
	assert.NotNil(t, findCommand(root, "github", "list-prs"))
}

func TestBuild_namespaceHelp_listsTools(t *testing.T) {
	root := &cobra.Command{Use: "broker-cli"}
	tools := []client.Tool{
		{Name: "git.push", Description: "Push commits"},
		{Name: "git.pull", Description: "Pull changes"},
	}
	tree.Build(root, tools, nil)

	ns := findCommand(root, "git")
	require.NotNil(t, ns)
	cmds := ns.Commands()
	names := make([]string, len(cmds))
	for i, c := range cmds {
		names[i] = c.Name()
	}
	assert.Contains(t, names, "push")
	assert.Contains(t, names, "pull")
}

func TestBuild_execFnCalled(t *testing.T) {
	root := &cobra.Command{Use: "broker-cli"}
	var calledWith struct{ tool string; args map[string]any }
	exec := func(tool string, args map[string]any) error {
		calledWith.tool = tool
		calledWith.args = args
		return nil
	}

	tools := []client.Tool{
		{Name: "git.push", Description: "Push", InputSchema: map[string]any{}},
	}
	tree.Build(root, tools, exec)

	cmd := findCommand(root, "git", "push")
	require.NotNil(t, cmd)
	require.NoError(t, cmd.RunE(cmd, nil))
	assert.Equal(t, "git.push", calledWith.tool)
}
```

**Step 2: Run tests to verify they fail**

```bash
cd broker-cli && go test ./internal/tree/... 2>&1 | head -5
```

Expected: FAIL — package not found.

**Step 3: Write `broker-cli/internal/tree/tree.go`**

```go
package tree

import (
	"strings"

	"github.com/averycrespi/agent-tools/broker-cli/internal/client"
	"github.com/averycrespi/agent-tools/broker-cli/internal/flags"
	"github.com/spf13/cobra"
)

// ExecFn is called when a tool command runs. It receives the original
// dot-separated tool name (e.g. "git.push") and the parsed arguments.
type ExecFn func(tool string, args map[string]any) error

// Build populates root with a cobra command tree derived from tools.
// Tool names like "git.push" become "root git push".
// Underscores in tool names are normalized to hyphens.
func Build(root *cobra.Command, tools []client.Tool, exec ExecFn) {
	namespaces := make(map[string]*cobra.Command)

	for _, tool := range tools {
		parts := strings.SplitN(tool.Name, ".", 2)
		if len(parts) != 2 {
			continue
		}
		ns, toolName := parts[0], parts[1]
		cmdName := strings.ReplaceAll(toolName, "_", "-")

		// Get or create namespace command.
		nsCmd, ok := namespaces[ns]
		if !ok {
			nsCmd = &cobra.Command{
				Use:   ns,
				Short: ns,
			}
			namespaces[ns] = nsCmd
			root.AddCommand(nsCmd)
		}

		// Capture loop variables for closure.
		t := tool
		toolCmd := &cobra.Command{
			Use:          cmdName,
			Short:        t.Description,
			SilenceUsage: true,
		}

		flags.AddSchemaFlags(toolCmd, t.InputSchema)

		if exec != nil {
			toolCmd.RunE = func(cmd *cobra.Command, _ []string) error {
				args, err := flags.BuildArgs(cmd, t.InputSchema)
				if err != nil {
					return err
				}
				return exec(t.Name, args)
			}
		}

		nsCmd.AddCommand(toolCmd)
	}
}
```

**Step 4: Run tests to verify they pass**

```bash
cd broker-cli && go test -race ./internal/tree/...
```

Expected: PASS.

**Step 5: Commit**

```bash
git add broker-cli/internal/tree/
git commit -m "feat(broker-cli): add dynamic command tree builder"
```

---

## Task 7: Wire root.go

Connect all packages in root.go: discover tools (with cache), build command tree, execute tool calls with approval wait, handle global flags.

**Files:**
- Modify: `broker-cli/cmd/broker-cli/root.go`

**Step 1: Rewrite `broker-cli/cmd/broker-cli/root.go`**

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/averycrespi/agent-tools/broker-cli/internal/cache"
	"github.com/averycrespi/agent-tools/broker-cli/internal/client"
	"github.com/averycrespi/agent-tools/broker-cli/internal/output"
	"github.com/averycrespi/agent-tools/broker-cli/internal/tree"
	"github.com/spf13/cobra"
)

var (
	noCache bool
	timeout int
)

var rootCmd = &cobra.Command{
	Use:          "broker-cli",
	Short:        "CLI frontend for the MCP broker",
	SilenceUsage: true,
	Long: `broker-cli connects to an MCP broker and exposes its tools as CLI subcommands.

Environment:
  MCP_BROKER_ENDPOINT    Broker URL (required)
  MCP_BROKER_AUTH_TOKEN  Bearer token (required)`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Skip discovery for built-in commands (e.g. completion).
		if cmd.Name() == "completion" || cmd.Parent() == nil {
			return nil
		}
		return nil // discovery happens in init() below
	},
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&noCache, "no-cache", false, "Bypass tool discovery cache")
	rootCmd.PersistentFlags().IntVar(&timeout, "timeout", 0, "Seconds to wait for approval (0 = no timeout)")

	cobra.OnInitialize(func() {
		if err := buildTree(); err != nil {
			writeError(err)
			os.Exit(1)
		}
	})
}

func buildTree() error {
	endpoint := os.Getenv("MCP_BROKER_ENDPOINT")
	token := os.Getenv("MCP_BROKER_AUTH_TOKEN")
	if endpoint == "" {
		return fmt.Errorf("MCP_BROKER_ENDPOINT is not set")
	}
	if token == "" {
		return fmt.Errorf("MCP_BROKER_AUTH_TOKEN is not set")
	}

	toolCache := cache.New(30 * time.Second)
	var tools []client.Tool

	if !noCache {
		if cached, ok := toolCache.Get(endpoint); ok {
			tools = cached
		}
	}

	if tools == nil {
		ctx := context.Background()
		c, err := client.New(ctx, endpoint+"/mcp", token)
		if err != nil {
			return fmt.Errorf("connect to broker: %w", err)
		}
		defer c.Close()

		tools, err = c.ListTools(ctx)
		if err != nil {
			return fmt.Errorf("list tools: %w", err)
		}

		_ = toolCache.Set(endpoint, tools) // cache miss is non-fatal
	}

	exec := func(toolName string, args map[string]any) error {
		return callTool(endpoint, token, toolName, args)
	}

	tree.Build(rootCmd, tools, exec)
	return nil
}

func callTool(endpoint, token, toolName string, args map[string]any) error {
	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		defer cancel()
	}

	c, err := client.New(ctx, endpoint+"/mcp", token)
	if err != nil {
		return fmt.Errorf("connect to broker: %w", err)
	}
	defer c.Close()

	// Print "waiting for approval..." to stderr if the call takes > 1s.
	done := make(chan struct{})
	go func() {
		select {
		case <-time.After(time.Second):
			fmt.Fprintln(os.Stderr, "waiting for approval...")
		case <-done:
		}
	}()

	result, err := c.CallTool(ctx, toolName, args)
	close(done)

	if err != nil {
		return err
	}

	if result.IsError {
		if len(result.Content) > 0 {
			return fmt.Errorf("%s", result.Content[0].Text)
		}
		return fmt.Errorf("tool call failed")
	}

	out, err := output.Format(result)
	if err != nil {
		return fmt.Errorf("format output: %w", err)
	}
	fmt.Println(out)
	return nil
}

// writeError prints a JSON error object to stderr.
func writeError(err error) {
	data, _ := json.Marshal(map[string]string{"error": err.Error()})
	fmt.Fprintln(os.Stderr, string(data))
}
```

**Note on `cobra.OnInitialize`:** This hook fires before command execution but after flag parsing. This means the command tree is populated before the user's subcommand runs. However, cobra's flag parsing uses the registered commands, so there's a chicken-and-egg issue: the subcommand flags (from tool schemas) need to be registered before parsing. 

To handle this correctly, move discovery out of `OnInitialize` and into a `PersistentPreRun` that runs only for tool commands, OR discover tools before calling `Execute()` in `main.go`. The simpler fix is to call `buildTree()` directly in `main.go` before `rootCmd.Execute()`:

**Step 2: Update `broker-cli/cmd/broker-cli/main.go` to call buildTree before Execute**

```go
package main

import (
	"log/slog"
	"os"
)

func main() {
	// Discovery must happen before Execute so that tool subcommand flags
	// are registered before cobra parses the command line.
	if err := buildTree(); err != nil {
		writeError(err)
		os.Exit(1)
	}

	if err := rootCmd.Execute(); err != nil {
		writeError(err)
		os.Exit(1)
	}
}
```

**Step 3: Remove `cobra.OnInitialize` block from root.go**

Remove the `cobra.OnInitialize(...)` call added in step 1 — buildTree is now called from main.go directly.

**Step 4: Build and smoke-test**

```bash
cd broker-cli && go build -o broker-cli ./cmd/broker-cli && ./broker-cli --help
```

Expected: help text printed with usage and environment variable docs. No tool subcommands (no broker running), but no panic.

**Step 5: Verify error output format**

```bash
./broker-cli git push 2>&1 || true
```

Expected: JSON error on stderr like `{"error": "MCP_BROKER_ENDPOINT is not set"}`.

**Step 6: Commit**

```bash
git add broker-cli/cmd/broker-cli/
git commit -m "feat(broker-cli): wire command tree and execution"
```

---

## Task 8: Run audit

**Step 1: Run full audit**

```bash
cd broker-cli && make audit
```

Expected: PASS. Fix any lint or vet issues before continuing.

**Step 2: Commit any fixes**

```bash
git add -p
git commit -m "fix(broker-cli): address audit findings"
```

---

## Task 9: Documentation

**Files:**
- Create: `broker-cli/CLAUDE.md`
- Create: `broker-cli/README.md`
- Modify: `README.md` (add broker-cli to the tool list if present)

**Step 1: Write `broker-cli/CLAUDE.md`**

```markdown
# broker-cli

CLI frontend for the MCP broker. Discovers tools at runtime and exposes them as subcommands.

## Development

```bash
make build              # go build -o broker-cli ./cmd/broker-cli
make install            # go install ./cmd/broker-cli
make test               # go test -race ./...
make test-integration   # go test -race -tags=integration ./...
make lint               # go tool golangci-lint run ./...
make fmt                # go tool goimports -w .
make tidy               # go mod tidy && go mod verify
make audit              # tidy + fmt + lint + test + govulncheck
```

Run `make audit` before committing.

## Architecture

CLI-only binary. Connects to the MCP broker's HTTP endpoint, discovers tools via `tools/list`, and builds a cobra command tree before `Execute()` runs.

```
cmd/broker-cli/      CLI entry point (main.go + root.go)
internal/
  client/            MCP HTTP client with bearer token auth
  cache/             File-based tool list cache (30s TTL, keyed by endpoint hash)
  flags/             JSON Schema → cobra flags mapper
  output/            MCP content blocks → JSON array formatter
  tree/              Dynamic cobra command tree builder
```

## Conventions

- Errors are wrapped with context: `fmt.Errorf("doing X: %w", err)`
- All errors printed to stderr as JSON: `{"error": "..."}`
- Output always printed to stdout as a JSON array
- `cmd/` has no tests (thin wrappers); all internal packages have tests
- `internal/client/` tests are integration-tagged (`//go:build integration`)
- Tool discovery is cached in `$TMPDIR/broker-cli-tools-<hash>.json` (30s TTL)
- Tool names: dots map to command hierarchy, underscores normalize to hyphens
- Approval wait: broker holds the HTTP connection; CLI prints "waiting for approval..." to stderr after 1s via goroutine
- `mcp-go` v0.45.0: `client.NewStreamableHttpClient` + `transport.WithHTTPHeaders` for auth
```

**Step 2: Write `broker-cli/README.md`**

```markdown
# broker-cli

CLI frontend for the MCP broker. Connects to the broker, discovers available tools, and exposes them as subcommands — one per tool, grouped by namespace.

## Usage

```bash
export MCP_BROKER_ENDPOINT=http://localhost:8200
export MCP_BROKER_AUTH_TOKEN=<token>

broker-cli <namespace> <command> [flags]
```

## Examples

```bash
# List available namespaces and commands
broker-cli --help
broker-cli git --help

# Call a tool
broker-cli git push --remote origin --branch main

# Complex inputs via --param or --raw-input
broker-cli github search-code --query "foo" --param 'include_patterns=["*.go"]'
broker-cli github create-pr --raw-input '{"title":"Fix bug","body":"..."}'
```

## Output

All output is a JSON array on stdout. Errors are a JSON object on stderr.

```bash
[{"pushed": true, "commits": 3}]     # stdout on success
{"error": "missing required flag: --branch"}  # stderr on error, exit 1
```

## Flags

| Flag | Description |
|---|---|
| `--no-cache` | Bypass tool discovery cache |
| `--timeout <seconds>` | Seconds to wait for approval (default: no timeout) |
| `--param key=<json>` | Set a field as raw JSON (per tool command) |
| `--raw-input <json>` | Pass entire input as JSON, bypassing flags (per tool command) |

## Environment

| Variable | Description |
|---|---|
| `MCP_BROKER_ENDPOINT` | Broker URL, e.g. `http://localhost:8200` (required) |
| `MCP_BROKER_AUTH_TOKEN` | Bearer token (required) |
```

**Step 3: Check if root README.md has a tool list to update**

```bash
grep -n "local-git-mcp\|local-gh-mcp\|mcp-broker" README.md | head -10
```

If a tool list exists, add `broker-cli` to it in the same format.

**Step 4: Commit**

```bash
git add broker-cli/CLAUDE.md broker-cli/README.md README.md
git commit -m "docs(broker-cli): add CLAUDE.md and README"
```
