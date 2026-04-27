package format

import (
	"fmt"
	"strings"
)

// writeListTruncationFooter writes the standard truncation footer for list
// tools that use the limit+1 lookahead trick to detect overflow. The lookahead
// only proves "at least one more item exists" — it doesn't yield a real total,
// so the footer reports just the page size and signals more results are
// available rather than fabricating an exact "of M" count.
func writeListTruncationFooter(b *strings.Builder, limit int, noun string) {
	fmt.Fprintf(b, "\n[showing first %d %s — more results available; increase limit or paginate]\n", limit, noun)
}

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
	AuthorAssociation   string   `json:"author_association"`
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
	DatabaseID int64  `json:"databaseId"`
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
	Body       string     `json:"body"`
	UpdatedAt  string     `json:"updatedAt"`
}

// SearchIssueItem represents an issue in search results. Distinct from
// SearchPRItem so issue-only fields can be added without affecting PR rendering.
type SearchIssueItem struct {
	Number     int        `json:"number"`
	Title      string     `json:"title"`
	State      string     `json:"state"`
	Author     Author     `json:"author"`
	Repository Repository `json:"repository"`
	Body       string     `json:"body"`
	UpdatedAt  string     `json:"updatedAt"`
}

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
	review := pr.ReviewDecision
	if review == "" {
		review = "(none)"
	}
	if pr.Mergeable != "" && pr.Mergeable != "UNKNOWN" {
		fmt.Fprintf(&sb, "**Draft:** %s | **Mergeable:** %s | **Review:** %s\n", draft, pr.Mergeable, review)
	} else {
		fmt.Fprintf(&sb, "**Draft:** %s | **Review:** %s\n", draft, review)
	}
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
// When limit > 0 and len(comments) > limit, the list is sliced to limit and
// a truncation trailer is appended. The caller should fetch limit+1 items.
func FormatComments(comments []Comment, maxBodyLen int, limit int) string {
	if len(comments) == 0 {
		return "No comments."
	}
	total := len(comments)
	if limit > 0 && total > limit {
		comments = comments[:limit]
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
	if limit > 0 && total > limit {
		writeListTruncationFooter(&sb, limit, "comments")
	}
	return sb.String()
}

// FormatReviews formats top-level PR reviews as markdown.
// When limit > 0 and len(reviews) > limit, the list is sliced to limit and
// a truncation trailer is appended. The caller should fetch limit+1 items.
func FormatReviews(reviews []Review, maxBodyLen int, limit int) string {
	if len(reviews) == 0 {
		return "No reviews."
	}
	total := len(reviews)
	if limit > 0 && total > limit {
		reviews = reviews[:limit]
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
		body := TruncateBody(StripReviewHTML(StripImages(r.Body)), maxBodyLen)
		if body == "" {
			sb.WriteString("(no body)\n")
		} else {
			sb.WriteString(body)
			sb.WriteString("\n")
		}
	}
	if limit > 0 && total > limit {
		writeListTruncationFooter(&sb, limit, "reviews")
	}
	return sb.String()
}

// FormatReviewComments formats inline PR review comments as markdown,
// grouped by file path and threaded by in_reply_to_id.
// When limit > 0 and len(comments) > limit, the list is sliced to limit and
// a truncation trailer is appended. The caller should fetch limit+1 items.
func FormatReviewComments(comments []ReviewComment, maxBodyLen int, limit int) string {
	if len(comments) == 0 {
		return "No review comments."
	}
	total := len(comments)
	if limit > 0 && total > limit {
		comments = comments[:limit]
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
	if limit > 0 && total > limit {
		writeListTruncationFooter(&sb, limit, "review comments")
	}
	return sb.String()
}

// writeReviewCommentThread renders one review comment and its replies with indentation.
func writeReviewCommentThread(sb *strings.Builder, comments []ReviewComment, replies map[int64][]int, idx, depth, maxBodyLen int) {
	c := comments[idx]
	indent := strings.Repeat("  ", depth)
	header := FormatAuthor(c.User.Author())
	if c.AuthorAssociation != "" && c.AuthorAssociation != "NONE" {
		header += fmt.Sprintf(" [%s]", c.AuthorAssociation)
	}
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

// FormatPRList formats a list of PRs as markdown bullets.
// When limit > 0 and len(prs) > limit, the list is sliced to limit and
// a truncation trailer is appended. The caller should fetch limit+1 items.
func FormatPRList(prs []PRListItem, limit int) string {
	total := len(prs)
	if limit > 0 && total > limit {
		prs = prs[:limit]
	}
	var b strings.Builder
	for _, pr := range prs {
		b.WriteString(FormatPRListItem(pr))
		b.WriteString("\n")
	}
	if limit > 0 && total > limit {
		writeListTruncationFooter(&b, limit, "pull requests")
	}
	return b.String()
}

// FormatIssueListItem formats a single issue list item as a markdown bullet.
func FormatIssueListItem(item IssueListItem) string {
	return fmt.Sprintf("- **#%d** %s — %s, %s, labels: %s, updated %s",
		item.Number, item.Title, FormatAuthor(item.Author), item.State,
		FormatLabels(item.Labels), FormatDate(item.UpdatedAt))
}

// FormatIssueList formats a list of issues as markdown bullets.
// When limit > 0 and len(issues) > limit, the list is sliced to limit and
// a truncation trailer is appended. The caller should fetch limit+1 items.
func FormatIssueList(issues []IssueListItem, limit int) string {
	total := len(issues)
	if limit > 0 && total > limit {
		issues = issues[:limit]
	}
	var b strings.Builder
	for _, issue := range issues {
		b.WriteString(FormatIssueListItem(issue))
		b.WriteString("\n")
	}
	if limit > 0 && total > limit {
		writeListTruncationFooter(&b, limit, "issues")
	}
	return b.String()
}

// FormatRunListItem formats a single workflow run list item as a markdown bullet.
func FormatRunListItem(item RunListItem) string {
	return fmt.Sprintf("- **#%d** %s — %s/%s, %s, %s, %s",
		item.DatabaseID, item.DisplayTitle, item.Status, item.Conclusion,
		item.Event, item.HeadBranch, FormatDate(item.UpdatedAt))
}

// FormatRunList formats a list of workflow runs as markdown bullets.
// When limit > 0 and len(runs) > limit, the list is sliced to limit and
// a truncation trailer is appended. The caller should fetch limit+1 items.
func FormatRunList(runs []RunListItem, limit int) string {
	total := len(runs)
	if limit > 0 && total > limit {
		runs = runs[:limit]
	}
	var b strings.Builder
	for _, run := range runs {
		b.WriteString(FormatRunListItem(run))
		b.WriteString("\n")
	}
	if limit > 0 && total > limit {
		writeListTruncationFooter(&b, limit, "runs")
	}
	return b.String()
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
				fmt.Fprintf(&sb, "- `%s` (job_id: %d) — %s (%s)\n", j.Name, j.DatabaseID, j.Conclusion, j.URL)
			} else {
				fmt.Fprintf(&sb, "- `%s` (job_id: %d) — %s\n", j.Name, j.DatabaseID, j.Conclusion)
			}
		}
	}
	return sb.String()
}

