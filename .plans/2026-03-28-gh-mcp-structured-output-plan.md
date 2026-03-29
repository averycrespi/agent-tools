# Structured Markdown Output for local-gh-mcp — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use Skill(executing-plans) to implement this plan task-by-task.

**Goal:** Replace raw JSON output from all local-gh-mcp read tools with structured markdown that's easier for LLM callers to parse, and split monolithic view tools so callers fetch only what they need.

**Architecture:** Add an `internal/format/` package with pure formatting functions. Each tool handler unmarshals `gh.Client` JSON output into lightweight Go structs, then calls format functions to produce markdown. Two new tools (`gh_list_pr_comments`, `gh_list_issue_comments`) decompose the monolithic view responses. The `gh.Client` layer is unchanged except for new comment-fetching methods and updated field constants.

**Tech Stack:** Go stdlib (`encoding/json`, `fmt`, `strings`, `regexp`, `strconv`), testify for assertions.

---

### Task 1: Format package — core helpers

Create the `internal/format/` package with the foundational helpers used by all subsequent tasks.

**Files:**
- Create: `local-gh-mcp/internal/format/format.go`
- Test: `local-gh-mcp/internal/format/format_test.go`

**Step 1: Write the failing tests**

```go
// local-gh-mcp/internal/format/format_test.go
package format

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFormatAuthor_Regular(t *testing.T) {
	a := Author{Login: "daviddl9"}
	assert.Equal(t, "@daviddl9", FormatAuthor(a))
}

func TestFormatAuthor_Bot(t *testing.T) {
	a := Author{Login: "dependabot", IsBot: true}
	assert.Equal(t, "@dependabot [bot]", FormatAuthor(a))
}

func TestFormatAuthor_Empty(t *testing.T) {
	a := Author{}
	assert.Equal(t, "@unknown", FormatAuthor(a))
}

func TestFormatDate_Full(t *testing.T) {
	assert.Equal(t, "2025-02-09", FormatDate("2025-02-09T10:26:21Z"))
}

func TestFormatDate_Empty(t *testing.T) {
	assert.Equal(t, "", FormatDate(""))
}

func TestFormatDate_Short(t *testing.T) {
	assert.Equal(t, "bad", FormatDate("bad"))
}

func TestTruncateBody_Short(t *testing.T) {
	assert.Equal(t, "hello", TruncateBody("hello", 100))
}

func TestTruncateBody_Exact(t *testing.T) {
	assert.Equal(t, "hello", TruncateBody("hello", 5))
}

func TestTruncateBody_Long(t *testing.T) {
	body := "word1 word2 word3 word4 word5"
	result := TruncateBody(body, 15)
	assert.Contains(t, result, "word1 word2")
	assert.Contains(t, result, "[truncated")
	assert.NotContains(t, result, "word5")
}

func TestTruncateBody_Zero(t *testing.T) {
	assert.Equal(t, "", TruncateBody("anything", 0))
}

func TestStripImages(t *testing.T) {
	assert.Equal(t, "[image]", StripImages("![alt text](http://example.com/img.png)"))
	assert.Equal(t, "before [image] after", StripImages("before ![alt](url) after"))
	assert.Equal(t, "no images here", StripImages("no images here"))
}

func TestFormatLabels(t *testing.T) {
	labels := []Label{{Name: "bug"}, {Name: "enhancement"}}
	assert.Equal(t, "bug, enhancement", FormatLabels(labels))
}

func TestFormatLabels_Empty(t *testing.T) {
	assert.Equal(t, "(none)", FormatLabels(nil))
}
```

**Step 2: Run tests to verify they fail**

Run: `cd local-gh-mcp && go test ./internal/format/ -v`
Expected: compilation error — package doesn't exist yet.

**Step 3: Write the implementation**

```go
// local-gh-mcp/internal/format/format.go
package format

import (
	"fmt"
	"regexp"
	"strings"
)

// Author represents a GitHub user in JSON responses.
type Author struct {
	Login string `json:"login"`
	IsBot bool   `json:"is_bot"`
}

// Label represents a GitHub label.
type Label struct {
	Name string `json:"name"`
}

// FormatAuthor returns "@login" or "@login [bot]".
func FormatAuthor(a Author) string {
	login := a.Login
	if login == "" {
		login = "unknown"
	}
	if a.IsBot {
		return "@" + login + " [bot]"
	}
	return "@" + login
}

// FormatDate extracts YYYY-MM-DD from an ISO 8601 timestamp.
func FormatDate(ts string) string {
	if len(ts) < 10 {
		return ts
	}
	return ts[:10]
}

// TruncateBody truncates text to maxLen characters on a whitespace boundary.
// If maxLen is 0, returns empty string. Appends a truncation marker if cut.
func TruncateBody(text string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if len(text) <= maxLen {
		return text
	}
	// Find last space before maxLen.
	cut := strings.LastIndex(text[:maxLen], " ")
	if cut <= 0 {
		cut = maxLen
	}
	return fmt.Sprintf("%s\n\n[truncated — %d/%d chars shown]", text[:cut], cut, len(text))
}

var imagePattern = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)

// StripImages replaces markdown image syntax with [image].
func StripImages(text string) string {
	return imagePattern.ReplaceAllString(text, "[image]")
}

// FormatLabels formats a slice of labels as "a, b, c" or "(none)".
func FormatLabels(labels []Label) string {
	if len(labels) == 0 {
		return "(none)"
	}
	names := make([]string, len(labels))
	for i, l := range labels {
		names[i] = l.Name
	}
	return strings.Join(names, ", ")
}
```

**Step 4: Run tests to verify they pass**

Run: `cd local-gh-mcp && go test ./internal/format/ -v`
Expected: all PASS.

**Step 5: Commit**

```bash
cd local-gh-mcp && git add internal/format/format.go internal/format/format_test.go
git commit -m "feat(gh-mcp): add format package with core helpers"
```

---

### Task 2: Format package — diff summary parser

Add `ParseDiffSummary` to parse unified diff text and produce a file summary table.

**Files:**
- Modify: `local-gh-mcp/internal/format/format.go`
- Modify: `local-gh-mcp/internal/format/format_test.go`

**Step 1: Write the failing tests**

Append to `format_test.go`:

```go
func TestParseDiffSummary_TwoFiles(t *testing.T) {
	diff := `diff --git a/foo.go b/foo.go
index abc..def 100644
--- a/foo.go
+++ b/foo.go
@@ -1,3 +1,4 @@
 line1
+added
 line2
 line3
diff --git a/bar.go b/bar.go
index abc..def 100644
--- a/bar.go
+++ b/bar.go
@@ -10,5 +10,4 @@
 existing
-removed
 remaining
`
	result := ParseDiffSummary(diff)
	assert.Contains(t, result, "Files changed (2)")
	assert.Contains(t, result, "foo.go")
	assert.Contains(t, result, "+1 -0")
	assert.Contains(t, result, "bar.go")
	assert.Contains(t, result, "+0 -1")
}

func TestParseDiffSummary_Empty(t *testing.T) {
	assert.Equal(t, "", ParseDiffSummary(""))
}

func TestFormatDiff(t *testing.T) {
	diff := `diff --git a/foo.go b/foo.go
--- a/foo.go
+++ b/foo.go
@@ -1,3 +1,4 @@
 line1
+added
 line2
 line3`
	result := FormatDiff(diff)
	assert.Contains(t, result, "## Files changed (1)")
	assert.Contains(t, result, "## Diff")
	assert.Contains(t, result, "+added")
}
```

**Step 2: Run tests to verify they fail**

Run: `cd local-gh-mcp && go test ./internal/format/ -v -run 'Diff'`
Expected: compilation error — `ParseDiffSummary` and `FormatDiff` undefined.

**Step 3: Write the implementation**

Append to `format.go`:

```go
// DiffFile represents one file's change summary from a unified diff.
type DiffFile struct {
	Path  string
	Added int
	Removed int
}

// ParseDiffSummary parses a unified diff and returns a markdown file summary table.
// Returns empty string if diff is empty.
func ParseDiffSummary(diff string) string {
	if diff == "" {
		return ""
	}
	var files []DiffFile
	var current *DiffFile
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			if current != nil {
				files = append(files, *current)
			}
			// Extract "b/path" from "diff --git a/path b/path"
			parts := strings.SplitN(line, " b/", 2)
			path := ""
			if len(parts) == 2 {
				path = parts[1]
			}
			current = &DiffFile{Path: path}
		} else if current != nil {
			if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
				current.Added++
			} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
				current.Removed++
			}
		}
	}
	if current != nil {
		files = append(files, *current)
	}
	if len(files) == 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Files changed (%d)\n\n", len(files))
	sb.WriteString("| File | Changes |\n")
	sb.WriteString("|------|--------|\n")
	for _, f := range files {
		fmt.Fprintf(&sb, "| %s | +%d -%d |\n", f.Path, f.Added, f.Removed)
	}
	return sb.String()
}

// FormatDiff prepends a file summary table to a raw unified diff.
func FormatDiff(diff string) string {
	summary := ParseDiffSummary(diff)
	if summary == "" {
		return diff
	}
	return summary + "\n## Diff\n\n" + diff
}
```

