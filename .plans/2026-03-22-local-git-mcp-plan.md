# local-git-mcp Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use Skill(executing-plans) to implement this plan task-by-task.

**Goal:** Build a stdio MCP server that executes authenticated git remote operations (push, pull, fetch, list remote refs, list remotes) on behalf of sandboxed agents.

**Architecture:** Minimal stdio MCP server using mcp-go. Shells out to the host's `git` binary via an `exec.Runner` interface for testability. No config, no state, no network listener.

**Tech Stack:** Go 1.25, mcp-go, Cobra, testify, slog

---

### Task 1: Module scaffolding and go.work integration

**Files:**
- Create: `local-git-mcp/go.mod`
- Create: `local-git-mcp/Makefile`
- Create: `local-git-mcp/CLAUDE.md`
- Modify: `go.work`

**Step 1: Create go.mod**

```bash
cd local-git-mcp && go mod init github.com/averycrespi/agent-tools/local-git-mcp
```

**Step 2: Add to go.work**

Add `./local-git-mcp` to the `use` block in `go.work`:

```
go 1.25.7

use (
	./local-git-mcp
	./mcp-broker
	./sandbox-manager
	./worktree-manager
)
```

**Step 3: Create Makefile**

```makefile
.PHONY: build install test lint fmt tidy audit

build:
	go build -o local-git-mcp ./cmd/local-git-mcp

install:
	GOBIN=$(shell go env GOPATH)/bin go install ./cmd/local-git-mcp

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

**Step 4: Create CLAUDE.md**

```markdown
# local-git-mcp

Stdio MCP server for authenticated git remote operations.

## Development

\`\`\`bash
make build              # go build -o local-git-mcp ./cmd/local-git-mcp
make install            # go install ./cmd/local-git-mcp
make test               # go test -race ./...
make test-integration   # go test -race -tags=integration ./...
make lint               # go tool golangci-lint run ./...
make fmt                # go tool goimports -w .
make tidy               # go mod tidy && go mod verify
make audit              # tidy + fmt + lint + test + govulncheck
\`\`\`

Run `make audit` before committing. Integration tests use `//go:build integration`.

## Architecture

Stdio MCP server. No config, no state, no network listener.

Shells out to the host's `git` binary for all operations.

\`\`\`
cmd/local-git-mcp/      CLI entry point (Cobra)
internal/
  exec/                  Runner interface for command execution
  git/                   Git remote operations via exec.Runner
  tools/                 MCP tool definitions and handlers
\`\`\`

## Conventions

- Errors are wrapped with context: `fmt.Errorf("doing X: %w", err)`
- All external commands go through `exec.Runner` interface
- `cmd/` has no tests (thin wrappers); all internal packages have tests
- Command failures use `%s` with trimmed output; Go errors use `%w` for wrapping
- gosec nolint directives on os/exec are intentional for CLI
```

**Step 5: Commit**

```bash
git add go.work local-git-mcp/go.mod local-git-mcp/Makefile local-git-mcp/CLAUDE.md
git commit -m "chore(local-git-mcp): scaffold module and add to workspace"
```

---

### Task 2: exec.Runner interface

**Files:**
- Create: `local-git-mcp/internal/exec/exec.go`
- Create: `local-git-mcp/internal/exec/exec_test.go`

**Step 1: Write the test**

```go
package exec

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOSRunner_ImplementsRunner(t *testing.T) {
	var _ Runner = &OSRunner{}
}

func TestOSRunner_Run(t *testing.T) {
	r := NewOSRunner()
	out, err := r.Run("echo", "hello")
	assert.NoError(t, err)
	assert.Contains(t, string(out), "hello")
}

func TestOSRunner_RunDir(t *testing.T) {
	r := NewOSRunner()
	out, err := r.RunDir("/tmp", "pwd")
	assert.NoError(t, err)
	assert.Contains(t, string(out), "tmp")
}
```

**Step 2: Run test to verify it fails**

Run: `cd local-git-mcp && go test ./internal/exec/...`
Expected: FAIL — types not defined yet.

**Step 3: Write implementation**

