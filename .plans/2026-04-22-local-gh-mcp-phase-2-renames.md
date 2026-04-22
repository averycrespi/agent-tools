# local-gh-mcp Phase 2 Renames — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use Skill(executing-plans) to implement this plan task-by-task.

**Goal:** Breaking-rename phase for `local-gh-mcp`: disambiguate the `number` parameter (→ `pr_number` / `issue_number`), rename `gh_check_pr` → `gh_list_pr_checks`, rename `gh_rerun` → `gh_rerun_run`, and add a convention guard test that blocks regressions.

**Architecture:** Changes are confined to the JSON-schema surface (tool registrations in `internal/tools/*.go`) and their tests. `internal/gh/` method signatures are untouched — handler Go-local variables remain `number` for clarity inside `PR*` / `Issue*` methods. Handler function names (e.g. `handleCheckPR`) are renamed to mirror the new tool names, since the dispatch in `tools.go` calls them by name. A new generic guard test in `tools_test.go` asserts no tool exposes a bare `number` or `id` property, forcing any future tool to pick a resource-qualified name.

**Tech Stack:** Go 1.x, `mark3labs/mcp-go`, `testify` (`require`, `assert`), standard `testing`. Makefile targets: `make test`, `make audit`.

**Deviation from design doc:** `.designs/2026-04-22-local-gh-mcp-phase-2-renames.md` orders the guard test first (intentionally failing). This plan moves it to the end so every commit leaves the branch green for `make audit`, matching the Phase 1 rhythm. The TDD flow is preserved per-task: test change first, observe failure, update implementation.

---

## Background — what's where

**Inventory from the current working tree:**

- `internal/tools/pr.go` — 11 tool defs use `"number"`: `gh_view_pr`, `gh_diff_pr`, `gh_comment_pr`, `gh_review_pr`, `gh_merge_pr`, `gh_edit_pr`, `gh_check_pr`, `gh_close_pr`, `gh_list_pr_comments`, `gh_list_pr_reviews`, `gh_list_pr_review_comments`. Each has a schema block and a handler that calls `intFromArgs(args, "number")` then returns `"number is required"` on zero.
- `internal/tools/issue.go` — 3 tool defs use `"number"`: `gh_view_issue`, `gh_comment_issue`, `gh_list_issue_comments`. Same pattern.
- `internal/tools/tools.go` — dispatch `switch` has `case "gh_check_pr"` and `case "gh_rerun"`.
- `internal/tools/run.go` — `gh_rerun` tool def and `handleRerun` function.
- `internal/tools/pr_test.go` — argument maps use `"number": float64(N)`; a table-driven annotation test references `"gh_check_pr"`; `TestCheckPR_FormatsMarkdown` sets `req.Params.Name = "gh_check_pr"`.
- `internal/tools/issue_test.go` — argument maps use `"number": float64(N)`. **Important:** JSON fixture strings like `{"number":7,"title":"..."}` mock GitHub's wire format and must NOT be renamed.
- `internal/tools/run_test.go` — table references `"gh_rerun"`; `TestRerun_Success` sets `req.Params.Name = "gh_rerun"`.
- `DESIGN.md`, `README.md`, `CLAUDE.md` — several references to the old names in inventory tables, parameter tables, and formatting sections.

**What does not change:**

- `internal/gh/gh.go` and `internal/gh/Client` interface: method signatures like `ViewPR(ctx, owner, repo string, number int)` keep `number` as the Go parameter name.
- `internal/tools/tools.go` `GHClient` interface mirror: same — Go-local `number int` stays.
- JSON fixtures in tests that represent `gh` CLI output (e.g. `{"number":7,...}`) stay.
- Annotation presets.
- `run_id`, `cache_id` (already resource-qualified).

---

### Task 1: Rename PR `number` → `pr_number`

**Files:**

