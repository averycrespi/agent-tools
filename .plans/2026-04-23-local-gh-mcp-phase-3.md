# local-gh-mcp Phase 3 Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use Skill(executing-plans) to implement this plan task-by-task.

**Goal:** Ship Phase 3 of local-gh-mcp improvements — 9 new MCP tools, 2 new filter params on `gh_list_runs`, and removal of the redundant `search` param from `gh_list_prs`/`gh_list_issues`.

**Architecture:** Each task adds (a) one or more methods to the `gh.Client` wrapper in `local-gh-mcp/internal/gh/gh.go`, (b) matching methods on the `GHClient` interface in `local-gh-mcp/internal/tools/tools.go`, (c) tool definitions and handlers in the relevant `internal/tools/*.go` file, and (d) dispatch wiring in `tools.go`'s `Handle()` switch. New tool categories get new files (`context.go`, `branch.go`, `release.go`); new tools in existing categories append to the existing file. TDD throughout — handler test with mock client first, then implementation.

**Tech Stack:** Go 1.x, `github.com/mark3labs/mcp-go` MCP library, `gh` CLI (shelled via `exec.Runner`), Cobra for CLI, `testify` for assertions (existing pattern).

**Design reference:** `.designs/2026-04-23-local-gh-mcp-phase-3.md`.

---

## Task 1: `gh_whoami` + new `context` tool category

**Files:**

- Create: `local-gh-mcp/internal/tools/context.go`
- Create: `local-gh-mcp/internal/tools/context_test.go`
- Modify: `local-gh-mcp/internal/gh/gh.go` (add `ViewUser` method near `AuthStatus`)
- Modify: `local-gh-mcp/internal/tools/tools.go` (add `ViewUser` to `GHClient` interface; add `contextTools()` to `Tools()`; add `gh_whoami` case to `Handle()`)
- Modify: `local-gh-mcp/internal/tools/pr_test.go` (add `viewUserFunc` field and stub method to `mockGHClient` — central mock used by all tool tests)

### Step 1.1: Write the failing handler test

Append to `local-gh-mcp/internal/tools/context_test.go` (new file):

```go
package tools

import (
	"context"
	"strings"
	"testing"

	gomcp "github.com/mark3labs/mcp-go/mcp"
)

func TestGhWhoami(t *testing.T) {
	mock := &mockGHClient{
		viewUserFunc: func(_ context.Context) (string, error) {
			return `{"login":"octocat","name":"The Octocat","html_url":"https://github.com/octocat","type":"User"}`, nil
		},
	}
	h := NewHandler(mock)
	res, err := h.Handle(context.Background(), gomcp.CallToolRequest{
		Params: gomcp.CallToolParams{Name: "gh_whoami", Arguments: map[string]any{}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := textOf(res)
	if !strings.Contains(out, "Logged in as `octocat`") {
		t.Errorf("missing login line, got: %s", out)
	}
	if !strings.Contains(out, "(The Octocat)") {
		t.Errorf("missing name, got: %s", out)
	}
	if !strings.Contains(out, "https://github.com/octocat") {
		t.Errorf("missing html_url, got: %s", out)
	}
}

func TestGhWhoamiBot(t *testing.T) {
	mock := &mockGHClient{
		viewUserFunc: func(_ context.Context) (string, error) {
			return `{"login":"dependabot","name":null,"html_url":"https://github.com/dependabot","type":"Bot"}`, nil
		},
	}
	h := NewHandler(mock)
	res, err := h.Handle(context.Background(), gomcp.CallToolRequest{
		Params: gomcp.CallToolParams{Name: "gh_whoami", Arguments: map[string]any{}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := textOf(res)
	if !strings.Contains(out, "`dependabot` [bot]") {
		t.Errorf("missing [bot] suffix, got: %s", out)
	}
	if strings.Contains(out, "()") {
		t.Errorf("null name should be omitted entirely, got: %s", out)
	}
}
```