```go
package exec

import (
	osexec "os/exec"
)

// Runner abstracts command execution for testability.
type Runner interface {
	// Run executes a command and returns its combined output.
	Run(name string, args ...string) ([]byte, error)
	// RunDir executes a command in a specific directory.
	RunDir(dir, name string, args ...string) ([]byte, error)
}

// OSRunner implements Runner using os/exec.
type OSRunner struct{}

// NewOSRunner returns a Runner that uses real OS commands.
func NewOSRunner() *OSRunner { return &OSRunner{} }

func (r *OSRunner) Run(name string, args ...string) ([]byte, error) {
	return osexec.Command(name, args...).CombinedOutput() //nolint:gosec
}

func (r *OSRunner) RunDir(dir, name string, args ...string) ([]byte, error) {
	cmd := osexec.Command(name, args...) //nolint:gosec
	cmd.Dir = dir
	return cmd.CombinedOutput()
}
```

Note: We don't include `RunInteractive` — local-git-mcp never needs interactive commands.

**Step 4: Run test to verify it passes**

Run: `cd local-git-mcp && go test ./internal/exec/...`
Expected: PASS

**Step 5: Commit**

```bash
git add local-git-mcp/internal/exec/
git commit -m "feat(local-git-mcp): add exec.Runner interface"
```

---

### Task 3: Git client — validation

**Files:**
- Create: `local-git-mcp/internal/git/git.go`
- Create: `local-git-mcp/internal/git/git_test.go`

**Step 1: Write the failing tests for validation**

```go
package git

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockRunner is a test double for exec.Runner.
type mockRunner struct {
	runDirFunc func(dir, name string, args ...string) ([]byte, error)
}

func (m *mockRunner) Run(name string, args ...string) ([]byte, error) {
	return m.RunDir("", name, args...)
}

func (m *mockRunner) RunDir(dir, name string, args ...string) ([]byte, error) {
	if m.runDirFunc != nil {
		return m.runDirFunc(dir, name, args...)
	}
	return nil, nil
}

func TestValidateRepo_RelativePath(t *testing.T) {
	c := NewClient(&mockRunner{})
	err := c.ValidateRepo("relative/path")
	assert.ErrorContains(t, err, "must be an absolute path")
}

func TestValidateRepo_NotAGitRepo(t *testing.T) {
	c := NewClient(&mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			return []byte("fatal: not a git repository"), fmt.Errorf("exit status 128")
		},
	})
	err := c.ValidateRepo("/some/path")
	assert.ErrorContains(t, err, "not a git repository")
}

func TestValidateRepo_Valid(t *testing.T) {
	c := NewClient(&mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			return []byte(".git\n"), nil
		},
	})
	err := c.ValidateRepo("/some/repo")
	require.NoError(t, err)
}
```

**Step 2: Run test to verify it fails**

Run: `cd local-git-mcp && go test ./internal/git/...`
Expected: FAIL — `git` package doesn't exist.

**Step 3: Write implementation**

```go
package git

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/averycrespi/agent-tools/local-git-mcp/internal/exec"
)

// Client wraps git remote operations with an injectable command runner.
type Client struct {
	runner exec.Runner
}

// NewClient returns a git Client using the given command runner.
func NewClient(runner exec.Runner) *Client {
	return &Client{runner: runner}
}

// ValidateRepo checks that the given path is absolute and is a git repository.
func (c *Client) ValidateRepo(repoPath string) error {
	if !filepath.IsAbs(repoPath) {
		return fmt.Errorf("repo_path must be an absolute path: %s", repoPath)
	}
	out, err := c.runner.RunDir(repoPath, "git", "rev-parse", "--git-dir")
	if err != nil {
		return fmt.Errorf("not a git repository: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
```

**Step 4: Run test to verify it passes**

Run: `cd local-git-mcp && go test ./internal/git/...`
Expected: PASS

**Step 5: Commit**

```bash
git add local-git-mcp/internal/git/
git commit -m "feat(local-git-mcp): add git client with repo validation"
```

---

### Task 4: Git client — push, pull, fetch

**Files:**
- Modify: `local-git-mcp/internal/git/git.go`
- Modify: `local-git-mcp/internal/git/git_test.go`

**Step 1: Write the failing tests**

Add to `git_test.go`:

```go
func TestPush_DefaultArgs(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte("Everything up-to-date\n"), nil
		},
	})
	out, err := c.Push("/repo", "origin", "", false)
	require.NoError(t, err)
	assert.Equal(t, "Everything up-to-date", out)
	assert.Equal(t, []string{"push", "origin"}, capturedArgs)
}

func TestPush_WithRefspec(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return nil, nil
		},
	})
	_, err := c.Push("/repo", "origin", "refs/heads/main", false)
	require.NoError(t, err)
	assert.Equal(t, []string{"push", "origin", "refs/heads/main"}, capturedArgs)
}

func TestPush_ForceWithLease(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return nil, nil
		},
	})
	_, err := c.Push("/repo", "origin", "", true)
	require.NoError(t, err)
	assert.Equal(t, []string{"push", "--force-with-lease", "origin"}, capturedArgs)
}

func TestPush_Error(t *testing.T) {
	c := NewClient(&mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			return []byte("error: failed to push"), fmt.Errorf("exit status 1")
		},
	})
	_, err := c.Push("/repo", "origin", "", false)
	assert.ErrorContains(t, err, "git push failed")
}

func TestPull_DefaultArgs(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte("Already up to date.\n"), nil
		},
	})
	out, err := c.Pull("/repo", "origin", "", false)
	require.NoError(t, err)
	assert.Equal(t, "Already up to date.", out)
	assert.Equal(t, []string{"pull", "origin"}, capturedArgs)
}

func TestPull_WithBranch(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return nil, nil
		},
	})
	_, err := c.Pull("/repo", "origin", "main", false)
	require.NoError(t, err)
	assert.Equal(t, []string{"pull", "origin", "main"}, capturedArgs)
}

func TestPull_WithRebase(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return nil, nil
		},
	})
	_, err := c.Pull("/repo", "origin", "", true)
	require.NoError(t, err)
	assert.Equal(t, []string{"pull", "--rebase", "origin"}, capturedArgs)
}

func TestFetch_DefaultArgs(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return nil, nil
		},
	})
	_, err := c.Fetch("/repo", "origin", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"fetch", "origin"}, capturedArgs)
}

func TestFetch_WithRefspec(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return nil, nil
		},
	})
	_, err := c.Fetch("/repo", "origin", "refs/heads/main")
	require.NoError(t, err)
	assert.Equal(t, []string{"fetch", "origin", "refs/heads/main"}, capturedArgs)
}
```

**Step 2: Run test to verify it fails**

Run: `cd local-git-mcp && go test ./internal/git/...`
Expected: FAIL — methods not defined.

**Step 3: Write implementation**

Add to `git.go`:

```go
// Push pushes commits to a remote.
// If force is true, uses --force-with-lease.
func (c *Client) Push(repoPath, remote, refspec string, force bool) (string, error) {
	args := []string{"push"}
	if force {
		args = append(args, "--force-with-lease")
	}
	args = append(args, remote)
	if refspec != "" {
		args = append(args, refspec)
	}
	out, err := c.runner.RunDir(repoPath, "git", args...)
	if err != nil {
		return "", fmt.Errorf("git push failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// Pull pulls from a remote.
// If rebase is true, uses --rebase.
func (c *Client) Pull(repoPath, remote, branch string, rebase bool) (string, error) {
	args := []string{"pull"}
	if rebase {
		args = append(args, "--rebase")
	}
	args = append(args, remote)
	if branch != "" {
		args = append(args, branch)
	}
	out, err := c.runner.RunDir(repoPath, "git", args...)
	if err != nil {
		return "", fmt.Errorf("git pull failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// Fetch fetches from a remote without merging.
func (c *Client) Fetch(repoPath, remote, refspec string) (string, error) {
	args := []string{"fetch", remote}
	if refspec != "" {
		args = append(args, refspec)
	}
	out, err := c.runner.RunDir(repoPath, "git", args...)
	if err != nil {
		return "", fmt.Errorf("git fetch failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
```

**Step 4: Run test to verify it passes**

Run: `cd local-git-mcp && go test ./internal/git/...`
Expected: PASS

**Step 5: Commit**