- Modify: `local-gh-mcp/internal/tools/pr.go` (11 schema blocks + 11 handler extractions + 11 error strings)
- Modify: `local-gh-mcp/internal/tools/pr_test.go` (argument maps in handler-level tests)

**Step 1: Update `pr_test.go` argument maps to use `pr_number`**

Change every occurrence of the schema-facing key `"number"` inside argument maps (i.e. `req.Params.Arguments = map[string]any{...}`) to `"pr_number"`. Do NOT touch JSON fixture strings like `{"number":42,...}` — those represent `gh` output.

Concretely, update these lines to use `"pr_number"`:

- `pr_test.go:317` (`"number": float64(42),` inside TestViewPR)
- `pr_test.go:336` (TestViewPR variant)
- `pr_test.go:351` (TestDiffPR)
- `pr_test.go:383` (TestCommentPR)
- `pr_test.go:408` (TestReviewPR)
- `pr_test.go:446` (TestMergePR)
- `pr_test.go:483` (TestEditPR)
- `pr_test.go:564` (TestCheckPR_FormatsMarkdown)
- `pr_test.go:617` (listing-tools suite)

Use this command to find them (after making the changes, re-run to verify none remain):

```bash
grep -n '"number": float64' local-gh-mcp/internal/tools/pr_test.go
```

Expected after change: empty output.

**Step 2: Run the tests to verify they fail**

Run: `cd local-gh-mcp && go test ./internal/tools/ -run 'TestViewPR|TestDiffPR|TestCommentPR|TestReviewPR|TestMergePR|TestEditPR|TestCheckPR|TestClosePR|TestListPR' -v`

Expected: multiple FAILs with messages like `number is required` (because the handlers still read `"number"` from args but tests send `"pr_number"`).

**Step 3: Update `pr.go` schema blocks**

In each of the 11 PR tool definitions in `pr.go`, change the schema:

- Rename property key `"number"` → `"pr_number"`
- Update the nested `"description"` field to say `"Pull request number"` (already is — leave as-is) but wording can stay
- Update `Required: []string{"owner", "repo", "number", ...}` to `Required: []string{"owner", "repo", "pr_number", ...}`

Tool-specific lines (approximate, verify by viewing each):

| Tool                         | Property line | Required line |
| ---------------------------- | ------------- | ------------- |
| `gh_view_pr`                 | pr.go:85      | pr.go:95      |
| `gh_diff_pr`                 | pr.go:162     | pr.go:167     |
| `gh_comment_pr`              | pr.go:185     | pr.go:194     |
| `gh_review_pr`               | pr.go:212     | pr.go:226     |
| `gh_merge_pr`                | pr.go:244     | pr.go:262     |
| `gh_edit_pr`                 | pr.go:280     | pr.go:327     |
| `gh_check_pr`                | pr.go:345     | pr.go:350     |
| `gh_close_pr`                | pr.go:368     | pr.go:377     |
| `gh_list_pr_comments`        | pr.go:395     | pr.go:410     |
| `gh_list_pr_reviews`         | pr.go:428     | pr.go:443     |
| `gh_list_pr_review_comments` | pr.go:461     | pr.go:476     |

Use this sanity check:

```bash
grep -n '"number"' local-gh-mcp/internal/tools/pr.go
```

Expected after edits: empty output.

**Step 4: Update `pr.go` handler extractions and error strings**

For each of the 11 handler functions, rename:

- `number := intFromArgs(args, "number")` → `number := intFromArgs(args, "pr_number")` (keep Go-local name `number`)
- `return gomcp.NewToolResultError("number is required"), nil` → `return gomcp.NewToolResultError("pr_number is required"), nil`

Affected handler extraction lines (11): pr.go:516, 571, 588, 609, 641, 670, 698, 719, 737, 760, 783.
Affected error-string lines (11): pr.go:518, 573, 590, 611, 643, 672, 700, 721, 739, 762, 785.

Verify:

```bash
grep -n 'intFromArgs(args, "number")\|"number is required"' local-gh-mcp/internal/tools/pr.go
```

