# local-gh-mcp Phase 1 — Schema Polish Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use Skill(executing-plans) to implement this plan task-by-task.

**Goal:** Add MCP tool annotations, put enums in JSON Schema, sharpen tool descriptions, declare schema defaults, and tighten error messages — all non-breaking for existing callers.

**Architecture:** Pure additive changes to `local-gh-mcp/internal/tools/*.go`. No handler logic changes except for error-message polish (#12). Annotations use `gomcp.ToolAnnotation` with `*bool` pointers built via `gomcp.ToBoolPtr(true/false)`. Enums are added as `"enum": []string{...}` fields in each param's schema map alongside existing `"type"` and `"description"`.

**Tech Stack:** Go 1.22+, `github.com/mark3labs/mcp-go` v0.45.0, testify for assertions, existing mock `GHClient` interface.

**Scope anchor:** See `.designs/2026-04-21-local-gh-mcp-improvements.md` — this plan implements items #1, #3, #5, #10b, #10e, #12 from that design. Items #2, #2b (renames), #9 (state-transition tools), #10a/c/d/f (param polish), #11 (new tools), #14 (behavior changes) are later phases.

---

## Prerequisites

Before starting: from the repo root, confirm the baseline is green.

```bash
cd local-gh-mcp && make audit
```

Expected: `tidy`, `fmt`, `lint`, `test`, `govulncheck` all pass.

If anything fails, stop and fix first — this plan assumes a clean baseline.

---

## Task 1: Add annotation preset helpers

Extract four annotation presets so each tool definition just references one. Keeps the schema literals compact and makes classification auditable in one place.

**Files:**

- Modify: `local-gh-mcp/internal/tools/tools.go` (append to file, after existing helpers)
- Test: `local-gh-mcp/internal/tools/tools_test.go` (new file)

**Step 1: Write the failing test**

Create `local-gh-mcp/internal/tools/tools_test.go`:

```go
package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnnotationPresets_ReadOnly(t *testing.T) {
	a := annRead
	require.NotNil(t, a.ReadOnlyHint)
	assert.True(t, *a.ReadOnlyHint)
	require.NotNil(t, a.OpenWorldHint)
	assert.True(t, *a.OpenWorldHint)
	assert.Nil(t, a.DestructiveHint)
	assert.Nil(t, a.IdempotentHint)
}

func TestAnnotationPresets_Additive(t *testing.T) {
	a := annAdditive
	require.NotNil(t, a.DestructiveHint)
	assert.False(t, *a.DestructiveHint)
	require.NotNil(t, a.OpenWorldHint)
	assert.True(t, *a.OpenWorldHint)
	assert.Nil(t, a.ReadOnlyHint)
}

func TestAnnotationPresets_Idempotent(t *testing.T) {
	a := annIdempotent
	require.NotNil(t, a.IdempotentHint)
	assert.True(t, *a.IdempotentHint)
	require.NotNil(t, a.DestructiveHint)
	assert.False(t, *a.DestructiveHint)
	require.NotNil(t, a.OpenWorldHint)
	assert.True(t, *a.OpenWorldHint)
}

func TestAnnotationPresets_Destructive(t *testing.T) {
	a := annDestructive
	require.NotNil(t, a.DestructiveHint)
	assert.True(t, *a.DestructiveHint)
	require.NotNil(t, a.OpenWorldHint)
	assert.True(t, *a.OpenWorldHint)
	assert.Nil(t, a.ReadOnlyHint)
}
```

**Step 2: Run test to verify it fails**

```bash
cd local-gh-mcp && go test -run TestAnnotationPresets -v ./internal/tools/
```

Expected: compile error — `annRead`, `annAdditive`, `annIdempotent`, `annDestructive` are undefined.

**Step 3: Add presets**

Append to `local-gh-mcp/internal/tools/tools.go` (after the existing `requireOwnerRepo` helper):

```go
// Annotation presets used across all tool definitions.
// See .designs/2026-04-21-local-gh-mcp-improvements.md section #1 for the classification table.
var (
	// Read tools: inspect GitHub state, never mutate.
	annRead = gomcp.ToolAnnotation{
		ReadOnlyHint:  gomcp.ToBoolPtr(true),
		OpenWorldHint: gomcp.ToBoolPtr(true),
	}
	// Additive writes: create new state (PRs, comments, reviews). Not destructive.
	annAdditive = gomcp.ToolAnnotation{
		DestructiveHint: gomcp.ToBoolPtr(false),
		OpenWorldHint:   gomcp.ToBoolPtr(true),
	}
	// Idempotent writes: edits or state transitions where repeat calls with same args have no additional effect.
	annIdempotent = gomcp.ToolAnnotation{
		IdempotentHint:  gomcp.ToBoolPtr(true),
		DestructiveHint: gomcp.ToBoolPtr(false),
		OpenWorldHint:   gomcp.ToBoolPtr(true),
	}
	// Destructive: removes or rewrites state in ways that cannot be trivially reversed.
	annDestructive = gomcp.ToolAnnotation{
		DestructiveHint: gomcp.ToBoolPtr(true),
		OpenWorldHint:   gomcp.ToBoolPtr(true),
	}
)
```

**Step 4: Run test to verify it passes**

```bash
cd local-gh-mcp && go test -run TestAnnotationPresets -v ./internal/tools/
```

Expected: 4 tests PASS.

**Step 5: Commit**

```bash
git add local-gh-mcp/internal/tools/tools.go local-gh-mcp/internal/tools/tools_test.go
git commit -m "feat(local-gh-mcp): add annotation presets"
```

---

## Task 2: Annotate PR tools

Apply the preset that matches each PR tool's classification.

**Files:**

- Modify: `local-gh-mcp/internal/tools/pr.go` (each tool literal in `prTools()`)
- Test: `local-gh-mcp/internal/tools/pr_test.go` (add annotation assertions)

**Classification (from design doc):**

| Tool                         | Preset           |
| ---------------------------- | ---------------- |
| `gh_create_pr`               | `annAdditive`    |
| `gh_view_pr`                 | `annRead`        |
| `gh_list_prs`                | `annRead`        |
| `gh_diff_pr`                 | `annRead`        |
| `gh_comment_pr`              | `annAdditive`    |
| `gh_review_pr`               | `annAdditive`    |
| `gh_merge_pr`                | `annDestructive` |
| `gh_edit_pr`                 | `annIdempotent`  |
| `gh_check_pr`                | `annRead`        |
| `gh_close_pr`                | `annDestructive` |
| `gh_list_pr_comments`        | `annRead`        |
| `gh_list_pr_reviews`         | `annRead`        |
| `gh_list_pr_review_comments` | `annRead`        |

**Step 1: Write the failing test**

Add to `local-gh-mcp/internal/tools/pr_test.go` (append a new `TestPRToolAnnotations` function at the bottom):

```go
func TestPRToolAnnotations(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	tools := h.prTools()
	byName := make(map[string]gomcp.Tool, len(tools))
	for _, tool := range tools {
		byName[tool.Name] = tool
	}

	cases := []struct {
		name     string
		expected gomcp.ToolAnnotation
	}{
		{"gh_create_pr", annAdditive},
		{"gh_view_pr", annRead},
		{"gh_list_prs", annRead},
		{"gh_diff_pr", annRead},
		{"gh_comment_pr", annAdditive},
		{"gh_review_pr", annAdditive},
		{"gh_merge_pr", annDestructive},
		{"gh_edit_pr", annIdempotent},
		{"gh_check_pr", annRead},
		{"gh_close_pr", annDestructive},
		{"gh_list_pr_comments", annRead},
		{"gh_list_pr_reviews", annRead},
		{"gh_list_pr_review_comments", annRead},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool, ok := byName[tc.name]
			require.True(t, ok, "tool %s not registered", tc.name)
			assert.Equal(t, tc.expected, tool.Annotations)
		})
	}
}
```

**Step 2: Run test to verify it fails**

```bash
cd local-gh-mcp && go test -run TestPRToolAnnotations -v ./internal/tools/
```

Expected: 13 subtests FAIL — each tool's `Annotations` is the zero value (all fields nil), not the expected preset.

**Step 3: Apply annotations**

In `local-gh-mcp/internal/tools/pr.go`, add `Annotations:` to each tool literal in `prTools()`. Example for `gh_create_pr`:

```go
{
    Name:        "gh_create_pr",
    Description: "Create a new pull request",
    Annotations: annAdditive,
    InputSchema: gomcp.ToolInputSchema{
        // ... unchanged ...
    },
},
```

Apply per the classification table above. The `Annotations` field goes between `Description` and `InputSchema` for consistency.

**Step 4: Run test to verify it passes**

```bash
cd local-gh-mcp && go test -run TestPRToolAnnotations -v ./internal/tools/
```

Expected: 13 subtests PASS.

Also run the full tools test suite to confirm nothing else broke:

```bash
cd local-gh-mcp && go test ./internal/tools/
```

Expected: all tests PASS.

**Step 5: Commit**

```bash
git add local-gh-mcp/internal/tools/pr.go local-gh-mcp/internal/tools/pr_test.go
git commit -m "feat(local-gh-mcp): annotate PR tools"
```

---

## Task 3: Annotate issue tools

Same pattern as Task 2.

**Files:**

- Modify: `local-gh-mcp/internal/tools/issue.go`
- Test: `local-gh-mcp/internal/tools/issue_test.go`

**Classification:**

| Tool                     | Preset        |
| ------------------------ | ------------- |
| `gh_view_issue`          | `annRead`     |
| `gh_list_issues`         | `annRead`     |
| `gh_comment_issue`       | `annAdditive` |
| `gh_list_issue_comments` | `annRead`     |

**Step 1: Write the failing test**

Add `TestIssueToolAnnotations` to `local-gh-mcp/internal/tools/issue_test.go` mirroring the PR version:

```go
func TestIssueToolAnnotations(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	tools := h.issueTools()
	byName := make(map[string]gomcp.Tool, len(tools))
	for _, tool := range tools {
		byName[tool.Name] = tool
	}

	cases := []struct {
		name     string
		expected gomcp.ToolAnnotation
	}{
		{"gh_view_issue", annRead},
		{"gh_list_issues", annRead},
		{"gh_comment_issue", annAdditive},
		{"gh_list_issue_comments", annRead},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool, ok := byName[tc.name]
			require.True(t, ok, "tool %s not registered", tc.name)
			assert.Equal(t, tc.expected, tool.Annotations)
		})
	}
}
```

**Step 2: Run test to verify it fails**

```bash
cd local-gh-mcp && go test -run TestIssueToolAnnotations -v ./internal/tools/
```

Expected: 4 subtests FAIL.

**Step 3: Apply annotations**

Add `Annotations:` to each tool in `local-gh-mcp/internal/tools/issue.go` per the classification table.

**Step 4: Run test to verify it passes**

```bash
cd local-gh-mcp && go test -run TestIssueToolAnnotations -v ./internal/tools/
```

Expected: 4 subtests PASS.

**Step 5: Commit**

```bash
git add local-gh-mcp/internal/tools/issue.go local-gh-mcp/internal/tools/issue_test.go
git commit -m "feat(local-gh-mcp): annotate issue tools"
```

---

## Task 4: Annotate run tools

**Files:**

- Modify: `local-gh-mcp/internal/tools/run.go`
- Test: `local-gh-mcp/internal/tools/run_test.go`

**Classification:**

| Tool            | Preset           |
| --------------- | ---------------- |
| `gh_list_runs`  | `annRead`        |
| `gh_view_run`   | `annRead`        |
| `gh_rerun`      | `annAdditive`    |
| `gh_cancel_run` | `annDestructive` |

Note: `gh_rerun` is additive, not destructive — re-executing a workflow adds a new run; it doesn't overwrite or remove state.

**Step 1: Write the failing test**

Add `TestRunToolAnnotations` to `local-gh-mcp/internal/tools/run_test.go` following the pattern from Task 2.

**Step 2: Run test to verify it fails**

```bash
cd local-gh-mcp && go test -run TestRunToolAnnotations -v ./internal/tools/
```

Expected: 4 subtests FAIL.

**Step 3: Apply annotations**

Add `Annotations:` to each tool in `local-gh-mcp/internal/tools/run.go`.

**Step 4: Run test to verify it passes**

```bash
cd local-gh-mcp && go test -run TestRunToolAnnotations -v ./internal/tools/
```

Expected: 4 subtests PASS.

**Step 5: Commit**

```bash
git add local-gh-mcp/internal/tools/run.go local-gh-mcp/internal/tools/run_test.go
git commit -m "feat(local-gh-mcp): annotate run tools"
```

---

## Task 5: Annotate cache tools

**Files:**

- Modify: `local-gh-mcp/internal/tools/cache.go`
- Test: `local-gh-mcp/internal/tools/cache_test.go`

**Classification:**

| Tool              | Preset           |
| ----------------- | ---------------- |
| `gh_list_caches`  | `annRead`        |
| `gh_delete_cache` | `annDestructive` |

**Step 1: Write the failing test**

Add `TestCacheToolAnnotations` to `local-gh-mcp/internal/tools/cache_test.go` following the pattern from Task 2.

**Step 2: Run test to verify it fails**

```bash
cd local-gh-mcp && go test -run TestCacheToolAnnotations -v ./internal/tools/
```

Expected: 2 subtests FAIL.

**Step 3: Apply annotations**

Add `Annotations:` to each tool in `local-gh-mcp/internal/tools/cache.go`.

**Step 4: Run test to verify it passes**

```bash
cd local-gh-mcp && go test -run TestCacheToolAnnotations -v ./internal/tools/
```

Expected: 2 subtests PASS.

**Step 5: Commit**

```bash
git add local-gh-mcp/internal/tools/cache.go local-gh-mcp/internal/tools/cache_test.go
git commit -m "feat(local-gh-mcp): annotate cache tools"
```

---

## Task 6: Annotate search tools

**Files:**

- Modify: `local-gh-mcp/internal/tools/search.go`
- Test: `local-gh-mcp/internal/tools/search_test.go`

**Classification:** All five search tools are `annRead`.

| Tool                | Preset    |
| ------------------- | --------- |
| `gh_search_prs`     | `annRead` |
| `gh_search_issues`  | `annRead` |
| `gh_search_repos`   | `annRead` |
| `gh_search_code`    | `annRead` |
| `gh_search_commits` | `annRead` |

**Step 1: Write the failing test**

Add `TestSearchToolAnnotations` to `local-gh-mcp/internal/tools/search_test.go` following the pattern from Task 2.

**Step 2: Run test to verify it fails**

```bash
cd local-gh-mcp && go test -run TestSearchToolAnnotations -v ./internal/tools/
```

Expected: 5 subtests FAIL.

**Step 3: Apply annotations**

Add `Annotations: annRead` to each tool in `local-gh-mcp/internal/tools/search.go`.

**Step 4: Run test to verify it passes**

```bash
cd local-gh-mcp && go test -run TestSearchToolAnnotations -v ./internal/tools/
```

Expected: 5 subtests PASS.

**Step 5: Commit**

```bash
git add local-gh-mcp/internal/tools/search.go local-gh-mcp/internal/tools/search_test.go
git commit -m "feat(local-gh-mcp): annotate search tools"
```

---

## Task 7: Coverage guard — every tool has annotations

Add one regression test to `tools_test.go` that iterates every registered tool and asserts `OpenWorldHint` is set. This catches any future tool added without annotations (every GitHub tool touches the open world).

**Files:**

- Modify: `local-gh-mcp/internal/tools/tools_test.go`

**Step 1: Write the test**

Append to `local-gh-mcp/internal/tools/tools_test.go`:

```go
func TestEveryToolHasOpenWorldHint(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	tools := h.Tools()
	require.NotEmpty(t, tools)
	for _, tool := range tools {
		t.Run(tool.Name, func(t *testing.T) {
			require.NotNilf(t, tool.Annotations.OpenWorldHint,
				"tool %s must set OpenWorldHint", tool.Name)
			assert.Truef(t, *tool.Annotations.OpenWorldHint,
				"tool %s: OpenWorldHint must be true (all tools touch GitHub)", tool.Name)
		})
	}
}
```

Note: `mockGHClient` lives in `pr_test.go`. It's in the same package so this test can reference it directly.

**Step 2: Run test to verify it passes**

```bash
cd local-gh-mcp && go test -run TestEveryToolHasOpenWorldHint -v ./internal/tools/
```

Expected: 28 subtests PASS (one per registered tool).

If any subtest fails, it means a tool from Tasks 2–6 was missed — go back and fix.

**Step 3: Commit**

```bash
git add local-gh-mcp/internal/tools/tools_test.go
git commit -m "test(local-gh-mcp): guard every tool has annotations"
```

---

## Task 8: Enum — PR/issue `state` params

Add `enum` arrays to the `state` param schemas. Four tools: `gh_list_prs`, `gh_search_prs`, `gh_list_issues`, `gh_search_issues`. Per the design doc, normalize the sets:

- PR tools: `open`, `closed`, `merged`, `all`
- Issue tools: `open`, `closed`, `all` (no `merged` — issues can't merge)

Handler-side validation is already absent for `state` today; the CLI rejects invalid values. Adding schema enums shifts that rejection earlier.

**Files:**

- Modify: `local-gh-mcp/internal/tools/pr.go` (schema for `gh_list_prs`)
- Modify: `local-gh-mcp/internal/tools/issue.go` (schema for `gh_list_issues`)
- Modify: `local-gh-mcp/internal/tools/search.go` (schemas for `gh_search_prs`, `gh_search_issues`)
- Test: `local-gh-mcp/internal/tools/pr_test.go`, `issue_test.go`, `search_test.go`

**Step 1: Write the failing test**

Add to `local-gh-mcp/internal/tools/pr_test.go`:

```go
func TestListPRs_StateEnum(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	for _, tool := range h.prTools() {
		if tool.Name != "gh_list_prs" {
			continue
		}
		prop, ok := tool.InputSchema.Properties["state"].(map[string]any)
		require.True(t, ok, "state property missing or wrong shape")
		enum, ok := prop["enum"].([]string)
		require.True(t, ok, "state must declare an enum")
		assert.ElementsMatch(t, []string{"open", "closed", "merged", "all"}, enum)
		return
	}
	t.Fatal("gh_list_prs not found")
}
```

Add mirror tests:

- `TestListIssues_StateEnum` in `issue_test.go` — enum `{"open", "closed", "all"}`
- `TestSearchPRs_StateEnum` in `search_test.go` — enum `{"open", "closed", "merged", "all"}`
- `TestSearchIssues_StateEnum` in `search_test.go` — enum `{"open", "closed", "all"}`

**Step 2: Run tests to verify they fail**

```bash
cd local-gh-mcp && go test -run "TestListPRs_StateEnum|TestListIssues_StateEnum|TestSearchPRs_StateEnum|TestSearchIssues_StateEnum" -v ./internal/tools/
```

Expected: all 4 FAIL — `prop["enum"]` is nil.

**Step 3: Add enums to schemas**

For `gh_list_prs` in `pr.go`, change the `state` property from:

```go
"state": map[string]any{
    "type":        "string",
    "description": "Filter by state: open, closed, merged, all",
},
```

To:

```go
"state": map[string]any{
    "type":        "string",
    "enum":        []string{"open", "closed", "merged", "all"},
    "description": "Filter by state.",
},
```

Apply the same change to `gh_list_issues` (enum `{"open", "closed", "all"}`), `gh_search_prs` (enum `{"open", "closed", "merged", "all"}`), `gh_search_issues` (enum `{"open", "closed", "all"}`).

**Step 4: Run tests to verify they pass**

```bash
cd local-gh-mcp && go test -run "TestListPRs_StateEnum|TestListIssues_StateEnum|TestSearchPRs_StateEnum|TestSearchIssues_StateEnum" -v ./internal/tools/
```

Expected: all 4 PASS. Run `./internal/tools/` in full to confirm no regressions.

**Step 5: Commit**

```bash
git add local-gh-mcp/internal/tools/pr.go local-gh-mcp/internal/tools/issue.go local-gh-mcp/internal/tools/search.go local-gh-mcp/internal/tools/pr_test.go local-gh-mcp/internal/tools/issue_test.go local-gh-mcp/internal/tools/search_test.go
git commit -m "feat(local-gh-mcp): add state enums to schema"
```

---

## Task 9: Enum — `gh_review_pr` event param

**Files:**

- Modify: `local-gh-mcp/internal/tools/pr.go` (schema for `gh_review_pr`)
- Test: `local-gh-mcp/internal/tools/pr_test.go`

Handler validation in `handleReviewPR` already rejects invalid events — keep that check as defense-in-depth.

**Step 1: Write the failing test**

Add to `local-gh-mcp/internal/tools/pr_test.go`:

```go
func TestReviewPR_EventEnum(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	for _, tool := range h.prTools() {
		if tool.Name != "gh_review_pr" {
			continue
		}
		prop := tool.InputSchema.Properties["event"].(map[string]any)
		enum, ok := prop["enum"].([]string)
		require.True(t, ok, "event must declare an enum")
		assert.ElementsMatch(t, []string{"approve", "request_changes", "comment"}, enum)
		return
	}
	t.Fatal("gh_review_pr not found")
}
```

**Step 2: Run test to verify it fails**

```bash
cd local-gh-mcp && go test -run TestReviewPR_EventEnum -v ./internal/tools/
```

Expected: FAIL.

**Step 3: Add enum**

In `pr.go`, change the `event` property of `gh_review_pr` to:

```go
"event": map[string]any{
    "type":        "string",
    "enum":        []string{"approve", "request_changes", "comment"},
    "description": "Review event type.",
},
```

**Step 4: Run test to verify it passes**

```bash
cd local-gh-mcp && go test -run TestReviewPR_EventEnum -v ./internal/tools/
```

Expected: PASS.

**Step 5: Commit**

```bash
git add local-gh-mcp/internal/tools/pr.go local-gh-mcp/internal/tools/pr_test.go
git commit -m "feat(local-gh-mcp): add event enum to review_pr"
```

---

## Task 10: Enum — `gh_merge_pr` method param

**Files:**

- Modify: `local-gh-mcp/internal/tools/pr.go` (schema for `gh_merge_pr`)
- Test: `local-gh-mcp/internal/tools/pr_test.go`

Handler validation already accepts `{"merge", "squash", "rebase", ""}`. The empty string is for the "no method specified" case — the schema enum should NOT include empty string (missing-optional is schema-absent, not schema-empty-string). Keep the handler check intact.

**Step 1: Write the failing test**

Add to `local-gh-mcp/internal/tools/pr_test.go`:

```go
func TestMergePR_MethodEnum(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	for _, tool := range h.prTools() {
		if tool.Name != "gh_merge_pr" {
			continue
		}
		prop := tool.InputSchema.Properties["method"].(map[string]any)
		enum, ok := prop["enum"].([]string)
		require.True(t, ok, "method must declare an enum")
		assert.ElementsMatch(t, []string{"merge", "squash", "rebase"}, enum)
		return
	}
	t.Fatal("gh_merge_pr not found")
}
```

**Step 2: Run test to verify it fails**

```bash
cd local-gh-mcp && go test -run TestMergePR_MethodEnum -v ./internal/tools/
```

Expected: FAIL.

**Step 3: Add enum**

In `pr.go`, change the `method` property of `gh_merge_pr` to:

```go
"method": map[string]any{
    "type":        "string",
    "enum":        []string{"merge", "squash", "rebase"},
    "description": "Merge method.",
},
```

**Step 4: Run test to verify it passes**

```bash
cd local-gh-mcp && go test -run TestMergePR_MethodEnum -v ./internal/tools/
```

Expected: PASS.

**Step 5: Commit**

```bash
git add local-gh-mcp/internal/tools/pr.go local-gh-mcp/internal/tools/pr_test.go
git commit -m "feat(local-gh-mcp): add method enum to merge_pr"
```

---

## Task 11: Enum — `gh_list_runs` status param

First verify the valid `status` values `gh run list --status` accepts.

**Files:**

- Modify: `local-gh-mcp/internal/tools/run.go` (schema for `gh_list_runs`)
- Test: `local-gh-mcp/internal/tools/run_test.go`

**Prep: confirm the status set**

```bash
gh run list --help 2>&1 | grep -A5 -- "--status"
```

Expected list (GitHub Actions run statuses): `queued`, `in_progress`, `completed`, `waiting`, `requested`, `pending`, `cancelled`, `failure`, `skipped`, `stale`, `startup_failure`, `success`, `timed_out`, `action_required`, `neutral`.

Use that exact set in the enum. If `gh run list --help` yields a narrower list, use that — the source of truth is the CLI.

**Step 1: Write the failing test**

Add to `local-gh-mcp/internal/tools/run_test.go`:

```go
func TestListRuns_StatusEnum(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	for _, tool := range h.runTools() {
		if tool.Name != "gh_list_runs" {
			continue
		}
		prop := tool.InputSchema.Properties["status"].(map[string]any)
		enum, ok := prop["enum"].([]string)
		require.True(t, ok, "status must declare an enum")
		// Full set from gh run list --status.
		expected := []string{
			"queued", "in_progress", "completed", "waiting", "requested", "pending",
			"cancelled", "failure", "skipped", "stale", "startup_failure", "success",
			"timed_out", "action_required", "neutral",
		}
		assert.ElementsMatch(t, expected, enum)
		return
	}
	t.Fatal("gh_list_runs not found")
}
```

If the CLI inspection in the prep step yielded a different set, substitute the correct list in BOTH the test and the schema.

**Step 2: Run test to verify it fails**

```bash
cd local-gh-mcp && go test -run TestListRuns_StatusEnum -v ./internal/tools/
```

Expected: FAIL.

**Step 3: Add enum**

In `run.go`, change the `status` property of `gh_list_runs` to:

```go
"status": map[string]any{
    "type":        "string",
    "enum":        []string{"queued", "in_progress", "completed", "waiting", "requested", "pending", "cancelled", "failure", "skipped", "stale", "startup_failure", "success", "timed_out", "action_required", "neutral"},
    "description": "Filter by workflow run status.",
},
```

**Step 4: Run test to verify it passes**

```bash
cd local-gh-mcp && go test -run TestListRuns_StatusEnum -v ./internal/tools/
```

Expected: PASS.

**Step 5: Commit**

```bash
git add local-gh-mcp/internal/tools/run.go local-gh-mcp/internal/tools/run_test.go
git commit -m "feat(local-gh-mcp): add status enum to list_runs"
```

---

## Task 12: Enum — `gh_list_caches` sort and order params

**Files:**

- Modify: `local-gh-mcp/internal/tools/cache.go` (schema for `gh_list_caches`)
- Test: `local-gh-mcp/internal/tools/cache_test.go`

**Step 1: Write the failing test**

Add to `local-gh-mcp/internal/tools/cache_test.go`:

```go
func TestListCaches_SortOrderEnums(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	for _, tool := range h.cacheTools() {
		if tool.Name != "gh_list_caches" {
			continue
		}
		sortProp := tool.InputSchema.Properties["sort"].(map[string]any)
		sortEnum, ok := sortProp["enum"].([]string)
		require.True(t, ok, "sort must declare an enum")
		assert.ElementsMatch(t, []string{"created_at", "last_accessed_at", "size_in_bytes"}, sortEnum)

		orderProp := tool.InputSchema.Properties["order"].(map[string]any)
		orderEnum, ok := orderProp["enum"].([]string)
		require.True(t, ok, "order must declare an enum")
		assert.ElementsMatch(t, []string{"asc", "desc"}, orderEnum)
		return
	}
	t.Fatal("gh_list_caches not found")
}
```

**Step 2: Run test to verify it fails**

```bash
cd local-gh-mcp && go test -run TestListCaches_SortOrderEnums -v ./internal/tools/
```

Expected: FAIL.

**Step 3: Add enums**

In `cache.go`, change the `sort` and `order` properties of `gh_list_caches`:

```go
"sort": map[string]any{
    "type":        "string",
    "enum":        []string{"created_at", "last_accessed_at", "size_in_bytes"},
    "description": "Sort key.",
},
"order": map[string]any{
    "type":        "string",
    "enum":        []string{"asc", "desc"},
    "description": "Sort order.",
},
```

**Step 4: Run test to verify it passes**

```bash
cd local-gh-mcp && go test -run TestListCaches_SortOrderEnums -v ./internal/tools/
```

Expected: PASS.

**Step 5: Commit**

```bash
git add local-gh-mcp/internal/tools/cache.go local-gh-mcp/internal/tools/cache_test.go
git commit -m "feat(local-gh-mcp): add sort/order enums to list_caches"
```

---

## Task 13: Schema defaults for `limit` and `max_body_length`

Declare `"default"` in schema for every `limit` param (30) and every `max_body_length` param (2000). Complements the existing clamp logic in `clampMaxBodyLength` — handler behavior is unchanged; only the schema becomes more explicit.

**Files:**

- Modify: `local-gh-mcp/internal/tools/pr.go` (all tools with `limit` or `max_body_length`)
- Modify: `local-gh-mcp/internal/tools/issue.go`
- Modify: `local-gh-mcp/internal/tools/run.go`
- Modify: `local-gh-mcp/internal/tools/cache.go`
- Modify: `local-gh-mcp/internal/tools/search.go`
- Modify: `local-gh-mcp/internal/tools/tools_test.go` (new coverage test)

**Step 1: Write the failing test**

Append to `local-gh-mcp/internal/tools/tools_test.go`:

```go
func TestEveryLimitParamDeclaresDefault(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	for _, tool := range h.Tools() {
		prop, ok := tool.InputSchema.Properties["limit"].(map[string]any)
		if !ok {
			continue
		}
		t.Run(tool.Name+"/limit", func(t *testing.T) {
			def, ok := prop["default"]
			require.True(t, ok, "tool %s: limit must declare a default", tool.Name)
			// JSON numbers round-trip as float64; accept both int and float64 literals.
			switch v := def.(type) {
			case int:
				assert.Equal(t, 30, v)
			case float64:
				assert.Equal(t, float64(30), v)
			default:
				t.Fatalf("tool %s: limit default wrong type %T", tool.Name, def)
			}
		})
	}
}

func TestEveryMaxBodyLengthParamDeclaresDefault(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	for _, tool := range h.Tools() {
		prop, ok := tool.InputSchema.Properties["max_body_length"].(map[string]any)
		if !ok {
			continue
		}
		t.Run(tool.Name+"/max_body_length", func(t *testing.T) {
			def, ok := prop["default"]
			require.True(t, ok, "tool %s: max_body_length must declare a default", tool.Name)
			switch v := def.(type) {
			case int:
				assert.Equal(t, 2000, v)
			case float64:
				assert.Equal(t, float64(2000), v)
			default:
				t.Fatalf("tool %s: max_body_length default wrong type %T", tool.Name, def)
			}
		})
	}
}
```

**Step 2: Run tests to verify they fail**

```bash
cd local-gh-mcp && go test -run "TestEveryLimitParamDeclaresDefault|TestEveryMaxBodyLengthParamDeclaresDefault" -v ./internal/tools/
```

Expected: multiple subtests FAIL — `default` key missing from every `limit` and `max_body_length` property.

**Step 3: Add defaults**

For every `limit` property across all five tool files, add `"default": 30`:

```go
"limit": map[string]any{
    "type":        "number",
    "default":     30,
    "description": "Max results (default 30, max 100).",
},
```

For every `max_body_length` property:

```go
"max_body_length": map[string]any{
    "type":        "number",
    "default":     2000,
    "description": "Max body length in chars (default 2000, max 50000).",
},
```

Tools with `limit`: `gh_list_prs`, `gh_list_issues`, `gh_list_runs`, `gh_list_caches`, `gh_list_pr_comments`, `gh_list_pr_reviews`, `gh_list_pr_review_comments`, `gh_list_issue_comments`, `gh_search_prs`, `gh_search_issues`, `gh_search_repos`, `gh_search_code`, `gh_search_commits`.

Tools with `max_body_length`: `gh_view_pr`, `gh_view_issue`, `gh_list_pr_comments`, `gh_list_pr_reviews`, `gh_list_pr_review_comments`, `gh_list_issue_comments`.

**Step 4: Run tests to verify they pass**

```bash
cd local-gh-mcp && go test -run "TestEveryLimitParamDeclaresDefault|TestEveryMaxBodyLengthParamDeclaresDefault" -v ./internal/tools/
```

Expected: all subtests PASS.

**Step 5: Commit**

```bash
git add local-gh-mcp/internal/tools/pr.go local-gh-mcp/internal/tools/issue.go local-gh-mcp/internal/tools/run.go local-gh-mcp/internal/tools/cache.go local-gh-mcp/internal/tools/search.go local-gh-mcp/internal/tools/tools_test.go
git commit -m "feat(local-gh-mcp): declare schema defaults for limit and max_body_length"
```

---

## Task 14: Sharpen PR tool descriptions

Rewrite one-liner descriptions to include "use when / don't use when" guidance. Focus on high-impact disambiguations — don't touch descriptions that are already dense (e.g., `gh_list_pr_comments`, `gh_list_pr_reviews`, `gh_list_pr_review_comments` already have good descriptions; leave them).

**Files:**

- Modify: `local-gh-mcp/internal/tools/pr.go`

**Target descriptions:**

| Tool            | New description                                                                                                                                                                               |
| --------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `gh_create_pr`  | `"Create a new pull request. Fails if a PR already exists for the head branch. Returns the PR URL."`                                                                                          |
| `gh_view_pr`    | `"View PR metadata and description as structured markdown. For the diff, use gh_diff_pr; for CI status, use gh_check_pr; for conversation, use gh_list_pr_comments/reviews/review_comments."` |
| `gh_list_prs`   | `"List PRs in a single repository. Use this when you know owner/repo. For cross-repo queries or GitHub search DSL filters (is:open, author:@me, etc.), use gh_search_prs instead."`           |
| `gh_diff_pr`    | `"View a PR's diff. Returns a file summary table followed by the full unified diff. Large PRs can be long; if you only need which files changed, consider the file summary alone."`           |
| `gh_comment_pr` | `"Post a conversation comment on a PR (issue-style, not tied to a line of the diff). For inline review comments, use gh_review_pr instead."`                                                  |
| `gh_review_pr`  | `"Submit a review: approve, request_changes, or comment. Requires owner, repo, PR number, and event. A body is optional for approve and comment; request_changes requires a body."`           |
| `gh_merge_pr`   | `"Merge a pull request. Method defaults to the repo's default; specify merge/squash/rebase explicitly to override. Set auto=true to enable auto-merge when checks pass."`                     |
| `gh_edit_pr`    | `"Edit PR metadata (title, body, base, labels, reviewers, assignees). Cannot change state or draft status — use gh_close_pr/gh_ready_pr/gh_draft_pr/gh_reopen_pr for those."`                 |
| `gh_check_pr`   | `"List CI status checks for a PR. Returns markdown bullets per check with state (success/failure/pending) and link."`                                                                         |
| `gh_close_pr`   | `"Close a PR without merging. Optionally attach a closing comment. To reopen later, use gh_reopen_pr."`                                                                                       |

Leave `gh_list_pr_comments`, `gh_list_pr_reviews`, `gh_list_pr_review_comments` unchanged — their descriptions are already exemplary.

Note: descriptions reference future tools (`gh_reopen_pr`, `gh_ready_pr`, `gh_draft_pr`) that don't exist yet. That's fine — they land in Phase 3. The description is aspirational and won't cause a runtime error.

**Step 1: No test** — description text isn't meaningfully testable. Skip to the edit.

**Step 2: Apply description changes**

Update the `Description` field on each listed tool in `local-gh-mcp/internal/tools/pr.go`.

**Step 3: Build and lint**

```bash
cd local-gh-mcp && make lint && go test ./internal/tools/
```

Expected: no lint warnings, all tests PASS.

**Step 4: Commit**

```bash
git add local-gh-mcp/internal/tools/pr.go
git commit -m "docs(local-gh-mcp): sharpen PR tool descriptions"
```

---

## Task 15: Sharpen issue tool descriptions

**Files:**

- Modify: `local-gh-mcp/internal/tools/issue.go`

**Target descriptions:**

| Tool                     | New description                                                                                                                                               |
| ------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `gh_view_issue`          | `"View issue metadata and description as structured markdown. For comments, use gh_list_issue_comments."`                                                     |
| `gh_list_issues`         | `"List issues in a single repository. Use this when you know owner/repo. For cross-repo queries or GitHub search DSL filters, use gh_search_issues instead."` |
| `gh_comment_issue`       | `"Post a comment on an issue. Returns the comment URL on success."`                                                                                           |
| `gh_list_issue_comments` | (leave as-is if already exemplary; otherwise add one line pointing callers to `gh_view_issue` for the issue body itself)                                      |

**Step 1: Apply description changes**

**Step 2: Build and test**

```bash
cd local-gh-mcp && make lint && go test ./internal/tools/
```

Expected: all PASS.

**Step 3: Commit**

```bash
git add local-gh-mcp/internal/tools/issue.go
git commit -m "docs(local-gh-mcp): sharpen issue tool descriptions"
```

---

## Task 16: Sharpen run tool descriptions

Document the `log_failed` behavior on `gh_view_run` explicitly.

**Files:**

- Modify: `local-gh-mcp/internal/tools/run.go`

**Target descriptions:**

| Tool            | New description                                                                                                                                                                                                                     |
| --------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `gh_list_runs`  | `"List workflow runs for a repository. Filter by branch, status, or workflow to narrow results."`                                                                                                                                   |
| `gh_view_run`   | `"View workflow run details. With log_failed=false (default), returns structured markdown: run header + per-job status list. With log_failed=true, returns raw concatenated logs for failed jobs — useful for debugging failures."` |
| `gh_rerun`      | `"Rerun a workflow run. Creates a new run attempt from the original commit. Use failed_only=true to rerun only the failed jobs rather than the full workflow."`                                                                     |
| `gh_cancel_run` | `"Cancel an in-progress workflow run. No effect on completed runs."`                                                                                                                                                                |

**Step 1: Apply description changes**

**Step 2: Build and test**

```bash
cd local-gh-mcp && make lint && go test ./internal/tools/
```

**Step 3: Commit**

```bash
git add local-gh-mcp/internal/tools/run.go
git commit -m "docs(local-gh-mcp): sharpen run tool descriptions"
```

---

## Task 17: Sharpen cache tool descriptions

**Files:**

- Modify: `local-gh-mcp/internal/tools/cache.go`

**Target descriptions:**

| Tool              | New description                                                                                                                                                        |
| ----------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `gh_list_caches`  | `"List GitHub Actions caches for a repository. Sort by created_at, last_accessed_at, or size_in_bytes. Useful for finding stale or oversized caches before deletion."` |
| `gh_delete_cache` | `"Delete a GitHub Actions cache by its numeric ID (obtained from gh_list_caches). Irreversible — the cache must be rebuilt from the next run."`                        |

**Step 1: Apply description changes**

**Step 2: Build and test**

```bash
cd local-gh-mcp && make lint && go test ./internal/tools/
```

**Step 3: Commit**

```bash
git add local-gh-mcp/internal/tools/cache.go
git commit -m "docs(local-gh-mcp): sharpen cache tool descriptions"
```

---

## Task 18: Sharpen search tool descriptions (with GitHub DSL example)

Every search tool's `query` description should mention the GitHub search DSL with one concrete example. Also add the 100-result cap clarification (design doc #10e).

**Files:**

- Modify: `local-gh-mcp/internal/tools/search.go`

**Target descriptions:**

| Tool                | New description                                                                                                                                                                                                                                  |
| ------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `gh_search_prs`     | `"Search pull requests across GitHub using the GitHub search DSL. Example: 'is:open author:@me review-requested:@me'. Use gh_list_prs instead if you have a specific owner/repo. Results truncated at 100; refine your query if you need more."` |
| `gh_search_issues`  | `"Search issues across GitHub using the GitHub search DSL. Example: 'is:open label:bug author:@me'. Use gh_list_issues instead if you have a specific owner/repo. Results truncated at 100; refine your query if you need more."`                |
| `gh_search_repos`   | `"Search repositories across GitHub using the GitHub search DSL. Example: 'language:go stars:>100 topic:cli'. Results truncated at 100; refine your query if you need more."`                                                                    |
| `gh_search_code`    | `"Search code across GitHub using the GitHub search DSL. Example: 'addEventListener language:javascript repo:facebook/react'. Results truncated at 100; refine your query if you need more."`                                                    |
| `gh_search_commits` | `"Search commits across GitHub using the GitHub search DSL. Example: 'author:octocat repo:github/docs merge:false'. Results truncated at 100; refine your query if you need more."`                                                              |

Also update the `query` parameter description on each search tool (same file). Change `"Search query"` to `"GitHub search DSL query (see tool description for example)."` so the param description stays concise while the tool description carries the example.

**Step 1: Apply description changes** — both the tool `Description` field and the `query` param `description` field for all five tools.

**Step 2: Build and test**

```bash
cd local-gh-mcp && make lint && go test ./internal/tools/
```

**Step 3: Commit**

```bash
git add local-gh-mcp/internal/tools/search.go
git commit -m "docs(local-gh-mcp): sharpen search descriptions with DSL examples"
```

---

## Task 19: Terse parse-failure errors

Replace verbose `fmt.Sprintf("failed to parse PR JSON: %v", err)` errors with a terse message, logging the raw `gh` output server-side for operators. Parse failures are server bugs — the agent can't fix them, so a short message tells it "stop retrying" while the log captures the data an operator needs.

**Files:**

- Modify: `local-gh-mcp/internal/tools/pr.go`
- Modify: `local-gh-mcp/internal/tools/issue.go`
- Modify: `local-gh-mcp/internal/tools/run.go`
- Modify: `local-gh-mcp/internal/tools/cache.go`
- Modify: `local-gh-mcp/internal/tools/search.go`
- Test: update one existing parse-failure test to match the new message shape

**Grep for all current sites:**

```bash
cd local-gh-mcp && grep -rn "failed to parse" internal/tools/
```

Expected: multiple sites, one per list/view handler that unmarshals JSON.

**Step 1: Write the failing test**

Add to `local-gh-mcp/internal/tools/pr_test.go`:

```go
func TestViewPR_ParseError_TerseMessage(t *testing.T) {
	mock := &mockGHClient{
		viewPRFunc: func(ctx context.Context, owner, repo string, number int) (string, error) {
			return "this is not valid JSON", nil
		},
	}
	h := NewHandler(mock)

	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_view_pr"
	req.Params.Arguments = map[string]any{
		"owner":  "octocat",
		"repo":   "hello-world",
		"number": float64(1),
	}

	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsError)

	// Extract the error text.
	require.Len(t, result.Content, 1)
	textContent, ok := result.Content[0].(gomcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "internal error: unable to parse gh output; check server logs", textContent.Text)
}
```

**Step 2: Run test to verify it fails**

```bash
cd local-gh-mcp && go test -run TestViewPR_ParseError_TerseMessage -v ./internal/tools/
```

Expected: FAIL — current message contains `"failed to parse PR JSON:"` with the raw error.

**Step 3: Replace error messages + log raw output**

At the top of each affected handler file, ensure `log/slog` is imported:

```go
import (
    // ... existing ...
    "log/slog"
)
```

Replace each parse-failure site. Before:

```go
var pr format.PRView
if err := json.Unmarshal([]byte(out), &pr); err != nil {
    return gomcp.NewToolResultError(fmt.Sprintf("failed to parse PR JSON: %v", err)), nil
}
```

After:

```go
var pr format.PRView
if err := json.Unmarshal([]byte(out), &pr); err != nil {
    slog.Error("failed to parse gh output",
        "tool", "gh_view_pr",
        "err", err,
        "raw", out)
    return gomcp.NewToolResultError("internal error: unable to parse gh output; check server logs"), nil
}
```

Apply to every `failed to parse ... JSON` site. The `tool` attribute should match the tool name for each handler. Consider extracting a small helper:

```go
// In tools.go, with the other helpers:
func parseError(toolName string, err error, raw string) *gomcp.CallToolResult {
    slog.Error("failed to parse gh output",
        "tool", toolName,
        "err", err,
        "raw", raw)
    return gomcp.NewToolResultError("internal error: unable to parse gh output; check server logs")
}
```

Then each call site becomes:

```go
if err := json.Unmarshal([]byte(out), &pr); err != nil {
    return parseError("gh_view_pr", err, out), nil
}
```

If you add the helper, import `log/slog` in `tools.go`, not in each individual file.

After the changes, `fmt` may no longer be needed in some files — `goimports` will clean that up.

**Step 4: Run test to verify it passes**

```bash
cd local-gh-mcp && go test -run TestViewPR_ParseError_TerseMessage -v ./internal/tools/
```

Expected: PASS. Then run the full suite:

```bash
cd local-gh-mcp && go test ./internal/tools/
```

Expected: all PASS. If pre-existing parse-error tests assert the old "failed to parse" text, update them to the new terse message.

**Step 5: Commit**

```bash
git add local-gh-mcp/internal/tools/
git commit -m "feat(local-gh-mcp): terse parse-failure errors with logged raw output"
```

---

## Task 20: Batch missing-required-field errors

Aggregate all missing required fields into a single error instead of stopping at the first. Saves agent round-trips.

Scope: tools with multiple required fields beyond `owner`/`repo` (which have their own helper). These are mainly write tools:

- `gh_create_pr` — currently checks `title`, then `body`, sequentially
- `gh_comment_pr` — checks `number`, then `body`
- `gh_comment_issue` — checks `number`, then `body`
- `gh_review_pr` — checks `number`, then `event`

For tools with a single required field besides owner/repo (e.g., `gh_view_pr` only requires `number`), batching buys nothing — leave them.

**Files:**

- Modify: `local-gh-mcp/internal/tools/tools.go` (new helper)
- Modify: `local-gh-mcp/internal/tools/pr.go` (handlers: create, comment, review)
- Modify: `local-gh-mcp/internal/tools/issue.go` (handler: comment)
- Test: `local-gh-mcp/internal/tools/pr_test.go`

**Step 1: Write the failing test**

Add to `local-gh-mcp/internal/tools/pr_test.go`:

```go
func TestCreatePR_BatchMissingFields(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_create_pr"
	req.Params.Arguments = map[string]any{
		"owner": "octocat",
		"repo":  "hello-world",
		// title and body intentionally missing
	}

	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)

	require.Len(t, result.Content, 1)
	textContent, ok := result.Content[0].(gomcp.TextContent)
	require.True(t, ok)
	// Must mention BOTH missing fields.
	assert.Contains(t, textContent.Text, "title")
	assert.Contains(t, textContent.Text, "body")
}
```

**Step 2: Run test to verify it fails**

```bash
cd local-gh-mcp && go test -run TestCreatePR_BatchMissingFields -v ./internal/tools/
```

Expected: FAIL — current behavior returns `"title is required"` without mentioning `body`.

**Step 3: Add helper and refactor handlers**

Add to `local-gh-mcp/internal/tools/tools.go`:

```go
// requireStringFields returns an error result if any of the given string
// fields are missing or empty. The error message lists all missing fields at
// once so the caller can fix them in one round-trip.
func requireStringFields(toolName string, args map[string]any, fields ...string) *gomcp.CallToolResult {
    var missing []string
    for _, f := range fields {
        if v, _ := args[f].(string); v == "" {
            missing = append(missing, f)
        }
    }
    if len(missing) == 0 {
        return nil
    }
    return gomcp.NewToolResultError(fmt.Sprintf("%s: required fields missing: %s", toolName, strings.Join(missing, ", ")))
}
```

`strings` and `fmt` are already imported in `tools.go` (check and add if not).

Refactor `handleCreatePR` in `pr.go`:

```go
func (h *Handler) handleCreatePR(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
    args := req.GetArguments()
    owner, repo, errResult := requireOwnerRepo(args)
    if errResult != nil {
        return errResult, nil
    }
    if errResult := requireStringFields("gh_create_pr", args, "title", "body"); errResult != nil {
        return errResult, nil
    }
    opts := gh.CreatePROpts{
        Title:     stringFromArgs(args, "title"),
        Body:      stringFromArgs(args, "body"),
        Base:      stringFromArgs(args, "base"),
        Head:      stringFromArgs(args, "head"),
        Draft:     boolFromArgs(args, "draft"),
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
```

Apply the same pattern to:

- `handleCommentPR` — batch check `number` via a separate int helper or keep its own check (see note below); batch `body`
- `handleCommentIssue` — same
- `handleReviewPR` — batch `number` and `event`

**Note on `number`:** `number` is an int, not a string. The existing handlers check `if number == 0 { return ... }` separately. For now, batch only string fields — the int check stays. If we want full batching later, extend `requireStringFields` with a variant that accepts int fields, but that's out of scope for Phase 1. Aim for the common case (two missing strings) first.

**Step 4: Run test to verify it passes**

```bash
cd local-gh-mcp && go test -run TestCreatePR_BatchMissingFields -v ./internal/tools/
```

Expected: PASS. Run the full suite:

```bash
cd local-gh-mcp && go test ./internal/tools/
```

Expected: all PASS. Pre-existing tests asserting `"title is required"` should be updated to the new message shape.

**Step 5: Commit**

```bash
git add local-gh-mcp/internal/tools/
git commit -m "feat(local-gh-mcp): batch missing-required-field errors"
```

---

## Task 21: Full audit and docs update

Run the full audit and update `DESIGN.md` / `CLAUDE.md` for anything that changed.

**Files:**

- Modify: `local-gh-mcp/DESIGN.md` (descriptions section, any stale enum text)
- Modify: `local-gh-mcp/CLAUDE.md` (if any new conventions emerged)

**Step 1: Run the full audit**

```bash
cd local-gh-mcp && make audit
```

Expected: all steps PASS. If `tidy` or `fmt` changed anything, commit those changes separately with `chore(local-gh-mcp): tidy`.

**Step 2: Review docs for stale content**

Scan `local-gh-mcp/DESIGN.md` for:

- Any tool description excerpts that disagree with the rewritten descriptions from Tasks 14–18. If DESIGN.md's tool table is generic (`"View pull request details"` style), leave it — DESIGN.md is spec, not doc dump.
- The "Validation and Error Handling" section — add a line noting that `event`, `method`, and `state` are now enforced via schema enums in addition to handler validation.

Scan `local-gh-mcp/CLAUDE.md` for:

- Any note about annotations — add one line under "Conventions" referencing the four presets in `tools.go`.

**Step 3: Apply doc updates**

In `local-gh-mcp/CLAUDE.md`, under "Conventions", add after the existing bullets:

```markdown
- Tool annotations: every `gomcp.Tool` in `internal/tools/*.go` must set `Annotations` to one of the four presets declared in `tools.go` (`annRead`, `annAdditive`, `annIdempotent`, `annDestructive`). Coverage enforced by `TestEveryToolHasOpenWorldHint`.
- Enums: parameters with a fixed value set declare `"enum": []string{...}` in the schema. Handler-side validation remains as defense-in-depth; the schema surfaces the set to agents without requiring a failed call first.
- Parse errors: JSON unmarshal failures return a terse message (`"internal error: unable to parse gh output; check server logs"`) and log the raw `gh` output at `slog.Error`. Never surface the raw parser error to the agent.
```

In `local-gh-mcp/DESIGN.md`, under "Validation and Error Handling", adjust the existing bullets to reflect schema-level enforcement:

```markdown
- `event`, `method`, and `state` parameters declare enums in JSON Schema and are also validated in the handler for defense-in-depth.
```

**Step 4: Run tests one more time**

```bash
cd local-gh-mcp && make audit
```

Expected: all PASS.

**Step 5: Commit**

```bash
git add local-gh-mcp/DESIGN.md local-gh-mcp/CLAUDE.md
git commit -m "docs(local-gh-mcp): document annotations and enum conventions"
```

---

## Wrap-up

After Task 21, Phase 1 is complete. Verify the commit log:

```bash
git log --oneline main..HEAD
```

Expected: a clean linear series of 20-ish commits from this plan, each scoped to one change.

Suggested PR title: `feat(local-gh-mcp): schema polish — annotations, enums, defaults, descriptions`.

Next phases to plan separately when ready:

- **Phase 2** — breaking renames (`number` → `pr_number`/`issue_number`; `gh_check_pr` → `gh_list_pr_checks`; `gh_rerun` → `gh_rerun_run`)
- **Phase 3** — new tools (`gh_whoami`, state-transition tools, `gh_list_pr_files`, etc.) and new filters
- **Phase 4** — output caps, `gh_create_pr` empty-body, `gh_list_issue_comments` `since`