```bash
git add local-git-mcp/internal/git/
git commit -m "feat(local-git-mcp): add push, pull, and fetch operations"
```

---

### Task 5: Git client — list remote refs and list remotes

**Files:**
- Modify: `local-git-mcp/internal/git/git.go`
- Modify: `local-git-mcp/internal/git/git_test.go`

**Step 1: Write the failing tests**

Add to `git_test.go`:

```go
func TestListRemoteRefs_Success(t *testing.T) {
	c := NewClient(&mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			return []byte("abc123\trefs/heads/main\ndef456\trefs/heads/feature\n"), nil
		},
	})
	out, err := c.ListRemoteRefs("/repo", "origin")
	require.NoError(t, err)
	assert.Contains(t, out, "refs/heads/main")
	assert.Contains(t, out, "refs/heads/feature")
}

func TestListRemoteRefs_Error(t *testing.T) {
	c := NewClient(&mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			return []byte("fatal: not a git repository"), fmt.Errorf("exit status 128")
		},
	})
	_, err := c.ListRemoteRefs("/repo", "origin")
	assert.ErrorContains(t, err, "git ls-remote failed")
}

func TestListRemotes_Success(t *testing.T) {
	c := NewClient(&mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			return []byte("origin\tgit@github.com:user/repo.git (fetch)\norigin\tgit@github.com:user/repo.git (push)\n"), nil
		},
	})
	out, err := c.ListRemotes("/repo")
	require.NoError(t, err)
	assert.Contains(t, out, "origin")
}

func TestListRemotes_Error(t *testing.T) {
	c := NewClient(&mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			return []byte("fatal: not a git repository"), fmt.Errorf("exit status 128")
		},
	})
	_, err := c.ListRemotes("/repo")
	assert.ErrorContains(t, err, "git remote failed")
}
```

**Step 2: Run test to verify it fails**

Run: `cd local-git-mcp && go test ./internal/git/...`
Expected: FAIL — methods not defined.

**Step 3: Write implementation**

Add to `git.go`:

```go
// ListRemoteRefs lists refs on a remote (branches, tags, etc.).
func (c *Client) ListRemoteRefs(repoPath, remote string) (string, error) {
	out, err := c.runner.RunDir(repoPath, "git", "ls-remote", remote)
	if err != nil {
		return "", fmt.Errorf("git ls-remote failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// ListRemotes lists configured remotes with their URLs.
func (c *Client) ListRemotes(repoPath string) (string, error) {
	out, err := c.runner.RunDir(repoPath, "git", "remote", "-v")
	if err != nil {
		return "", fmt.Errorf("git remote failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
```

**Step 4: Run test to verify it passes**

Run: `cd local-git-mcp && go test ./internal/git/...`
Expected: PASS

**Step 5: Commit**

```bash
git add local-git-mcp/internal/git/
git commit -m "feat(local-git-mcp): add list remote refs and list remotes"
```

---

### Task 6: MCP tool definitions and handlers

**Files:**
- Create: `local-git-mcp/internal/tools/tools.go`
- Create: `local-git-mcp/internal/tools/tools_test.go`

**Step 1: Write the failing tests**

