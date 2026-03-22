# local-gh-mcp Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use Skill(executing-plans) to implement this plan task-by-task.

**Goal:** Build a stdio MCP server that performs GitHub operations by shelling out to the host's `gh` CLI binary, exposing 24 tools across PRs, issues, workflow runs, caches, and search.

**Architecture:** Stdio MCP server following local-git-mcp's pattern — `exec.Runner` interface for testability, a `gh.Client` wrapping the runner, and tool handlers that dispatch to the client. Uses `owner`/`repo` params (not `repo_path`) mapped to `gh -R owner/repo`. Startup auth check via `gh auth status`.

**Tech Stack:** Go 1.25+, mcp-go v0.45.0, cobra, testify, log/slog

---

### Task 1: Go module and project scaffolding

**Files:**
- Create: `local-gh-mcp/go.mod`
- Create: `local-gh-mcp/Makefile`
- Create: `local-gh-mcp/.gitignore`
- Modify: `go.work`

**Step 1: Create the go.mod file**

```bash
cd local-gh-mcp && go mod init github.com/averycrespi/agent-tools/local-gh-mcp
```

Then add tool directives and dependencies to match local-git-mcp's go.mod:

```bash
cd local-gh-mcp && go get github.com/mark3labs/mcp-go@v0.45.0 github.com/spf13/cobra@v1.10.2 github.com/stretchr/testify@v1.11.1
```

**Step 2: Create the Makefile**

Create `local-gh-mcp/Makefile` — identical pattern to local-git-mcp's but with `local-gh-mcp` substituted:

```makefile
.PHONY: build install test lint fmt tidy audit

build:
	go build -o local-gh-mcp ./cmd/local-gh-mcp

install:
	GOBIN=$(shell go env GOPATH)/bin go install ./cmd/local-gh-mcp

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

**Step 3: Create .gitignore**

Create `local-gh-mcp/.gitignore`:

```
/local-gh-mcp
```

**Step 4: Add to go.work**

Add `./local-gh-mcp` to the `use` block in `go.work`:

```go
go 1.25.7

use (
	./local-git-mcp
	./local-gh-mcp
	./mcp-broker
	./sandbox-manager
	./worktree-manager
)
```

**Step 5: Verify the module compiles**

```bash
cd local-gh-mcp && go mod tidy
```

Expected: no errors

**Step 6: Commit**

```bash
git add local-gh-mcp/go.mod local-gh-mcp/go.sum local-gh-mcp/Makefile local-gh-mcp/.gitignore go.work
git commit -m "chore(local-gh-mcp): scaffold Go module and build files"
```

---

### Task 2: exec.Runner interface

**Files:**
- Create: `local-gh-mcp/internal/exec/runner.go`
- Create: `local-gh-mcp/internal/exec/runner_test.go`

**Step 1: Write the tests**

Create `local-gh-mcp/internal/exec/runner_test.go`:

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
```

**Step 2: Run test to verify it fails**

```bash
cd local-gh-mcp && go test ./internal/exec/...
```

Expected: FAIL — package doesn't exist yet

**Step 3: Write the implementation**

Create `local-gh-mcp/internal/exec/runner.go`:

```go
package exec

import (
	osexec "os/exec"
)

// Runner abstracts command execution for testability.
type Runner interface {
	// Run executes a command and returns its combined output.
	Run(name string, args ...string) ([]byte, error)
}

// OSRunner implements Runner using os/exec.
type OSRunner struct{}

// NewOSRunner returns a Runner that uses real OS commands.
func NewOSRunner() *OSRunner { return &OSRunner{} }

func (r *OSRunner) Run(name string, args ...string) ([]byte, error) {
	return osexec.Command(name, args...).CombinedOutput() //nolint:gosec
}
```

Note: unlike local-git-mcp, we don't need `RunDir` — `gh` commands use `-R owner/repo` rather than running in a specific directory.

**Step 4: Run tests to verify they pass**

```bash
cd local-gh-mcp && go test ./internal/exec/...
```

Expected: PASS

**Step 5: Commit**

```bash
git add local-gh-mcp/internal/exec/
git commit -m "feat(local-gh-mcp): add exec.Runner interface"
```

---

### Task 3: GH client — validation and auth

**Files:**
- Create: `local-gh-mcp/internal/gh/gh.go`
- Create: `local-gh-mcp/internal/gh/gh_test.go`

This task implements the `Client` struct, the `AuthStatus` method, and the `ValidateOwnerRepo` helper. Subsequent tasks add the operation methods.

**Step 1: Write the tests**

Create `local-gh-mcp/internal/gh/gh_test.go`:

```go
package gh

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockRunner is a test double for exec.Runner.
type mockRunner struct {
	runFunc func(name string, args ...string) ([]byte, error)
}

func (m *mockRunner) Run(name string, args ...string) ([]byte, error) {
	if m.runFunc != nil {
		return m.runFunc(name, args...)
	}
	return nil, nil
}

func TestAuthStatus_Success(t *testing.T) {
	c := NewClient(&mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			assert.Equal(t, "gh", name)
			assert.Equal(t, []string{"auth", "status"}, args)
			return []byte("Logged in to github.com"), nil
		},
	})
	err := c.AuthStatus(context.Background())
	require.NoError(t, err)
}

func TestAuthStatus_Failure(t *testing.T) {
	c := NewClient(&mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			return []byte("You are not logged into any GitHub hosts"), fmt.Errorf("exit status 1")
		},
	})
	err := c.AuthStatus(context.Background())
	assert.ErrorContains(t, err, "gh auth status failed")
}

func TestValidateOwnerRepo_Valid(t *testing.T) {
	assert.NoError(t, ValidateOwnerRepo("octocat", "hello-world"))
	assert.NoError(t, ValidateOwnerRepo("my.org", "repo_name"))
	assert.NoError(t, ValidateOwnerRepo("user-123", "repo.v2"))
}

func TestValidateOwnerRepo_Invalid(t *testing.T) {
	assert.Error(t, ValidateOwnerRepo("", "repo"))
	assert.Error(t, ValidateOwnerRepo("owner", ""))
	assert.Error(t, ValidateOwnerRepo("owner/evil", "repo"))
	assert.Error(t, ValidateOwnerRepo("owner", "repo;rm -rf"))
	assert.Error(t, ValidateOwnerRepo("owner", "repo name"))
}
```

**Step 2: Run tests to verify they fail**

```bash
cd local-gh-mcp && go test ./internal/gh/...
```

