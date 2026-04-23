package format

import (
	"strings"
	"testing"
)

func TestFormatPRView(t *testing.T) {
	pr := PRView{
		Number:         42,
		Title:          "Fix bug",
		Body:           "This fixes the bug.",
		State:          "OPEN",
		Author:         Author{Login: "octocat"},
		BaseRefName:    "main",
		HeadRefName:    "fix-bug",
		URL:            "https://github.com/cli/cli/pull/42",
		IsDraft:        false,
		Mergeable:      "MERGEABLE",
		ReviewDecision: "APPROVED",
		Labels:         []Label{{Name: "bug"}},
		CreatedAt:      "2025-01-01T00:00:00Z",
		UpdatedAt:      "2025-01-02T00:00:00Z",
	}
	got := FormatPRView(pr, 10000)
	for _, want := range []string{
		"# PR #42: Fix bug (OPEN)",
		"@octocat",
		"main <- fix-bug",
		"2025-01-01",
		"Draft:** no",
		"MERGEABLE",
		"APPROVED",
		"bug",
		"This fixes the bug.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestFormatPRView_TruncatesBody(t *testing.T) {
	pr := PRView{
		Number: 1,
		Title:  "T",
		Body:   "hello world this is a long body",
		State:  "OPEN",
		Author: Author{Login: "a"},
	}
	got := FormatPRView(pr, 10)
	if !strings.Contains(got, "[truncated") {
		t.Errorf("expected truncation marker in:\n%s", got)
	}
}

func TestFormatIssueView(t *testing.T) {
	issue := IssueView{
		Number: 7,
		Title:  "Add feature",
		Body:   "Please add this.",
		State:  "OPEN",
		Author: Author{Login: "alice"},
		Labels: []Label{{Name: "enhancement"}},
		Milestone: &struct {
			Title string `json:"title"`
		}{Title: "v1.0"},
		URL:       "https://github.com/cli/cli/issues/7",
		CreatedAt: "2025-02-01T00:00:00Z",
		UpdatedAt: "2025-02-02T00:00:00Z",
	}
	got := FormatIssueView(issue, 10000)
	for _, want := range []string{
		"# Issue #7: Add feature (OPEN)",
		"@alice",
		"enhancement",
		"v1.0",
		"Please add this.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestFormatComments(t *testing.T) {
	comments := []Comment{
		{
			Author:            Author{Login: "alice"},
			AuthorAssociation: "MEMBER",
			Body:              "Looks good!",
			CreatedAt:         "2025-01-01T00:00:00Z",
		},
		{
			Author:            Author{Login: "spammer"},
			AuthorAssociation: "",
			Body:              "Buy stuff!",
			CreatedAt:         "2025-01-02T00:00:00Z",
			IsMinimized:       true,
			MinimizedReason:   "SPAM",
		},
	}
	got := FormatComments(comments, 10000)
	for _, want := range []string{
		"## Comments (2)",
		"### @alice [MEMBER]",
		"2025-01-01",
		"Looks good!",
		"### @spammer",
		"[minimized: SPAM]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// Minimized comment body should NOT appear
	if strings.Contains(got, "Buy stuff!") {
		t.Error("minimized comment body should be hidden")
	}
}

func TestFormatComments_NONEAssociation(t *testing.T) {
	comments := []Comment{
		{
			Author:            Author{Login: "visitor"},
			AuthorAssociation: "NONE",
			Body:              "Nice work!",
			CreatedAt:         "2025-01-01T00:00:00Z",
		},
	}
	got := FormatComments(comments, 10000)
	if strings.Contains(got, "[NONE]") {
		t.Error("NONE author association should not be displayed")
	}
	if !strings.Contains(got, "### @visitor (") {
		t.Errorf("expected header without association in:\n%s", got)
	}
}

func TestFormatComments_Empty(t *testing.T) {
	got := FormatComments(nil, 10000)
	if got != "No comments." {
		t.Errorf("got %q, want %q", got, "No comments.")
	}
}

func TestFormatComments_StripImages(t *testing.T) {
	comments := []Comment{
		{
			Author:    Author{Login: "bob"},
			Body:      "See ![screenshot](https://img.png) here",
			CreatedAt: "2025-01-01T00:00:00Z",
		},
	}
	got := FormatComments(comments, 10000)
	if strings.Contains(got, "![screenshot]") {
		t.Error("image markdown should be stripped")
	}
	if !strings.Contains(got, "[image]") {
		t.Error("image should be replaced with [image]")
	}
}

func TestFormatReviews(t *testing.T) {
	reviews := []Review{
		{
			Author:            Author{Login: "alice"},
			AuthorAssociation: "MEMBER",
			Body:              "LGTM",
			State:             "APPROVED",
			SubmittedAt:       "2026-04-10T00:00:00Z",
		},
		{
			Author:            Author{Login: "bob"},
			AuthorAssociation: "NONE",
			Body:              "",
			State:             "COMMENTED",
			SubmittedAt:       "2026-04-11T00:00:00Z",
		},
	}
	got := FormatReviews(reviews, 10000)
	for _, want := range []string{
		"## Reviews (2)",
		"### @alice [MEMBER] — APPROVED (2026-04-10)",
		"LGTM",
		"### @bob — COMMENTED (2026-04-11)",
		"(no body)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "[NONE]") {
		t.Error("NONE author association should not be displayed")
	}
}

func TestFormatReviews_Empty(t *testing.T) {
	got := FormatReviews(nil, 10000)
	if got != "No reviews." {
		t.Errorf("got %q, want %q", got, "No reviews.")
	}
}

func TestFormatReviewComments(t *testing.T) {
	comments := []ReviewComment{
		{
			ID:        1,
			User:      RESTUser{Login: "alice", Type: "User"},
			Body:      "nil-check this",
			Path:      "src/foo.go",
			Line:      42,
			CreatedAt: "2026-04-10T00:00:00Z",
		},
		{
			ID:          2,
			InReplyToID: 1,
			User:        RESTUser{Login: "author", Type: "User"},
			Body:        "done",
			Path:        "src/foo.go",
			Line:        42,
			CreatedAt:   "2026-04-11T00:00:00Z",
		},
		{
			ID:        3,
			User:      RESTUser{Login: "bot", Type: "Bot"},
			Body:      "style issue",
			Path:      "src/bar.go",
			Line:      7,
			CreatedAt: "2026-04-12T00:00:00Z",
		},
	}
	got := FormatReviewComments(comments, 10000)
	for _, want := range []string{
		"## Review Comments (3)",
		"### src/foo.go",
		"**Line 42** — @alice (2026-04-10):",
		"nil-check this",
		"↳ @author (2026-04-11):",
		"done",
		"### src/bar.go",
		"**Line 7** — @bot [bot] (2026-04-12):",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestFormatReviewComments_Empty(t *testing.T) {
	got := FormatReviewComments(nil, 10000)
	if got != "No review comments." {
		t.Errorf("got %q, want %q", got, "No review comments.")
	}
}

func TestFormatReviewComments_OrphanReply(t *testing.T) {
	// Reply whose parent isn't in the page should render as a root.
	comments := []ReviewComment{
		{
			ID:          5,
			InReplyToID: 999,
			User:        RESTUser{Login: "late", Type: "User"},
			Body:        "late reply",
			Path:        "x.go",
			Line:        1,
			CreatedAt:   "2026-04-15T00:00:00Z",
		},
	}
	got := FormatReviewComments(comments, 10000)
	if !strings.Contains(got, "**Line 1** — @late") {
		t.Errorf("orphan reply should render as root, got:\n%s", got)
	}
}

func TestFormatReviewComments_FallsBackToOriginalLine(t *testing.T) {
	comments := []ReviewComment{
		{
			ID:           1,
			User:         RESTUser{Login: "alice", Type: "User"},
			Body:         "outdated",
			Path:         "x.go",
			Line:         0,
			OriginalLine: 17,
			CreatedAt:    "2026-04-10T00:00:00Z",
		},
	}
	got := FormatReviewComments(comments, 10000)
	if !strings.Contains(got, "**Line 17**") {
		t.Errorf("expected original_line fallback to Line 17, got:\n%s", got)
	}
}

func TestFormatCheckList(t *testing.T) {
	checks := []Check{
		{Name: "build", State: "SUCCESS"},
		{Name: "test", State: "FAILURE", Link: "https://example.com/run/1"},
		{Name: "lint", State: "SKIPPED"},
	}
	got := FormatCheckList(checks)
	for _, want := range []string{
		"## Status Checks (3)",
		"- build: SUCCESS",
		"- test: FAILURE (https://example.com/run/1)",
		"- lint: SKIPPED",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestFormatCheckList_Empty(t *testing.T) {
	got := FormatCheckList(nil)
	if got != "No status checks." {
		t.Errorf("got %q, want %q", got, "No status checks.")
	}
}

func TestFormatPRListItem(t *testing.T) {
	pr := PRListItem{
		Number:      13053,
		Title:       "fix(repo list): use search",
		State:       "OPEN",
		Author:      Author{Login: "Maa-ly"},
		HeadRefName: "fix-repo-list",
		IsDraft:     false,
		UpdatedAt:   "2026-03-28T00:00:00Z",
	}
	got := FormatPRListItem(pr)
	for _, want := range []string{
		"**#13053**",
		"fix(repo list): use search",
		"@Maa-ly",
		"OPEN",
		"2026-03-28",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestFormatPRListItem_Draft(t *testing.T) {
	pr := PRListItem{
		Number:  1,
		Title:   "WIP",
		State:   "OPEN",
		Author:  Author{Login: "dev"},
		IsDraft: true,
	}
	got := FormatPRListItem(pr)
	if !strings.Contains(got, "DRAFT") {
		t.Errorf("missing DRAFT in:\n%s", got)
	}
}

func TestFormatIssueListItem(t *testing.T) {
	item := IssueListItem{
		Number:    5,
		Title:     "Bug report",
		State:     "OPEN",
		Author:    Author{Login: "user1"},
		Labels:    []Label{{Name: "bug"}, {Name: "priority"}},
		UpdatedAt: "2025-03-01T00:00:00Z",
	}
	got := FormatIssueListItem(item)
	for _, want := range []string{
		"**#5**",
		"Bug report",
		"@user1",
		"OPEN",
		"labels: bug, priority",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestFormatRunListItem(t *testing.T) {
	item := RunListItem{
		DatabaseID:   123,
		Name:         "Build",
		DisplayTitle: "Build",
		Status:       "completed",
		Conclusion:   "success",
		Event:        "push",
		HeadBranch:   "main",
		UpdatedAt:    "2025-01-01T00:00:00Z",
	}
	got := FormatRunListItem(item)
	for _, want := range []string{
		"**#123**",
		"Build",
		"completed/success",
		"push",
		"main",
		"2025-01-01",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestFormatRunView(t *testing.T) {
	run := RunView{
		DatabaseID:   456,
		Name:         "CI",
		DisplayTitle: "CI Pipeline",
		Status:       "completed",
		Conclusion:   "failure",
		Event:        "pull_request",
		HeadBranch:   "feature",
		HeadSha:      "abc1234567890def",
		URL:          "https://github.com/cli/cli/actions/runs/456",
		CreatedAt:    "2025-01-01T00:00:00Z",
		UpdatedAt:    "2025-01-02T00:00:00Z",
		Jobs: []Job{
			{DatabaseID: 111, Name: "build", Status: "completed", Conclusion: "success", URL: "https://example.com/job/1"},
			{DatabaseID: 222, Name: "test", Status: "completed", Conclusion: "failure", URL: "https://example.com/job/2"},
		},
	}
	got := FormatRunView(run)
	for _, want := range []string{
		"# Run #456: CI Pipeline",
		"completed/failure",
		"abc1234",
		"feature",
		"(job_id: 111)",
		"(job_id: 222)",
		"— success",
		"— failure (https://example.com/job/2)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// SHA should be truncated to 7
	if strings.Contains(got, "abc1234567890def") {
		t.Error("SHA should be truncated to 7 chars")
	}
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
	got := FormatSearchPRItem(item)
	for _, want := range []string{
		"cli/cli#42",
		"Fix bug",
		"@octocat",
		"OPEN",
		"2025-01-01",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestFormatSearchRepoItem(t *testing.T) {
	item := SearchRepoItem{
		FullName:        "cli/cli",
		Description:     "GitHub's CLI",
		URL:             "https://github.com/cli/cli",
		StargazersCount: 35000,
		Language:        "Go",
		UpdatedAt:       "2025-01-01T00:00:00Z",
	}
	got := FormatSearchRepoItem(item)
	for _, want := range []string{
		"cli/cli",
		"GitHub's CLI",
		"35000",
		"Go",
		"updated 2025-01-01",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestFormatSearchCodeItem(t *testing.T) {
	item := SearchCodeItem{
		Path:       "main.go",
		Repository: Repository{NameWithOwner: "cli/cli"},
		SHA:        "abc1234",
		TextMatches: []TextMatch{
			{Fragment: "func main()"},
		},
		URL: "https://github.com/cli/cli/blob/abc1234/main.go",
	}
	got := FormatSearchCodeItem(item)
	for _, want := range []string{
		"cli/cli",
		"main.go",
		"func main()",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestFormatSearchCommitItem(t *testing.T) {
	item := SearchCommitItem{
		SHA:        "abc1234567890",
		Commit:     CommitDetail{Message: "fix: resolve issue", Author: CommitAuthor{Date: "2025-06-15T00:00:00Z"}},
		Author:     Author{Login: "dev"},
		Repository: Repository{NameWithOwner: "cli/cli"},
		URL:        "https://github.com/cli/cli/commit/abc1234",
	}
	got := FormatSearchCommitItem(item)
	for _, want := range []string{
		"abc1234",
		"fix: resolve issue",
		"@dev",
		"cli/cli",
		"2025-06-15",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// SHA should not be wrapped in backticks
	if strings.Contains(got, "`abc1234`") {
		t.Error("SHA should not be wrapped in backticks")
	}
}