```go
package tools

import (
	"context"
	"fmt"
	"testing"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockGitClient is a test double for the git.Client interface used by tools.
type mockGitClient struct {
	validateRepoFunc    func(repoPath string) error
	pushFunc            func(repoPath, remote, refspec string, force bool) (string, error)
	pullFunc            func(repoPath, remote, branch string, rebase bool) (string, error)
	fetchFunc           func(repoPath, remote, refspec string) (string, error)
	listRemoteRefsFunc  func(repoPath, remote string) (string, error)
	listRemotesFunc     func(repoPath string) (string, error)
}

func (m *mockGitClient) ValidateRepo(repoPath string) error {
	if m.validateRepoFunc != nil {
		return m.validateRepoFunc(repoPath)
	}
	return nil
}

func (m *mockGitClient) Push(repoPath, remote, refspec string, force bool) (string, error) {
	if m.pushFunc != nil {
		return m.pushFunc(repoPath, remote, refspec, force)
	}
	return "", nil
}

func (m *mockGitClient) Pull(repoPath, remote, branch string, rebase bool) (string, error) {
	if m.pullFunc != nil {
		return m.pullFunc(repoPath, remote, branch, rebase)
	}
	return "", nil
}

func (m *mockGitClient) Fetch(repoPath, remote, refspec string) (string, error) {
	if m.fetchFunc != nil {
		return m.fetchFunc(repoPath, remote, refspec)
	}
	return "", nil
}

func (m *mockGitClient) ListRemoteRefs(repoPath, remote string) (string, error) {
	if m.listRemoteRefsFunc != nil {
		return m.listRemoteRefsFunc(repoPath, remote)
	}
	return "", nil
}

func (m *mockGitClient) ListRemotes(repoPath string) (string, error) {
	if m.listRemotesFunc != nil {
		return m.listRemotesFunc(repoPath)
	}
	return "", nil
}

func TestToolCount(t *testing.T) {
	h := NewHandler(&mockGitClient{})
	tools := h.Tools()
	assert.Len(t, tools, 5)
}

func TestPushHandler_Success(t *testing.T) {
	h := NewHandler(&mockGitClient{
		pushFunc: func(repoPath, remote, refspec string, force bool) (string, error) {
			assert.Equal(t, "/my/repo", repoPath)
			assert.Equal(t, "origin", remote)
			assert.Equal(t, "", refspec)
			assert.False(t, force)
			return "Everything up-to-date", nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "git_push"
	req.Params.Arguments = map[string]any{
		"repo_path": "/my/repo",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

func TestPushHandler_ValidationError(t *testing.T) {
	h := NewHandler(&mockGitClient{
		validateRepoFunc: func(repoPath string) error {
			return fmt.Errorf("not a git repository")
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "git_push"
	req.Params.Arguments = map[string]any{
		"repo_path": "/bad/path",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestPushHandler_MissingRepoPath(t *testing.T) {
	h := NewHandler(&mockGitClient{})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "git_push"
	req.Params.Arguments = map[string]any{}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestPullHandler_WithRebase(t *testing.T) {
	h := NewHandler(&mockGitClient{
		pullFunc: func(repoPath, remote, branch string, rebase bool) (string, error) {
			assert.True(t, rebase)
			assert.Equal(t, "main", branch)
			return "Already up to date.", nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "git_pull"
	req.Params.Arguments = map[string]any{
		"repo_path": "/my/repo",
		"branch":    "main",
		"rebase":    true,
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

func TestUnknownTool(t *testing.T) {
	h := NewHandler(&mockGitClient{})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "git_unknown"
	req.Params.Arguments = map[string]any{"repo_path": "/repo"}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}
```

**Step 2: Run test to verify it fails**

Run: `cd local-git-mcp && go test ./internal/tools/...`
Expected: FAIL — package doesn't exist.

**Step 3: Write implementation**