Expected: FAIL — package doesn't exist

**Step 3: Write the implementation**

Create `local-gh-mcp/internal/gh/gh.go`:

```go
package gh

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/exec"
)

var validNamePattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// ValidateOwnerRepo checks that owner and repo contain only safe characters.
func ValidateOwnerRepo(owner, repo string) error {
	if owner == "" {
		return fmt.Errorf("owner is required")
	}
	if repo == "" {
		return fmt.Errorf("repo is required")
	}
	if !validNamePattern.MatchString(owner) {
		return fmt.Errorf("invalid owner: %q", owner)
	}
	if !validNamePattern.MatchString(repo) {
		return fmt.Errorf("invalid repo: %q", repo)
	}
	return nil
}

// Client wraps gh CLI operations with an injectable command runner.
type Client struct {
	runner exec.Runner
}

// NewClient returns a Client using the given command runner.
func NewClient(runner exec.Runner) *Client {
	return &Client{runner: runner}
}

// repoFlag returns the -R flag value for targeting a specific repo.
func repoFlag(owner, repo string) string {
	return owner + "/" + repo
}

// AuthStatus checks whether the gh CLI is authenticated.
func (c *Client) AuthStatus(_ context.Context) error {
	out, err := c.runner.Run("gh", "auth", "status")
	if err != nil {
		return fmt.Errorf("gh auth status failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
```

**Step 4: Run tests to verify they pass**

```bash
cd local-gh-mcp && go test ./internal/gh/...
```

Expected: PASS

**Step 5: Commit**

```bash
git add local-gh-mcp/internal/gh/
git commit -m "feat(local-gh-mcp): add GH client with auth check and validation"
```

---

### Task 4: GH client — PR operations

**Files:**
- Modify: `local-gh-mcp/internal/gh/gh.go`
- Modify: `local-gh-mcp/internal/gh/gh_test.go`

**Step 1: Write the tests**

Append to `local-gh-mcp/internal/gh/gh_test.go`. Test argument construction for each PR method. Example tests (write tests for all 10 PR methods — CreatePR, ViewPR, ListPRs, DiffPR, CommentPR, ReviewPR, MergePR, EditPR, CheckPR, ClosePR):

```go
func TestCreatePR_Args(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte(`{"number":1,"url":"https://github.com/o/r/pull/1"}`), nil
		},
	})
	_, err := c.CreatePR(context.Background(), "owner", "repo", CreatePROpts{
		Title: "my pr",
		Body:  "description",
		Draft: true,
	})
	require.NoError(t, err)
	assert.Contains(t, capturedArgs, "-R")
	assert.Contains(t, capturedArgs, "owner/repo")
	assert.Contains(t, capturedArgs, "--title")
	assert.Contains(t, capturedArgs, "my pr")
	assert.Contains(t, capturedArgs, "--body")
	assert.Contains(t, capturedArgs, "description")
	assert.Contains(t, capturedArgs, "--draft")
}

func TestCreatePR_Error(t *testing.T) {
	c := NewClient(&mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			return []byte("pull request create failed"), fmt.Errorf("exit status 1")
		},
	})
	_, err := c.CreatePR(context.Background(), "owner", "repo", CreatePROpts{
		Title: "pr", Body: "body",
	})
	assert.ErrorContains(t, err, "gh pr create failed")
}

func TestViewPR_Args(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte(`{"number":42}`), nil
		},
	})
	_, err := c.ViewPR(context.Background(), "owner", "repo", 42)
	require.NoError(t, err)
	assert.Contains(t, capturedArgs, "-R")
	assert.Contains(t, capturedArgs, "owner/repo")
	assert.Contains(t, capturedArgs, "42")
	assert.Contains(t, capturedArgs, "--json")
}

func TestListPRs_DefaultLimit(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte(`[]`), nil
		},
	})
	_, err := c.ListPRs(context.Background(), "owner", "repo", ListPROpts{})
	require.NoError(t, err)
	assert.Contains(t, capturedArgs, "--limit")
	assert.Contains(t, capturedArgs, "30")
}

func TestListPRs_ClampedLimit(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte(`[]`), nil
		},
	})
	_, err := c.ListPRs(context.Background(), "owner", "repo", ListPROpts{Limit: 500})
	require.NoError(t, err)
	assert.Contains(t, capturedArgs, "--limit")
	assert.Contains(t, capturedArgs, "100")
}

func TestDiffPR_Args(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte("diff --git a/file b/file"), nil
		},
	})
	_, err := c.DiffPR(context.Background(), "owner", "repo", 10)
	require.NoError(t, err)
	assert.Contains(t, capturedArgs, "10")
	// diff does NOT use --json
	assert.NotContains(t, capturedArgs, "--json")
}

func TestCommentPR_Args(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte(""), nil
		},
	})
	_, err := c.CommentPR(context.Background(), "owner", "repo", 5, "LGTM")
	require.NoError(t, err)
	assert.Contains(t, capturedArgs, "--body")
	assert.Contains(t, capturedArgs, "LGTM")
}

func TestReviewPR_Args(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte(""), nil
		},
	})
	_, err := c.ReviewPR(context.Background(), "owner", "repo", 5, "approve", "looks good")
	require.NoError(t, err)
	assert.Contains(t, capturedArgs, "--approve")
	assert.Contains(t, capturedArgs, "--body")
	assert.Contains(t, capturedArgs, "looks good")
}

func TestMergePR_Squash(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte(""), nil
		},
	})
	_, err := c.MergePR(context.Background(), "owner", "repo", 7, MergePROpts{
		Method:       "squash",
		DeleteBranch: true,
	})
	require.NoError(t, err)
	assert.Contains(t, capturedArgs, "--squash")
	assert.Contains(t, capturedArgs, "--delete-branch")
}

func TestEditPR_Labels(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte(""), nil
		},
	})
	_, err := c.EditPR(context.Background(), "owner", "repo", 3, EditPROpts{
		AddLabels:    []string{"bug", "urgent"},
		RemoveLabels: []string{"wontfix"},
	})
	require.NoError(t, err)
	assert.Contains(t, capturedArgs, "--add-label")
	assert.Contains(t, capturedArgs, "bug,urgent")
	assert.Contains(t, capturedArgs, "--remove-label")
	assert.Contains(t, capturedArgs, "wontfix")
}

func TestCheckPR_Args(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte(`[]`), nil
		},
	})
	_, err := c.CheckPR(context.Background(), "owner", "repo", 8)
	require.NoError(t, err)
	assert.Contains(t, capturedArgs, "8")
	assert.Contains(t, capturedArgs, "--json")
}

func TestClosePR_WithComment(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte(""), nil
		},
	})
	_, err := c.ClosePR(context.Background(), "owner", "repo", 9, "closing this")
	require.NoError(t, err)
	assert.Contains(t, capturedArgs, "--comment")
	assert.Contains(t, capturedArgs, "closing this")
}
```