Expected after edits: empty output.

**Step 5: Run the tests to verify they pass**

Run: `cd local-gh-mcp && go test ./internal/tools/ -v`

Expected: all PR and cross-package tests PASS.

**Step 6: Run `make audit`**

Run: `cd local-gh-mcp && make audit`

Expected: tidy + fmt + lint + test + govulncheck all clean.

**Step 7: Commit**

```bash
git add local-gh-mcp/internal/tools/pr.go local-gh-mcp/internal/tools/pr_test.go
git commit -m "$(cat <<'EOF'
feat(local-gh-mcp)!: rename PR number params to pr_number

Resolves the PR-vs-issue ambiguity flagged in Phase 2 design #2. All 11
PR tool schemas now use `pr_number`; handlers still read into a Go-local
`number int` since the PR-scoped call site is unambiguous.

BREAKING CHANGE: callers must send `pr_number` instead of `number` to
every PR tool (gh_view_pr, gh_diff_pr, gh_comment_pr, gh_review_pr,
gh_merge_pr, gh_edit_pr, gh_check_pr, gh_close_pr, gh_list_pr_comments,
gh_list_pr_reviews, gh_list_pr_review_comments).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Rename issue `number` → `issue_number`

**Files:**

- Modify: `local-gh-mcp/internal/tools/issue.go` (3 schema blocks + 3 handler extractions + 3 error strings)
- Modify: `local-gh-mcp/internal/tools/issue_test.go` (argument maps)

**Step 1: Update `issue_test.go` argument maps to use `issue_number`**

Same pattern as Task 1. Change every `"number": float64(N)` inside Go map literals (argument maps) to `"issue_number": float64(N)`. Do NOT touch JSON fixture strings.

Lines to update: issue_test.go:27, 95, 110, 128, 200.

Verify:

```bash
grep -n '"number": float64' local-gh-mcp/internal/tools/issue_test.go
```

Expected after change: empty output.

**Step 2: Run the tests to verify they fail**

Run: `cd local-gh-mcp && go test ./internal/tools/ -run 'TestViewIssue|TestCommentIssue|TestListIssueComments|TestIssue' -v`

Expected: FAILs with `number is required`.

**Step 3: Update `issue.go` schema blocks**

For each of the 3 issue tool definitions:

| Tool                     | Property line | Required line |
| ------------------------ | ------------- | ------------- |
| `gh_view_issue`          | issue.go:30   | issue.go:40   |
| `gh_comment_issue`       | issue.go:107  | issue.go:116  |
| `gh_list_issue_comments` | issue.go:134  | issue.go:149  |

Rename the property key `"number"` → `"issue_number"`, and update the `Required` slice.

Verify:

```bash
grep -n '"number"' local-gh-mcp/internal/tools/issue.go
```

Expected after edits: empty output.

**Step 4: Update `issue.go` handler extractions and error strings**

Rename in 3 handlers:

- issue.go:161, 216, 237: `intFromArgs(args, "number")` → `intFromArgs(args, "issue_number")`
- issue.go:163, 218, 239: `"number is required"` → `"issue_number is required"`

Verify:

```bash
grep -n 'intFromArgs(args, "number")\|"number is required"' local-gh-mcp/internal/tools/issue.go
```

Expected after edits: empty output.

**Step 5: Run the tests to verify they pass**

Run: `cd local-gh-mcp && go test ./internal/tools/ -v`

Expected: all issue tests PASS.

**Step 6: Run `make audit`**

Run: `cd local-gh-mcp && make audit`

Expected: clean.

**Step 7: Commit**

```bash
git add local-gh-mcp/internal/tools/issue.go local-gh-mcp/internal/tools/issue_test.go
git commit -m "$(cat <<'EOF'
feat(local-gh-mcp)!: rename issue number params to issue_number

Mirrors the PR-side rename from the prior commit. All 3 issue tool
schemas now use `issue_number`.