```go
package tools

import (
	"context"
	"fmt"

	gomcp "github.com/mark3labs/mcp-go/mcp"
)

// GitClient defines the git operations needed by MCP tool handlers.
type GitClient interface {
	ValidateRepo(repoPath string) error
	Push(repoPath, remote, refspec string, force bool) (string, error)
	Pull(repoPath, remote, branch string, rebase bool) (string, error)
	Fetch(repoPath, remote, refspec string) (string, error)
	ListRemoteRefs(repoPath, remote string) (string, error)
	ListRemotes(repoPath string) (string, error)
}

// Handler manages MCP tool definitions and dispatches calls to the git client.
type Handler struct {
	git GitClient
}

// NewHandler creates a Handler with the given git client.
func NewHandler(git GitClient) *Handler {
	return &Handler{git: git}
}

// Tools returns the MCP tool definitions.
func (h *Handler) Tools() []gomcp.Tool {
	return []gomcp.Tool{
		{
			Name:        "git_push",
			Description: "Push commits to a remote repository",
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"repo_path": map[string]any{
						"type":        "string",
						"description": "Absolute path to the git repository",
					},
					"remote": map[string]any{
						"type":        "string",
						"description": "Remote name (default: origin)",
					},
					"refspec": map[string]any{
						"type":        "string",
						"description": "Refspec to push (e.g., refs/heads/main)",
					},
					"force": map[string]any{
						"type":        "boolean",
						"description": "Force push using --force-with-lease",
					},
				},
				Required: []string{"repo_path"},
			},
		},
		{
			Name:        "git_pull",
			Description: "Pull from a remote repository",
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"repo_path": map[string]any{
						"type":        "string",
						"description": "Absolute path to the git repository",
					},
					"remote": map[string]any{
						"type":        "string",
						"description": "Remote name (default: origin)",
					},
					"branch": map[string]any{
						"type":        "string",
						"description": "Branch name to pull",
					},
					"rebase": map[string]any{
						"type":        "boolean",
						"description": "Use --rebase instead of merge",
					},
				},
				Required: []string{"repo_path"},
			},
		},
		{
			Name:        "git_fetch",
			Description: "Fetch from a remote without merging",
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"repo_path": map[string]any{
						"type":        "string",
						"description": "Absolute path to the git repository",
					},
					"remote": map[string]any{
						"type":        "string",
						"description": "Remote name (default: origin)",
					},
					"refspec": map[string]any{
						"type":        "string",
						"description": "Refspec to fetch",
					},
				},
				Required: []string{"repo_path"},
			},
		},
		{
			Name:        "git_list_remote_refs",
			Description: "List refs (branches, tags) on a remote",
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"repo_path": map[string]any{
						"type":        "string",
						"description": "Absolute path to the git repository",
					},
					"remote": map[string]any{
						"type":        "string",
						"description": "Remote name (default: origin)",
					},
				},
				Required: []string{"repo_path"},
			},
		},
		{
			Name:        "git_list_remotes",
			Description: "List configured remotes and their URLs",
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"repo_path": map[string]any{
						"type":        "string",
						"description": "Absolute path to the git repository",
					},
				},
				Required: []string{"repo_path"},
			},
		},
	}
}

// Handle dispatches an MCP tool call to the appropriate git operation.
func (h *Handler) Handle(_ context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.Params.Arguments
	if args == nil {
		args = map[string]any{}
	}

	repoPath, _ := args["repo_path"].(string)
	if repoPath == "" {
		return gomcp.NewToolResultError("repo_path is required"), nil
	}

	if err := h.git.ValidateRepo(repoPath); err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}

	switch req.Params.Name {
	case "git_push":
		remote := stringOrDefault(args, "remote", "origin")
		refspec, _ := args["refspec"].(string)
		force, _ := args["force"].(bool)
		out, err := h.git.Push(repoPath, remote, refspec, force)
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		return gomcp.NewToolResultText(out), nil

	case "git_pull":
		remote := stringOrDefault(args, "remote", "origin")
		branch, _ := args["branch"].(string)
		rebase, _ := args["rebase"].(bool)
		out, err := h.git.Pull(repoPath, remote, branch, rebase)
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		return gomcp.NewToolResultText(out), nil

	case "git_fetch":
		remote := stringOrDefault(args, "remote", "origin")
		refspec, _ := args["refspec"].(string)
		out, err := h.git.Fetch(repoPath, remote, refspec)
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		return gomcp.NewToolResultText(out), nil

	case "git_list_remote_refs":
		remote := stringOrDefault(args, "remote", "origin")
		out, err := h.git.ListRemoteRefs(repoPath, remote)
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		return gomcp.NewToolResultText(out), nil

	case "git_list_remotes":
		out, err := h.git.ListRemotes(repoPath)
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		return gomcp.NewToolResultText(out), nil

	default:
		return gomcp.NewToolResultError(fmt.Sprintf("unknown tool: %s", req.Params.Name)), nil
	}
}

func stringOrDefault(args map[string]any, key, defaultVal string) string {
	if v, ok := args[key].(string); ok && v != "" {
		return v
	}
	return defaultVal
}
```

**Step 4: Run test to verify it passes**

Run: `cd local-git-mcp && go test ./internal/tools/...`
Expected: PASS

**Step 5: Commit**

```bash
git add local-git-mcp/internal/tools/
git commit -m "feat(local-git-mcp): add MCP tool definitions and handlers"
```

---

### Task 7: CLI entry point and MCP server wiring

**Files:**
- Create: `local-git-mcp/cmd/local-git-mcp/main.go`
- Create: `local-git-mcp/cmd/local-git-mcp/root.go`

