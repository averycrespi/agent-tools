package format

import (
	"strconv"
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

func TestFormatPRView_HidesMergeableUnknown(t *testing.T) {
	pr := PRView{
		Number:    1,
		Title:     "T",
		State:     "OPEN",
		Author:    Author{Login: "a"},
		Mergeable: "UNKNOWN",
	}
	got := FormatPRView(pr, 1000)
	if strings.Contains(got, "Mergeable") {
		t.Errorf("expected Mergeable line to be hidden when value is UNKNOWN, got:\n%s", got)
	}
}

func TestFormatPRView_EmptyReviewDecision(t *testing.T) {
	pr := PRView{
		Number:         1,
		Title:          "T",
		State:          "OPEN",
		Author:         Author{Login: "a"},
		Mergeable:      "MERGEABLE",
		ReviewDecision: "",
	}
	got := FormatPRView(pr, 1000)
	if !strings.Contains(got, "**Review:** (none)") {
		t.Errorf("expected `(none)` review fallback, got:\n%s", got)
	}
	if strings.Contains(got, "**Review:** \n") || strings.Contains(got, "**Review:** \r\n") {
		t.Errorf("review value should not be empty/blank, got:\n%s", got)
	}
}

func TestFormatPRView_EmptyReviewDecision_NoMergeable(t *testing.T) {
	pr := PRView{
		Number:         2,
		Title:          "T",
		State:          "OPEN",
		Author:         Author{Login: "a"},
		Mergeable:      "UNKNOWN",
		ReviewDecision: "",
	}
	got := FormatPRView(pr, 1000)
	if !strings.Contains(got, "**Review:** (none)") {
		t.Errorf("expected `(none)` review fallback, got:\n%s", got)
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
	got := FormatComments(comments, 10000, 0)
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
	got := FormatComments(comments, 10000, 0)
	if strings.Contains(got, "[NONE]") {
		t.Error("NONE author association should not be displayed")
	}
	if !strings.Contains(got, "### @visitor (") {
		t.Errorf("expected header without association in:\n%s", got)
	}
}

func TestFormatComments_Empty(t *testing.T) {
	got := FormatComments(nil, 10000, 0)
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
	got := FormatComments(comments, 10000, 0)
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
	got := FormatReviews(reviews, 10000, 0)
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
	got := FormatReviews(nil, 10000, 0)
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
	got := FormatReviewComments(comments, 10000, 0)
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
	got := FormatReviewComments(nil, 10000, 0)
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
	got := FormatReviewComments(comments, 10000, 0)
	if !strings.Contains(got, "**Line 1** — @late") {
		t.Errorf("orphan reply should render as root, got:\n%s", got)
	}
}

func TestFormatReviewComments_AuthorAssociation(t *testing.T) {
	comments := []ReviewComment{
		{
			ID:                1,
			User:              RESTUser{Login: "alice", Type: "User"},
			AuthorAssociation: "MEMBER",
			Body:              "consider this",
			Path:              "x.go",
			Line:              1,
			CreatedAt:         "2026-04-10T00:00:00Z",
		},
		{
			ID:                2,
			InReplyToID:       1,
			User:              RESTUser{Login: "bob", Type: "User"},
			AuthorAssociation: "CONTRIBUTOR",
			Body:              "ack",
			Path:              "x.go",
			Line:              1,
			CreatedAt:         "2026-04-11T00:00:00Z",
		},
		{
			ID:                3,
			User:              RESTUser{Login: "drive-by", Type: "User"},
			AuthorAssociation: "NONE",
			Body:              "drive-by",
			Path:              "y.go",
			Line:              5,
			CreatedAt:         "2026-04-12T00:00:00Z",
		},
	}
	got := FormatReviewComments(comments, 10000, 0)
	for _, want := range []string{
		"@alice [MEMBER]",
		"@bob [CONTRIBUTOR]",
		"@drive-by (",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "[NONE]") {
		t.Errorf("NONE association should be suppressed in:\n%s", got)
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
	got := FormatReviewComments(comments, 10000, 0)
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
	got := FormatSearchPRItem(item, 200)
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
	if strings.Contains(got, "\n  > ") {
		t.Errorf("expected no body line for empty body, got:\n%s", got)
	}
}

func TestFormatSearchPRItem_EmptyBody(t *testing.T) {
	item := SearchPRItem{
		Number:     1,
		Title:      "T",
		State:      "OPEN",
		Author:     Author{Login: "alice"},
		Repository: Repository{NameWithOwner: "o/r"},
		Body:       "",
		UpdatedAt:  "2025-01-01T00:00:00Z",
	}
	got := FormatSearchPRItem(item, 200)
	if strings.Count(got, "\n") != 0 {
		t.Errorf("expected single line for empty body, got:\n%s", got)
	}
	if strings.Contains(got, "  > ") {
		t.Errorf("expected no quote line for empty body, got:\n%s", got)
	}
}

func TestFormatSearchPRItem_ShortBody(t *testing.T) {
	item := SearchPRItem{
		Number:     1,
		Title:      "T",
		State:      "OPEN",
		Author:     Author{Login: "alice"},
		Repository: Repository{NameWithOwner: "o/r"},
		Body:       "First line.\n\nSecond\tline.",
		UpdatedAt:  "2025-01-01T00:00:00Z",
	}
	got := FormatSearchPRItem(item, 200)
	if !strings.Contains(got, "\n  > First line. Second line.") {
		t.Errorf("expected whitespace-collapsed body line, got:\n%s", got)
	}
	if strings.Contains(got, "[truncated") {
		t.Errorf("short body should not be truncated, got:\n%s", got)
	}
}

func TestFormatSearchPRItem_LongBody(t *testing.T) {
	body := strings.Repeat("abcdefghij ", 30) // 330 bytes
	item := SearchPRItem{
		Number:     1,
		Title:      "T",
		State:      "OPEN",
		Author:     Author{Login: "alice"},
		Repository: Repository{NameWithOwner: "o/r"},
		Body:       body,
		UpdatedAt:  "2025-01-01T00:00:00Z",
	}
	got := FormatSearchPRItem(item, 50)
	if !strings.Contains(got, "\n  > ") {
		t.Errorf("expected body excerpt line, got:\n%s", got)
	}
	if !strings.Contains(got, "[truncated — showing ") {
		t.Errorf("expected unified truncation marker, got:\n%s", got)
	}
	if !strings.Contains(got, " of "+strconv.Itoa(len(strings.Join(strings.Fields(body), " ")))+" bytes]") {
		t.Errorf("expected truncation footer to reference original length, got:\n%s", got)
	}
}

func TestFormatSearchIssueItem(t *testing.T) {
	item := SearchIssueItem{
		Number:     7,
		Title:      "Bad behavior",
		State:      "CLOSED",
		Author:     Author{Login: "octocat"},
		Repository: Repository{NameWithOwner: "cli/cli"},
		UpdatedAt:  "2025-02-03T00:00:00Z",
	}
	got := FormatSearchIssueItem(item, 200)
	for _, want := range []string{
		"cli/cli#7",
		"Bad behavior",
		"@octocat",
		"CLOSED",
		"2025-02-03",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\n  > ") {
		t.Errorf("expected no body line for empty body, got:\n%s", got)
	}
}

func TestFormatSearchIssueItem_EmptyBody(t *testing.T) {
	item := SearchIssueItem{
		Number:     1,
		Title:      "T",
		State:      "OPEN",
		Author:     Author{Login: "alice"},
		Repository: Repository{NameWithOwner: "o/r"},
		Body:       "",
		UpdatedAt:  "2025-01-01T00:00:00Z",
	}
	got := FormatSearchIssueItem(item, 200)
	if strings.Count(got, "\n") != 0 {
		t.Errorf("expected single line for empty body, got:\n%s", got)
	}
}

func TestFormatSearchIssueItem_ShortBody(t *testing.T) {
	item := SearchIssueItem{
		Number:     1,
		Title:      "T",
		State:      "OPEN",
		Author:     Author{Login: "alice"},
		Repository: Repository{NameWithOwner: "o/r"},
		Body:       "Steps to reproduce:\n\n  1. Run\ttest",
		UpdatedAt:  "2025-01-01T00:00:00Z",
	}
	got := FormatSearchIssueItem(item, 200)
	if !strings.Contains(got, "\n  > Steps to reproduce: 1. Run test") {
		t.Errorf("expected whitespace-collapsed body line, got:\n%s", got)
	}
	if strings.Contains(got, "[truncated") {
		t.Errorf("short body should not be truncated, got:\n%s", got)
	}
}

func TestFormatSearchIssueItem_LongBody(t *testing.T) {
	body := strings.Repeat("abcdefghij ", 30) // 330 bytes
	item := SearchIssueItem{
		Number:     1,
		Title:      "T",
		State:      "OPEN",
		Author:     Author{Login: "alice"},
		Repository: Repository{NameWithOwner: "o/r"},
		Body:       body,
		UpdatedAt:  "2025-01-01T00:00:00Z",
	}
	got := FormatSearchIssueItem(item, 50)
	if !strings.Contains(got, "\n  > ") {
		t.Errorf("expected body excerpt line, got:\n%s", got)
	}
	if !strings.Contains(got, "[truncated — showing ") {
		t.Errorf("expected unified truncation marker, got:\n%s", got)
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

func TestFormatSearchCodeItem_CollapsesFragmentWhitespace(t *testing.T) {
	item := SearchCodeItem{
		Path:       "main.go",
		Repository: Repository{NameWithOwner: "cli/cli"},
		TextMatches: []TextMatch{
			{Fragment: "func\tmain()\n{\n  return\n}"},
			{Fragment: "  multiple   spaces\tand\ttabs  "},
		},
	}
	got := FormatSearchCodeItem(item)
	if strings.ContainsAny(got, "\t\n") {
		t.Errorf("fragment whitespace should be collapsed, got: %q", got)
	}
	for _, want := range []string{
		"func main() { return }",
		"multiple spaces and tabs",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in: %q", want, got)
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

func TestFormatCaches(t *testing.T) {
	caches := []Cache{
		{
			ID:             1234567,
			Key:            "npm-cache-abc123",
			Ref:            "refs/heads/main",
			SizeInBytes:    4_400_000,
			CreatedAt:      "2026-04-01T10:00:00Z",
			LastAccessedAt: "2026-04-15T12:30:00Z",
		},
		{
			ID:             7654321,
			Key:            "go-mod-cache-def456",
			Ref:            "refs/pull/42/merge",
			SizeInBytes:    512,
			CreatedAt:      "2026-04-10T08:00:00Z",
			LastAccessedAt: "2026-04-10T08:05:00Z",
		},
	}
	got := FormatCaches(caches, 0)
	for _, want := range []string{
		"`1234567`",
		"`npm-cache-abc123`",
		"refs/heads/main",
		"4.2 MiB",
		"2026-04-01",
		"2026-04-15",
		"`7654321`",
		"`go-mod-cache-def456`",
		"refs/pull/42/merge",
		"512 B",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestFormatCaches_Empty(t *testing.T) {
	if got := FormatCaches(nil, 0); got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestFormatRelease_BotAuthor(t *testing.T) {
	r := Release{
		TagName:     "v1.2.3",
		Name:        "Release 1.2.3",
		PublishedAt: "2026-04-01T12:00:00Z",
		Author:      Author{Login: "github-actions[bot]", IsBot: false},
		Body:        "Automated release.",
	}
	got := FormatRelease(r, 2000)
	if !strings.Contains(got, "by @github-actions [bot]") {
		t.Errorf("expected bot author line 'by @github-actions [bot]' in:\n%s", got)
	}
}

func TestFormatRelease_RegularAuthor(t *testing.T) {
	r := Release{
		TagName:     "v2.0.0",
		PublishedAt: "2026-04-10T00:00:00Z",
		Author:      Author{Login: "octocat", IsBot: false},
		Body:        "Manual release.",
	}
	got := FormatRelease(r, 2000)
	if !strings.Contains(got, "by @octocat") {
		t.Errorf("expected 'by @octocat' in:\n%s", got)
	}
	if strings.Contains(got, "[bot]") {
		t.Errorf("unexpected '[bot]' for regular author in:\n%s", got)
	}
}

func makePRListItems(n int) []PRListItem {
	items := make([]PRListItem, n)
	for i := range items {
		items[i] = PRListItem{Number: i + 1, Title: "t", State: "OPEN", UpdatedAt: "2026-04-01T00:00:00Z"}
	}
	return items
}

func TestFormatPRList_OverflowAndEmpty(t *testing.T) {
	cases := []struct {
		name        string
		items       []PRListItem
		limit       int
		wantTrailer string
	}{
		{"empty", nil, 5, ""},
		{"no overflow", makePRListItems(3), 5, ""},
		{"exact limit", makePRListItems(5), 5, ""},
		{"overflow by one", makePRListItems(6), 5, "[showing first 5 pull requests — more results available"},
		{"large overflow", makePRListItems(20), 5, "[showing first 5 pull requests — more results available"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatPRList(tc.items, tc.limit)
			if tc.wantTrailer == "" {
				if strings.Contains(got, "more results available") {
					t.Errorf("did not expect truncation trailer in:\n%s", got)
				}
			} else if !strings.Contains(got, tc.wantTrailer) {
				t.Errorf("missing %q in:\n%s", tc.wantTrailer, got)
			}
		})
	}
}

func TestFormatIssueList_OverflowAndEmpty(t *testing.T) {
	make := func(n int) []IssueListItem {
		out := make([]IssueListItem, n)
		for i := range out {
			out[i] = IssueListItem{Number: i + 1, Title: "t", State: "OPEN", UpdatedAt: "2026-04-01T00:00:00Z"}
		}
		return out
	}
	if got := FormatIssueList(nil, 5); got != "" {
		t.Errorf("expected empty for empty list, got %q", got)
	}
	if got := FormatIssueList(make(3), 5); strings.Contains(got, "more results available") {
		t.Errorf("did not expect trailer for no-overflow, got:\n%s", got)
	}
	got := FormatIssueList(make(6), 5)
	if !strings.Contains(got, "[showing first 5 issues — more results available") {
		t.Errorf("missing overflow-by-one trailer in:\n%s", got)
	}
}

func TestFormatRunList_OverflowAndEmpty(t *testing.T) {
	make := func(n int) []RunListItem {
		out := make([]RunListItem, n)
		for i := range out {
			out[i] = RunListItem{DatabaseID: int64(i + 1), Name: "Build", DisplayTitle: "Build", UpdatedAt: "2026-04-01T00:00:00Z"}
		}
		return out
	}
	if got := FormatRunList(nil, 5); got != "" {
		t.Errorf("expected empty for empty list, got %q", got)
	}
	got := FormatRunList(make(11), 10)
	if !strings.Contains(got, "[showing first 10 runs — more results available") {
		t.Errorf("missing trailer in:\n%s", got)
	}
}

func TestFormatBranches_Overflow(t *testing.T) {
	make := func(n int) []Branch {
		out := make([]Branch, n)
		for i := range out {
			out[i] = Branch{Name: "b"}
			out[i].Commit.SHA = "deadbeef"
		}
		return out
	}
	got := FormatBranches(make(7), 5)
	if !strings.Contains(got, "[showing first 5 branches — more results available") {
		t.Errorf("missing trailer in:\n%s", got)
	}
	if got := FormatBranches(make(3), 5); strings.Contains(got, "more results available") {
		t.Errorf("did not expect trailer for no-overflow, got:\n%s", got)
	}
}

func TestFormatReleases_Author(t *testing.T) {
	releases := []Release{
		{
			TagName:     "v1.0.0",
			Name:        "First",
			Author:      Author{Login: "octocat"},
			PublishedAt: "2026-04-10T00:00:00Z",
		},
		{
			TagName:     "v1.1.0",
			Name:        "Bot release",
			Author:      Author{Login: "github-actions[bot]"},
			PublishedAt: "2026-04-11T00:00:00Z",
		},
		{
			TagName:     "v1.2.0",
			Name:        "Anonymous",
			PublishedAt: "2026-04-12T00:00:00Z",
		},
	}
	got := FormatReleases(releases, 0)
	for _, want := range []string{
		"by @octocat",
		"by @github-actions [bot]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// Anonymous author (empty login) should not produce an empty `by @` line.
	if strings.Contains(got, "by @ ") || strings.Contains(got, "by @\n") {
		t.Errorf("anonymous author should be omitted, got:\n%s", got)
	}
}

func TestFormatReleases_Overflow(t *testing.T) {
	make := func(n int) []Release {
		out := make([]Release, n)
		for i := range out {
			out[i] = Release{TagName: "v"}
		}
		return out
	}
	got := FormatReleases(make(6), 5)
	if !strings.Contains(got, "[showing first 5 releases — more results available") {
		t.Errorf("missing trailer in:\n%s", got)
	}
}

func TestFormatPRFiles_Overflow(t *testing.T) {
	make := func(n int) []PRFile {
		out := make([]PRFile, n)
		for i := range out {
			out[i] = PRFile{Filename: "f", Status: "modified"}
		}
		return out
	}
	got := FormatPRFiles(make(11), 10)
	if !strings.Contains(got, "[showing first 10 files — more results available") {
		t.Errorf("missing trailer in:\n%s", got)
	}
}

func TestFormatCaches_Overflow(t *testing.T) {
	make := func(n int) []Cache {
		out := make([]Cache, n)
		for i := range out {
			out[i] = Cache{ID: int64(i + 1), Key: "k"}
		}
		return out
	}
	got := FormatCaches(make(6), 5)
	if !strings.Contains(got, "[showing first 5 caches — more results available") {
		t.Errorf("missing trailer in:\n%s", got)
	}
}

func TestFormatComments_Overflow(t *testing.T) {
	make := func(n int) []Comment {
		out := make([]Comment, n)
		for i := range out {
			out[i] = Comment{Author: Author{Login: "u"}, Body: "b", CreatedAt: "2026-04-01T00:00:00Z"}
		}
		return out
	}
	got := FormatComments(make(6), 100, 5)
	if !strings.Contains(got, "[showing first 5 comments — more results available") {
		t.Errorf("missing trailer in:\n%s", got)
	}
}

func TestFormatReviews_Overflow(t *testing.T) {
	make := func(n int) []Review {
		out := make([]Review, n)
		for i := range out {
			out[i] = Review{Author: Author{Login: "u"}, State: "APPROVED", SubmittedAt: "2026-04-01T00:00:00Z"}
		}
		return out
	}
	got := FormatReviews(make(6), 100, 5)
	if !strings.Contains(got, "[showing first 5 reviews — more results available") {
		t.Errorf("missing trailer in:\n%s", got)
	}
}

func TestFormatReviewComments_Overflow(t *testing.T) {
	make := func(n int) []ReviewComment {
		out := make([]ReviewComment, n)
		for i := range out {
			out[i] = ReviewComment{ID: int64(i + 1), User: RESTUser{Login: "u"}, Body: "b", Path: "p", Line: 1, CreatedAt: "2026-04-01T00:00:00Z"}
		}
		return out
	}
	got := FormatReviewComments(make(6), 100, 5)
	if !strings.Contains(got, "[showing first 5 review comments — more results available") {
		t.Errorf("missing trailer in:\n%s", got)
	}
}

func TestTruncateBytes_Marker(t *testing.T) {
	s := strings.Repeat("a", 200)
	got := TruncateBytes(s, 50)
	if !strings.Contains(got, "[truncated — showing ") {
		t.Errorf("missing unified marker in:\n%s", got)
	}
	if !strings.Contains(got, " of 200 bytes]") {
		t.Errorf("missing unit and total in:\n%s", got)
	}
}