**Step 4: Run tests to verify they pass**

Run: `cd local-gh-mcp && go test ./internal/format/ -v`
Expected: all PASS.

**Step 5: Commit**

```bash
cd local-gh-mcp && git add internal/format/format.go internal/format/format_test.go
git commit -m "feat(gh-mcp): add diff summary parser to format package"
```

---

### Task 3: Format package — JSON struct types and view/list/check formatters

Add all the Go struct types representing `gh` JSON output and the formatting functions that turn them into markdown. This is the largest format package task — it defines the output format for every tool.

**Files:**
- Create: `local-gh-mcp/internal/format/github.go`
- Create: `local-gh-mcp/internal/format/github_test.go`

**Step 1: Write the failing tests**

```go
// local-gh-mcp/internal/format/github_test.go
package format

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFormatPRView(t *testing.T) {
	pr := PRView{
		Number:         42,
		Title:          "Fix bug",
		State:          "OPEN",
		Author:         Author{Login: "octocat"},
		BaseRefName:    "main",
		HeadRefName:    "fix-bug",
		IsDraft:        false,
		Mergeable:      "MERGEABLE",
		ReviewDecision: "APPROVED",
		Labels:         []Label{{Name: "bug"}},
		Body:           "This fixes the bug.",
		CreatedAt:      "2025-01-01T00:00:00Z",
		UpdatedAt:      "2025-01-02T00:00:00Z",
	}
	result := FormatPRView(pr, 2000)
	assert.Contains(t, result, "# PR #42: Fix bug (OPEN)")
	assert.Contains(t, result, "@octocat")
	assert.Contains(t, result, "main <- fix-bug")
	assert.Contains(t, result, "MERGEABLE")
	assert.Contains(t, result, "APPROVED")
	assert.Contains(t, result, "bug")
	assert.Contains(t, result, "This fixes the bug.")
}

func TestFormatPRView_TruncatesBody(t *testing.T) {
	pr := PRView{
		Number: 1,
		Title:  "T",
		State:  "OPEN",
		Author: Author{Login: "a"},
		Body:   "word1 word2 word3 word4 word5 word6 word7 word8 word9 word10",
	}
	result := FormatPRView(pr, 20)
	assert.Contains(t, result, "[truncated")
}

func TestFormatIssueView(t *testing.T) {
	issue := IssueView{
		Number:    100,
		Title:     "Something broken",
		State:     "CLOSED",
		Author:    Author{Login: "celloza"},
		Labels:    []Label{{Name: "bug"}, {Name: "gh-repo"}},
		Body:      "Describe the bug.",
		CreatedAt: "2024-12-03T23:01:53Z",
		UpdatedAt: "2024-12-06T21:12:52Z",
	}
	result := FormatIssueView(issue, 2000)
	assert.Contains(t, result, "# Issue #100: Something broken (CLOSED)")
	assert.Contains(t, result, "@celloza")
	assert.Contains(t, result, "bug, gh-repo")
	assert.Contains(t, result, "Describe the bug.")
}

func TestFormatComments(t *testing.T) {
	comments := []Comment{
		{
			Author:            Author{Login: "alice"},
			AuthorAssociation: "MEMBER",
			Body:              "Looks good!",
			CreatedAt:         "2025-01-01T00:00:00Z",
			IsMinimized:       false,
		},
		{
			Author:          Author{Login: "spammer"},
			Body:            "buy stuff",
			CreatedAt:       "2025-01-02T00:00:00Z",
			IsMinimized:     true,
			MinimizedReason: "SPAM",
		},
	}
	result := FormatComments(comments, 2000)
	assert.Contains(t, result, "## Comments (2)")
	assert.Contains(t, result, "@alice [MEMBER]")
	assert.Contains(t, result, "Looks good!")
	assert.Contains(t, result, "[minimized: SPAM]")
	assert.NotContains(t, result, "buy stuff")
}

func TestFormatComments_Empty(t *testing.T) {
	assert.Equal(t, "No comments.", FormatComments(nil, 2000))
}

func TestFormatComments_StripImages(t *testing.T) {
	comments := []Comment{
		{
			Author:    Author{Login: "user"},
			Body:      "![screenshot](http://img.png)",
			CreatedAt: "2025-01-01T00:00:00Z",
		},
	}
	result := FormatComments(comments, 2000)
	assert.Contains(t, result, "[image]")
	assert.NotContains(t, result, "http://img.png")
}

func TestFormatCheckList(t *testing.T) {
	checks := []Check{
		{Name: "build", State: "SUCCESS"},
		{Name: "test", State: "FAILURE", Link: "https://example.com/run/1"},
		{Name: "lint", State: "SKIPPED"},
	}
	result := FormatCheckList(checks)
	assert.Contains(t, result, "## Status Checks (3)")
	assert.Contains(t, result, "- build: SUCCESS")
	assert.Contains(t, result, "- test: FAILURE (https://example.com/run/1)")
	assert.Contains(t, result, "- lint: SKIPPED")
}

func TestFormatCheckList_Empty(t *testing.T) {
	assert.Equal(t, "No status checks.", FormatCheckList(nil))
}

func TestFormatPRListItem(t *testing.T) {
	pr := PRListItem{
		Number:      13053,
		Title:       "fix(repo list): use search for private visibility",
		State:       "OPEN",
		Author:      Author{Login: "Maa-ly"},
		HeadRefName: "fix/repo-list-private-visibility",
		UpdatedAt:   "2026-03-28T11:51:45Z",
	}
	result := FormatPRListItem(pr)
	assert.Contains(t, result, "**#13053**")
	assert.Contains(t, result, "fix(repo list): use search for private visibility")
	assert.Contains(t, result, "@Maa-ly")
	assert.Contains(t, result, "OPEN")
	assert.Contains(t, result, "2026-03-28")
}

func TestFormatIssueListItem(t *testing.T) {
	item := IssueListItem{
		Number:    10000,
		Title:     "allow-forking bug",
		State:     "CLOSED",
		Author:    Author{Login: "celloza"},
		Labels:    []Label{{Name: "bug"}, {Name: "gh-repo"}},
		UpdatedAt: "2024-12-06T00:00:00Z",
	}
	result := FormatIssueListItem(item)
	assert.Contains(t, result, "**#10000**")
	assert.Contains(t, result, "@celloza")
	assert.Contains(t, result, "bug, gh-repo")
}

func TestFormatRunListItem(t *testing.T) {
	item := RunListItem{
		DatabaseID:   23696524799,
		Name:         "CI",
		DisplayTitle: "Triage Scheduled Tasks",
		Status:       "completed",
		Conclusion:   "success",
		Event:        "schedule",
		HeadBranch:   "trunk",
		UpdatedAt:    "2026-03-28T23:16:09Z",
	}
	result := FormatRunListItem(item)
	assert.Contains(t, result, "**#23696524799**")
	assert.Contains(t, result, "Triage Scheduled Tasks")
	assert.Contains(t, result, "completed/success")
	assert.Contains(t, result, "schedule")
	assert.Contains(t, result, "trunk")
}

func TestFormatRunView(t *testing.T) {
	run := RunView{
		DatabaseID:   123,
		Name:         "CI",
		DisplayTitle: "Fix build",
		Status:       "completed",
		Conclusion:   "failure",
		Event:        "push",
		HeadBranch:   "main",
		HeadSha:      "abc1234def5678",
		CreatedAt:    "2025-01-01T00:00:00Z",
		UpdatedAt:    "2025-01-01T00:05:00Z",
		Jobs: []Job{
			{Name: "build", Status: "completed", Conclusion: "success"},
			{Name: "test", Status: "completed", Conclusion: "failure", URL: "https://example.com"},
		},
	}
	result := FormatRunView(run)
	assert.Contains(t, result, "# Run #123: Fix build (completed/failure)")
	assert.Contains(t, result, "push")
	assert.Contains(t, result, "main")
	assert.Contains(t, result, "abc1234")
	assert.Contains(t, result, "- build: success")
	assert.Contains(t, result, "- test: failure (https://example.com)")
}

func TestFormatSearchPRItem(t *testing.T) {
	item := SearchPRItem{
		Number:     42,
		Title:      "Fix bug",
		State:      "OPEN",
		Author:     Author{Login: "octocat"},
		Repository: Repository{NameWithOwner: "cli/cli"},
		UpdatedAt:  "2025-01-01T00:00:00Z",
	}
	result := FormatSearchPRItem(item)
	assert.Contains(t, result, "**cli/cli#42**")
	assert.Contains(t, result, "@octocat")
}

func TestFormatSearchRepoItem(t *testing.T) {
	item := SearchRepoItem{
		FullName:        "cli/cli",
		Description:     "GitHub CLI",
		StargazersCount: 38200,
		Language:        "Go",
		UpdatedAt:       "2025-01-01T00:00:00Z",
	}
	result := FormatSearchRepoItem(item)
	assert.Contains(t, result, "**cli/cli**")
	assert.Contains(t, result, "GitHub CLI")
	assert.Contains(t, result, "Go")
	assert.Contains(t, result, "38200")
}

func TestFormatSearchCodeItem(t *testing.T) {
	item := SearchCodeItem{
		Path:       "pkg/cmd/repo/list/http.go",
		Repository: Repository{NameWithOwner: "cli/cli"},
		TextMatches: []TextMatch{
			{Fragment: "func listRepos("},
		},
	}
	result := FormatSearchCodeItem(item)
	assert.Contains(t, result, "**cli/cli**")
	assert.Contains(t, result, "pkg/cmd/repo/list/http.go")
	assert.Contains(t, result, "func listRepos(")
}

func TestFormatSearchCommitItem(t *testing.T) {
	item := SearchCommitItem{
		SHA:        "abc1234def5678",
		Repository: Repository{NameWithOwner: "cli/cli"},
		Commit:     CommitDetail{Message: "fix: handle edge case"},
		Author:     Author{Login: "octocat"},
	}
	result := FormatSearchCommitItem(item)
	assert.Contains(t, result, "**cli/cli**")
	assert.Contains(t, result, "abc1234")
	assert.Contains(t, result, "fix: handle edge case")
	assert.Contains(t, result, "@octocat")
}
```

