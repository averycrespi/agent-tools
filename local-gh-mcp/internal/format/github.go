package format

import (
	"fmt"
	"strings"
)

// PRView represents the JSON output of `gh pr view --json`.
type PRView struct {
	Number         int     `json:"number"`
	Title          string  `json:"title"`
	Body           string  `json:"body"`
	State          string  `json:"state"`
	Author         Author  `json:"author"`
	BaseRefName    string  `json:"baseRefName"`
	HeadRefName    string  `json:"headRefName"`
	URL            string  `json:"url"`
	IsDraft        bool    `json:"isDraft"`
	Mergeable      string  `json:"mergeable"`
	ReviewDecision string  `json:"reviewDecision"`
	Labels         []Label `json:"labels"`
	CreatedAt      string  `json:"createdAt"`
	UpdatedAt      string  `json:"updatedAt"`
}

// IssueView represents the JSON output of `gh issue view --json`.
type IssueView struct {
	Number    int     `json:"number"`
	Title     string  `json:"title"`
	Body      string  `json:"body"`
	State     string  `json:"state"`
	Author    Author  `json:"author"`
	Labels    []Label `json:"labels"`
	Milestone *struct {
		Title string `json:"title"`
	} `json:"milestone"`
	URL       string `json:"url"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

// Comment represents a single comment on an issue or PR.
type Comment struct {
	Author            Author `json:"author"`
	AuthorAssociation string `json:"authorAssociation"`
	Body              string `json:"body"`
	CreatedAt         string `json:"createdAt"`
	IsMinimized       bool   `json:"isMinimized"`
	MinimizedReason   string `json:"minimizedReason"`
}

// Review represents a top-level PR review submission from `gh pr view --json reviews`.
type Review struct {
	Author            Author `json:"author"`
	AuthorAssociation string `json:"authorAssociation"`
	Body              string `json:"body"`
	State             string `json:"state"`
	SubmittedAt       string `json:"submittedAt"`
}

// RESTUser represents a user from a GitHub REST API response.
// The REST API uses `login` and `type` (User/Bot) rather than gh's `login`/`is_bot`.
type RESTUser struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

// Author converts a RESTUser to the common Author type.
func (u RESTUser) Author() Author {
	return Author{Login: u.Login, IsBot: u.Type == "Bot"}
}

// ReviewComment represents a single inline review comment on a PR diff, from the REST API.
type ReviewComment struct {
	ID                  int64    `json:"id"`
	InReplyToID         int64    `json:"in_reply_to_id"`
	PullRequestReviewID int64    `json:"pull_request_review_id"`
	User                RESTUser `json:"user"`
	Body                string   `json:"body"`
	Path                string   `json:"path"`
	Line                int      `json:"line"`
	OriginalLine        int      `json:"original_line"`
	Side                string   `json:"side"`
	DiffHunk            string   `json:"diff_hunk"`
	CreatedAt           string   `json:"created_at"`
}

// Check represents a single status check on a PR.
type Check struct {
	Name  string `json:"name"`
	State string `json:"state"`
	Link  string `json:"link"`
}

// PRListItem represents a single PR in a list response.
type PRListItem struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	State       string `json:"state"`
	Author      Author `json:"author"`
	HeadRefName string `json:"headRefName"`
	IsDraft     bool   `json:"isDraft"`
	UpdatedAt   string `json:"updatedAt"`
}

// IssueListItem represents a single issue in a list response.
type IssueListItem struct {
	Number    int     `json:"number"`
	Title     string  `json:"title"`
	State     string  `json:"state"`
	Author    Author  `json:"author"`
	Labels    []Label `json:"labels"`
	UpdatedAt string  `json:"updatedAt"`
}

// RunListItem represents a single workflow run in a list response.
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

// Job represents a single job within a workflow run.
type Job struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
}

// RunView represents the JSON output of `gh run view --json`.
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

// Repository represents a GitHub repository reference.
// Some gh subcommands use "nameWithOwner", others use "fullName".
type Repository struct {
	NameWithOwner string `json:"nameWithOwner"`
	FullName      string `json:"fullName"`
}

// Name returns the best available repository identifier.
func (r Repository) Name() string {
	if r.NameWithOwner != "" {
		return r.NameWithOwner
	}
	return r.FullName
}

// SearchPRItem represents a PR in search results.
type SearchPRItem struct {
	Number     int        `json:"number"`
	Title      string     `json:"title"`
	State      string     `json:"state"`
	Author     Author     `json:"author"`
	Repository Repository `json:"repository"`
	UpdatedAt  string     `json:"updatedAt"`
}

// SearchIssueItem is an alias for SearchPRItem as they share the same shape.
type SearchIssueItem = SearchPRItem

// SearchRepoItem represents a repository in search results.
type SearchRepoItem struct {
	FullName        string `json:"fullName"`
	Description     string `json:"description"`
	URL             string `json:"url"`
	StargazersCount int    `json:"stargazersCount"`
	Language        string `json:"language"`
	UpdatedAt       string `json:"updatedAt"`
}

// TextMatch represents a code search text match fragment.
type TextMatch struct {
	Fragment string `json:"fragment"`
}

// SearchCodeItem represents a code search result.
type SearchCodeItem struct {
	Path        string      `json:"path"`
	Repository  Repository  `json:"repository"`
	SHA         string      `json:"sha"`
	TextMatches []TextMatch `json:"textMatches"`
	URL         string      `json:"url"`
}

// CommitDetail holds the commit message for search results.
type CommitDetail struct {
	Message string       `json:"message"`
	Author  CommitAuthor `json:"author"`
}

// CommitAuthor represents the author metadata inside a commit object.
type CommitAuthor struct {
	Date string `json:"date"`
}

// SearchCommitItem represents a commit in search results.
type SearchCommitItem struct {
	SHA        string       `json:"sha"`
	Commit     CommitDetail `json:"commit"`
	Author     Author       `json:"author"`
	Repository Repository   `json:"repository"`
	URL        string       `json:"url"`
}

// FormatPRView formats a PR view as markdown.
func FormatPRView(pr PRView, maxBodyLen int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# PR #%d: %s (%s)\n\n", pr.Number, pr.Title, pr.State)
	fmt.Fprintf(&sb, "**Author:** %s | **Base:** %s <- %s\n", FormatAuthor(pr.Author), pr.BaseRefName, pr.HeadRefName)
	fmt.Fprintf(&sb, "**Created:** %s | **Updated:** %s\n", FormatDate(pr.CreatedAt), FormatDate(pr.UpdatedAt))

	draft := "no"
	if pr.IsDraft {
		draft = "yes"
	}
	fmt.Fprintf(&sb, "**Draft:** %s | **Mergeable:** %s | **Review:** %s\n", draft, pr.Mergeable, pr.ReviewDecision)
	fmt.Fprintf(&sb, "**Labels:** %s\n", FormatLabels(pr.Labels))

	body := TruncateBody(StripImages(pr.Body), maxBodyLen)
	if body != "" {
		fmt.Fprintf(&sb, "\n## Description\n\n%s\n", body)
	}
	return sb.String()
}

// FormatIssueView formats an issue view as markdown.
func FormatIssueView(issue IssueView, maxBodyLen int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Issue #%d: %s (%s)\n\n", issue.Number, issue.Title, issue.State)
	fmt.Fprintf(&sb, "**Author:** %s\n", FormatAuthor(issue.Author))
	fmt.Fprintf(&sb, "**Created:** %s | **Updated:** %s\n", FormatDate(issue.CreatedAt), FormatDate(issue.UpdatedAt))
	fmt.Fprintf(&sb, "**Labels:** %s\n", FormatLabels(issue.Labels))

	milestone := "(none)"
	if issue.Milestone != nil && issue.Milestone.Title != "" {
		milestone = issue.Milestone.Title
	}
	fmt.Fprintf(&sb, "**Milestone:** %s\n", milestone)

	body := TruncateBody(StripImages(issue.Body), maxBodyLen)
	if body != "" {
		fmt.Fprintf(&sb, "\n## Description\n\n%s\n", body)
	}
	return sb.String()
}

// FormatComments formats a list of comments as markdown.
func FormatComments(comments []Comment, maxBodyLen int) string {
	if len(comments) == 0 {
		return "No comments."
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Comments (%d)\n", len(comments))
	for _, c := range comments {
		sb.WriteString("\n")
		header := FormatAuthor(c.Author)
		if c.AuthorAssociation != "" && c.AuthorAssociation != "NONE" {
			header += fmt.Sprintf(" [%s]", c.AuthorAssociation)
		}
		fmt.Fprintf(&sb, "### %s (%s)\n\n", header, FormatDate(c.CreatedAt))
		if c.IsMinimized {
			fmt.Fprintf(&sb, "[minimized: %s]\n", c.MinimizedReason)
		} else {
			body := TruncateBody(StripImages(c.Body), maxBodyLen)
			sb.WriteString(body)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// FormatReviews formats top-level PR reviews as markdown.
func FormatReviews(reviews []Review, maxBodyLen int) string {
	if len(reviews) == 0 {
		return "No reviews."
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Reviews (%d)\n", len(reviews))
	for _, r := range reviews {
		sb.WriteString("\n")
		header := FormatAuthor(r.Author)
		if r.AuthorAssociation != "" && r.AuthorAssociation != "NONE" {
			header += fmt.Sprintf(" [%s]", r.AuthorAssociation)
		}
		state := r.State
		if state == "" {
			state = "(no state)"
		}
		fmt.Fprintf(&sb, "### %s — %s (%s)\n\n", header, state, FormatDate(r.SubmittedAt))
		body := TruncateBody(StripImages(r.Body), maxBodyLen)
		if body == "" {
			sb.WriteString("(no body)\n")
		} else {
			sb.WriteString(body)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// FormatReviewComments formats inline PR review comments as markdown,
// grouped by file path and threaded by in_reply_to_id.
func FormatReviewComments(comments []ReviewComment, maxBodyLen int) string {
	if len(comments) == 0 {
		return "No review comments."
	}

	// Group comments by file, preserving first-seen order.
	byFile := make(map[string][]ReviewComment)
	var fileOrder []string
	for _, c := range comments {
		if _, seen := byFile[c.Path]; !seen {
			fileOrder = append(fileOrder, c.Path)
		}
		byFile[c.Path] = append(byFile[c.Path], c)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "## Review Comments (%d)\n", len(comments))

	for _, path := range fileOrder {
		fileComments := byFile[path]
		fmt.Fprintf(&sb, "\n### %s\n", path)

		// Build parent → replies map; roots are comments with InReplyToID == 0
		// or whose parent is not present in this page.
		indexByID := make(map[int64]int, len(fileComments))
		for i, c := range fileComments {
			indexByID[c.ID] = i
		}
		replies := make(map[int64][]int)
		var roots []int
		for i, c := range fileComments {
			if c.InReplyToID == 0 {
				roots = append(roots, i)
				continue
			}
			if _, ok := indexByID[c.InReplyToID]; !ok {
				// Parent not in the page — treat as root.
				roots = append(roots, i)
				continue
			}
			replies[c.InReplyToID] = append(replies[c.InReplyToID], i)
		}

		for _, i := range roots {
			writeReviewCommentThread(&sb, fileComments, replies, i, 0, maxBodyLen)
		}
	}
	return sb.String()
}

// writeReviewCommentThread renders one review comment and its replies with indentation.
func writeReviewCommentThread(sb *strings.Builder, comments []ReviewComment, replies map[int64][]int, idx, depth, maxBodyLen int) {
	c := comments[idx]
	indent := strings.Repeat("  ", depth)
	header := FormatAuthor(c.User.Author())
	line := c.Line
	if line == 0 {
		line = c.OriginalLine
	}
	sb.WriteString("\n")
	if depth == 0 {
		fmt.Fprintf(sb, "%s- **Line %d** — %s (%s):\n", indent, line, header, FormatDate(c.CreatedAt))
	} else {
		fmt.Fprintf(sb, "%s- ↳ %s (%s):\n", indent, header, FormatDate(c.CreatedAt))
	}
	body := TruncateBody(StripImages(c.Body), maxBodyLen)
	if body == "" {
		fmt.Fprintf(sb, "%s  (no body)\n", indent)
	} else {
		// Indent each line of the body.
		for _, bl := range strings.Split(body, "\n") {
			fmt.Fprintf(sb, "%s  %s\n", indent, bl)
		}
	}
	for _, childIdx := range replies[c.ID] {
		writeReviewCommentThread(sb, comments, replies, childIdx, depth+1, maxBodyLen)
	}
}

// FormatCheckList formats status checks as a markdown bullet list.
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

// FormatPRListItem formats a single PR list item as a markdown bullet.
func FormatPRListItem(pr PRListItem) string {
	state := pr.State
	if pr.IsDraft {
		state = "DRAFT"
	}
	return fmt.Sprintf("- **#%d** %s — %s, %s, updated %s",
		pr.Number, pr.Title, FormatAuthor(pr.Author), state, FormatDate(pr.UpdatedAt))
}

// FormatIssueListItem formats a single issue list item as a markdown bullet.
func FormatIssueListItem(item IssueListItem) string {
	return fmt.Sprintf("- **#%d** %s — %s, %s, labels: %s, updated %s",
		item.Number, item.Title, FormatAuthor(item.Author), item.State,
		FormatLabels(item.Labels), FormatDate(item.UpdatedAt))
}

// FormatRunListItem formats a single workflow run list item as a markdown bullet.
func FormatRunListItem(item RunListItem) string {
	return fmt.Sprintf("- **#%d** %s — %s/%s, %s, %s, %s",
		item.DatabaseID, item.DisplayTitle, item.Status, item.Conclusion,
		item.Event, item.HeadBranch, FormatDate(item.UpdatedAt))
}

// FormatRunView formats a workflow run view as markdown.
func FormatRunView(run RunView) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Run #%d: %s\n\n", run.DatabaseID, run.DisplayTitle)
	fmt.Fprintf(&sb, "**Workflow:** %s\n", run.Name)
	fmt.Fprintf(&sb, "**Status:** %s/%s\n", run.Status, run.Conclusion)
	fmt.Fprintf(&sb, "**Event:** %s | **Branch:** %s\n", run.Event, run.HeadBranch)

	sha := run.HeadSha
	if len(sha) > 7 {
		sha = sha[:7]
	}
	fmt.Fprintf(&sb, "**SHA:** %s\n", sha)
	fmt.Fprintf(&sb, "**Created:** %s | **Updated:** %s\n", FormatDate(run.CreatedAt), FormatDate(run.UpdatedAt))
	fmt.Fprintf(&sb, "**URL:** %s\n", run.URL)

	if len(run.Jobs) > 0 {
		fmt.Fprintf(&sb, "\n## Jobs (%d)\n\n", len(run.Jobs))
		for _, j := range run.Jobs {
			if j.Conclusion == "failure" && j.URL != "" {
				fmt.Fprintf(&sb, "- %s: %s (%s)\n", j.Name, j.Conclusion, j.URL)
			} else {
				fmt.Fprintf(&sb, "- %s: %s\n", j.Name, j.Conclusion)
			}
		}
	}
	return sb.String()
}

// FormatSearchPRItem formats a search PR/issue item as a markdown bullet.
func FormatSearchPRItem(item SearchPRItem) string {
	return fmt.Sprintf("- **%s#%d** %s — %s, %s, updated %s",
		item.Repository.Name(), item.Number, item.Title,
		FormatAuthor(item.Author), item.State, FormatDate(item.UpdatedAt))
}

// FormatSearchRepoItem formats a search repo item as a markdown bullet.
func FormatSearchRepoItem(item SearchRepoItem) string {
	return fmt.Sprintf("- **%s** %s — %d stars, %s, updated %s",
		item.FullName, item.Description, item.StargazersCount, item.Language, FormatDate(item.UpdatedAt))
}

// FormatSearchCodeItem formats a search code item as a markdown bullet.
func FormatSearchCodeItem(item SearchCodeItem) string {
	var fragments []string
	for _, tm := range item.TextMatches {
		fragments = append(fragments, tm.Fragment)
	}
	match := ""
	if len(fragments) > 0 {
		match = " — " + strings.Join(fragments, " | ")
	}
	return fmt.Sprintf("- **%s** %s%s",
		item.Repository.Name(), item.Path, match)
}

// PRFile represents a single file entry from the GitHub REST pulls files API.
type PRFile struct {
	Filename  string `json:"filename"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// FormatPRFiles formats a list of PR files with +/- counts as markdown bullets.
// Files beyond limit are truncated; a trailer is appended when truncation occurs.
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

// FormatSearchCommitItem formats a search commit item as a markdown bullet.
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
	return fmt.Sprintf("- **%s** %s %s — %s, %s",
		item.Repository.Name(), sha, msg, FormatAuthor(item.Author), FormatDate(item.Commit.Author.Date))
}