**Step 2: Run tests to verify they fail**

```bash
cd local-gh-mcp && go test ./internal/gh/...
```

Expected: FAIL — methods don't exist yet

**Step 3: Write the implementation**

Add to `local-gh-mcp/internal/gh/gh.go` — opts structs and all 10 PR methods. Key patterns:

- Every method starts: `args := []string{"pr", "<subcommand>", "-R", repoFlag(owner, repo)}`
- Number-targeted methods add `strconv.Itoa(number)` to args
- Optional string flags: `if opts.Field != "" { args = append(args, "--flag", opts.Field) }`
- Optional bool flags: `if opts.Flag { args = append(args, "--flag") }`
- Optional slice flags: `if len(opts.Items) > 0 { args = append(args, "--flag", strings.Join(opts.Items, ",")) }`
- View/list/check methods add `--json` with curated field lists
- ListPRs uses `clampLimit(opts.Limit)` helper
- All methods end: run command, check error, return trimmed output

```go
const (
	defaultLimit = 30
	maxLimit     = 100
)

// clampLimit returns a limit value within [1, maxLimit], defaulting to defaultLimit.
func clampLimit(limit int) int {
	if limit <= 0 {
		return defaultLimit
	}
	if limit > maxLimit {
		return maxLimit
	}
	return limit
}

// PR JSON field sets
const (
	prViewFields = "number,title,body,state,author,baseRefName,headRefName,url,isDraft,mergeable,reviewDecision,statusCheckRollup,labels,assignees,createdAt,updatedAt"
	prListFields = "number,title,state,author,headRefName,url,isDraft,createdAt,updatedAt"
	prCheckFields = "name,state,description,targetUrl,startedAt,completedAt"
)

type CreatePROpts struct {
	Title     string
	Body      string
	Base      string
	Head      string
	Draft     bool
	Labels    []string
	Reviewers []string
	Assignees []string
}

type ListPROpts struct {
	State  string
	Author string
	Label  string
	Base   string
	Head   string
	Search string
	Limit  int
}

type MergePROpts struct {
	Method       string // merge, squash, rebase
	DeleteBranch bool
	Auto         bool
}

type EditPROpts struct {
	Title           string
	Body            string
	Base            string
	AddLabels       []string
	RemoveLabels    []string
	AddReviewers    []string
	RemoveReviewers []string
	AddAssignees    []string
	RemoveAssignees []string
}
```

Implement each method. Example for `CreatePR`:

```go
func (c *Client) CreatePR(_ context.Context, owner, repo string, opts CreatePROpts) (string, error) {
	args := []string{"pr", "create", "-R", repoFlag(owner, repo),
		"--title", opts.Title, "--body", opts.Body}
	if opts.Base != "" {
		args = append(args, "--base", opts.Base)
	}
	if opts.Head != "" {
		args = append(args, "--head", opts.Head)
	}
	if opts.Draft {
		args = append(args, "--draft")
	}
	if len(opts.Labels) > 0 {
		args = append(args, "--label", strings.Join(opts.Labels, ","))
	}
	if len(opts.Reviewers) > 0 {
		args = append(args, "--reviewer", strings.Join(opts.Reviewers, ","))
	}
	if len(opts.Assignees) > 0 {
		args = append(args, "--assignee", strings.Join(opts.Assignees, ","))
	}
	out, err := c.runner.Run("gh", args...)
	if err != nil {
		return "", fmt.Errorf("gh pr create failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
```

Follow the same pattern for all 10 PR methods. `ReviewPR` maps event strings to flags:

```go
func (c *Client) ReviewPR(_ context.Context, owner, repo string, number int, event, body string) (string, error) {
	args := []string{"pr", "review", "-R", repoFlag(owner, repo), strconv.Itoa(number)}
	switch event {
	case "approve":
		args = append(args, "--approve")
	case "request_changes":
		args = append(args, "--request-changes")
	case "comment":
		args = append(args, "--comment")
	}
	if body != "" {
		args = append(args, "--body", body)
	}
	out, err := c.runner.Run("gh", args...)
	if err != nil {
		return "", fmt.Errorf("gh pr review failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
```

**Step 4: Run tests to verify they pass**

```bash
cd local-gh-mcp && go test ./internal/gh/...
```

Expected: PASS

**Step 5: Commit**

```bash
git add local-gh-mcp/internal/gh/
git commit -m "feat(local-gh-mcp): add GH client PR operations"
```

---

### Task 5: GH client — issue, run, cache, and search operations

**Files:**
- Modify: `local-gh-mcp/internal/gh/gh.go`
- Modify: `local-gh-mcp/internal/gh/gh_test.go`

**Step 1: Write the tests**

Append tests to `local-gh-mcp/internal/gh/gh_test.go` for all remaining methods. Follow the same pattern as Task 4 — capture args, verify flag construction:

```go
// --- Issue tests ---

func TestViewIssue_Args(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte(`{"number":1}`), nil
		},
	})
	_, err := c.ViewIssue(context.Background(), "owner", "repo", 1)
	require.NoError(t, err)
	assert.Contains(t, capturedArgs, "-R")
	assert.Contains(t, capturedArgs, "owner/repo")
	assert.Contains(t, capturedArgs, "1")
	assert.Contains(t, capturedArgs, "--json")
}

func TestListIssues_WithFilters(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte(`[]`), nil
		},
	})
	_, err := c.ListIssues(context.Background(), "owner", "repo", ListIssuesOpts{
		State: "open",
		Label: "bug",
		Limit: 10,
	})
	require.NoError(t, err)
	assert.Contains(t, capturedArgs, "--state")
	assert.Contains(t, capturedArgs, "open")
	assert.Contains(t, capturedArgs, "--label")
	assert.Contains(t, capturedArgs, "bug")
	assert.Contains(t, capturedArgs, "--limit")
	assert.Contains(t, capturedArgs, "10")
}

func TestCommentIssue_Args(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte(""), nil
		},
	})
	_, err := c.CommentIssue(context.Background(), "owner", "repo", 5, "hello")
	require.NoError(t, err)
	assert.Contains(t, capturedArgs, "--body")
	assert.Contains(t, capturedArgs, "hello")
}

// --- Run tests ---

func TestListRuns_Args(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte(`[]`), nil
		},
	})
	_, err := c.ListRuns(context.Background(), "owner", "repo", ListRunsOpts{
		Branch: "main",
		Status: "failure",
	})
	require.NoError(t, err)
	assert.Contains(t, capturedArgs, "--branch")
	assert.Contains(t, capturedArgs, "main")
	assert.Contains(t, capturedArgs, "--status")
	assert.Contains(t, capturedArgs, "failure")
}

func TestViewRun_WithFailedLogs(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte("log output"), nil
		},
	})
	_, err := c.ViewRun(context.Background(), "owner", "repo", 12345, true)
	require.NoError(t, err)
	assert.Contains(t, capturedArgs, "12345")
	assert.Contains(t, capturedArgs, "--log-failed")
}

func TestViewRun_WithoutLogs(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte(`{"databaseId":12345}`), nil
		},
	})
	_, err := c.ViewRun(context.Background(), "owner", "repo", 12345, false)
	require.NoError(t, err)
	assert.Contains(t, capturedArgs, "--json")
	assert.NotContains(t, capturedArgs, "--log-failed")
}

func TestRerun_FailedOnly(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte(""), nil
		},
	})
	_, err := c.Rerun(context.Background(), "owner", "repo", 999, true)
	require.NoError(t, err)
	assert.Contains(t, capturedArgs, "999")
	assert.Contains(t, capturedArgs, "--failed")
}

func TestCancelRun_Args(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte(""), nil
		},
	})
	_, err := c.CancelRun(context.Background(), "owner", "repo", 111)
	require.NoError(t, err)
	assert.Contains(t, capturedArgs, "111")
}

// --- Cache tests ---

func TestListCaches_Args(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte(`[]`), nil
		},
	})
	_, err := c.ListCaches(context.Background(), "owner", "repo", ListCachesOpts{Limit: 50})
	require.NoError(t, err)
	assert.Contains(t, capturedArgs, "--limit")
	assert.Contains(t, capturedArgs, "50")
}

func TestDeleteCache_Args(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte(""), nil
		},
	})
	_, err := c.DeleteCache(context.Background(), "owner", "repo", 42)
	require.NoError(t, err)
	assert.Contains(t, capturedArgs, "42")
}

// --- Search tests ---

func TestSearchPRs_Args(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte(`[]`), nil
		},
	})
	_, err := c.SearchPRs(context.Background(), "fix auth bug", SearchPRsOpts{
		Repo:  "owner/repo",
		State: "open",
		Limit: 10,
	})
	require.NoError(t, err)
	assert.Contains(t, capturedArgs, "fix auth bug")
	assert.Contains(t, capturedArgs, "--repo")
	assert.Contains(t, capturedArgs, "owner/repo")
	assert.Contains(t, capturedArgs, "--state")
	assert.Contains(t, capturedArgs, "open")
}

func TestSearchCode_Args(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte(`[]`), nil
		},
	})
	_, err := c.SearchCode(context.Background(), "func main", SearchCodeOpts{
		Language:  "go",
		Extension: "go",
	})
	require.NoError(t, err)
	assert.Contains(t, capturedArgs, "func main")
	assert.Contains(t, capturedArgs, "--language")
	assert.Contains(t, capturedArgs, "go")
	assert.Contains(t, capturedArgs, "--extension")
	assert.Contains(t, capturedArgs, "go")
}
```

**Step 2: Run tests to verify they fail**

```bash
cd local-gh-mcp && go test ./internal/gh/...
```

Expected: FAIL — methods don't exist

**Step 3: Write the implementation**

Add opts structs and methods to `local-gh-mcp/internal/gh/gh.go`:

```go
// Issue JSON field sets
const (
	issueViewFields = "number,title,body,state,author,labels,assignees,milestone,url,createdAt,updatedAt,comments"
	issueListFields = "number,title,state,author,labels,url,createdAt,updatedAt"
)

// Run JSON field sets
const (
	runListFields = "databaseId,name,displayTitle,status,conclusion,event,headBranch,url,createdAt,updatedAt"
	runViewFields = "databaseId,name,displayTitle,status,conclusion,event,headBranch,headSha,url,createdAt,updatedAt,jobs"
)

// Search JSON field sets
const (
	searchPRFields     = "number,title,state,author,repository,url,createdAt,updatedAt"
	searchIssueFields  = "number,title,state,author,repository,url,createdAt,updatedAt"
	searchRepoFields   = "fullName,description,url,stargazersCount,language,updatedAt"
	searchCodeFields   = "path,repository,sha,textMatches,url"
	searchCommitFields = "sha,message,author,repository,url,committer"
)

type ListIssuesOpts struct {
	State     string
	Author    string
	Assignee  string
	Label     string
	Milestone string
	Search    string
	Limit     int
}

type ListRunsOpts struct {
	Branch   string
	Status   string
	Workflow string
	Limit    int
}

type ListCachesOpts struct {
	Limit int
	Sort  string
	Order string
}

type SearchPRsOpts struct {
	Repo   string
	Owner  string
	State  string
	Author string
	Label  string
	Limit  int
}

type SearchIssuesOpts struct {
	Repo   string
	Owner  string
	State  string
	Author string
	Label  string
	Limit  int
}

type SearchReposOpts struct {
	Owner    string
	Language string
	Topic    string
	Stars    string
	Limit    int
}

type SearchCodeOpts struct {
	Repo      string
	Owner     string
	Language  string
	Extension string
	Filename  string
	Limit     int
}

type SearchCommitsOpts struct {
	Repo   string
	Owner  string
	Author string
	Limit  int
}
```

Implement all methods following the same pattern as PR methods. Key notes:
- `ViewRun` with `logFailed=true` uses `--log-failed` flag and does NOT add `--json` (log output is plain text)
- `ViewRun` with `logFailed=false` uses `--json` with `runViewFields`
- `DeleteCache` uses `strconv.Itoa(cacheID)` as a positional arg
- Search methods use the query as a positional arg after the subcommand: `args := []string{"search", "prs", query, "--json", searchPRFields, "--limit", ...}`

**Step 4: Run tests to verify they pass**

```bash
cd local-gh-mcp && go test ./internal/gh/...
```

Expected: PASS

**Step 5: Commit**

```bash
git add local-gh-mcp/internal/gh/
git commit -m "feat(local-gh-mcp): add issue, run, cache, and search operations"
```