**Step 2: Run tests to verify they fail**

Run: `cd local-gh-mcp && go test ./internal/format/ -v -run 'GitHub|PRView|IssueView|Comment|Check|ListItem|RunView|Search'`
Expected: compilation error — types and functions undefined.

**Step 3: Write the implementation**

```go
// local-gh-mcp/internal/format/github.go
package format

import (
	"fmt"
	"strings"
)

// --- Struct types for JSON unmarshalling ---

type PRView struct {
	Number         int      `json:"number"`
	Title          string   `json:"title"`
	Body           string   `json:"body"`
	State          string   `json:"state"`
	Author         Author   `json:"author"`
	BaseRefName    string   `json:"baseRefName"`
	HeadRefName    string   `json:"headRefName"`
	URL            string   `json:"url"`
	IsDraft        bool     `json:"isDraft"`
	Mergeable      string   `json:"mergeable"`
	ReviewDecision string   `json:"reviewDecision"`
	Labels         []Label  `json:"labels"`
	CreatedAt      string   `json:"createdAt"`
	UpdatedAt      string   `json:"updatedAt"`
}

type IssueView struct {
	Number    int      `json:"number"`
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	State     string   `json:"state"`
	Author    Author   `json:"author"`
	Labels    []Label  `json:"labels"`
	Milestone *struct {
		Title string `json:"title"`
	} `json:"milestone"`
	URL       string `json:"url"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type Comment struct {
	Author            Author `json:"author"`
	AuthorAssociation string `json:"authorAssociation"`
	Body              string `json:"body"`
	CreatedAt         string `json:"createdAt"`
	IsMinimized       bool   `json:"isMinimized"`
	MinimizedReason   string `json:"minimizedReason"`
}

type Check struct {
	Name  string `json:"name"`
	State string `json:"state"`
	Link  string `json:"link"`
}

type PRListItem struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	State       string `json:"state"`
	Author      Author `json:"author"`
	HeadRefName string `json:"headRefName"`
	IsDraft     bool   `json:"isDraft"`
	UpdatedAt   string `json:"updatedAt"`
}

type IssueListItem struct {
	Number    int     `json:"number"`
	Title     string  `json:"title"`
	State     string  `json:"state"`
	Author    Author  `json:"author"`
	Labels    []Label `json:"labels"`
	UpdatedAt string  `json:"updatedAt"`
}

type RunListItem struct {
	DatabaseID   int64  `json:"databaseId"`
	Name         string `json:"name"`
	DisplayTitle string `json:"displayTitle"`
	Status       string `json:"status"`
	Conclusion   string `json:"conclusion"`
	Event        string `json:"event"`
	HeadBranch   string `json:"headBranch"`
	UpdatedAt    string `json:"updatedAt"`
}

type Job struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
}