// FormatSearchPRItem formats a search PR item as a markdown bullet. When the
// body is non-empty, a second indented quote line carries a whitespace-collapsed
// excerpt truncated to maxBody bytes.
func FormatSearchPRItem(item SearchPRItem, maxBody int) string {
	line := fmt.Sprintf("- **%s#%d** %s — %s, %s, updated %s",
		item.Repository.Name(), item.Number, item.Title,
		FormatAuthor(item.Author), item.State, FormatDate(item.UpdatedAt))
	if excerpt := searchBodyExcerpt(item.Body, maxBody); excerpt != "" {
		line += "\n  > " + excerpt
	}
	return line
}

// FormatSearchIssueItem formats a search issue item as a markdown bullet. When
// the body is non-empty, a second indented quote line carries a whitespace-
// collapsed excerpt truncated to maxBody bytes.
func FormatSearchIssueItem(item SearchIssueItem, maxBody int) string {
	line := fmt.Sprintf("- **%s#%d** %s — %s, %s, updated %s",
		item.Repository.Name(), item.Number, item.Title,
		FormatAuthor(item.Author), item.State, FormatDate(item.UpdatedAt))
	if excerpt := searchBodyExcerpt(item.Body, maxBody); excerpt != "" {
		line += "\n  > " + excerpt
	}
	return line
}

// searchBodyExcerpt strips HTML comments (PR/issue template instructions
// leak otherwise), collapses whitespace runs to single spaces, and truncates
// to maxBody bytes via TruncateBody so the unified truncation marker is
// reused. Returns "" when the resulting excerpt would be empty.
func searchBodyExcerpt(body string, maxBody int) string {
	body = StripHTMLComments(body)
	collapsed := strings.Join(strings.Fields(body), " ")
	if collapsed == "" {
		return ""
	}
	return TruncateBody(collapsed, maxBody)
}

// FormatSearchRepoItem formats a search repo item as a markdown bullet.
func FormatSearchRepoItem(item SearchRepoItem) string {
	return fmt.Sprintf("- **%s** %s — %d stars, %s, updated %s",
		item.FullName, item.Description, item.StargazersCount, item.Language, FormatDate(item.UpdatedAt))
}