BREAKING CHANGE: callers must send `issue_number` instead of `number` to
gh_view_issue, gh_comment_issue, and gh_list_issue_comments.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Rename `gh_check_pr` → `gh_list_pr_checks`

**Files:**

- Modify: `local-gh-mcp/internal/tools/pr.go` (tool def + handler function + parseError tool-name arg)
- Modify: `local-gh-mcp/internal/tools/tools.go` (dispatch case)
- Modify: `local-gh-mcp/internal/tools/pr_test.go` (annotation-table entry, handler test)

**Step 1: Update test references to the new name**

In `pr_test.go`:

- Line 538: change `{"gh_check_pr", annRead},` → `{"gh_list_pr_checks", annRead},`
- Line 560: change `req.Params.Name = "gh_check_pr"` → `req.Params.Name = "gh_list_pr_checks"`

Verify:

```bash
grep -n 'gh_check_pr' local-gh-mcp/internal/tools/pr_test.go
```

Expected: empty output.

**Step 2: Run tests to verify they fail**

Run: `cd local-gh-mcp && go test ./internal/tools/ -v`

Expected: FAIL — `tool gh_list_pr_checks not registered` and/or `unknown tool: gh_list_pr_checks`.

**Step 3: Update `pr.go`**

- Rename the tool definition: `Name: "gh_check_pr"` → `Name: "gh_list_pr_checks"` (pr.go:331).
- Rename the description to match the new name if it says "check" as a verb. Current text: `"List CI status checks for a PR. Returns markdown bullets per check with state (success/failure/pending) and link."` — already describes listing, no change needed.
- Rename the handler function: `func (h *Handler) handleCheckPR(...)` → `func (h *Handler) handleListPRChecks(...)` (pr.go:692).
- Update the parseError tool-name arg: `parseError("gh_check_pr", err, out)` → `parseError("gh_list_pr_checks", err, out)` (pr.go:708).

**Step 4: Update `tools.go` dispatch**

Change:

```go
case "gh_check_pr":
    return h.handleCheckPR(ctx, req)
```

to:

```go
case "gh_list_pr_checks":
    return h.handleListPRChecks(ctx, req)
```

(tools.go:92–93).

Verify:

```bash
grep -n 'gh_check_pr\|handleCheckPR' local-gh-mcp/internal/tools/
```

Expected: empty output.

**Step 5: Run tests to verify they pass**

Run: `cd local-gh-mcp && go test ./internal/tools/ -v`

Expected: all tests PASS, including `TestCheckPR_FormatsMarkdown` (the test function name can stay — the test subject, not the tool name, is what matters).

**Step 6: Run `make audit`**

Run: `cd local-gh-mcp && make audit`

Expected: clean.

**Step 7: Commit**

```bash
git add local-gh-mcp/internal/tools/pr.go local-gh-mcp/internal/tools/tools.go local-gh-mcp/internal/tools/pr_test.go
git commit -m "$(cat <<'EOF'
feat(local-gh-mcp)!: rename gh_check_pr to gh_list_pr_checks

Aligns the name with every other `gh_list_*` tool: the output is a
markdown bullet list of CI status checks. "Check" as a verb was
ambiguous relative to the gh_check subcommand surface.

BREAKING CHANGE: callers must invoke gh_list_pr_checks; gh_check_pr is
removed.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Rename `gh_rerun` → `gh_rerun_run`

**Files:**

- Modify: `local-gh-mcp/internal/tools/run.go` (tool def + handler function)
- Modify: `local-gh-mcp/internal/tools/tools.go` (dispatch case)
- Modify: `local-gh-mcp/internal/tools/run_test.go` (annotation-table entry, handler test)

**Step 1: Update test references to the new name**

In `run_test.go`:

- Line 160: change `req.Params.Name = "gh_rerun"` → `req.Params.Name = "gh_rerun_run"`
- Line 228: change `{"gh_rerun", annAdditive},` → `{"gh_rerun_run", annAdditive},`

Verify:

```bash
grep -n '"gh_rerun"' local-gh-mcp/internal/tools/run_test.go
```

Expected: empty output.

**Step 2: Run tests to verify they fail**

Run: `cd local-gh-mcp && go test ./internal/tools/ -v`

Expected: FAIL — `unknown tool: gh_rerun_run` in `TestRerun_Success`; `tool gh_rerun_run not registered` in annotation test.

**Step 3: Update `run.go`**

- Rename the tool definition: `Name: "gh_rerun"` → `Name: "gh_rerun_run"` (run.go:80).
- Rename the handler function: `func (h *Handler) handleRerun(...)` → `func (h *Handler) handleRerunRun(...)` (run.go:187).

**Step 4: Update `tools.go` dispatch**

Change:

```go
case "gh_rerun":
    return h.handleRerun(ctx, req)