type RunView struct {
	DatabaseID   int64  `json:"databaseId"`
	Name         string `json:"name"`
	DisplayTitle string `json:"displayTitle"`
	Status       string `json:"status"`
	Conclusion   string `json:"conclusion"`
	Event        string `json:"event"`
	HeadBranch   string `json:"headBranch"`
	HeadSha      string `json:"headSha"`
	URL          string `json:"url"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
	Jobs         []Job  `json:"jobs"`
}

type Repository struct {
	NameWithOwner string `json:"nameWithOwner"`
}

type SearchPRItem struct {
	Number     int        `json:"number"`
	Title      string     `json:"title"`
	State      string     `json:"state"`
	Author     Author     `json:"author"`
	Repository Repository `json:"repository"`
	UpdatedAt  string     `json:"updatedAt"`
}

type SearchIssueItem = SearchPRItem // Same fields.

type SearchRepoItem struct {
	FullName        string `json:"fullName"`
	Description     string `json:"description"`
	URL             string `json:"url"`
	StargazersCount int    `json:"stargazersCount"`
	Language        string `json:"language"`
	UpdatedAt       string `json:"updatedAt"`
}

type TextMatch struct {
	Fragment string `json:"fragment"`
}

type SearchCodeItem struct {
	Path        string     `json:"path"`
	Repository  Repository `json:"repository"`
	SHA         string     `json:"sha"`
	TextMatches []TextMatch `json:"textMatches"`
	URL         string     `json:"url"`
}

type CommitDetail struct {
	Message string `json:"message"`
}

type SearchCommitItem struct {
	SHA        string       `json:"sha"`
	Commit     CommitDetail `json:"commit"`
	Author     Author       `json:"author"`
	Repository Repository   `json:"repository"`
	URL        string       `json:"url"`
}

// --- Formatting functions ---

func FormatPRView(pr PRView, maxBodyLen int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# PR #%d: %s (%s)\n\n", pr.Number, pr.Title, pr.State)
	fmt.Fprintf(&sb, "**Author:** %s | **Base:** %s <- %s\n", FormatAuthor(pr.Author), pr.BaseRefName, pr.HeadRefName)
	fmt.Fprintf(&sb, "**Created:** %s | **Updated:** %s\n", FormatDate(pr.CreatedAt), FormatDate(pr.UpdatedAt))
	draft := "no"
	if pr.IsDraft {
		draft = "yes"
	}
	mergeable := pr.Mergeable
	if mergeable == "" {
		mergeable = "unknown"
	}
	review := pr.ReviewDecision
	if review == "" {
		review = "none"
	}
	fmt.Fprintf(&sb, "**Draft:** %s | **Mergeable:** %s | **Review:** %s\n", draft, mergeable, review)
	fmt.Fprintf(&sb, "**Labels:** %s\n", FormatLabels(pr.Labels))
	if pr.Body != "" {
		sb.WriteString("\n## Description\n\n")
		sb.WriteString(TruncateBody(pr.Body, maxBodyLen))
		sb.WriteString("\n")
	}
	return sb.String()
}

func FormatIssueView(issue IssueView, maxBodyLen int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Issue #%d: %s (%s)\n\n", issue.Number, issue.Title, issue.State)
	fmt.Fprintf(&sb, "**Author:** %s | **Labels:** %s\n", FormatAuthor(issue.Author), FormatLabels(issue.Labels))
	fmt.Fprintf(&sb, "**Created:** %s | **Updated:** %s\n", FormatDate(issue.CreatedAt), FormatDate(issue.UpdatedAt))
	milestone := "(none)"
	if issue.Milestone != nil && issue.Milestone.Title != "" {
		milestone = issue.Milestone.Title
	}
	fmt.Fprintf(&sb, "**Milestone:** %s\n", milestone)
	if issue.Body != "" {
		sb.WriteString("\n## Description\n\n")
		sb.WriteString(TruncateBody(issue.Body, maxBodyLen))
		sb.WriteString("\n")
	}
	return sb.String()
}

func FormatComments(comments []Comment, maxBodyLen int) string {
	if len(comments) == 0 {
		return "No comments."
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Comments (%d)\n", len(comments))
	for _, c := range comments {
		authorStr := FormatAuthor(c.Author)
		if c.AuthorAssociation != "" && c.AuthorAssociation != "NONE" {
			authorStr += " [" + c.AuthorAssociation + "]"
		}
		fmt.Fprintf(&sb, "\n### %s (%s)\n\n", authorStr, FormatDate(c.CreatedAt))
		if c.IsMinimized {
			reason := c.MinimizedReason
			if reason == "" {
				reason = "hidden"
			}
			fmt.Fprintf(&sb, "[minimized: %s]\n", reason)
		} else {
			body := StripImages(c.Body)
			sb.WriteString(TruncateBody(body, maxBodyLen))
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func FormatCheckList(checks []Check) string {
	if len(checks) == 0 {
		return "No status checks."
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Status Checks (%d)\n\n", len(checks))
	for _, c := range checks {
		if (c.State == "FAILURE" || c.State == "ERROR") && c.Link != "" {
			fmt.Fprintf(&sb, "- %s: %s (%s)\n", c.Name, c.State, c.Link)
		} else {
			fmt.Fprintf(&sb, "- %s: %s\n", c.Name, c.State)
		}
	}
	return sb.String()
}

func FormatPRListItem(pr PRListItem) string {
	draft := ""
	if pr.IsDraft {
		draft = ", draft"
	}
	return fmt.Sprintf("- **#%d** %s — %s, %s%s, updated %s",
		pr.Number, pr.Title, FormatAuthor(pr.Author), pr.State, draft, FormatDate(pr.UpdatedAt))
}

func FormatIssueListItem(item IssueListItem) string {
	labels := ""
	if len(item.Labels) > 0 {
		labels = ", labels: " + FormatLabels(item.Labels)
	}
	return fmt.Sprintf("- **#%d** %s — %s, %s%s, updated %s",
		item.Number, item.Title, FormatAuthor(item.Author), item.State, labels, FormatDate(item.UpdatedAt))
}

func FormatRunListItem(item RunListItem) string {
	conclusion := item.Status
	if item.Conclusion != "" {
		conclusion = item.Status + "/" + item.Conclusion
	}
	return fmt.Sprintf("- **#%d** %s — %s, %s, %s, %s",
		item.DatabaseID, item.DisplayTitle, conclusion, item.Event, item.HeadBranch, FormatDate(item.UpdatedAt))
}

func FormatRunView(run RunView) string {
	var sb strings.Builder
	conclusion := run.Status
	if run.Conclusion != "" {
		conclusion = run.Status + "/" + run.Conclusion
	}
	fmt.Fprintf(&sb, "# Run #%d: %s (%s)\n\n", run.DatabaseID, run.DisplayTitle, conclusion)
	sha := run.HeadSha
	if len(sha) > 7 {
		sha = sha[:7]
	}
	fmt.Fprintf(&sb, "**Event:** %s | **Branch:** %s | **SHA:** %s\n", run.Event, run.HeadBranch, sha)
	fmt.Fprintf(&sb, "**Created:** %s | **Updated:** %s\n", FormatDate(run.CreatedAt), FormatDate(run.UpdatedAt))
	if len(run.Jobs) > 0 {
		sb.WriteString("\n## Jobs\n\n")
		for _, j := range run.Jobs {
			status := j.Conclusion
			if status == "" {
				status = j.Status
			}
			if (j.Conclusion == "failure" || j.Conclusion == "cancelled") && j.URL != "" {
				fmt.Fprintf(&sb, "- %s: %s (%s)\n", j.Name, status, j.URL)
			} else {
				fmt.Fprintf(&sb, "- %s: %s\n", j.Name, status)
			}
		}
	}
	return sb.String()
}

func FormatSearchPRItem(item SearchPRItem) string {
	return fmt.Sprintf("- **%s#%d** %s — %s, %s, updated %s",
		item.Repository.NameWithOwner, item.Number, item.Title,
		FormatAuthor(item.Author), item.State, FormatDate(item.UpdatedAt))
}

func FormatSearchRepoItem(item SearchRepoItem) string {
	lang := item.Language
	if lang == "" {
		lang = "unknown"
	}
	return fmt.Sprintf("- **%s** %s — %s, %d stars, updated %s",
		item.FullName, item.Description, lang, item.StargazersCount, FormatDate(item.UpdatedAt))
}

func FormatSearchCodeItem(item SearchCodeItem) string {
	preview := ""
	if len(item.TextMatches) > 0 {
		preview = item.TextMatches[0].Fragment
	}
	return fmt.Sprintf("- **%s** %s — %s",
		item.Repository.NameWithOwner, item.Path, preview)
}

func FormatSearchCommitItem(item SearchCommitItem) string {
	sha := item.SHA
	if len(sha) > 7 {
		sha = sha[:7]
	}
	// Use first line of commit message.
	msg := item.Commit.Message
	if idx := strings.IndexByte(msg, '\n'); idx >= 0 {
		msg = msg[:idx]
	}
	return fmt.Sprintf("- **%s** %s %s — %s",
		item.Repository.NameWithOwner, sha, msg, FormatAuthor(item.Author))
}
```

**Step 4: Run tests to verify they pass**

Run: `cd local-gh-mcp && go test ./internal/format/ -v`
Expected: all PASS.

**Step 5: Commit**

```bash
cd local-gh-mcp && git add internal/format/github.go internal/format/github_test.go
git commit -m "feat(gh-mcp): add GitHub struct types and markdown formatters"
```

---

### Task 4: GH client — add comment methods and update field constants

Add `PRComments` and `IssueComments` methods to `gh.Client`. Update `prViewFields` and `issueViewFields` to drop comments (they'll be served by the new comment tools).

**Files:**
- Modify: `local-gh-mcp/internal/gh/gh.go:28-31` (field constants) and append new methods
- Modify: `local-gh-mcp/internal/gh/gh_test.go` (add tests for new methods)

**Step 1: Write the failing tests**

Append to `gh_test.go`:

```go
func TestPRComments(t *testing.T) {
	r := &mockRunner{
		fn: func(name string, args ...string) ([]byte, error) {
			assert.Equal(t, "gh", name)
			assert.Contains(t, args, "pr")
			assert.Contains(t, args, "view")
			assert.Contains(t, args, "--json")
			assert.Contains(t, args, "comments")
			assert.Contains(t, args, "42")
			return []byte(`{"comments":[]}`), nil
		},
	}
	c := NewClient(r)
	out, err := c.PRComments(context.Background(), "cli", "cli", 42, 30)
	require.NoError(t, err)
	assert.Contains(t, out, "comments")
}

func TestIssueComments(t *testing.T) {
	r := &mockRunner{
		fn: func(name string, args ...string) ([]byte, error) {
			assert.Equal(t, "gh", name)
			assert.Contains(t, args, "issue")
			assert.Contains(t, args, "view")
			assert.Contains(t, args, "--json")
			assert.Contains(t, args, "comments")
			assert.Contains(t, args, "100")
			return []byte(`{"comments":[]}`), nil
		},
	}
	c := NewClient(r)
	out, err := c.IssueComments(context.Background(), "cli", "cli", 100, 30)
	require.NoError(t, err)
	assert.Contains(t, out, "comments")
}
```

**Step 2: Run tests to verify they fail**

Run: `cd local-gh-mcp && go test ./internal/gh/ -v -run 'Comments'`
Expected: compilation error — `PRComments` and `IssueComments` undefined.

**Step 3: Write the implementation**

In `gh.go`, update the field constants (lines 28-31):

```go
// Change prViewFields — remove statusCheckRollup (there's a dedicated check tool), keep everything else.
const (
	prViewFields  = "number,title,body,state,author,baseRefName,headRefName,url,isDraft,mergeable,reviewDecision,labels,assignees,createdAt,updatedAt"
	prListFields  = "number,title,state,author,headRefName,url,isDraft,createdAt,updatedAt"
	prCheckFields = "name,state,description,link,startedAt,completedAt"
)
```

Update `issueViewFields` (line 298) — remove `comments`:

```go
const (
	issueViewFields = "number,title,body,state,author,labels,assignees,milestone,url,createdAt,updatedAt"
	issueListFields = "number,title,state,author,labels,url,createdAt,updatedAt"
)
```

Add a new constant for comment fields:

```go
const commentFields = "author,authorAssociation,body,createdAt,isMinimized,minimizedReason"
```

Append new methods to `gh.go`:

```go
// PRComments retrieves comments on a pull request.
func (c *Client) PRComments(_ context.Context, owner, repo string, number int, limit int) (string, error) {
	out, err := c.runner.Run("gh", "pr", "view", "-R", repoFlag(owner, repo),
		"--json", "comments", "--jq",
		fmt.Sprintf(".comments[:%d] | map({author,authorAssociation,body,createdAt,isMinimized,minimizedReason})", clampLimit(limit)),
		strconv.Itoa(number))
	if err != nil {
		return "", fmt.Errorf("gh pr comments failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// IssueComments retrieves comments on an issue.
func (c *Client) IssueComments(_ context.Context, owner, repo string, number int, limit int) (string, error) {
	out, err := c.runner.Run("gh", "issue", "view", "-R", repoFlag(owner, repo),
		"--json", "comments", "--jq",
		fmt.Sprintf(".comments[:%d] | map({author,authorAssociation,body,createdAt,isMinimized,minimizedReason})", clampLimit(limit)),
		strconv.Itoa(number))
	if err != nil {
		return "", fmt.Errorf("gh issue comments failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
```

**Step 4: Run tests to verify they pass**

Run: `cd local-gh-mcp && go test ./internal/gh/ -v`
Expected: all PASS.

**Step 5: Commit**

```bash
cd local-gh-mcp && git add internal/gh/gh.go internal/gh/gh_test.go
git commit -m "feat(gh-mcp): add comment methods, drop comments from view field lists"
```

---

### Task 5: Update GHClient interface and mock

Add the new `PRComments` and `IssueComments` methods to the `GHClient` interface in `tools.go` and the `mockGHClient` in `pr_test.go`.

**Files:**
- Modify: `local-gh-mcp/internal/tools/tools.go:11-43` (GHClient interface)
- Modify: `local-gh-mcp/internal/tools/pr_test.go:13-38` (mockGHClient struct) and append mock method implementations

**Step 1: Update the interface**

In `tools.go`, add to the `GHClient` interface after the existing `CommentIssue` line:

```go
	// Comment listing operations
	PRComments(ctx context.Context, owner, repo string, number int, limit int) (string, error)
	IssueComments(ctx context.Context, owner, repo string, number int, limit int) (string, error)
```

**Step 2: Update the mock**

In `pr_test.go`, add fields to `mockGHClient`:

```go
	prCommentsFunc     func(ctx context.Context, owner, repo string, number int, limit int) (string, error)
	issueCommentsFunc  func(ctx context.Context, owner, repo string, number int, limit int) (string, error)
```

Add method implementations:

```go
func (m *mockGHClient) PRComments(ctx context.Context, owner, repo string, number int, limit int) (string, error) {
	if m.prCommentsFunc != nil {
		return m.prCommentsFunc(ctx, owner, repo, number, limit)
	}
	return "", nil
}

func (m *mockGHClient) IssueComments(ctx context.Context, owner, repo string, number int, limit int) (string, error) {
	if m.issueCommentsFunc != nil {
		return m.issueCommentsFunc(ctx, owner, repo, number, limit)
	}
	return "", nil
}
```

**Step 3: Run tests to verify everything compiles**

Run: `cd local-gh-mcp && go test ./internal/tools/ -v`
Expected: all existing tests PASS.

**Step 4: Commit**

```bash
cd local-gh-mcp && git add internal/tools/tools.go internal/tools/pr_test.go
git commit -m "feat(gh-mcp): add comment methods to GHClient interface and mock"
```

---

### Task 6: Format gh_view_pr handler output

Update `handleViewPR` to parse JSON and return structured markdown. Add `max_body_length` parameter to the tool schema.

**Files:**
- Modify: `local-gh-mcp/internal/tools/pr.go:66-87` (tool schema) and `pr.go:392-407` (handler)
- Modify: `local-gh-mcp/internal/tools/pr_test.go` (update test)

**Step 1: Update the test**

Change `TestViewPR_Success` in `pr_test.go` to return realistic JSON and assert markdown output:

```go
func TestViewPR_Success(t *testing.T) {
	h := NewHandler(&mockGHClient{
		viewPRFunc: func(_ context.Context, owner, repo string, number int) (string, error) {
			assert.Equal(t, "octocat", owner)
			assert.Equal(t, "hello-world", repo)
			assert.Equal(t, 42, number)
			return `{"number":42,"title":"Fix bug","body":"Fixes it","state":"OPEN","author":{"login":"octocat","is_bot":false},"baseRefName":"main","headRefName":"fix","isDraft":false,"mergeable":"MERGEABLE","reviewDecision":"APPROVED","labels":[],"createdAt":"2025-01-01T00:00:00Z","updatedAt":"2025-01-02T00:00:00Z"}`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_view_pr"
	req.Params.Arguments = map[string]any{
		"owner":  "octocat",
		"repo":   "hello-world",
		"number": float64(42),
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "# PR #42: Fix bug (OPEN)")
	assert.Contains(t, text, "@octocat")
	assert.Contains(t, text, "main <- fix")
	assert.Contains(t, text, "Fixes it")
}
```

**Step 2: Run test to verify it fails**

Run: `cd local-gh-mcp && go test ./internal/tools/ -v -run TestViewPR_Success`
Expected: FAIL — output is raw JSON, not markdown.

**Step 3: Update the tool schema and handler**

In `pr.go`, add `max_body_length` to the `gh_view_pr` schema properties:

```go
"max_body_length": map[string]any{
	"type":        "number",
	"description": "Max body length in chars (default 2000, max 50000). Set to 0 to omit body.",
},
```

Update `handleViewPR`:

```go
func (h *Handler) handleViewPR(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	number := intFromArgs(args, "number")
	if number == 0 {
		return gomcp.NewToolResultError("number is required"), nil
	}
	maxBody := clampMaxBodyLength(intFromArgs(args, "max_body_length"))
	out, err := h.gh.ViewPR(ctx, owner, repo, number)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var pr format.PRView
	if err := json.Unmarshal([]byte(out), &pr); err != nil {
		return gomcp.NewToolResultError(fmt.Sprintf("failed to parse PR JSON: %v", err)), nil
	}
	return gomcp.NewToolResultText(format.FormatPRView(pr, maxBody)), nil
}
```

Add `clampMaxBodyLength` helper to `tools.go`:

```go
const (
	defaultMaxBodyLength = 2000
	maxMaxBodyLength     = 50000
)

func clampMaxBodyLength(v int) int {
	if v <= 0 {
		return defaultMaxBodyLength
	}
	if v > maxMaxBodyLength {
		return maxMaxBodyLength
	}
	return v
}
```

Add import for `"encoding/json"` and the format package to `pr.go`:

```go
import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/format"
	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/gh"
	gomcp "github.com/mark3labs/mcp-go/mcp"
)
```

**Step 4: Run tests to verify they pass**

Run: `cd local-gh-mcp && go test ./internal/tools/ -v`
Expected: all PASS.

**Step 5: Commit**

```bash
cd local-gh-mcp && git add internal/tools/pr.go internal/tools/pr_test.go internal/tools/tools.go
git commit -m "feat(gh-mcp): format gh_view_pr output as structured markdown"
```

---

### Task 7: Format gh_view_issue handler output

Same pattern as Task 6 but for issues. Update handler to parse JSON and return markdown. Add `max_body_length` parameter.

**Files:**
- Modify: `local-gh-mcp/internal/tools/issue.go:10-107` (schema) and `issue.go:109-124` (handler)
- Modify: `local-gh-mcp/internal/tools/issue_test.go`

**Step 1: Update the test**

Add a test in `issue_test.go`:

```go
func TestViewIssue_FormatsMarkdown(t *testing.T) {
	h := NewHandler(&mockGHClient{
		viewIssueFunc: func(_ context.Context, owner, repo string, number int) (string, error) {
			return `{"number":100,"title":"Bug report","body":"Steps to reproduce","state":"OPEN","author":{"login":"alice"},"labels":[{"name":"bug"}],"milestone":null,"createdAt":"2025-01-01T00:00:00Z","updatedAt":"2025-01-02T00:00:00Z"}`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_view_issue"
	req.Params.Arguments = map[string]any{
		"owner":  "octocat",
		"repo":   "hello-world",
		"number": float64(100),
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "# Issue #100: Bug report (OPEN)")
	assert.Contains(t, text, "@alice")
	assert.Contains(t, text, "bug")
	assert.Contains(t, text, "Steps to reproduce")
}
```

**Step 2: Run test to verify it fails**

Run: `cd local-gh-mcp && go test ./internal/tools/ -v -run TestViewIssue_FormatsMarkdown`
Expected: FAIL — output is raw JSON.

**Step 3: Update the schema and handler**

Add `max_body_length` to `gh_view_issue` schema. Update `handleViewIssue`:

```go
func (h *Handler) handleViewIssue(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	number := intFromArgs(args, "number")
	if number == 0 {
		return gomcp.NewToolResultError("number is required"), nil
	}
	maxBody := clampMaxBodyLength(intFromArgs(args, "max_body_length"))
	out, err := h.gh.ViewIssue(ctx, owner, repo, number)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var issue format.IssueView
	if err := json.Unmarshal([]byte(out), &issue); err != nil {
		return gomcp.NewToolResultError(fmt.Sprintf("failed to parse issue JSON: %v", err)), nil
	}
	return gomcp.NewToolResultText(format.FormatIssueView(issue, maxBody)), nil
}
```

Add imports for `"encoding/json"`, `"fmt"`, and format package to `issue.go`.

**Step 4: Run tests to verify they pass**

Run: `cd local-gh-mcp && go test ./internal/tools/ -v`
Expected: all PASS.

**Step 5: Commit**

```bash
cd local-gh-mcp && git add internal/tools/issue.go internal/tools/issue_test.go
git commit -m "feat(gh-mcp): format gh_view_issue output as structured markdown"
```

---

### Task 8: New gh_list_pr_comments and gh_list_issue_comments tools

Add two new MCP tools that fetch and format comments separately.

**Files:**
- Modify: `local-gh-mcp/internal/tools/pr.go` (add tool def + handler)
- Modify: `local-gh-mcp/internal/tools/issue.go` (add tool def + handler)
- Modify: `local-gh-mcp/internal/tools/tools.go:68-119` (add dispatch cases)
- Modify: `local-gh-mcp/internal/tools/pr_test.go` (add test)
- Modify: `local-gh-mcp/internal/tools/issue_test.go` (add test)
- Modify: `local-gh-mcp/internal/tools/search_test.go` (update tool count assertion from 24 to 26)

**Step 1: Write the failing tests**

In `pr_test.go`:

```go
func TestListPRComments_Success(t *testing.T) {
	h := NewHandler(&mockGHClient{
		prCommentsFunc: func(_ context.Context, owner, repo string, number int, limit int) (string, error) {
			assert.Equal(t, "octocat", owner)
			assert.Equal(t, "hello-world", repo)
			assert.Equal(t, 42, number)
			return `[{"author":{"login":"reviewer"},"authorAssociation":"MEMBER","body":"LGTM","createdAt":"2025-01-01T00:00:00Z","isMinimized":false,"minimizedReason":""}]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_list_pr_comments"
	req.Params.Arguments = map[string]any{
		"owner":  "octocat",
		"repo":   "hello-world",
		"number": float64(42),
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "## Comments (1)")
	assert.Contains(t, text, "@reviewer [MEMBER]")
	assert.Contains(t, text, "LGTM")
}
```

In `issue_test.go`:

```go
func TestListIssueComments_Success(t *testing.T) {
	h := NewHandler(&mockGHClient{
		issueCommentsFunc: func(_ context.Context, owner, repo string, number int, limit int) (string, error) {
			return `[{"author":{"login":"alice"},"authorAssociation":"NONE","body":"Thanks!","createdAt":"2025-01-01T00:00:00Z","isMinimized":false,"minimizedReason":""}]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_list_issue_comments"
	req.Params.Arguments = map[string]any{
		"owner":  "octocat",
		"repo":   "hello-world",
		"number": float64(100),
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "## Comments (1)")
	assert.Contains(t, text, "@alice")
	assert.Contains(t, text, "Thanks!")
}
```

**Step 2: Run tests to verify they fail**

Run: `cd local-gh-mcp && go test ./internal/tools/ -v -run 'ListPRComments|ListIssueComments'`
Expected: FAIL — unknown tool.

**Step 3: Write the implementation**

Add tool definitions to `prTools()` in `pr.go`:

```go
{
	Name:        "gh_list_pr_comments",
	Description: "List comments on a pull request. Returns markdown-formatted comment list.",
	InputSchema: gomcp.ToolInputSchema{
		Type: "object",
		Properties: map[string]any{
			"owner": map[string]any{
				"type":        "string",
				"description": "Repository owner",
			},
			"repo": map[string]any{
				"type":        "string",
				"description": "Repository name",
			},
			"number": map[string]any{
				"type":        "number",
				"description": "Pull request number",
			},
			"max_body_length": map[string]any{
				"type":        "number",
				"description": "Max body length per comment in chars (default 2000, max 50000)",
			},
			"limit": map[string]any{
				"type":        "number",
				"description": "Max comments to return (default 30, max 100)",
			},
		},
		Required: []string{"owner", "repo", "number"},
	},
},
```

Add handler in `pr.go`:

```go
func (h *Handler) handleListPRComments(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	number := intFromArgs(args, "number")
	if number == 0 {
		return gomcp.NewToolResultError("number is required"), nil
	}
	maxBody := clampMaxBodyLength(intFromArgs(args, "max_body_length"))
	limit := intFromArgs(args, "limit")
	out, err := h.gh.PRComments(ctx, owner, repo, number, limit)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var comments []format.Comment
	if err := json.Unmarshal([]byte(out), &comments); err != nil {
		return gomcp.NewToolResultError(fmt.Sprintf("failed to parse comments JSON: %v", err)), nil
	}
	return gomcp.NewToolResultText(format.FormatComments(comments, maxBody)), nil
}
```

Add the same pattern for `gh_list_issue_comments` in `issue.go` (tool def + `handleListIssueComments`).

Add dispatch cases in `tools.go` Handle switch:

```go
case "gh_list_pr_comments":
	return h.handleListPRComments(ctx, req)
case "gh_list_issue_comments":
	return h.handleListIssueComments(ctx, req)
```

Update the total tool count assertion in `search_test.go` from 24 to 26.

**Step 4: Run tests to verify they pass**

Run: `cd local-gh-mcp && go test ./internal/tools/ -v`
Expected: all PASS.

**Step 5: Commit**

```bash
cd local-gh-mcp && git add internal/tools/pr.go internal/tools/issue.go internal/tools/tools.go internal/tools/pr_test.go internal/tools/issue_test.go internal/tools/search_test.go
git commit -m "feat(gh-mcp): add gh_list_pr_comments and gh_list_issue_comments tools"
```

---

### Task 9: Format gh_diff_pr handler output

Update `handleDiffPR` to prepend the file summary table.

**Files:**
- Modify: `local-gh-mcp/internal/tools/pr.go:431-446` (handler)
- Modify: `local-gh-mcp/internal/tools/pr_test.go`

**Step 1: Write the failing test**

```go
func TestDiffPR_FormatsWithSummary(t *testing.T) {
	diffText := "diff --git a/foo.go b/foo.go\n--- a/foo.go\n+++ b/foo.go\n@@ -1,3 +1,4 @@\n line1\n+added\n line2\n line3"
	h := NewHandler(&mockGHClient{
		diffPRFunc: func(_ context.Context, owner, repo string, number int) (string, error) {
			return diffText, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_diff_pr"
	req.Params.Arguments = map[string]any{
		"owner":  "octocat",
		"repo":   "hello-world",
		"number": float64(1),
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "## Files changed (1)")
	assert.Contains(t, text, "foo.go")
	assert.Contains(t, text, "+1 -0")
	assert.Contains(t, text, "## Diff")
	assert.Contains(t, text, "+added")
}
```

**Step 2: Run test to verify it fails**

Run: `cd local-gh-mcp && go test ./internal/tools/ -v -run TestDiffPR_FormatsWithSummary`
Expected: FAIL — no summary table in output.

**Step 3: Update the handler**

```go
func (h *Handler) handleDiffPR(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	number := intFromArgs(args, "number")
	if number == 0 {
		return gomcp.NewToolResultError("number is required"), nil
	}
	out, err := h.gh.DiffPR(ctx, owner, repo, number)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return gomcp.NewToolResultText(format.FormatDiff(out)), nil
}
```

**Step 4: Run tests to verify they pass**

Run: `cd local-gh-mcp && go test ./internal/tools/ -v`
Expected: all PASS.

**Step 5: Commit**

```bash
cd local-gh-mcp && git add internal/tools/pr.go internal/tools/pr_test.go
git commit -m "feat(gh-mcp): format gh_diff_pr with file summary table"
```

---

### Task 10: Format gh_check_pr handler output

Update `handleCheckPR` to parse JSON and return markdown bullet list.

**Files:**
- Modify: `local-gh-mcp/internal/tools/pr.go:555-570` (handler)
- Modify: `local-gh-mcp/internal/tools/pr_test.go`

**Step 1: Write the failing test**

```go
func TestCheckPR_FormatsMarkdown(t *testing.T) {
	h := NewHandler(&mockGHClient{
		checkPRFunc: func(_ context.Context, owner, repo string, number int) (string, error) {
			return `[{"name":"build","state":"SUCCESS","link":""},{"name":"test","state":"FAILURE","link":"https://example.com/run/1"}]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_check_pr"
	req.Params.Arguments = map[string]any{
		"owner":  "octocat",
		"repo":   "hello-world",
		"number": float64(1),
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "## Status Checks (2)")
	assert.Contains(t, text, "- build: SUCCESS")
	assert.Contains(t, text, "- test: FAILURE (https://example.com/run/1)")
}
```

**Step 2: Run test to verify it fails**

Run: `cd local-gh-mcp && go test ./internal/tools/ -v -run TestCheckPR_FormatsMarkdown`
Expected: FAIL — output is raw JSON.

**Step 3: Update the handler**

```go
func (h *Handler) handleCheckPR(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	number := intFromArgs(args, "number")
	if number == 0 {
		return gomcp.NewToolResultError("number is required"), nil
	}
	out, err := h.gh.CheckPR(ctx, owner, repo, number)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var checks []format.Check
	if err := json.Unmarshal([]byte(out), &checks); err != nil {
		return gomcp.NewToolResultError(fmt.Sprintf("failed to parse checks JSON: %v", err)), nil
	}
	return gomcp.NewToolResultText(format.FormatCheckList(checks)), nil
}
```

**Step 4: Run tests to verify they pass**

Run: `cd local-gh-mcp && go test ./internal/tools/ -v`
Expected: all PASS.

**Step 5: Commit**

```bash
cd local-gh-mcp && git add internal/tools/pr.go internal/tools/pr_test.go
git commit -m "feat(gh-mcp): format gh_check_pr as markdown bullet list"
```

---

### Task 11: Format gh_list_prs and gh_list_issues handlers

Update both list handlers to parse JSON arrays and return markdown bullets.

**Files:**
- Modify: `local-gh-mcp/internal/tools/pr.go:409-429` (handleListPRs)
- Modify: `local-gh-mcp/internal/tools/issue.go:126-146` (handleListIssues)
- Modify: `local-gh-mcp/internal/tools/pr_test.go`
- Modify: `local-gh-mcp/internal/tools/issue_test.go`

**Step 1: Write the failing tests**

In `pr_test.go`:

```go
func TestListPRs_FormatsMarkdown(t *testing.T) {
	h := NewHandler(&mockGHClient{
		listPRsFunc: func(_ context.Context, owner, repo string, opts gh.ListPROpts) (string, error) {
			return `[{"number":1,"title":"Fix","state":"OPEN","author":{"login":"alice"},"headRefName":"fix","isDraft":false,"updatedAt":"2025-01-01T00:00:00Z"}]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_list_prs"
	req.Params.Arguments = map[string]any{"owner": "octocat", "repo": "hello-world"}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "**#1**")
	assert.Contains(t, text, "Fix")
	assert.Contains(t, text, "@alice")
}
```

In `issue_test.go`:

```go
func TestListIssues_FormatsMarkdown(t *testing.T) {
	h := NewHandler(&mockGHClient{
		listIssuesFunc: func(_ context.Context, owner, repo string, opts gh.ListIssuesOpts) (string, error) {
			return `[{"number":10,"title":"Bug","state":"OPEN","author":{"login":"bob"},"labels":[{"name":"bug"}],"updatedAt":"2025-01-01T00:00:00Z"}]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_list_issues"
	req.Params.Arguments = map[string]any{"owner": "octocat", "repo": "hello-world"}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "**#10**")
	assert.Contains(t, text, "Bug")
	assert.Contains(t, text, "@bob")
	assert.Contains(t, text, "bug")
}
```

**Step 2: Run tests to verify they fail**

Run: `cd local-gh-mcp && go test ./internal/tools/ -v -run 'TestListPRs_FormatsMarkdown|TestListIssues_FormatsMarkdown'`
Expected: FAIL — output is raw JSON.

**Step 3: Update the handlers**

`handleListPRs`:

```go
func (h *Handler) handleListPRs(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	opts := gh.ListPROpts{
		State:  stringFromArgs(args, "state"),
		Author: stringFromArgs(args, "author"),
		Label:  stringFromArgs(args, "label"),
		Base:   stringFromArgs(args, "base"),
		Head:   stringFromArgs(args, "head"),
		Search: stringFromArgs(args, "search"),
		Limit:  intFromArgs(args, "limit"),
	}
	out, err := h.gh.ListPRs(ctx, owner, repo, opts)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var items []format.PRListItem
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		return gomcp.NewToolResultError(fmt.Sprintf("failed to parse PR list JSON: %v", err)), nil
	}
	var lines []string
	for _, item := range items {
		lines = append(lines, format.FormatPRListItem(item))
	}
	if len(lines) == 0 {
		return gomcp.NewToolResultText("No pull requests found."), nil
	}
	return gomcp.NewToolResultText(strings.Join(lines, "\n")), nil
}
```

`handleListIssues` — same pattern with `format.IssueListItem` and `format.FormatIssueListItem`.

Add `"strings"` import to both files.

**Step 4: Run tests to verify they pass**

Run: `cd local-gh-mcp && go test ./internal/tools/ -v`
Expected: all PASS.

**Step 5: Commit**

```bash
cd local-gh-mcp && git add internal/tools/pr.go internal/tools/issue.go internal/tools/pr_test.go internal/tools/issue_test.go
git commit -m "feat(gh-mcp): format gh_list_prs and gh_list_issues as markdown bullets"
```

---

### Task 12: Format gh_list_runs and gh_view_run handlers

Update both run handlers to return markdown.

**Files:**
- Modify: `local-gh-mcp/internal/tools/run.go:123-158` (both handlers)
- Modify: `local-gh-mcp/internal/tools/run_test.go`

**Step 1: Write the failing tests**

```go
func TestListRuns_FormatsMarkdown(t *testing.T) {
	h := NewHandler(&mockGHClient{
		listRunsFunc: func(_ context.Context, owner, repo string, opts gh.ListRunsOpts) (string, error) {
			return `[{"databaseId":123,"name":"CI","displayTitle":"Build","status":"completed","conclusion":"success","event":"push","headBranch":"main","updatedAt":"2025-01-01T00:00:00Z"}]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_list_runs"
	req.Params.Arguments = map[string]any{"owner": "octocat", "repo": "hello-world"}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "**#123**")
	assert.Contains(t, text, "Build")
	assert.Contains(t, text, "completed/success")
}

func TestViewRun_FormatsMarkdown(t *testing.T) {
	h := NewHandler(&mockGHClient{
		viewRunFunc: func(_ context.Context, owner, repo string, runID string, logFailed bool) (string, error) {
			if logFailed {
				return "some log output", nil
			}
			return `{"databaseId":123,"name":"CI","displayTitle":"Build","status":"completed","conclusion":"success","event":"push","headBranch":"main","headSha":"abc1234def","createdAt":"2025-01-01T00:00:00Z","updatedAt":"2025-01-01T00:05:00Z","jobs":[{"name":"build","status":"completed","conclusion":"success","url":""}]}`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_view_run"
	req.Params.Arguments = map[string]any{"owner": "octocat", "repo": "hello-world", "run_id": "123"}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "# Run #123: Build (completed/success)")
	assert.Contains(t, text, "- build: success")
}

func TestViewRun_LogFailed_Passthrough(t *testing.T) {
	h := NewHandler(&mockGHClient{
		viewRunFunc: func(_ context.Context, owner, repo string, runID string, logFailed bool) (string, error) {
			assert.True(t, logFailed)
			return "raw log output here", nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_view_run"
	req.Params.Arguments = map[string]any{"owner": "octocat", "repo": "hello-world", "run_id": "123", "log_failed": true}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Equal(t, "raw log output here", text)
}
```

**Step 2: Run tests to verify they fail**

Run: `cd local-gh-mcp && go test ./internal/tools/ -v -run 'TestListRuns_Formats|TestViewRun_Formats|TestViewRun_LogFailed'`
Expected: FAIL — output is raw JSON.

**Step 3: Update the handlers**

```go
func (h *Handler) handleListRuns(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	opts := gh.ListRunsOpts{
		Branch:   stringFromArgs(args, "branch"),
		Status:   stringFromArgs(args, "status"),
		Workflow: stringFromArgs(args, "workflow"),
		Limit:    intFromArgs(args, "limit"),
	}
	out, err := h.gh.ListRuns(ctx, owner, repo, opts)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var items []format.RunListItem
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		return gomcp.NewToolResultError(fmt.Sprintf("failed to parse run list JSON: %v", err)), nil
	}
	var lines []string
	for _, item := range items {
		lines = append(lines, format.FormatRunListItem(item))
	}
	if len(lines) == 0 {
		return gomcp.NewToolResultText("No workflow runs found."), nil
	}
	return gomcp.NewToolResultText(strings.Join(lines, "\n")), nil
}

func (h *Handler) handleViewRun(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	runID := stringFromArgs(args, "run_id")
	if runID == "" {
		return gomcp.NewToolResultError("run_id is required"), nil
	}
	logFailed := boolFromArgs(args, "log_failed")
	out, err := h.gh.ViewRun(ctx, owner, repo, runID, logFailed)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	// log_failed mode returns raw logs — pass through unchanged.
	if logFailed {
		return gomcp.NewToolResultText(out), nil
	}
	var run format.RunView
	if err := json.Unmarshal([]byte(out), &run); err != nil {
		return gomcp.NewToolResultError(fmt.Sprintf("failed to parse run JSON: %v", err)), nil
	}
	return gomcp.NewToolResultText(format.FormatRunView(run)), nil
}
```

Add imports for `"encoding/json"`, `"fmt"`, `"strings"`, and format package to `run.go`.

**Step 4: Run tests to verify they pass**

Run: `cd local-gh-mcp && go test ./internal/tools/ -v`
Expected: all PASS.

**Step 5: Commit**

```bash
cd local-gh-mcp && git add internal/tools/run.go internal/tools/run_test.go
git commit -m "feat(gh-mcp): format gh_list_runs and gh_view_run as structured markdown"
```

---

### Task 13: Format all search tool handlers

Update all 5 search handlers to parse JSON and return markdown bullets.

**Files:**
- Modify: `local-gh-mcp/internal/tools/search.go:193-294` (all 5 handlers)
- Modify: `local-gh-mcp/internal/tools/search_test.go`

**Step 1: Write the failing tests**

```go
func TestSearchPRs_FormatsMarkdown(t *testing.T) {
	h := NewHandler(&mockGHClient{
		searchPRsFunc: func(_ context.Context, query string, opts gh.SearchPRsOpts) (string, error) {
			return `[{"number":1,"title":"Fix","state":"OPEN","author":{"login":"alice"},"repository":{"nameWithOwner":"cli/cli"},"updatedAt":"2025-01-01T00:00:00Z"}]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_search_prs"
	req.Params.Arguments = map[string]any{"query": "fix"}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "**cli/cli#1**")
	assert.Contains(t, text, "@alice")
}

func TestSearchRepos_FormatsMarkdown(t *testing.T) {
	h := NewHandler(&mockGHClient{
		searchReposFunc: func(_ context.Context, query string, opts gh.SearchReposOpts) (string, error) {
			return `[{"fullName":"cli/cli","description":"GitHub CLI","stargazersCount":38200,"language":"Go","updatedAt":"2025-01-01T00:00:00Z"}]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_search_repos"
	req.Params.Arguments = map[string]any{"query": "cli"}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "**cli/cli**")
	assert.Contains(t, text, "Go")
}

func TestSearchCode_FormatsMarkdown(t *testing.T) {
	h := NewHandler(&mockGHClient{
		searchCodeFunc: func(_ context.Context, query string, opts gh.SearchCodeOpts) (string, error) {
			return `[{"path":"main.go","repository":{"nameWithOwner":"cli/cli"},"sha":"abc","textMatches":[{"fragment":"func main()"}],"url":""}]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_search_code"
	req.Params.Arguments = map[string]any{"query": "main"}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "**cli/cli**")
	assert.Contains(t, text, "main.go")
}

func TestSearchCommits_FormatsMarkdown(t *testing.T) {
	h := NewHandler(&mockGHClient{
		searchCommitsFunc: func(_ context.Context, query string, opts gh.SearchCommitsOpts) (string, error) {
			return `[{"sha":"abc1234def","commit":{"message":"fix: thing"},"author":{"login":"alice"},"repository":{"nameWithOwner":"cli/cli"},"url":""}]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_search_commits"
	req.Params.Arguments = map[string]any{"query": "fix"}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "**cli/cli**")
	assert.Contains(t, text, "abc1234")
	assert.Contains(t, text, "fix: thing")
}
```

**Step 2: Run tests to verify they fail**

Run: `cd local-gh-mcp && go test ./internal/tools/ -v -run 'TestSearch.*FormatsMarkdown'`
Expected: FAIL — output is raw JSON.

**Step 3: Update all 5 handlers**

Each handler follows the same pattern: unmarshal JSON array → format each item → join with newlines. Example for `handleSearchPRs`:

```go
func (h *Handler) handleSearchPRs(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	query := stringFromArgs(args, "query")
	if query == "" {
		return gomcp.NewToolResultError("query is required"), nil
	}
	opts := gh.SearchPRsOpts{
		Repo:   stringFromArgs(args, "repo"),
		Owner:  stringFromArgs(args, "owner"),
		State:  stringFromArgs(args, "state"),
		Author: stringFromArgs(args, "author"),
		Label:  stringFromArgs(args, "label"),
		Limit:  intFromArgs(args, "limit"),
	}
	out, err := h.gh.SearchPRs(ctx, query, opts)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var items []format.SearchPRItem
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		return gomcp.NewToolResultError(fmt.Sprintf("failed to parse search PR JSON: %v", err)), nil
	}
	var lines []string
	for _, item := range items {
		lines = append(lines, format.FormatSearchPRItem(item))
	}
	if len(lines) == 0 {
		return gomcp.NewToolResultText("No results found."), nil
	}
	return gomcp.NewToolResultText(strings.Join(lines, "\n")), nil
}
```

Apply the same pattern to `handleSearchIssues` (same struct as SearchPRItem / same format func as FormatSearchPRItem since `SearchIssueItem = SearchPRItem`), `handleSearchRepos`, `handleSearchCode`, and `handleSearchCommits`.

Add imports for `"encoding/json"`, `"fmt"`, `"strings"`, and format package to `search.go`.

**Step 4: Run tests to verify they pass**

Run: `cd local-gh-mcp && go test ./internal/tools/ -v`
Expected: all PASS.

**Step 5: Commit**

```bash
cd local-gh-mcp && git add internal/tools/search.go internal/tools/search_test.go
git commit -m "feat(gh-mcp): format all search tools as markdown bullets"
```

---

### Task 14: Update tool descriptions

Update MCP tool description strings to briefly indicate the output format, so callers know what to expect.

**Files:**
- Modify: `local-gh-mcp/internal/tools/pr.go` (descriptions for view, list, diff, check)
- Modify: `local-gh-mcp/internal/tools/issue.go` (descriptions for view, list)
- Modify: `local-gh-mcp/internal/tools/run.go` (descriptions for list, view)
- Modify: `local-gh-mcp/internal/tools/search.go` (descriptions for all 5)

**Step 1: Update descriptions**

Change tool `Description` fields. Examples:

- `gh_view_pr`: `"View pull request details. Returns structured markdown with metadata and description."`
- `gh_list_prs`: `"List pull requests. Returns markdown bullet list."`
- `gh_diff_pr`: `"Get pull request diff. Returns file summary table followed by unified diff."`
- `gh_check_pr`: `"View status checks for a pull request. Returns markdown bullet list."`
- `gh_view_issue`: `"View issue details. Returns structured markdown with metadata and description."`
- `gh_list_issues`: `"List issues. Returns markdown bullet list."`
- `gh_list_pr_comments`: `"List comments on a pull request. Returns markdown-formatted comment list."`
- `gh_list_issue_comments`: `"List comments on an issue. Returns markdown-formatted comment list."`
- `gh_list_runs`: `"List workflow runs. Returns markdown bullet list."`
- `gh_view_run`: `"View workflow run details. Returns structured markdown with job list. Use log_failed=true for raw failure logs."`
- Search tools: `"Search for [X]. Returns markdown bullet list."`

**Step 2: Run tests to verify nothing broke**

Run: `cd local-gh-mcp && go test ./internal/tools/ -v`
Expected: all PASS.

**Step 3: Commit**

```bash
cd local-gh-mcp && git add internal/tools/pr.go internal/tools/issue.go internal/tools/run.go internal/tools/search.go
git commit -m "docs(gh-mcp): update tool descriptions to reflect markdown output format"
```

---

### Task 15: Update CLAUDE.md and run full audit

Update project documentation and run the full audit to verify everything works.

**Files:**
- Modify: `local-gh-mcp/CLAUDE.md`

**Step 1: Update CLAUDE.md**

In the Architecture section, add `format/` to the directory listing:

```
internal/
  exec/                  Runner interface for command execution
  format/                Markdown formatting for tool output (authors, dates, truncation, diff summaries)
  gh/                    GitHub operations via exec.Runner
  tools/
    tools.go             Tool registration and dispatch
    pr.go                PR tool definitions and handlers (includes gh_list_pr_comments)
    issue.go             Issue tool definitions and handlers (includes gh_list_issue_comments)
    run.go               Workflow run tool definitions and handlers
    cache.go             Cache tool definitions and handlers
    search.go            Search tool definitions and handlers
```

In the Conventions section, replace the `JSON output` bullet:

```
- Markdown output: all read tools (view, list, search, diff, check) return structured markdown instead of raw JSON. Write tools (create, comment, merge, edit, close, rerun, cancel, delete) return plain text confirmations.
- Body truncation: tools returning text bodies accept `max_body_length` param (default 2000, max 50000). Bodies exceeding the limit are cut on a whitespace boundary with `[truncated — N/M chars shown]`.
- Author flattening: all author objects rendered as `@login` or `@login [bot]` — never raw JSON.
```

**Step 2: Run the full audit**

Run: `cd local-gh-mcp && make audit`
Expected: all checks pass (tidy, fmt, lint, test, govulncheck).

**Step 3: Commit**

```bash
cd local-gh-mcp && git add CLAUDE.md
git commit -m "docs(gh-mcp): update CLAUDE.md for structured markdown output"
```

<!-- Documentation updates are covered in this task -->