// FormatSearchCodeItem formats a search code item as a markdown bullet.
// Fragment whitespace runs (including embedded newlines and tabs) are collapsed
// to single spaces so the bullet stays on one line.
func FormatSearchCodeItem(item SearchCodeItem) string {
	var fragments []string
	for _, tm := range item.TextMatches {
		f := strings.Join(strings.Fields(tm.Fragment), " ")
		if f != "" {
			fragments = append(fragments, f)
		}
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
// The caller should fetch limit+1 items so overflow is detectable.
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
		writeListTruncationFooter(&b, limit, "files")
	}
	return b.String()
}

// Cache represents a single GitHub Actions cache entry from `gh cache list --json`.
type Cache struct {
	ID             int64  `json:"id"`
	Key            string `json:"key"`
	Ref            string `json:"ref"`
	SizeInBytes    int64  `json:"sizeInBytes"`
	CreatedAt      string `json:"createdAt"`
	LastAccessedAt string `json:"lastAccessedAt"`
}

// FormatCaches formats a list of GitHub Actions caches as markdown bullets.
// Each entry shows id, key, size, ref, and access dates so agents can triage
// stale or oversized caches before deletion.
// The caller should fetch limit+1 items so overflow is detectable; when
// len(caches) > limit the list is sliced to limit and a trailer is appended.
func FormatCaches(caches []Cache, limit int) string {
	total := len(caches)
	if limit > 0 && total > limit {
		caches = caches[:limit]
	}
	var b strings.Builder
	for _, c := range caches {
		fmt.Fprintf(&b, "- `%d` `%s` — %s, ref `%s`, created %s, accessed %s\n",
			c.ID, c.Key, humanBytes(c.SizeInBytes), c.Ref,
			FormatDate(c.CreatedAt), FormatDate(c.LastAccessedAt))
	}
	if limit > 0 && total > limit {
		writeListTruncationFooter(&b, limit, "caches")
	}
	return b.String()
}

// Branch represents a single branch from the GitHub REST branches API.
type Branch struct {
	Name   string `json:"name"`
	Commit struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

// FormatBranches formats a list of branches with HEAD commit short-SHAs.
// Branches beyond limit are truncated; a trailer is appended when truncation occurs.
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
		writeListTruncationFooter(&b, limit, "branches")
	}
	return b.String()
}

// Release represents the JSON output of `gh release view --json` or a single
// element from `gh release list --json`.
type Release struct {
	TagName      string         `json:"tagName"`
	Name         string         `json:"name"`
	Author       Author         `json:"author"`
	PublishedAt  string         `json:"publishedAt"`
	Body         string         `json:"body"`
	IsDraft      bool           `json:"isDraft"`
	IsPrerelease bool           `json:"isPrerelease"`
	Assets       []ReleaseAsset `json:"assets"`
}

// ReleaseAsset represents a single asset attached to a GitHub release.
type ReleaseAsset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// FormatReleases formats a list of releases as a markdown bullet list.
// releases should be pre-fetched with limit+1 entries; FormatReleases trims to
// limit and appends a truncation trailer when total > limit.
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
		if r.Author.Login != "" {
			line += fmt.Sprintf(" by %s", FormatAuthor(r.Author))
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
		writeListTruncationFooter(&b, limit, "releases")
	}
	return b.String()
}

// FormatRelease formats a single release as markdown with optional assets section.
func FormatRelease(r Release, maxBodyLength int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s", r.TagName)
	if r.Name != "" {
		fmt.Fprintf(&b, " — %s", r.Name)
	}
	b.WriteString("\n")
	if r.Author.Login != "" {
		fmt.Fprintf(&b, "by %s", FormatAuthor(r.Author))
	}
	if r.PublishedAt != "" {
		date := r.PublishedAt
		if len(date) >= 10 {
			date = date[:10]
		}
		fmt.Fprintf(&b, " · published %s", date)
	}
	b.WriteString("\n\n")
	body := TruncateBody(r.Body, maxBodyLength)
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

// humanBytes renders a byte count in human-readable IEC units.
// Examples: 512 → "512 B", 1536 → "1.5 KiB", 3355443 → "3.2 MiB".
func humanBytes(n int64) string {
	const (
		kib = 1 << 10
		mib = 1 << 20
		gib = 1 << 30
	)
	switch {
	case n < kib:
		return fmt.Sprintf("%d B", n)
	case n < mib:
		return fmt.Sprintf("%.1f KiB", float64(n)/kib)
	case n < gib:
		return fmt.Sprintf("%.1f MiB", float64(n)/mib)
	default:
		return fmt.Sprintf("%.1f GiB", float64(n)/gib)
	}
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