---

### Task 6: Tool handlers — shared infrastructure and PR tools

**Files:**
- Create: `local-gh-mcp/internal/tools/tools.go`
- Create: `local-gh-mcp/internal/tools/pr.go`
- Create: `local-gh-mcp/internal/tools/pr_test.go`

**Step 1: Write the tests**

Create `local-gh-mcp/internal/tools/pr_test.go`. Define a `mockGHClient` that implements the full `GHClient` interface (all 24 methods), then write tests for PR tool handlers:

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

// mockGHClient lives in a shared test file (tools_test.go or pr_test.go).
// It implements GHClient with function fields for each method.
// Only the methods under test need non-nil funcs — others return ("", nil).

func TestCreatePR_Success(t *testing.T) {
	h := NewHandler(&mockGHClient{
		createPRFunc: func(ctx context.Context, owner, repo string, opts any) (string, error) {
			return `{"number":1,"url":"https://github.com/o/r/pull/1"}`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_create_pr"
	req.Params.Arguments = map[string]any{
		"owner": "myorg",
		"repo":  "myrepo",
		"title": "Add feature",
		"body":  "This adds a feature",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

func TestCreatePR_MissingOwner(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_create_pr"
	req.Params.Arguments = map[string]any{
		"repo":  "myrepo",
		"title": "Add feature",
		"body":  "body",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestCreatePR_InvalidOwner(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_create_pr"
	req.Params.Arguments = map[string]any{
		"owner": "evil;rm",
		"repo":  "myrepo",
		"title": "title",
		"body":  "body",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestCreatePR_MissingTitle(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_create_pr"
	req.Params.Arguments = map[string]any{
		"owner": "o",
		"repo":  "r",
		"body":  "body",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestViewPR_Success(t *testing.T) {
	h := NewHandler(&mockGHClient{
		viewPRFunc: func(ctx context.Context, owner, repo string, number int) (string, error) {
			return `{"number":42,"title":"test"}`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_view_pr"
	req.Params.Arguments = map[string]any{
		"owner":  "o",
		"repo":   "r",
		"number": float64(42), // JSON numbers are float64
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

func TestMergePR_InvalidMethod(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_merge_pr"
	req.Params.Arguments = map[string]any{
		"owner":  "o",
		"repo":   "r",
		"number": float64(1),
		"method": "yolo",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestReviewPR_InvalidEvent(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_review_pr"
	req.Params.Arguments = map[string]any{
		"owner":  "o",
		"repo":   "r",
		"number": float64(1),
		"event":  "reject",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestUnknownTool(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_nonexistent"
	req.Params.Arguments = map[string]any{}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}
```

**Step 2: Run tests to verify they fail**

```bash
cd local-gh-mcp && go test ./internal/tools/...
```

Expected: FAIL

**Step 3: Write tools.go (shared infrastructure)**

Create `local-gh-mcp/internal/tools/tools.go`:

```go
package tools

import (
	"context"
	"fmt"

	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/gh"
	gomcp "github.com/mark3labs/mcp-go/mcp"
)

// GHClient defines the GitHub operations needed by MCP tool handlers.
type GHClient interface {
	CreatePR(ctx context.Context, owner, repo string, opts gh.CreatePROpts) (string, error)
	ViewPR(ctx context.Context, owner, repo string, number int) (string, error)
	ListPRs(ctx context.Context, owner, repo string, opts gh.ListPROpts) (string, error)
	DiffPR(ctx context.Context, owner, repo string, number int) (string, error)
	CommentPR(ctx context.Context, owner, repo string, number int, body string) (string, error)
	ReviewPR(ctx context.Context, owner, repo string, number int, event, body string) (string, error)
	MergePR(ctx context.Context, owner, repo string, number int, opts gh.MergePROpts) (string, error)
	EditPR(ctx context.Context, owner, repo string, number int, opts gh.EditPROpts) (string, error)
	CheckPR(ctx context.Context, owner, repo string, number int) (string, error)
	ClosePR(ctx context.Context, owner, repo string, number int, comment string) (string, error)
	ViewIssue(ctx context.Context, owner, repo string, number int) (string, error)
	ListIssues(ctx context.Context, owner, repo string, opts gh.ListIssuesOpts) (string, error)
	CommentIssue(ctx context.Context, owner, repo string, number int, body string) (string, error)
	ListRuns(ctx context.Context, owner, repo string, opts gh.ListRunsOpts) (string, error)
	ViewRun(ctx context.Context, owner, repo string, runID int, logFailed bool) (string, error)
	Rerun(ctx context.Context, owner, repo string, runID int, failedOnly bool) (string, error)
	CancelRun(ctx context.Context, owner, repo string, runID int) (string, error)
	ListCaches(ctx context.Context, owner, repo string, opts gh.ListCachesOpts) (string, error)
	DeleteCache(ctx context.Context, owner, repo string, cacheID int) (string, error)
	SearchPRs(ctx context.Context, query string, opts gh.SearchPRsOpts) (string, error)
	SearchIssues(ctx context.Context, query string, opts gh.SearchIssuesOpts) (string, error)
	SearchRepos(ctx context.Context, query string, opts gh.SearchReposOpts) (string, error)
	SearchCode(ctx context.Context, query string, opts gh.SearchCodeOpts) (string, error)
	SearchCommits(ctx context.Context, query string, opts gh.SearchCommitsOpts) (string, error)
}

// Handler manages MCP tool definitions and dispatches calls.
type Handler struct {
	gh GHClient
}

// NewHandler creates a Handler with the given GH client.
func NewHandler(gh GHClient) *Handler {
	return &Handler{gh: gh}
}

// Tools returns all MCP tool definitions.
func (h *Handler) Tools() []gomcp.Tool {
	var tools []gomcp.Tool
	tools = append(tools, h.prTools()...)
	tools = append(tools, h.issueTools()...)
	tools = append(tools, h.runTools()...)
	tools = append(tools, h.cacheTools()...)
	tools = append(tools, h.searchTools()...)
	return tools
}

// Handle dispatches an MCP tool call to the appropriate handler.
func (h *Handler) Handle(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	switch req.Params.Name {
	// PR tools
	case "gh_create_pr":
		return h.handleCreatePR(ctx, req)
	case "gh_view_pr":
		return h.handleViewPR(ctx, req)
	case "gh_list_prs":
		return h.handleListPRs(ctx, req)
	case "gh_diff_pr":
		return h.handleDiffPR(ctx, req)
	case "gh_comment_pr":
		return h.handleCommentPR(ctx, req)
	case "gh_review_pr":
		return h.handleReviewPR(ctx, req)
	case "gh_merge_pr":
		return h.handleMergePR(ctx, req)
	case "gh_edit_pr":
		return h.handleEditPR(ctx, req)
	case "gh_check_pr":
		return h.handleCheckPR(ctx, req)
	case "gh_close_pr":
		return h.handleClosePR(ctx, req)
	// Issue tools
	case "gh_view_issue":
		return h.handleViewIssue(ctx, req)
	case "gh_list_issues":
		return h.handleListIssues(ctx, req)
	case "gh_comment_issue":
		return h.handleCommentIssue(ctx, req)
	// Run tools
	case "gh_list_runs":
		return h.handleListRuns(ctx, req)
	case "gh_view_run":
		return h.handleViewRun(ctx, req)
	case "gh_rerun":
		return h.handleRerun(ctx, req)
	case "gh_cancel_run":
		return h.handleCancelRun(ctx, req)
	// Cache tools
	case "gh_list_caches":
		return h.handleListCaches(ctx, req)
	case "gh_delete_cache":
		return h.handleDeleteCache(ctx, req)
	// Search tools
	case "gh_search_prs":
		return h.handleSearchPRs(ctx, req)
	case "gh_search_issues":
		return h.handleSearchIssues(ctx, req)
	case "gh_search_repos":
		return h.handleSearchRepos(ctx, req)
	case "gh_search_code":
		return h.handleSearchCode(ctx, req)
	case "gh_search_commits":
		return h.handleSearchCommits(ctx, req)
	default:
		return gomcp.NewToolResultError(fmt.Sprintf("unknown tool: %s", req.Params.Name)), nil
	}
}

// --- Shared helpers ---

func stringOrDefault(args map[string]any, key, defaultVal string) string {
	if v, ok := args[key].(string); ok && v != "" {
		return v
	}
	return defaultVal
}

func intFromArgs(args map[string]any, key string) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

func stringSliceFromArgs(args map[string]any, key string) []string {
	val, ok := args[key]
	if !ok {
		return nil
	}
	switch v := val.(type) {
	case []any:
		result := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	}
	return nil
}

// requireOwnerRepo extracts and validates owner/repo from args.
// Returns owner, repo, error result. If error result is non-nil, return it immediately.
func requireOwnerRepo(args map[string]any) (string, string, *gomcp.CallToolResult) {
	owner, _ := args["owner"].(string)
	repo, _ := args["repo"].(string)
	if err := gh.ValidateOwnerRepo(owner, repo); err != nil {
		return "", "", gomcp.NewToolResultError(err.Error())
	}
	return owner, repo, nil
}
```

**Step 4: Write pr.go (PR tool definitions + handlers)**

Create `local-gh-mcp/internal/tools/pr.go`:

```go
package tools

import (
	"context"

	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/gh"
	gomcp "github.com/mark3labs/mcp-go/mcp"
)

func (h *Handler) prTools() []gomcp.Tool {
	return []gomcp.Tool{
		{
			Name:        "gh_create_pr",
			Description: "Create a pull request",
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"owner":     map[string]any{"type": "string", "description": "Repository owner"},
					"repo":      map[string]any{"type": "string", "description": "Repository name"},
					"title":     map[string]any{"type": "string", "description": "PR title"},
					"body":      map[string]any{"type": "string", "description": "PR body"},
					"base":      map[string]any{"type": "string", "description": "Base branch (default: repo default branch)"},
					"head":      map[string]any{"type": "string", "description": "Head branch"},
					"draft":     map[string]any{"type": "boolean", "description": "Create as draft PR"},
					"labels":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Labels to add"},
					"reviewers": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Reviewers to request"},
					"assignees": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Assignees to add"},
				},
				Required: []string{"owner", "repo", "title", "body"},
			},
		},
		// ... define all 10 PR tools with their schemas
	}
}

func (h *Handler) handleCreatePR(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	title, _ := args["title"].(string)
	if title == "" {
		return gomcp.NewToolResultError("title is required"), nil
	}
	body, _ := args["body"].(string)
	if body == "" {
		return gomcp.NewToolResultError("body is required"), nil
	}
	opts := gh.CreatePROpts{
		Title:     title,
		Body:      body,
		Base:      stringOrDefault(args, "base", ""),
		Head:      stringOrDefault(args, "head", ""),
		Draft:     args["draft"] == true,
		Labels:    stringSliceFromArgs(args, "labels"),
		Reviewers: stringSliceFromArgs(args, "reviewers"),
		Assignees: stringSliceFromArgs(args, "assignees"),
	}
	out, err := h.gh.CreatePR(ctx, owner, repo, opts)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return gomcp.NewToolResultText(out), nil
}

// Implement all 10 PR handlers following this pattern.
// handleReviewPR must validate event against {"approve", "request_changes", "comment"}.
// handleMergePR must validate method against {"merge", "squash", "rebase", ""}.
```

**Step 5: Run tests to verify they pass**

```bash
cd local-gh-mcp && go test ./internal/tools/...
```

Expected: PASS

**Step 6: Commit**

```bash
git add local-gh-mcp/internal/tools/
git commit -m "feat(local-gh-mcp): add tool infrastructure and PR tool handlers"
```

---

### Task 7: Tool handlers — issue, run, cache, and search tools

**Files:**
- Create: `local-gh-mcp/internal/tools/issue.go`
- Create: `local-gh-mcp/internal/tools/issue_test.go`
- Create: `local-gh-mcp/internal/tools/run.go`
- Create: `local-gh-mcp/internal/tools/run_test.go`
- Create: `local-gh-mcp/internal/tools/cache.go`
- Create: `local-gh-mcp/internal/tools/cache_test.go`
- Create: `local-gh-mcp/internal/tools/search.go`
- Create: `local-gh-mcp/internal/tools/search_test.go`

**Step 1: Write the tests**

Create test files for each category. Follow the same patterns as PR tests: mock the GHClient, test success cases, test missing required params, test validation errors.

Key tests per category:

**issue_test.go:**
- `TestViewIssue_Success` — verify handler calls ViewIssue with correct owner/repo/number
- `TestViewIssue_MissingNumber` — returns error
- `TestListIssues_Success` — verify filter params are passed through
- `TestCommentIssue_MissingBody` — returns error

**run_test.go:**
- `TestListRuns_Success` — verify filter params
- `TestViewRun_WithLogs` — verify log_failed=true is passed
- `TestViewRun_WithoutLogs` — verify log_failed=false
- `TestRerun_FailedOnly` — verify failed_only flag
- `TestCancelRun_Success` — basic success

**cache_test.go:**
- `TestListCaches_Success` — basic success
- `TestDeleteCache_MissingCacheID` — returns error

**search_test.go:**
- `TestSearchPRs_Success` — verify query and filters
- `TestSearchPRs_MissingQuery` — returns error
- `TestSearchIssues_Success`
- `TestSearchRepos_Success`
- `TestSearchCode_Success`
- `TestSearchCommits_Success`

**Step 2: Run tests to verify they fail**

```bash
cd local-gh-mcp && go test ./internal/tools/...
```

Expected: FAIL

**Step 3: Write the implementations**

Create each file with tool definitions and handlers:

**issue.go:** 3 tools (gh_view_issue, gh_list_issues, gh_comment_issue). `issueTools()` returns tool definitions, handlers extract params and call GHClient.

**run.go:** 4 tools (gh_list_runs, gh_view_run, gh_rerun, gh_cancel_run). `runTools()` returns definitions. `run_id` is extracted as int via `intFromArgs`. `log_failed` and `failed_only` are booleans.

**cache.go:** 2 tools (gh_list_caches, gh_delete_cache). `cacheTools()` returns definitions. `cache_id` extracted as int.

**search.go:** 5 tools (gh_search_prs, gh_search_issues, gh_search_repos, gh_search_code, gh_search_commits). `searchTools()` returns definitions. All require `query` string. Optional filters are passed through as opts.

**Step 4: Run tests to verify they pass**

```bash
cd local-gh-mcp && go test ./internal/tools/...
```

Expected: PASS

**Step 5: Add a tool count test**

Add to one of the test files (or `tools_test.go` if the mock lives there):

```go
func TestToolCount(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	tools := h.Tools()
	assert.Len(t, tools, 24)
}
```

Run: `cd local-gh-mcp && go test ./internal/tools/...`

Expected: PASS

**Step 6: Commit**

```bash
git add local-gh-mcp/internal/tools/
git commit -m "feat(local-gh-mcp): add issue, run, cache, and search tool handlers"
```

---

### Task 8: CLI entry point with auth check

**Files:**
- Create: `local-gh-mcp/cmd/local-gh-mcp/main.go`
- Create: `local-gh-mcp/cmd/local-gh-mcp/root.go`

**Step 1: Create main.go**

Create `local-gh-mcp/cmd/local-gh-mcp/main.go`:

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

**Step 2: Create root.go with auth check**

Create `local-gh-mcp/cmd/local-gh-mcp/root.go`:

```go
package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/exec"
	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/gh"
	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/tools"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "local-gh-mcp",
	Short: "Stdio MCP server for GitHub operations via the gh CLI",
	RunE: func(_ *cobra.Command, _ []string) error {
		runner := exec.NewOSRunner()
		ghClient := gh.NewClient(runner)

		// Fast-fail if gh CLI is not authenticated
		if err := ghClient.AuthStatus(context.Background()); err != nil {
			return fmt.Errorf("gh CLI is not authenticated — run 'gh auth login' first: %w", err)
		}

		handler := tools.NewHandler(ghClient)

		srv := mcpserver.NewMCPServer("local-gh-mcp", "0.1.0")
		for _, tool := range handler.Tools() {
			srv.AddTool(tool, handler.Handle)
		}

		slog.Info("starting local-gh-mcp stdio server")
		return mcpserver.ServeStdio(srv)
	},
	SilenceUsage: true,
}
```

**Step 3: Verify it compiles**

```bash
cd local-gh-mcp && make build
```

Expected: binary created at `./local-gh-mcp`

**Step 4: Commit**

```bash
git add local-gh-mcp/cmd/
git commit -m "feat(local-gh-mcp): add CLI entry point with startup auth check"
```

---

### Task 9: Run make audit

**Step 1: Run the full audit**

```bash
cd local-gh-mcp && make audit
```

Expected: all checks pass (tidy, fmt, lint, test, govulncheck). If lint or fmt flags issues, fix them before continuing.

**Step 2: Commit any fixes**

If `make audit` required changes (formatting, lint fixes):

```bash
git add -A local-gh-mcp/
git commit -m "chore(local-gh-mcp): fix lint and formatting issues"
```

---

### Task 10: Documentation — DESIGN.md, CLAUDE.md, README.md

**Files:**
- Create: `local-gh-mcp/DESIGN.md` (copy from `.plans/2026-03-22-local-gh-mcp-design.md` — this is the canonical location)
- Create: `local-gh-mcp/CLAUDE.md`
- Create: `local-gh-mcp/README.md`
- Modify: `README.md` (root)
- Modify: `CLAUDE.md` (root)
- Modify: `go.work`

**Step 1: Create DESIGN.md**

Copy the design document from `.plans/2026-03-22-local-gh-mcp-design.md` to `local-gh-mcp/DESIGN.md`. This is the canonical copy — the plans file was a working draft.

**Step 2: Create CLAUDE.md**

Create `local-gh-mcp/CLAUDE.md` following local-git-mcp's pattern:

```markdown
# local-gh-mcp

Stdio MCP server for GitHub operations via the gh CLI.

## Development

```bash
make build              # go build -o local-gh-mcp ./cmd/local-gh-mcp
make install            # go install ./cmd/local-gh-mcp
make test               # go test -race ./...
make lint               # go tool golangci-lint run ./...
make fmt                # go tool goimports -w .
make tidy               # go mod tidy && go mod verify
make audit              # tidy + fmt + lint + test + govulncheck
```

Run `make audit` before committing.

## Architecture

Stdio MCP server. No config, no state, no network listener.

Shells out to the host's `gh` binary for all operations. Validates `gh auth status` on startup — exits immediately if not authenticated.

```
cmd/local-gh-mcp/       CLI entry point (Cobra)
internal/
  exec/                  Runner interface for command execution
  gh/                    GitHub operations via exec.Runner
  tools/
    tools.go             Tool registration and dispatch
    pr.go                PR tool definitions and handlers
    issue.go             Issue tool definitions and handlers
    run.go               Workflow run tool definitions and handlers
    cache.go             Cache tool definitions and handlers
    search.go            Search tool definitions and handlers
```

## Conventions

- Errors are wrapped with context: `fmt.Errorf("doing X: %w", err)`
- All external commands go through `exec.Runner` interface
- `cmd/` has no tests (thin wrappers); all internal packages have tests
- Command failures use `%s` with trimmed output; Go errors use `%w` for wrapping
- gosec nolint directives on os/exec are intentional for CLI
- `owner` and `repo` validated against `[a-zA-Z0-9._-]` pattern before use
- Repo targeting: `-R owner/repo` flag (not repo_path)
- JSON output: all list/view tools use `--json` with curated field sets
- Limits: default 30, max 100, clamped silently (derived from gh CLI defaults and GitHub API page size)
- Stdio MCP server setup: `mcpserver.NewMCPServer()` + `srv.AddTool(tool, handler.Handle)` + `mcpserver.ServeStdio(srv)` — handler signature is `func(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error)`
- `mcp-go` v0.45.0: use `req.GetArguments()` helper instead of `req.Params.Arguments` (typed as `any`, not `map[string]any`)
```

**Step 3: Create README.md**

Create `local-gh-mcp/README.md` following local-git-mcp's pattern:

```markdown
# local-gh-mcp

A stdio MCP server that performs GitHub operations on behalf of sandboxed agents using the host's `gh` CLI.

Sandboxed agents need to interact with GitHub — creating PRs, checking CI, reading issues — but giving them credentials defeats the purpose of sandboxing. The official GitHub MCP server requires OAuth or PATs, adding complexity. local-gh-mcp reuses the host's existing `gh` CLI authentication, avoiding extra secrets.

## How it works

```
Agent (in sandbox)                    Host
─────────────────                    ─────
needs to create PR,  ──MCP──▶    local-gh-mcp
check CI, read issues                │
(no credentials)                 gh pr create, gh run view, ...
                                 (uses host's gh auth)
```

local-gh-mcp is a stdio MCP server — a caller spawns it as a subprocess and communicates over stdin/stdout. It shells out to the host's `gh` binary, which uses the existing authentication from `gh auth login`.

## Prerequisites

The `gh` CLI must be installed and authenticated:

```bash
gh auth status    # verify authentication
gh auth login     # authenticate if needed
```

local-gh-mcp validates authentication on startup and exits with a clear error if `gh` is not authenticated.

## Tools

### PR Tools

| Tool | Description |
|------|-------------|
| `gh_create_pr` | Create a pull request |
| `gh_view_pr` | View PR details as JSON |
| `gh_list_prs` | List PRs with filters (state, author, label, etc.) |
| `gh_diff_pr` | View the diff for a PR |
| `gh_comment_pr` | Add a comment to a PR |
| `gh_review_pr` | Submit a review (approve, request changes, or comment) |
| `gh_merge_pr` | Merge a PR (merge, squash, or rebase) |
| `gh_edit_pr` | Edit PR metadata (title, body, labels, reviewers, assignees) |
| `gh_check_pr` | View CI/status check results for a PR |
| `gh_close_pr` | Close a PR |

### Issue Tools

| Tool | Description |
|------|-------------|
| `gh_view_issue` | View issue details as JSON |
| `gh_list_issues` | List issues with filters (state, author, label, etc.) |
| `gh_comment_issue` | Add a comment to an issue |

### Workflow Run Tools

| Tool | Description |
|------|-------------|
| `gh_list_runs` | List workflow runs with filters (branch, status, workflow) |
| `gh_view_run` | View run details, logs, or failed logs |
| `gh_rerun` | Rerun a failed or specific workflow run |
| `gh_cancel_run` | Cancel an in-progress workflow run |

### Cache Tools

| Tool | Description |
|------|-------------|
| `gh_list_caches` | List GitHub Actions caches for a repo |
| `gh_delete_cache` | Delete a GitHub Actions cache entry |

### Search Tools

| Tool | Description |
|------|-------------|
| `gh_search_prs` | Search pull requests across GitHub |
| `gh_search_issues` | Search issues across GitHub |
| `gh_search_repos` | Search repositories across GitHub |
| `gh_search_code` | Search code across GitHub |
| `gh_search_commits` | Search commits across GitHub |

All tools that list or search use `owner` and `repo` parameters to target a specific repository. Search tools use a `query` parameter for cross-repo search.

## Quick start

```bash
# Build
make build

# Use as a stdio MCP backend (e.g., in mcp-broker config)
{
  "servers": {
    "local-gh": {
      "command": "local-gh-mcp"
    }
  }
}
```

## Development

```bash
make build              # Build binary to ./local-gh-mcp
make test               # Run tests with race detector
make lint               # Run golangci-lint
make fmt                # Format with goimports
make tidy               # go mod tidy + verify
make audit              # tidy + fmt + lint + test + govulncheck
```

Requires Go 1.25+. Tool dependencies (golangci-lint, goimports, govulncheck) are managed via `go tool` directives in `go.mod`.

## Architecture

See [DESIGN.md](DESIGN.md) for the full design document.

```
cmd/local-gh-mcp/       CLI entry point (Cobra)
internal/
  exec/                  Runner interface for command execution
  gh/                    GitHub operations via exec.Runner
  tools/                 MCP tool definitions and handlers (split by category)
```
```

**Step 4: Update root README.md**

Add a `### Local GH MCP` section to `README.md` after the Local Git MCP section, following the same pattern:

```markdown
### Local GH MCP

Sandboxed agents need to interact with GitHub — creating PRs, checking CI status, reading issues, debugging workflow failures — but giving them credentials defeats the purpose of sandboxing. The official GitHub MCP server requires OAuth or personal access tokens.

`local-gh-mcp` is a stdio MCP server that runs on the host where `gh` CLI is already authenticated. It exposes 24 tools across PRs, issues, workflow runs, caches, and search — over MCP. Designed to be used as a backend for mcp-broker, letting agents interact with GitHub without managing additional credentials.

See the [README](local-gh-mcp/README.md) for more information.
```

Also add `cd local-gh-mcp && make install` to the Getting Started install commands.

**Step 5: Update root CLAUDE.md**

Add `local-gh-mcp/` to the project structure listing with its description:

```
local-gh-mcp/       Stdio MCP server for GitHub operations via gh CLI — see local-gh-mcp/CLAUDE.md
```

**Step 6: Commit**

```bash
git add local-gh-mcp/DESIGN.md local-gh-mcp/CLAUDE.md local-gh-mcp/README.md README.md CLAUDE.md
git commit -m "docs(local-gh-mcp): add README, CLAUDE.md, DESIGN.md and update root docs"
```