(Note: `textOf(res)` is a helper used by existing tests in the package — inherit it from `pr_test.go`. If it doesn't exist with that exact name, use whatever the existing tests use to extract text from a `CallToolResult`. Verify by grepping `pr_test.go`.)

### Step 1.2: Run the test — verify it fails

```bash
cd local-gh-mcp && go test ./internal/tools/ -run TestGhWhoami
```

Expected: compile error on `viewUserFunc` field (mock doesn't have it yet) or dispatch error "unknown tool gh_whoami".

### Step 1.3: Add `ViewUser` to the gh client

Append to `local-gh-mcp/internal/gh/gh.go` (near `AuthStatus`):

```go
// ViewUser returns the authenticated user as JSON from `gh api /user`.
func (c *Client) ViewUser(_ context.Context) (string, error) {
	out, err := c.runner.Run("gh", "api", "/user")
	if err != nil {
		return "", fmt.Errorf("gh api /user failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
```

### Step 1.4: Add `ViewUser` to the `GHClient` interface

In `local-gh-mcp/internal/tools/tools.go`, inside the `GHClient` interface (~line 15-50), add:

```go
ViewUser(ctx context.Context) (string, error)
```

### Step 1.5: Add stub to mockGHClient

In `local-gh-mcp/internal/tools/pr_test.go` (or wherever `mockGHClient` lives — likely there as a shared test fixture), add a field:

```go
viewUserFunc func(ctx context.Context) (string, error)
```

And a method receiver:

```go
func (m *mockGHClient) ViewUser(ctx context.Context) (string, error) {
	if m.viewUserFunc != nil {
		return m.viewUserFunc(ctx)
	}
	return "", nil
}
```

### Step 1.6: Create `context.go` with the tool definition and handler

Create `local-gh-mcp/internal/tools/context.go`:

```go
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	gomcp "github.com/mark3labs/mcp-go/mcp"
)

func (h *Handler) contextTools() []gomcp.Tool {
	return []gomcp.Tool{
		{
			Name:        "gh_whoami",
			Description: "Show the authenticated GitHub user (login, display name, profile URL). Useful for grounding `author:@me` or `review-requested:@me` search queries.",
			Annotations: annRead,
			InputSchema: gomcp.ToolInputSchema{
				Type:       "object",
				Properties: map[string]any{},
			},
		},
	}
}

type userResponse struct {
	Login   string  `json:"login"`
	Name    *string `json:"name"`
	HTMLURL string  `json:"html_url"`
	Type    string  `json:"type"`
}

func (h *Handler) handleWhoami(ctx context.Context, _ gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	raw, err := h.gh.ViewUser(ctx)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var u userResponse
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		return parseError("gh_whoami", raw, err), nil
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Logged in as `%s`", u.Login))
	if u.Type == "Bot" {
		b.WriteString(" [bot]")
	}
	if u.Name != nil && *u.Name != "" {
		b.WriteString(fmt.Sprintf(" (%s)", *u.Name))
	}
	b.WriteString("\n")
	b.WriteString(u.HTMLURL)
	return gomcp.NewToolResultText(b.String()), nil
}
```

### Step 1.7: Wire into `Tools()` and `Handle()`

In `local-gh-mcp/internal/tools/tools.go`:

- In `Tools()` (~line 14-70), append `h.contextTools()...` to the returned slice.
- In `Handle()` switch (~line 73-135), add:

```go
case "gh_whoami":
    return h.handleWhoami(ctx, req)
```

### Step 1.8: Run the test — verify it passes

```bash
cd local-gh-mcp && go test ./internal/tools/ -run TestGhWhoami -v
```

Expected: both subtests PASS.

### Step 1.9: Run the full package tests and audit

```bash
cd local-gh-mcp && make test && make lint
```

Expected: all tests pass; no lint errors. The existing `TestEveryToolHasOpenWorldHint` automatically covers `gh_whoami` since it iterates `handler.Tools()`.

### Step 1.10: Commit

```bash
git add local-gh-mcp/internal/gh/gh.go local-gh-mcp/internal/tools/tools.go local-gh-mcp/internal/tools/context.go local-gh-mcp/internal/tools/context_test.go local-gh-mcp/internal/tools/pr_test.go
git commit -F- <<'EOF'
feat(local-gh-mcp): add gh_whoami and context category

Grounds the agent in "who am I via this server" — unlocks
author:@me and review-requested:@me search queries. New
context/ tool category reserves space for future identity
tools (notifications, assigned items) without polluting PR
or issue categories.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
```

---

## Task 2: PR state transitions — `gh_ready_pr`, `gh_draft_pr`, `gh_reopen_pr`

**Files:**

- Modify: `local-gh-mcp/internal/gh/gh.go` (add `ReadyPR`, `DraftPR`, `ReopenPR` methods)
- Modify: `local-gh-mcp/internal/tools/tools.go` (add to `GHClient` interface, add 3 dispatch cases)
- Modify: `local-gh-mcp/internal/tools/pr.go` (add 3 tool definitions to `prTools()`; add 3 handlers)
- Modify: `local-gh-mcp/internal/tools/pr_test.go` (3 handler tests; mock fields)

### Step 2.1: Write failing tests

Append to `local-gh-mcp/internal/tools/pr_test.go`:

```go
func TestGhReadyPR(t *testing.T) {
	var capturedOwner, capturedRepo string
	var capturedNumber int
	mock := &mockGHClient{
		readyPRFunc: func(_ context.Context, owner, repo string, number int) (string, error) {
			capturedOwner, capturedRepo, capturedNumber = owner, repo, number
			return "https://github.com/x/y/pull/42", nil
		},
	}
	h := NewHandler(mock)
	res, err := h.Handle(context.Background(), gomcp.CallToolRequest{
		Params: gomcp.CallToolParams{Name: "gh_ready_pr", Arguments: map[string]any{
			"owner": "x", "repo": "y", "pr_number": float64(42),
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedOwner != "x" || capturedRepo != "y" || capturedNumber != 42 {
		t.Errorf("client args not threaded: %q/%q/#%d", capturedOwner, capturedRepo, capturedNumber)
	}
	out := textOf(res)
	if !strings.Contains(out, "PR #42 in x/y marked ready for review") {
		t.Errorf("confirmation missing, got: %s", out)
	}
}

func TestGhDraftPR(t *testing.T) {
	mock := &mockGHClient{
		draftPRFunc: func(_ context.Context, _ /*owner*/, _ /*repo*/ string, _ /*number*/ int) (string, error) {
			return "", nil
		},
	}
	h := NewHandler(mock)
	res, err := h.Handle(context.Background(), gomcp.CallToolRequest{
		Params: gomcp.CallToolParams{Name: "gh_draft_pr", Arguments: map[string]any{
			"owner": "x", "repo": "y", "pr_number": float64(7),
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(textOf(res), "PR #7 in x/y converted to draft") {
		t.Errorf("confirmation missing, got: %s", textOf(res))
	}
}

func TestGhReopenPR(t *testing.T) {
	mock := &mockGHClient{
		reopenPRFunc: func(_ context.Context, _ /*owner*/, _ /*repo*/ string, _ /*number*/ int) (string, error) {
			return "", nil
		},
	}
	h := NewHandler(mock)
	res, err := h.Handle(context.Background(), gomcp.CallToolRequest{
		Params: gomcp.CallToolParams{Name: "gh_reopen_pr", Arguments: map[string]any{
			"owner": "x", "repo": "y", "pr_number": float64(99),
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(textOf(res), "PR #99 in x/y reopened") {
		t.Errorf("confirmation missing, got: %s", textOf(res))
	}
}

func TestGhReadyPRPassesThroughGhError(t *testing.T) {
	mock := &mockGHClient{
		readyPRFunc: func(_ context.Context, _, _ string, _ int) (string, error) {
			return "", fmt.Errorf("gh pr ready failed: Pull request is already ready for review")
		},
	}
	h := NewHandler(mock)
	res, _ := h.Handle(context.Background(), gomcp.CallToolRequest{
		Params: gomcp.CallToolParams{Name: "gh_ready_pr", Arguments: map[string]any{
			"owner": "x", "repo": "y", "pr_number": float64(42),
		}},
	})
	if res == nil || !res.IsError {
		t.Fatalf("expected error result, got %+v", res)
	}
	if !strings.Contains(textOf(res), "already ready for review") {
		t.Errorf("gh error message not surfaced, got: %s", textOf(res))
	}
}
```

Also add mock fields and stub methods to the `mockGHClient` block:

```go
readyPRFunc  func(ctx context.Context, owner, repo string, number int) (string, error)
draftPRFunc  func(ctx context.Context, owner, repo string, number int) (string, error)
reopenPRFunc func(ctx context.Context, owner, repo string, number int) (string, error)
```

```go
func (m *mockGHClient) ReadyPR(ctx context.Context, owner, repo string, number int) (string, error) {
	if m.readyPRFunc != nil { return m.readyPRFunc(ctx, owner, repo, number) }
	return "", nil
}
func (m *mockGHClient) DraftPR(ctx context.Context, owner, repo string, number int) (string, error) {
	if m.draftPRFunc != nil { return m.draftPRFunc(ctx, owner, repo, number) }
	return "", nil
}
func (m *mockGHClient) ReopenPR(ctx context.Context, owner, repo string, number int) (string, error) {
	if m.reopenPRFunc != nil { return m.reopenPRFunc(ctx, owner, repo, number) }
	return "", nil
}
```

### Step 2.2: Run tests — verify they fail

```bash
cd local-gh-mcp && go test ./internal/tools/ -run "TestGh(Ready|Draft|Reopen)PR"
```

Expected: compile error on mock fields not existing, or dispatch error for unknown tool names.

### Step 2.3: Add client methods

In `local-gh-mcp/internal/gh/gh.go` (near `ClosePR`):

```go
func (c *Client) ReadyPR(_ context.Context, owner, repo string, number int) (string, error) {
	out, err := c.runner.Run("gh", "pr", "ready", fmt.Sprintf("%d", number), "-R", repoFlag(owner, repo))
	if err != nil {
		return "", fmt.Errorf("gh pr ready failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func (c *Client) DraftPR(_ context.Context, owner, repo string, number int) (string, error) {
	out, err := c.runner.Run("gh", "pr", "ready", fmt.Sprintf("%d", number), "--undo", "-R", repoFlag(owner, repo))
	if err != nil {
		return "", fmt.Errorf("gh pr ready --undo failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func (c *Client) ReopenPR(_ context.Context, owner, repo string, number int) (string, error) {
	out, err := c.runner.Run("gh", "pr", "reopen", fmt.Sprintf("%d", number), "-R", repoFlag(owner, repo))
	if err != nil {
		return "", fmt.Errorf("gh pr reopen failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
```

### Step 2.4: Add to `GHClient` interface

In `local-gh-mcp/internal/tools/tools.go` `GHClient` interface:

```go
ReadyPR(ctx context.Context, owner, repo string, number int) (string, error)
DraftPR(ctx context.Context, owner, repo string, number int) (string, error)
ReopenPR(ctx context.Context, owner, repo string, number int) (string, error)
```

### Step 2.5: Add tool definitions

In `local-gh-mcp/internal/tools/pr.go`, inside `prTools()`, append three tool definitions alongside `gh_close_pr`. Model their shape on the existing `gh_close_pr` (copy its schema, keep `owner`/`repo`/`pr_number` required, no other params):

```go
{
    Name:        "gh_ready_pr",
    Description: "Mark a draft pull request as ready for review (`gh pr ready`). See also gh_draft_pr to convert back, gh_close_pr / gh_reopen_pr for state transitions.",
    Annotations: annIdempotent,
    InputSchema: prNumberOnlySchema("Pull request number to mark ready."),
},
{
    Name:        "gh_draft_pr",
    Description: "Convert a pull request back to draft (`gh pr ready --undo`). See also gh_ready_pr.",
    Annotations: annIdempotent,
    InputSchema: prNumberOnlySchema("Pull request number to convert to draft."),
},
{
    Name:        "gh_reopen_pr",
    Description: "Reopen a closed pull request (`gh pr reopen`). See also gh_close_pr.",
    Annotations: annIdempotent,
    InputSchema: prNumberOnlySchema("Pull request number to reopen."),
},
```

Add a small helper near the top of `pr.go` (if it doesn't already exist — check first; if a similar schema-builder exists, use that instead):

```go
func prNumberOnlySchema(prDesc string) gomcp.ToolInputSchema {
	return gomcp.ToolInputSchema{
		Type: "object",
		Properties: map[string]any{
			"owner":     map[string]any{"type": "string", "description": "Repository owner."},
			"repo":      map[string]any{"type": "string", "description": "Repository name."},
			"pr_number": map[string]any{"type": "number", "description": prDesc},
		},
		Required: []string{"owner", "repo", "pr_number"},
	}
}
```

### Step 2.6: Add handlers

In `local-gh-mcp/internal/tools/pr.go`, near `handleClosePR`:

```go
func (h *Handler) handleReadyPR(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	owner, repo, err := requireOwnerRepo(req)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	number, ok := intFromArgs(req.GetArguments(), "pr_number")
	if !ok {
		return gomcp.NewToolResultError("gh_ready_pr: required field missing: pr_number"), nil
	}
	if _, err := h.gh.ReadyPR(ctx, owner, repo, number); err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return gomcp.NewToolResultText(fmt.Sprintf("PR #%d in %s/%s marked ready for review", number, owner, repo)), nil
}

func (h *Handler) handleDraftPR(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	owner, repo, err := requireOwnerRepo(req)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	number, ok := intFromArgs(req.GetArguments(), "pr_number")
	if !ok {
		return gomcp.NewToolResultError("gh_draft_pr: required field missing: pr_number"), nil
	}
	if _, err := h.gh.DraftPR(ctx, owner, repo, number); err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return gomcp.NewToolResultText(fmt.Sprintf("PR #%d in %s/%s converted to draft", number, owner, repo)), nil
}

func (h *Handler) handleReopenPR(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	owner, repo, err := requireOwnerRepo(req)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	number, ok := intFromArgs(req.GetArguments(), "pr_number")
	if !ok {
		return gomcp.NewToolResultError("gh_reopen_pr: required field missing: pr_number"), nil
	}
	if _, err := h.gh.ReopenPR(ctx, owner, repo, number); err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return gomcp.NewToolResultText(fmt.Sprintf("PR #%d in %s/%s reopened", number, owner, repo)), nil
}
```

### Step 2.7: Wire dispatch

In `local-gh-mcp/internal/tools/tools.go` `Handle()` switch:

```go
case "gh_ready_pr":
    return h.handleReadyPR(ctx, req)
case "gh_draft_pr":
    return h.handleDraftPR(ctx, req)
case "gh_reopen_pr":
    return h.handleReopenPR(ctx, req)
```

### Step 2.8: Run tests, audit

```bash
cd local-gh-mcp && go test ./internal/tools/ -run "TestGh(Ready|Draft|Reopen)PR" -v && make test && make lint
```

Expected: new tests PASS; no regressions; no lint errors.

### Step 2.9: Commit

```bash
git add local-gh-mcp/internal/gh/gh.go local-gh-mcp/internal/tools/tools.go local-gh-mcp/internal/tools/pr.go local-gh-mcp/internal/tools/pr_test.go
git commit -F- <<'EOF'
feat(local-gh-mcp): add PR state transition tools

Adds gh_ready_pr, gh_draft_pr, gh_reopen_pr — each maps 1:1
to a gh subcommand, symmetric with the existing gh_close_pr.
All three are idempotent (readyHint=false, idempotentHint=true).
gh errors on state mismatch ("already ready") pass through
unchanged so agents get actionable feedback.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
```

---

## Task 3: `gh_list_pr_files`

**Files:**

- Modify: `local-gh-mcp/internal/gh/gh.go` (add `ListPRFiles`)
- Modify: `local-gh-mcp/internal/tools/tools.go` (add to `GHClient` interface; dispatch case)
- Modify: `local-gh-mcp/internal/tools/pr.go` (tool def, handler)
- Modify: `local-gh-mcp/internal/tools/pr_test.go` (test + mock)
- Modify: `local-gh-mcp/internal/format/github.go` (add `PRFile` struct + `FormatPRFiles` formatter — follow existing `FormatComments` pattern)

### Step 3.1: Write failing test

Append to `local-gh-mcp/internal/tools/pr_test.go`:

```go
func TestGhListPRFiles(t *testing.T) {
	mock := &mockGHClient{
		listPRFilesFunc: func(_ context.Context, _, _ string, _ /*number*/ int, _ /*limit*/ int) (string, error) {
			return `[
				{"filename":"a.go","status":"modified","additions":12,"deletions":3},
				{"filename":"b.go","status":"added","additions":5,"deletions":0}
			]`, nil
		},
	}
	h := NewHandler(mock)
	res, err := h.Handle(context.Background(), gomcp.CallToolRequest{
		Params: gomcp.CallToolParams{Name: "gh_list_pr_files", Arguments: map[string]any{
			"owner": "x", "repo": "y", "pr_number": float64(42),
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := textOf(res)
	for _, want := range []string{"`a.go`", "+12/-3", "(modified)", "`b.go`", "+5/-0", "(added)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestGhListPRFilesTruncates(t *testing.T) {
	// Build a 40-file payload; with limit=30 (default), trailer should say "showing 30 of 40".
	parts := make([]string, 40)
	for i := range parts {
		parts[i] = fmt.Sprintf(`{"filename":"f%d.go","status":"modified","additions":1,"deletions":0}`, i)
	}
	payload := "[" + strings.Join(parts, ",") + "]"
	mock := &mockGHClient{
		listPRFilesFunc: func(_ context.Context, _, _ string, _, _ int) (string, error) { return payload, nil },
	}
	h := NewHandler(mock)
	res, _ := h.Handle(context.Background(), gomcp.CallToolRequest{
		Params: gomcp.CallToolParams{Name: "gh_list_pr_files", Arguments: map[string]any{
			"owner": "x", "repo": "y", "pr_number": float64(1),
		}},
	})
	if !strings.Contains(textOf(res), "showing 30 of 40") {
		t.Errorf("expected truncation trailer, got: %s", textOf(res))
	}
}
```

Mock additions:

```go
listPRFilesFunc func(ctx context.Context, owner, repo string, number, limit int) (string, error)
// method:
func (m *mockGHClient) ListPRFiles(ctx context.Context, owner, repo string, number, limit int) (string, error) {
	if m.listPRFilesFunc != nil { return m.listPRFilesFunc(ctx, owner, repo, number, limit) }
	return "", nil
}
```

### Step 3.2: Run — verify failing

```bash
cd local-gh-mcp && go test ./internal/tools/ -run TestGhListPRFiles
```

### Step 3.3: Add formatter

Append to `local-gh-mcp/internal/format/github.go`:

```go
type PRFile struct {
	Filename  string `json:"filename"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

func FormatPRFiles(files []PRFile, limit int) string {
	total := len(files)
	if limit > 0 && total > limit {
		files = files[:limit]
	}
	var b strings.Builder
	for _, f := range files {
		fmt.Fprintf(&b, "- `%s` — +%d/-%d (%s)\n", f.Filename, f.Additions, f.Deletions, f.Status)
	}
	if limit > 0 && total > limit {
		fmt.Fprintf(&b, "\n[truncated — showing %d of %d files]\n", limit, total)
	}
	return b.String()
}
```

### Step 3.4: Add client method

In `local-gh-mcp/internal/gh/gh.go`:

```go
func (c *Client) ListPRFiles(_ context.Context, owner, repo string, number, limit int) (string, error) {
	// Request one extra so the handler can detect truncation; we pass per_page=100 max.
	perPage := limit + 1
	if perPage > 100 {
		perPage = 100
	}
	out, err := c.runner.Run("gh", "api",
		fmt.Sprintf("repos/%s/%s/pulls/%d/files?per_page=%d", owner, repo, number, perPage),
	)
	if err != nil {
		return "", fmt.Errorf("gh api pulls/%d/files failed: %s", number, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
```

### Step 3.5: Add tool def and handler

In `local-gh-mcp/internal/tools/pr.go` `prTools()`:

```go
{
    Name:        "gh_list_pr_files",
    Description: "List files touched by a pull request with +/- counts per file (no diff content). Use gh_diff_pr if you need diff hunks. Results truncated at `limit` (default 30, max 100).",
    Annotations: annRead,
    InputSchema: gomcp.ToolInputSchema{
        Type: "object",
        Properties: map[string]any{
            "owner":     map[string]any{"type": "string", "description": "Repository owner."},
            "repo":      map[string]any{"type": "string", "description": "Repository name."},
            "pr_number": map[string]any{"type": "number", "description": "Pull request number."},
            "limit":     map[string]any{"type": "number", "default": 30, "description": "Max files shown (default 30, max 100)."},
        },
        Required: []string{"owner", "repo", "pr_number"},
    },
},
```

Handler:

```go
func (h *Handler) handleListPRFiles(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	owner, repo, err := requireOwnerRepo(req)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	args := req.GetArguments()
	number, ok := intFromArgs(args, "pr_number")
	if !ok {
		return gomcp.NewToolResultError("gh_list_pr_files: required field missing: pr_number"), nil
	}
	limit := clampLimit(intFromArgsOr(args, "limit", 30))
	raw, err := h.gh.ListPRFiles(ctx, owner, repo, number, limit)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var files []format.PRFile
	if err := json.Unmarshal([]byte(raw), &files); err != nil {
		return parseError("gh_list_pr_files", raw, err), nil
	}
	return gomcp.NewToolResultText(format.FormatPRFiles(files, limit)), nil
}
```

(Note: `clampLimit` and `intFromArgsOr` are helpers in `tools.go`; if named differently, use the existing equivalents — check `tools.go` for the canonical names.)

### Step 3.6: Update interface + dispatch

In `local-gh-mcp/internal/tools/tools.go`:

```go
// GHClient interface:
ListPRFiles(ctx context.Context, owner, repo string, number, limit int) (string, error)
// Handle() switch:
case "gh_list_pr_files":
    return h.handleListPRFiles(ctx, req)
```

### Step 3.7: Run tests, audit

```bash
cd local-gh-mcp && go test ./... -v -run TestGhListPRFiles && make test && make lint
```

### Step 3.8: Commit

```bash
git add local-gh-mcp/internal/gh/gh.go local-gh-mcp/internal/tools/tools.go local-gh-mcp/internal/tools/pr.go local-gh-mcp/internal/tools/pr_test.go local-gh-mcp/internal/format/github.go
git commit -F- <<'EOF'
feat(local-gh-mcp): add gh_list_pr_files

Lists files a PR touches with +/- counts and status, without
diff content. Complements gh_diff_pr, which is often too large
for agent context windows.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
```

---

## Task 4: `gh_list_branches`

**Files:**

- Create: `local-gh-mcp/internal/tools/branch.go`
- Create: `local-gh-mcp/internal/tools/branch_test.go`
- Modify: `local-gh-mcp/internal/gh/gh.go` (add `ListBranches`)
- Modify: `local-gh-mcp/internal/tools/tools.go` (interface method, `Tools()` aggregation via new `branchTools()`, dispatch case)
- Modify: `local-gh-mcp/internal/format/github.go` (add `Branch` struct + `FormatBranches` formatter)
- Modify: `local-gh-mcp/internal/tools/pr_test.go` (mock field + stub for shared `mockGHClient`)

### Step 4.1: Write failing test

Create `local-gh-mcp/internal/tools/branch_test.go`:

```go
package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"

	gomcp "github.com/mark3labs/mcp-go/mcp"
)

func TestGhListBranches(t *testing.T) {
	mock := &mockGHClient{
		listBranchesFunc: func(_ context.Context, _, _ string, _ int) (string, error) {
			return `[
				{"name":"main","commit":{"sha":"abc1234xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}},
				{"name":"feat/foo","commit":{"sha":"def5678xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}}
			]`, nil
		},
	}
	h := NewHandler(mock)
	res, err := h.Handle(context.Background(), gomcp.CallToolRequest{
		Params: gomcp.CallToolParams{Name: "gh_list_branches", Arguments: map[string]any{
			"owner": "x", "repo": "y",
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := textOf(res)
	for _, want := range []string{"`main`", "(abc1234)", "`feat/foo`", "(def5678)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestGhListBranchesTruncates(t *testing.T) {
	parts := make([]string, 40)
	for i := range parts {
		parts[i] = fmt.Sprintf(`{"name":"b%d","commit":{"sha":"0000000xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}}`, i)
	}
	payload := "[" + strings.Join(parts, ",") + "]"
	mock := &mockGHClient{
		listBranchesFunc: func(_ context.Context, _, _ string, _ int) (string, error) { return payload, nil },
	}
	h := NewHandler(mock)
	res, _ := h.Handle(context.Background(), gomcp.CallToolRequest{
		Params: gomcp.CallToolParams{Name: "gh_list_branches", Arguments: map[string]any{
			"owner": "x", "repo": "y",
		}},
	})
	if !strings.Contains(textOf(res), "showing 30 of 40") {
		t.Errorf("expected truncation trailer")
	}
}
```

Mock field + stub in `pr_test.go`:

```go
listBranchesFunc func(ctx context.Context, owner, repo string, limit int) (string, error)
func (m *mockGHClient) ListBranches(ctx context.Context, owner, repo string, limit int) (string, error) {
	if m.listBranchesFunc != nil { return m.listBranchesFunc(ctx, owner, repo, limit) }
	return "", nil
}
```

### Step 4.2: Run — verify failing

```bash
cd local-gh-mcp && go test ./internal/tools/ -run TestGhListBranches
```

### Step 4.3: Add formatter

Append to `local-gh-mcp/internal/format/github.go`:

```go
type Branch struct {
	Name   string `json:"name"`
	Commit struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

func FormatBranches(branches []Branch, limit int) string {
	total := len(branches)
	if limit > 0 && total > limit {
		branches = branches[:limit]
	}
	var b strings.Builder
	for _, br := range branches {
		shortSHA := br.Commit.SHA
		if len(shortSHA) > 7 {
			shortSHA = shortSHA[:7]
		}
		fmt.Fprintf(&b, "- `%s` (%s)\n", br.Name, shortSHA)
	}
	if limit > 0 && total > limit {
		fmt.Fprintf(&b, "\n[truncated — showing %d of %d branches]\n", limit, total)
	}
	return b.String()
}
```

### Step 4.4: Add client method

In `local-gh-mcp/internal/gh/gh.go`:

```go
func (c *Client) ListBranches(_ context.Context, owner, repo string, limit int) (string, error) {
	perPage := limit + 1
	if perPage > 100 {
		perPage = 100
	}
	out, err := c.runner.Run("gh", "api",
		fmt.Sprintf("repos/%s/%s/branches?per_page=%d", owner, repo, perPage),
	)
	if err != nil {
		return "", fmt.Errorf("gh api branches failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
```

### Step 4.5: Create `branch.go`

Create `local-gh-mcp/internal/tools/branch.go`:

```go
package tools

import (
	"context"
	"encoding/json"

	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/format"
	gomcp "github.com/mark3labs/mcp-go/mcp"
)

func (h *Handler) branchTools() []gomcp.Tool {
	return []gomcp.Tool{
		{
			Name:        "gh_list_branches",
			Description: "List branches in a GitHub repository, newest first. Each entry shows the branch name and its HEAD commit SHA. Results truncated at `limit` (default 30, max 100).",
			Annotations: annRead,
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"owner": map[string]any{"type": "string", "description": "Repository owner."},
					"repo":  map[string]any{"type": "string", "description": "Repository name."},
					"limit": map[string]any{"type": "number", "default": 30, "description": "Max branches shown (default 30, max 100)."},
				},
				Required: []string{"owner", "repo"},
			},
		},
	}
}

func (h *Handler) handleListBranches(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	owner, repo, err := requireOwnerRepo(req)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	limit := clampLimit(intFromArgsOr(req.GetArguments(), "limit", 30))
	raw, err := h.gh.ListBranches(ctx, owner, repo, limit)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var branches []format.Branch
	if err := json.Unmarshal([]byte(raw), &branches); err != nil {
		return parseError("gh_list_branches", raw, err), nil
	}
	return gomcp.NewToolResultText(format.FormatBranches(branches, limit)), nil
}
```

### Step 4.6: Wire into `Tools()` + interface + dispatch

In `local-gh-mcp/internal/tools/tools.go`:

- Interface: `ListBranches(ctx context.Context, owner, repo string, limit int) (string, error)`
- `Tools()`: append `h.branchTools()...`
- `Handle()`: `case "gh_list_branches": return h.handleListBranches(ctx, req)`

### Step 4.7: Run tests

```bash
cd local-gh-mcp && go test ./... -v -run TestGhListBranches && make test
```

### Step 4.8: Commit

```bash
git add local-gh-mcp/internal/gh/gh.go local-gh-mcp/internal/tools/tools.go local-gh-mcp/internal/tools/branch.go local-gh-mcp/internal/tools/branch_test.go local-gh-mcp/internal/tools/pr_test.go local-gh-mcp/internal/format/github.go
git commit -F- <<'EOF'
feat(local-gh-mcp): add gh_list_branches

Lists branches in a repo with HEAD commit short-SHAs. Closes
the gap for agents composing branch-based PRs or inspecting
branch state without cloning the repo.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
```

---

## Task 5: Surface job IDs in `gh_view_run` + add `gh_view_run_job_logs`

This task has two coupled parts — the new tool needs `job_id` to be discoverable, and `gh_view_run` is the discovery path. Single commit.

**Files:**

- Modify: `local-gh-mcp/internal/format/github.go` (add `DatabaseID` to `Job` struct; update `FormatRunView` output to include job ID)
- Modify: `local-gh-mcp/internal/gh/gh.go` (update `ViewRun`'s `--json` field list to request `jobs.databaseId`; add `ViewRunJobLog`)
- Modify: `local-gh-mcp/internal/tools/run.go` (add `gh_view_run_job_logs` tool def + handler)
- Modify: `local-gh-mcp/internal/tools/tools.go` (interface + dispatch)
- Modify: `local-gh-mcp/internal/tools/run_test.go` (update existing `gh_view_run` test expectations; add new test for `gh_view_run_job_logs`)
- Modify: `local-gh-mcp/internal/tools/pr_test.go` (mock field + stub)

### Step 5.1: Update `Job` struct and `FormatRunView`

In `local-gh-mcp/internal/format/github.go`, amend `Job`:

```go
type Job struct {
	DatabaseID int64  `json:"databaseId"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
}
```

In `FormatRunView` (the function that renders jobs, around line 458-467), update the per-job line to include the ID. Example:

```go
// Before: fmt.Fprintf(&b, "- %s — %s\n", j.Name, j.Conclusion)
// After:
fmt.Fprintf(&b, "- `%s` (job_id: %d) — %s\n", j.Name, j.DatabaseID, j.Conclusion)
```

(Confirm the existing line shape by reading the function before editing; keep other fields intact.)

### Step 5.2: Update `ViewRun` to request `databaseId`

In `local-gh-mcp/internal/gh/gh.go`, find the `ViewRun` function and its `--json` field list. Append `jobs.databaseId` (or include `databaseId` inside the `jobs` JSON path selector — mirror whatever gh's `--json` flag accepts for nested fields). A working incantation:

```go
out, err := c.runner.Run("gh", "run", "view", fmt.Sprintf("%d", runID),
    "-R", repoFlag(owner, repo),
    "--json", "status,conclusion,workflowName,displayTitle,event,headBranch,url,jobs",
)
```

(The `jobs` field, when requested, returns each job with `databaseId` populated by default.)

### Step 5.3: Update existing `gh_view_run` test expectations

In `local-gh-mcp/internal/tools/run_test.go`, find the existing `TestGhViewRun` (or equivalently named test) and update the mock JSON to include `"databaseId": 12345` for each job. Assert the output contains `(job_id: 12345)`.

### Step 5.4: Add failing test for `gh_view_run_job_logs`

Append to `local-gh-mcp/internal/tools/run_test.go`:

```go
func TestGhViewRunJobLogs(t *testing.T) {
	var capturedJobID int64
	var capturedTail int
	mock := &mockGHClient{
		viewRunJobLogFunc: func(_ context.Context, _, _ string, jobID int64, tail int) (string, error) {
			capturedJobID, capturedTail = jobID, tail
			return "line 1\nline 2\nline 3\n", nil
		},
	}
	h := NewHandler(mock)
	res, err := h.Handle(context.Background(), gomcp.CallToolRequest{
		Params: gomcp.CallToolParams{Name: "gh_view_run_job_logs", Arguments: map[string]any{
			"owner": "x", "repo": "y", "job_id": float64(12345),
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedJobID != 12345 {
		t.Errorf("job_id not threaded: %d", capturedJobID)
	}
	if capturedTail != 500 {
		t.Errorf("default tail_lines should be 500, got %d", capturedTail)
	}
	if !strings.Contains(textOf(res), "line 1") {
		t.Errorf("log body missing, got: %s", textOf(res))
	}
}

func TestGhViewRunJobLogsRespectsTailLines(t *testing.T) {
	var capturedTail int
	mock := &mockGHClient{
		viewRunJobLogFunc: func(_ context.Context, _, _ string, _ int64, tail int) (string, error) {
			capturedTail = tail
			return "", nil
		},
	}
	h := NewHandler(mock)
	_, _ = h.Handle(context.Background(), gomcp.CallToolRequest{
		Params: gomcp.CallToolParams{Name: "gh_view_run_job_logs", Arguments: map[string]any{
			"owner": "x", "repo": "y", "job_id": float64(1), "tail_lines": float64(100),
		}},
	})
	if capturedTail != 100 {
		t.Errorf("tail_lines not threaded: %d", capturedTail)
	}
}
```

Mock additions in `pr_test.go`:

```go
viewRunJobLogFunc func(ctx context.Context, owner, repo string, jobID int64, tail int) (string, error)
func (m *mockGHClient) ViewRunJobLog(ctx context.Context, owner, repo string, jobID int64, tail int) (string, error) {
	if m.viewRunJobLogFunc != nil { return m.viewRunJobLogFunc(ctx, owner, repo, jobID, tail) }
	return "", nil
}
```

### Step 5.5: Add `ViewRunJobLog` client method

In `local-gh-mcp/internal/gh/gh.go`:

```go
func (c *Client) ViewRunJobLog(_ context.Context, owner, repo string, jobID int64, tailLines int) (string, error) {
	out, err := c.runner.Run("gh", "run", "view", "--job", fmt.Sprintf("%d", jobID), "--log", "-R", repoFlag(owner, repo))
	if err != nil {
		return "", fmt.Errorf("gh run view --job failed: %s", strings.TrimSpace(string(out)))
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if tailLines > 0 && len(lines) > tailLines {
		lines = lines[len(lines)-tailLines:]
	}
	return strings.Join(lines, "\n"), nil
}
```

### Step 5.6: Add tool def + handler

In `local-gh-mcp/internal/tools/run.go`, append to `runTools()`:

```go
{
    Name:        "gh_view_run_job_logs",
    Description: "Fetch logs for a single workflow job by ID. Returns the last `tail_lines` lines (default 500, max 5000). Complementary to gh_view_run's log_failed=true, which concatenates all failed-job logs. Use gh_view_run first to discover job IDs.",
    Annotations: annRead,
    InputSchema: gomcp.ToolInputSchema{
        Type: "object",
        Properties: map[string]any{
            "owner":      map[string]any{"type": "string", "description": "Repository owner."},
            "repo":       map[string]any{"type": "string", "description": "Repository name."},
            "job_id":     map[string]any{"type": "number", "description": "GitHub job ID (obtained from gh_view_run output)."},
            "tail_lines": map[string]any{"type": "number", "default": 500, "description": "Return the last N lines (default 500, max 5000)."},
        },
        Required: []string{"owner", "repo", "job_id"},
    },
},
```

Handler:

```go
func (h *Handler) handleViewRunJobLogs(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	owner, repo, err := requireOwnerRepo(req)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	args := req.GetArguments()
	jobIDInt, ok := intFromArgs(args, "job_id")
	if !ok {
		return gomcp.NewToolResultError("gh_view_run_job_logs: required field missing: job_id"), nil
	}
	tail := intFromArgsOr(args, "tail_lines", 500)
	if tail > 5000 {
		tail = 5000
	}
	out, err := h.gh.ViewRunJobLog(ctx, owner, repo, int64(jobIDInt), tail)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return gomcp.NewToolResultText(out), nil
}
```

### Step 5.7: Wire interface + dispatch

In `local-gh-mcp/internal/tools/tools.go`:

```go
// interface:
ViewRunJobLog(ctx context.Context, owner, repo string, jobID int64, tailLines int) (string, error)
// dispatch:
case "gh_view_run_job_logs":
    return h.handleViewRunJobLogs(ctx, req)
```

### Step 5.8: Run tests, audit

```bash
cd local-gh-mcp && go test ./... -v -run "TestGhViewRun" && make test && make lint
```

### Step 5.9: Commit

```bash
git add local-gh-mcp/internal/gh/gh.go local-gh-mcp/internal/tools/tools.go local-gh-mcp/internal/tools/run.go local-gh-mcp/internal/tools/run_test.go local-gh-mcp/internal/tools/pr_test.go local-gh-mcp/internal/format/github.go
git commit -F- <<'EOF'
feat(local-gh-mcp): add gh_view_run_job_logs

Per-job log access, complementary to gh_view_run's
log_failed=true which concatenates all failed-job logs
(often too large for a multi-job failure). gh_view_run
now surfaces job IDs in its output so agents have a clean
discovery path.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
```

---

## Task 6: `gh_list_releases` + `gh_view_release`

**Files:**

- Create: `local-gh-mcp/internal/tools/release.go`
- Create: `local-gh-mcp/internal/tools/release_test.go`
- Modify: `local-gh-mcp/internal/gh/gh.go` (add `ListReleases`, `ViewRelease`)
- Modify: `local-gh-mcp/internal/tools/tools.go` (interface, `Tools()` aggregation, 2 dispatch cases)
- Modify: `local-gh-mcp/internal/format/github.go` (add `Release` + `ReleaseAsset` structs; `FormatReleases`, `FormatRelease` formatters)
- Modify: `local-gh-mcp/internal/tools/pr_test.go` (mock fields + stubs)

### Step 6.1: Write failing tests

Create `local-gh-mcp/internal/tools/release_test.go`:

```go
package tools

import (
	"context"
	"strings"
	"testing"

	gomcp "github.com/mark3labs/mcp-go/mcp"
)

func TestGhListReleases(t *testing.T) {
	mock := &mockGHClient{
		listReleasesFunc: func(_ context.Context, _, _ string, _ int) (string, error) {
			return `[
				{"tagName":"v1.2.3","name":"Feature X","publishedAt":"2026-04-15T10:00:00Z","isDraft":false,"isPrerelease":false},
				{"tagName":"v1.3.0-rc1","name":"","publishedAt":"2026-04-20T10:00:00Z","isDraft":false,"isPrerelease":true}
			]`, nil
		},
	}
	h := NewHandler(mock)
	res, err := h.Handle(context.Background(), gomcp.CallToolRequest{
		Params: gomcp.CallToolParams{Name: "gh_list_releases", Arguments: map[string]any{
			"owner": "x", "repo": "y",
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := textOf(res)
	for _, want := range []string{"`v1.2.3`", "\"Feature X\"", "`v1.3.0-rc1`", "[prerelease]"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestGhViewReleaseLatest(t *testing.T) {
	var capturedTag string
	mock := &mockGHClient{
		viewReleaseFunc: func(_ context.Context, _, _ string, tag string) (string, error) {
			capturedTag = tag
			return `{"tagName":"v2.0.0","name":"Big Release","author":{"login":"octocat"},"publishedAt":"2026-04-22T10:00:00Z","body":"Release notes","assets":[{"name":"binary.tar.gz","size":1048576}]}`, nil
		},
	}
	h := NewHandler(mock)
	res, err := h.Handle(context.Background(), gomcp.CallToolRequest{
		Params: gomcp.CallToolParams{Name: "gh_view_release", Arguments: map[string]any{
			"owner": "x", "repo": "y",
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedTag != "" {
		t.Errorf("omitted tag should pass empty string (latest), got: %q", capturedTag)
	}
	out := textOf(res)
	for _, want := range []string{"v2.0.0", "Big Release", "@octocat", "binary.tar.gz", "1.0 MiB"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestGhViewReleaseWithTag(t *testing.T) {
	var capturedTag string
	mock := &mockGHClient{
		viewReleaseFunc: func(_ context.Context, _, _ string, tag string) (string, error) {
			capturedTag = tag
			return `{"tagName":"v1.0.0","name":"","body":"","assets":[]}`, nil
		},
	}
	h := NewHandler(mock)
	_, _ = h.Handle(context.Background(), gomcp.CallToolRequest{
		Params: gomcp.CallToolParams{Name: "gh_view_release", Arguments: map[string]any{
			"owner": "x", "repo": "y", "tag": "v1.0.0",
		}},
	})
	if capturedTag != "v1.0.0" {
		t.Errorf("tag not threaded: %q", capturedTag)
	}
}
```

Mock additions in `pr_test.go`:

```go
listReleasesFunc func(ctx context.Context, owner, repo string, limit int) (string, error)
viewReleaseFunc  func(ctx context.Context, owner, repo string, tag string) (string, error)

func (m *mockGHClient) ListReleases(ctx context.Context, owner, repo string, limit int) (string, error) {
	if m.listReleasesFunc != nil { return m.listReleasesFunc(ctx, owner, repo, limit) }
	return "", nil
}
func (m *mockGHClient) ViewRelease(ctx context.Context, owner, repo, tag string) (string, error) {
	if m.viewReleaseFunc != nil { return m.viewReleaseFunc(ctx, owner, repo, tag) }
	return "", nil
}
```

### Step 6.2: Run — verify failing

```bash
cd local-gh-mcp && go test ./internal/tools/ -run "TestGh(ListReleases|ViewRelease)"
```

### Step 6.3: Add formatters

Append to `local-gh-mcp/internal/format/github.go`:

```go
type Release struct {
	TagName      string         `json:"tagName"`
	Name         string         `json:"name"`
	Author       struct{ Login string `json:"login"` } `json:"author"`
	PublishedAt  string         `json:"publishedAt"`
	Body         string         `json:"body"`
	IsDraft      bool           `json:"isDraft"`
	IsPrerelease bool           `json:"isPrerelease"`
	Assets       []ReleaseAsset `json:"assets"`
}

type ReleaseAsset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

func FormatReleases(releases []Release, limit int) string {
	total := len(releases)
	if limit > 0 && total > limit {
		releases = releases[:limit]
	}
	var b strings.Builder
	for _, r := range releases {
		line := fmt.Sprintf("- `%s`", r.TagName)
		if r.Name != "" {
			line += fmt.Sprintf(" — %q", r.Name)
		}
		if r.PublishedAt != "" {
			date := r.PublishedAt
			if len(date) >= 10 {
				date = date[:10]
			}
			line += fmt.Sprintf(" (published %s)", date)
		}
		if r.IsDraft {
			line += " [draft]"
		}
		if r.IsPrerelease {
			line += " [prerelease]"
		}
		b.WriteString(line + "\n")
	}
	if limit > 0 && total > limit {
		fmt.Fprintf(&b, "\n[truncated — showing %d of %d releases]\n", limit, total)
	}
	return b.String()
}

func FormatRelease(r Release, maxBodyLength int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s", r.TagName)
	if r.Name != "" {
		fmt.Fprintf(&b, " — %s", r.Name)
	}
	b.WriteString("\n")
	if r.Author.Login != "" {
		fmt.Fprintf(&b, "by @%s", r.Author.Login)
	}
	if r.PublishedAt != "" {
		date := r.PublishedAt
		if len(date) >= 10 { date = date[:10] }
		fmt.Fprintf(&b, " · published %s", date)
	}
	b.WriteString("\n\n")
	body := truncateBody(r.Body, maxBodyLength)
	b.WriteString(body)
	b.WriteString("\n")
	if len(r.Assets) > 0 {
		b.WriteString("\nAssets:\n")
		for _, a := range r.Assets {
			fmt.Fprintf(&b, "- %s (%s)\n", a.Name, humanBytes(a.Size))
		}
	}
	return b.String()
}

// humanBytes renders a byte count as e.g. "512 B", "1.5 KiB", "3.2 MiB".
func humanBytes(n int64) string {
	const (
		KiB = 1 << 10
		MiB = 1 << 20
		GiB = 1 << 30
	)
	switch {
	case n < KiB:
		return fmt.Sprintf("%d B", n)
	case n < MiB:
		return fmt.Sprintf("%.1f KiB", float64(n)/KiB)
	case n < GiB:
		return fmt.Sprintf("%.1f MiB", float64(n)/MiB)
	default:
		return fmt.Sprintf("%.1f GiB", float64(n)/GiB)
	}
}
```

(Note: `truncateBody` is an existing helper — verify its name in `format/github.go` and substitute if different.)

### Step 6.4: Add client methods

In `local-gh-mcp/internal/gh/gh.go`:

```go
func (c *Client) ListReleases(_ context.Context, owner, repo string, limit int) (string, error) {
	args := []string{"release", "list", "-R", repoFlag(owner, repo), "--limit", fmt.Sprintf("%d", limit+1), "--json", "tagName,name,publishedAt,isDraft,isPrerelease"}
	out, err := c.runner.Run("gh", args...)
	if err != nil {
		return "", fmt.Errorf("gh release list failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func (c *Client) ViewRelease(_ context.Context, owner, repo, tag string) (string, error) {
	args := []string{"release", "view"}
	if tag != "" {
		args = append(args, tag)
	}
	args = append(args, "-R", repoFlag(owner, repo), "--json", "tagName,name,author,publishedAt,body,isDraft,isPrerelease,assets")
	out, err := c.runner.Run("gh", args...)
	if err != nil {
		return "", fmt.Errorf("gh release view failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
```

### Step 6.5: Create `release.go`

Create `local-gh-mcp/internal/tools/release.go`:

```go
package tools

import (
	"context"
	"encoding/json"

	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/format"
	gomcp "github.com/mark3labs/mcp-go/mcp"
)

func (h *Handler) releaseTools() []gomcp.Tool {
	return []gomcp.Tool{
		{
			Name:        "gh_list_releases",
			Description: "List releases in a repository, newest first. Shows tag, title, publish date, and draft/pre-release flags. Use gh_view_release for full notes and assets.",
			Annotations: annRead,
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"owner": map[string]any{"type": "string", "description": "Repository owner."},
					"repo":  map[string]any{"type": "string", "description": "Repository name."},
					"limit": map[string]any{"type": "number", "default": 30, "description": "Max releases shown (default 30, max 100)."},
				},
				Required: []string{"owner", "repo"},
			},
		},
		{
			Name:        "gh_view_release",
			Description: "Show a single release with notes and assets. Omit `tag` to get the latest release. Assets are listed by name and size; download URLs are not surfaced (signed URLs expire).",
			Annotations: annRead,
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"owner":           map[string]any{"type": "string", "description": "Repository owner."},
					"repo":            map[string]any{"type": "string", "description": "Repository name."},
					"tag":             map[string]any{"type": "string", "description": "Release tag (optional; omit for the latest release)."},
					"max_body_length": map[string]any{"type": "number", "default": 2000, "description": "Max release-notes body characters (default 2000)."},
				},
				Required: []string{"owner", "repo"},
			},
		},
	}
}

func (h *Handler) handleListReleases(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	owner, repo, err := requireOwnerRepo(req)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	limit := clampLimit(intFromArgsOr(req.GetArguments(), "limit", 30))
	raw, err := h.gh.ListReleases(ctx, owner, repo, limit)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var releases []format.Release
	if err := json.Unmarshal([]byte(raw), &releases); err != nil {
		return parseError("gh_list_releases", raw, err), nil
	}
	return gomcp.NewToolResultText(format.FormatReleases(releases, limit)), nil
}

func (h *Handler) handleViewRelease(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	owner, repo, err := requireOwnerRepo(req)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	args := req.GetArguments()
	tag, _ := stringFromArgs(args, "tag")
	maxBody := clampMaxBodyLength(intFromArgsOr(args, "max_body_length", 2000))
	raw, err := h.gh.ViewRelease(ctx, owner, repo, tag)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var rel format.Release
	if err := json.Unmarshal([]byte(raw), &rel); err != nil {
		return parseError("gh_view_release", raw, err), nil
	}
	return gomcp.NewToolResultText(format.FormatRelease(rel, maxBody)), nil
}
```

### Step 6.6: Wire interface + `Tools()` + dispatch

In `local-gh-mcp/internal/tools/tools.go`:

- Interface methods: `ListReleases`, `ViewRelease`
- `Tools()`: append `h.releaseTools()...`
- `Handle()`:
  ```go
  case "gh_list_releases":
      return h.handleListReleases(ctx, req)
  case "gh_view_release":
      return h.handleViewRelease(ctx, req)
  ```

### Step 6.7: Run tests, audit

```bash
cd local-gh-mcp && go test ./... -v -run "TestGh(ListReleases|ViewRelease)" && make test && make lint
```

### Step 6.8: Commit

```bash
git add local-gh-mcp/internal/gh/gh.go local-gh-mcp/internal/tools/tools.go local-gh-mcp/internal/tools/release.go local-gh-mcp/internal/tools/release_test.go local-gh-mcp/internal/tools/pr_test.go local-gh-mcp/internal/format/github.go
git commit -F- <<'EOF'
feat(local-gh-mcp): add gh_list_releases and gh_view_release

Release-metadata tools for release-notes generation and
versioned-bug triage. gh_view_release defaults to the latest
release when `tag` is omitted. Assets are shown by name and
size only; download URLs are not surfaced because signed
URLs expire and sandboxed agents can't typically fetch them.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
```

---

## Task 7: `gh_list_runs` — add `actor` + `event` filters

**Files:**

- Modify: `local-gh-mcp/internal/gh/gh.go` (extend `ListRunsOpts` or `ListRuns` signature with `actor`, `event`; pass `--user`, `--event` flags when set)
- Modify: `local-gh-mcp/internal/tools/tools.go` (interface signature if `ListRuns` signature changed)
- Modify: `local-gh-mcp/internal/tools/run.go` (tool def adds 2 optional string params; handler threads them through)
- Modify: `local-gh-mcp/internal/tools/run_test.go` (test that filters are threaded)

### Step 7.1: Write failing test

Append to `local-gh-mcp/internal/tools/run_test.go`:

```go
func TestGhListRunsActorEventFilters(t *testing.T) {
	var capturedActor, capturedEvent string
	mock := &mockGHClient{
		listRunsFunc: func(_ context.Context, _, _ string, opts gh.ListRunsOpts) (string, error) {
			capturedActor, capturedEvent = opts.Actor, opts.Event
			return "[]", nil
		},
	}
	h := NewHandler(mock)
	_, err := h.Handle(context.Background(), gomcp.CallToolRequest{
		Params: gomcp.CallToolParams{Name: "gh_list_runs", Arguments: map[string]any{
			"owner": "x", "repo": "y",
			"actor": "octocat", "event": "push",
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedActor != "octocat" {
		t.Errorf("actor not threaded: %q", capturedActor)
	}
	if capturedEvent != "push" {
		t.Errorf("event not threaded: %q", capturedEvent)
	}
}
```

(If `ListRuns` doesn't already take an `Opts` struct, you'll need to introduce one — see Step 7.2.)

### Step 7.2: Extend `ListRunsOpts` (or add if absent)

In `local-gh-mcp/internal/gh/gh.go`:

```go
type ListRunsOpts struct {
	Status string // existing
	Limit  int    // existing
	Actor  string // new
	Event  string // new
}

func (c *Client) ListRuns(_ context.Context, owner, repo string, opts ListRunsOpts) (string, error) {
	args := []string{"run", "list", "-R", repoFlag(owner, repo), "--json", "databaseId,status,conclusion,name,displayTitle,event,headBranch,createdAt,url"}
	if opts.Status != "" { args = append(args, "--status", opts.Status) }
	if opts.Actor != "" { args = append(args, "--user", opts.Actor) }
	if opts.Event != "" { args = append(args, "--event", opts.Event) }
	if opts.Limit > 0 { args = append(args, "--limit", fmt.Sprintf("%d", opts.Limit)) }
	out, err := c.runner.Run("gh", args...)
	if err != nil {
		return "", fmt.Errorf("gh run list failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
```

(If `ListRunsOpts` doesn't currently exist, you'll also update the interface signature and the existing handler that calls it.)

### Step 7.3: Update `GHClient` interface if signature changed

Ensure `tools.go` interface reflects the current `ListRuns` signature (probably already takes an `opts` struct — check).

### Step 7.4: Update tool def and handler

In `local-gh-mcp/internal/tools/run.go`, inside the `gh_list_runs` tool schema, append:

```go
"actor": map[string]any{"type": "string", "description": "Filter by actor login (GitHub username who triggered the run)."},
"event": map[string]any{"type": "string", "description": "Filter by triggering event (e.g. push, pull_request, schedule, workflow_dispatch)."},
```

In the `handleListRuns` handler, extract and thread through:

```go
actor, _ := stringFromArgs(args, "actor")
event, _ := stringFromArgs(args, "event")
// ...
opts := gh.ListRunsOpts{Status: status, Limit: limit, Actor: actor, Event: event}
```

### Step 7.5: Update existing `ListRuns` mock signature

The `mockGHClient.listRunsFunc` signature (in `pr_test.go`) must match the real interface. If `ListRuns` takes `(ctx, owner, repo, opts)`, the mock signature matches.

### Step 7.6: Run tests, audit

```bash
cd local-gh-mcp && go test ./internal/tools/ -run TestGhListRuns -v && make test
```

### Step 7.7: Commit

```bash
git add local-gh-mcp/internal/gh/gh.go local-gh-mcp/internal/tools/tools.go local-gh-mcp/internal/tools/run.go local-gh-mcp/internal/tools/run_test.go
git commit -F- <<'EOF'
feat(local-gh-mcp): add actor and event filters to gh_list_runs

Pass-through filters for `gh run list --user` and `--event`.
Free-form strings; description lists the common events
(push, pull_request, schedule, workflow_dispatch) since the
full set includes 40+ webhook types that drift over time.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
```

---

## Task 8: Remove `search` param from `gh_list_prs` and `gh_list_issues` (breaking)

**Files:**

- Modify: `local-gh-mcp/internal/gh/gh.go` (drop `Search` field from `ListPRsOpts` / `ListIssuesOpts`; remove flag-construction branch)
- Modify: `local-gh-mcp/internal/tools/pr.go` (remove `search` property from `gh_list_prs` schema; drop handler extraction)
- Modify: `local-gh-mcp/internal/tools/issue.go` (same for `gh_list_issues`)
- Modify: `local-gh-mcp/internal/tools/pr_test.go` / `issue_test.go` (drop tests exercising `search`; update any assertions that expected the param)
- Modify: `local-gh-mcp/internal/tools/tools.go` (only if interface signature changes; it shouldn't if `search` is on the opts struct)

### Step 8.1: Verify descriptions already redirect

Read `local-gh-mcp/internal/tools/pr.go` and find the `gh_list_prs` tool's `Description`. Confirm it includes language pointing DSL users to `gh_search_prs`. If it doesn't, adjust to e.g. `"List PRs in a repository. For cross-repo queries or GitHub search DSL (e.g. author:@me review-requested:@me), use gh_search_prs instead."`. Same for `gh_list_issues` → `gh_search_issues`.

### Step 8.2: Remove from schema

In `gh_list_prs` schema (`pr.go`): delete the `"search"` map entry from `Properties`. In `gh_list_issues` schema (`issue.go`): same.

### Step 8.3: Remove from handlers

In `pr.go` `handleListPRs`: delete the line(s) extracting `search` from args. In `issue.go` `handleListIssues`: same.

### Step 8.4: Drop from opts struct and client

In `gh.go`:

- `ListPRsOpts`: remove `Search string`.
- `ListPRs`: remove the `if opts.Search != "" { args = append(args, "--search", opts.Search) }` branch.
- Same for `ListIssuesOpts` / `ListIssues`.

### Step 8.5: Update tests

Grep for `search` in `pr_test.go` and `issue_test.go`. Any test that sets `search` in `Arguments` or asserts on `--search` flag construction must be deleted. Verify the existing list-tool tests (without `search`) still pass.

### Step 8.6: Run full audit

```bash
cd local-gh-mcp && make test && make lint && make audit
```

Expected: all green. `TestNoBareIDParameters` and annotation tests still pass automatically.

### Step 8.7: Commit

```bash
git add local-gh-mcp/internal/gh/gh.go local-gh-mcp/internal/tools/pr.go local-gh-mcp/internal/tools/issue.go local-gh-mcp/internal/tools/pr_test.go local-gh-mcp/internal/tools/issue_test.go local-gh-mcp/internal/tools/tools.go
git commit -F- <<'EOF'
feat(local-gh-mcp)!: remove search param from list tools

Drops `search` from gh_list_prs and gh_list_issues. Two
overlapping entry points for the same DSL confuses agents;
gh_search_prs / gh_search_issues are the canonical path for
query-DSL work. Descriptions already redirect.

BREAKING CHANGE: gh_list_prs and gh_list_issues no longer
accept a `search` argument.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
```

---

## Task 9: Documentation updates

**Files:**

- Modify: `local-gh-mcp/DESIGN.md` (update tool count; add new tools to the tool inventory section)
- Modify: `local-gh-mcp/README.md` (update tool list/count if it enumerates tools)
- Modify: `local-gh-mcp/CLAUDE.md` (add note about the `context` category if category listing exists)

### Step 9.1: Read current docs

Read all three files and identify sections that mention:

- The tool count (current: 28)
- Enumerated tool lists or category layouts
- The `search` param on list tools (any example usage)
- The `number` param (should already be updated from Phase 2)

### Step 9.2: Update DESIGN.md

- Tool count: 28 → 37.
- Add new tools to the inventory with one-line descriptions.
- If there's a "Tool Categories" section, add `context` (containing `gh_whoami`).
- Remove any reference to `search` on `gh_list_prs` / `gh_list_issues`.
- Note `actor`/`event` filters on `gh_list_runs`.

### Step 9.3: Update README.md

- If tool list is enumerated, add the 9 new tools to the right categories.
- If example usage mentions `search` on list tools, replace with `gh_search_*` equivalents.

### Step 9.4: Update CLAUDE.md if needed

- If there's a category list, add `context`.
- If there's an annotation-preset mapping, confirm the new tools are covered.

### Step 9.5: Verify docs align with code

```bash
grep -n "search" local-gh-mcp/README.md local-gh-mcp/DESIGN.md local-gh-mcp/CLAUDE.md
grep -n "28\|tool count\|Tool count" local-gh-mcp/README.md local-gh-mcp/DESIGN.md local-gh-mcp/CLAUDE.md
```

No stale references should remain.

### Step 9.6: Commit

```bash
git add local-gh-mcp/DESIGN.md local-gh-mcp/README.md local-gh-mcp/CLAUDE.md
git commit -F- <<'EOF'
docs(local-gh-mcp): document phase 3 additions

Updates tool count (28 → 37), adds context category and 9
new tools to inventory, notes actor/event filters on
gh_list_runs, removes stale references to the search param
on list tools.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
```

---

## Final verification

After all tasks complete:

```bash
cd local-gh-mcp && make audit
cd .. && make audit   # run root audit to confirm no cross-tool regressions
git log main..HEAD --oneline   # review the Phase 3 commit series
```

Expected: audit passes in all tools, Phase 3 adds ~9 focused commits (one per task).