**Step 1: Create main.go**

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

**Step 2: Create root.go**

```go
package main

import (
	"log/slog"
	"os"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/local-git-mcp/internal/exec"
	"github.com/averycrespi/agent-tools/local-git-mcp/internal/git"
	"github.com/averycrespi/agent-tools/local-git-mcp/internal/tools"
)

var rootCmd = &cobra.Command{
	Use:           "local-git-mcp",
	Short:         "MCP server for authenticated git remote operations",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runServer,
}

func runServer(_ *cobra.Command, _ []string) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger.Info("starting local-git-mcp")

	runner := exec.NewOSRunner()
	gitClient := git.NewClient(runner)
	handler := tools.NewHandler(gitClient)

	srv := mcpserver.NewMCPServer("local-git-mcp", "0.1.0",
		mcpserver.WithToolCapabilities(true),
	)

	for _, tool := range handler.Tools() {
		srv.AddTool(tool, func(ctx gomcp.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
			return handler.Handle(ctx, req)
		})
	}

	logger.Info("serving on stdio")
	return mcpserver.ServeStdio(srv)
}
```

Note: Check the exact `mcpserver.WithToolCapabilities` API — it may be `mcpserver.WithToolCapabilities(true)` or a different option func. Look at how mcp-broker uses it in `serve.go`. The mcp-broker creates the server with just `mcpserver.NewMCPServer("mcp-broker", "0.1.0")` — no options — and that works. Tools are auto-discovered when `AddTool` is called. So the simpler version may just be:

```go
srv := mcpserver.NewMCPServer("local-git-mcp", "0.1.0")
```

Also check the handler function signature — mcp-go may have changed. The mcp-broker uses:
```go
func(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error)
```

Use whichever signature compiles.

**Step 3: Add dependencies**

```bash
cd local-git-mcp && go get github.com/mark3labs/mcp-go@v0.45.0 github.com/spf13/cobra && go mod tidy
```

Also add tool dependencies for linting:

```bash
cd local-git-mcp && go get -tool github.com/golangci/golangci-lint/v2/cmd/golangci-lint golang.org/x/tools/cmd/goimports golang.org/x/vuln/cmd/govulncheck
```

**Step 4: Verify it builds**

Run: `cd local-git-mcp && go build ./cmd/local-git-mcp`
Expected: builds without errors.

**Step 5: Commit**

```bash
git add local-git-mcp/cmd/ local-git-mcp/go.mod local-git-mcp/go.sum
git commit -m "feat(local-git-mcp): add CLI entry point and MCP server wiring"
```

---

### Task 8: Build verification and audit

**Step 1: Run full build**

```bash
cd local-git-mcp && make build
```

Expected: produces `local-git-mcp` binary.

**Step 2: Run all tests**

```bash
cd local-git-mcp && make test
```

Expected: all tests pass.

**Step 3: Run lint**

```bash
cd local-git-mcp && make lint
```

Expected: no lint errors. Fix any that appear.

**Step 4: Run full audit**

```bash
cd local-git-mcp && make audit
```

Expected: tidy + fmt + lint + test + govulncheck all pass. Fix any issues.

**Step 5: Commit any fixes**

If there were fixes from linting or formatting:

```bash
git add local-git-mcp/
git commit -m "chore(local-git-mcp): fix lint and formatting issues"
```

---

### Task 9: Update project documentation

**Files:**
- Modify: `CLAUDE.md` (root)
- Modify: `go.work` (verify it's already updated)

**Step 1: Update root CLAUDE.md**

Add `local-git-mcp` to the structure section:

```markdown
## Structure

\`\`\`
worktree-manager/    Git worktree manager with tmux integration — see worktree-manager/CLAUDE.md
mcp-broker/          MCP proxy for sandboxed agents — see mcp-broker/CLAUDE.md
sandbox-manager/     Lima VM sandbox manager for isolated agent environments — see sandbox-manager/CLAUDE.md
local-git-mcp/       Stdio MCP server for authenticated git remote operations — see local-git-mcp/CLAUDE.md
\`\`\`
```

**Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: add local-git-mcp to project structure"
```