```

to:

```go
case "gh_rerun_run":
    return h.handleRerunRun(ctx, req)
```

(tools.go:114–115).

Verify:

```bash
grep -n '"gh_rerun"\|handleRerun\b' local-gh-mcp/internal/tools/
```

Expected: empty output.

**Step 5: Run tests to verify they pass**

Run: `cd local-gh-mcp && go test ./internal/tools/ -v`

Expected: all tests PASS.

**Step 6: Run `make audit`**

Run: `cd local-gh-mcp && make audit`

Expected: clean.

**Step 7: Commit**

```bash
git add local-gh-mcp/internal/tools/run.go local-gh-mcp/internal/tools/tools.go local-gh-mcp/internal/tools/run_test.go
git commit -m "$(cat <<'EOF'
feat(local-gh-mcp)!: rename gh_rerun to gh_rerun_run

Restores the `gh_<verb>_run` pattern shared by gh_list_runs, gh_view_run,
and gh_cancel_run. Awkward repetition accepted — the pattern aids
discovery.

BREAKING CHANGE: callers must invoke gh_rerun_run; gh_rerun is removed.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: Add bare-ID parameter guard test

**Files:**

- Modify: `local-gh-mcp/internal/tools/tools_test.go` (append new test)

**Step 1: Add the guard test**

Append to `local-gh-mcp/internal/tools/tools_test.go`, after `TestEveryMaxBodyLengthParamDeclaresDefault`:

```go
func TestNoBareIDParameters(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	banned := []string{"number", "id"}
	for _, tool := range h.Tools() {
		for _, key := range banned {
			_, exists := tool.InputSchema.Properties[key]
			require.Falsef(t, exists,
				"tool %q exposes bare %q property; use a resource-qualified name (pr_number, issue_number, run_id, cache_id)",
				tool.Name, key)
		}
	}
}
```

**Step 2: Run the guard to verify it passes**

Run: `cd local-gh-mcp && go test ./internal/tools/ -run TestNoBareIDParameters -v`

Expected: PASS. (If FAIL, something in Tasks 1–4 was missed — grep the failure's tool name and fix.)

**Step 3: Run the full test suite**

Run: `cd local-gh-mcp && go test ./internal/tools/ -v`

Expected: all PASS.

**Step 4: Run `make audit`**

Run: `cd local-gh-mcp && make audit`

Expected: clean.

**Step 5: Commit**

```bash
git add local-gh-mcp/internal/tools/tools_test.go
git commit -m "$(cat <<'EOF'
test(local-gh-mcp): guard against bare ID parameters

Asserts every tool's InputSchema uses a resource-qualified ID
(pr_number, issue_number, run_id, cache_id) rather than a bare
`number` or `id`. Parallels TestEveryToolHasOpenWorldHint — forces
future tool authors to pick a disambiguated name.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Update documentation

**Files:**

- Modify: `local-gh-mcp/DESIGN.md`
- Modify: `local-gh-mcp/README.md`
- Modify: `local-gh-mcp/CLAUDE.md`

**Step 1: Update `DESIGN.md`**

Known references (verify with grep before editing):

```bash
grep -n 'gh_check_pr\|gh_rerun\b\|`number`\|\*\*owner, repo, number\*\*\|\*\*owner, repo, number,' local-gh-mcp/DESIGN.md
```

Expected pre-edit hits:

- Line 52 — tool-inventory PR table row: `gh_check_pr` → `gh_list_pr_checks`
- Line 73 — tool-inventory run table row: `gh_rerun` → `gh_rerun_run`
- Line 109 — PR parameter-table row for `gh_check_pr`: rename tool, rename required-column `number` → `pr_number`
- Line 130 — run parameter-table row: `gh_rerun` → `gh_rerun_run`
- Line 183 — "Check tool" formatting bullet: `(gh_check_pr)` → `(gh_list_pr_checks)`
- Every row in the PR parameter table (around lines 99–113) referencing `number` as a required param needs `pr_number`.
- Every row in the issue parameter table referencing `number` as required needs `issue_number`.

Open the file, make the edits section by section. There is no canonical list of all rows — use the grep above and read each match in context.

Re-verify:

```bash
grep -n 'gh_check_pr\|gh_rerun\b\|`number`' local-gh-mcp/DESIGN.md
```

Expected: empty.

**Step 2: Update `README.md`**

```bash
grep -n 'gh_check_pr\|gh_rerun\b' local-gh-mcp/README.md
```

Expected pre-edit hits:

- Line 43 — `gh_check_pr` row in the tool table.
- Line 64 — `gh_rerun` row in the tool table.

Rename both and update any accompanying command examples if they use the old tool names or the `number` param (unlikely given the README is mostly a tool list, but sanity-check with `grep '`number`' local-gh-mcp/README.md` before finishing).

Re-verify:

```bash
grep -n 'gh_check_pr\|gh_rerun\b' local-gh-mcp/README.md
```

Expected: empty.

**Step 3: Update `CLAUDE.md`**

Append one bullet under `## Conventions` documenting the rule, next to the existing "Tool annotations" and "Enums" entries. Suggested text:

```
- Resource-qualified IDs: tools accepting an integer ID declare `pr_number` or `issue_number`; string IDs use `run_id` or `cache_id`. Bare `number` or `id` properties are forbidden — `TestNoBareIDParameters` enforces this. Handler Go-local variables may keep the shorter `number` name since a `PR*` or `Issue*` call site is unambiguous.
```

**Step 4: Build and test to catch any broken references**

Run: `cd local-gh-mcp && make audit`

Expected: clean. (Docs are not compiled, but `make audit` still covers code & tests.)

**Step 5: Commit**

```bash
git add local-gh-mcp/DESIGN.md local-gh-mcp/README.md local-gh-mcp/CLAUDE.md
git commit -m "$(cat <<'EOF'
docs(local-gh-mcp): document resource-qualified ID convention

Updates the tool-inventory and parameter tables in DESIGN.md and the
tool table in README.md to match Phase 2 renames. Adds a convention
bullet in CLAUDE.md noting the rule and the guard test.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Verification checklist

After all tasks are complete:

```bash
# No old names anywhere in the tool package
grep -rn 'gh_check_pr\|"gh_rerun"\|handleCheckPR\|handleRerun\b\|"number": float64\|intFromArgs(args, "number")\|"number is required"' local-gh-mcp/internal/tools/
# Expected: empty

# No old names in docs
grep -n 'gh_check_pr\|gh_rerun\b\|`number`' local-gh-mcp/README.md local-gh-mcp/DESIGN.md local-gh-mcp/CLAUDE.md
# Expected: empty

# Full audit
cd local-gh-mcp && make audit
# Expected: clean

# Commit history looks right
git log --oneline main..HEAD | head -10
# Expected: 6 new commits on top of current branch tip
```
