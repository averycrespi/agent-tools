package gh

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/exec"
)

const (
	defaultLimit = 30
	maxLimit     = 100
)

func clampLimit(limit int) int {
	if limit <= 0 {
		return defaultLimit
	}
	if limit > maxLimit {
		return maxLimit
	}
	return limit
}

const (
	prViewFields  = "number,title,body,state,author,baseRefName,headRefName,url,isDraft,mergeable,reviewDecision,labels,assignees,createdAt,updatedAt"
	prListFields  = "number,title,state,author,headRefName,url,isDraft,createdAt,updatedAt"
	prCheckFields = "name,state,description,link,startedAt,completedAt"
)

// CreatePROpts holds options for creating a pull request.
type CreatePROpts struct {
	Title, Body, Base, Head      string
	Draft                        bool
	Labels, Reviewers, Assignees []string
}

// ListPROpts holds options for listing pull requests.
type ListPROpts struct {
	State, Author, Label, Base, Head, Search string
	Limit                                    int
}

// MergePROpts holds options for merging a pull request.
type MergePROpts struct {
	Method       string // merge, squash, rebase
	DeleteBranch bool
	Auto         bool
}

// EditPROpts holds options for editing a pull request.
type EditPROpts struct {
	Title, Body, Base             string
	AddLabels, RemoveLabels       []string
	AddReviewers, RemoveReviewers []string
	AddAssignees, RemoveAssignees []string
}

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

// ViewUser returns the authenticated user as JSON from `gh api /user`.
func (c *Client) ViewUser(_ context.Context) (string, error) {
	out, err := c.runner.Run("gh", "api", "/user")
	if err != nil {
		return "", fmt.Errorf("gh api /user failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// CreatePR creates a new pull request.
func (c *Client) CreatePR(_ context.Context, owner, repo string, opts CreatePROpts) (string, error) {
	args := []string{"pr", "create", "-R", repoFlag(owner, repo), "--title", opts.Title, "--body", opts.Body}
	if opts.Base != "" {
		args = append(args, "--base", opts.Base)
	}
	if opts.Head != "" {
		args = append(args, "--head", opts.Head)
	}
	if opts.Draft {
		args = append(args, "--draft")
	}
	for _, l := range opts.Labels {
		args = append(args, "--label", l)
	}
	for _, r := range opts.Reviewers {
		args = append(args, "--reviewer", r)
	}
	for _, a := range opts.Assignees {
		args = append(args, "--assignee", a)
	}
	out, err := c.runner.Run("gh", args...)
	if err != nil {
		return "", fmt.Errorf("gh pr create failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// ViewPR retrieves details for a pull request.
func (c *Client) ViewPR(_ context.Context, owner, repo string, number int) (string, error) {
	out, err := c.runner.Run("gh", "pr", "view", "-R", repoFlag(owner, repo), "--json", prViewFields, strconv.Itoa(number))
	if err != nil {
		return "", fmt.Errorf("gh pr view failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// ListPRs lists pull requests for a repository.
func (c *Client) ListPRs(_ context.Context, owner, repo string, opts ListPROpts) (string, error) {
	args := []string{"pr", "list", "-R", repoFlag(owner, repo), "--json", prListFields, "--limit", strconv.Itoa(clampLimit(opts.Limit))}
	if opts.State != "" {
		args = append(args, "--state", opts.State)
	}
	if opts.Author != "" {
		args = append(args, "--author", opts.Author)
	}
	if opts.Label != "" {
		args = append(args, "--label", opts.Label)
	}
	if opts.Base != "" {
		args = append(args, "--base", opts.Base)
	}
	if opts.Head != "" {
		args = append(args, "--head", opts.Head)
	}
	if opts.Search != "" {
		args = append(args, "--search", opts.Search)
	}
	out, err := c.runner.Run("gh", args...)
	if err != nil {
		return "", fmt.Errorf("gh pr list failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// DiffPR retrieves the diff for a pull request.
func (c *Client) DiffPR(_ context.Context, owner, repo string, number int) (string, error) {
	out, err := c.runner.Run("gh", "pr", "diff", "-R", repoFlag(owner, repo), strconv.Itoa(number))
	if err != nil {
		return "", fmt.Errorf("gh pr diff failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// CommentPR adds a comment to a pull request.
func (c *Client) CommentPR(_ context.Context, owner, repo string, number int, body string) (string, error) {
	out, err := c.runner.Run("gh", "pr", "comment", "-R", repoFlag(owner, repo), "--body", body, strconv.Itoa(number))
	if err != nil {
		return "", fmt.Errorf("gh pr comment failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// ReviewPR submits a review on a pull request.
func (c *Client) ReviewPR(_ context.Context, owner, repo string, number int, event, body string) (string, error) {
	args := []string{"pr", "review", "-R", repoFlag(owner, repo)}
	switch event {
	case "approve":
		args = append(args, "--approve")
	case "request-changes":
		args = append(args, "--request-changes")
	case "comment":
		args = append(args, "--comment")
	}
	if body != "" {
		args = append(args, "--body", body)
	}
	args = append(args, strconv.Itoa(number))
	out, err := c.runner.Run("gh", args...)
	if err != nil {
		return "", fmt.Errorf("gh pr review failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// MergePR merges a pull request.
func (c *Client) MergePR(_ context.Context, owner, repo string, number int, opts MergePROpts) (string, error) {
	args := []string{"pr", "merge", "-R", repoFlag(owner, repo)}
	switch opts.Method {
	case "squash":
		args = append(args, "--squash")
	case "rebase":
		args = append(args, "--rebase")
	default:
		args = append(args, "--merge")
	}
	if opts.DeleteBranch {
		args = append(args, "--delete-branch")
	}
	if opts.Auto {
		args = append(args, "--auto")
	}
	args = append(args, strconv.Itoa(number))
	out, err := c.runner.Run("gh", args...)
	if err != nil {
		return "", fmt.Errorf("gh pr merge failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// EditPR edits a pull request.
func (c *Client) EditPR(_ context.Context, owner, repo string, number int, opts EditPROpts) (string, error) {
	args := []string{"pr", "edit", "-R", repoFlag(owner, repo)}
	if opts.Title != "" {
		args = append(args, "--title", opts.Title)
	}
	if opts.Body != "" {
		args = append(args, "--body", opts.Body)
	}
	if opts.Base != "" {
		args = append(args, "--base", opts.Base)
	}
	if len(opts.AddLabels) > 0 {
		args = append(args, "--add-label", strings.Join(opts.AddLabels, ","))
	}
	if len(opts.RemoveLabels) > 0 {
		args = append(args, "--remove-label", strings.Join(opts.RemoveLabels, ","))
	}
	if len(opts.AddReviewers) > 0 {
		args = append(args, "--add-reviewer", strings.Join(opts.AddReviewers, ","))
	}
	if len(opts.RemoveReviewers) > 0 {
		args = append(args, "--remove-reviewer", strings.Join(opts.RemoveReviewers, ","))
	}
	if len(opts.AddAssignees) > 0 {
		args = append(args, "--add-assignee", strings.Join(opts.AddAssignees, ","))
	}
	if len(opts.RemoveAssignees) > 0 {
		args = append(args, "--remove-assignee", strings.Join(opts.RemoveAssignees, ","))
	}
	args = append(args, strconv.Itoa(number))
	out, err := c.runner.Run("gh", args...)
	if err != nil {
		return "", fmt.Errorf("gh pr edit failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// CheckPR retrieves status checks for a pull request.
func (c *Client) CheckPR(_ context.Context, owner, repo string, number int) (string, error) {
	out, err := c.runner.Run("gh", "pr", "checks", "-R", repoFlag(owner, repo), "--json", prCheckFields, strconv.Itoa(number))
	if err != nil {
		return "", fmt.Errorf("gh pr checks failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// ClosePR closes a pull request.
func (c *Client) ClosePR(_ context.Context, owner, repo string, number int, comment string) (string, error) {
	args := []string{"pr", "close", "-R", repoFlag(owner, repo)}
	if comment != "" {
		args = append(args, "--comment", comment)
	}
	args = append(args, strconv.Itoa(number))
	out, err := c.runner.Run("gh", args...)
	if err != nil {
		return "", fmt.Errorf("gh pr close failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// ReadyPR marks a draft pull request as ready for review.
func (c *Client) ReadyPR(_ context.Context, owner, repo string, number int) (string, error) {
	out, err := c.runner.Run("gh", "pr", "ready", fmt.Sprintf("%d", number), "-R", repoFlag(owner, repo))
	if err != nil {
		return "", fmt.Errorf("gh pr ready failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// DraftPR converts a pull request back to draft.
func (c *Client) DraftPR(_ context.Context, owner, repo string, number int) (string, error) {
	out, err := c.runner.Run("gh", "pr", "ready", fmt.Sprintf("%d", number), "--undo", "-R", repoFlag(owner, repo))
	if err != nil {
		return "", fmt.Errorf("gh pr ready --undo failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// ReopenPR reopens a closed pull request.
func (c *Client) ReopenPR(_ context.Context, owner, repo string, number int) (string, error) {
	out, err := c.runner.Run("gh", "pr", "reopen", fmt.Sprintf("%d", number), "-R", repoFlag(owner, repo))
	if err != nil {
		return "", fmt.Errorf("gh pr reopen failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// Issue field constants.
const (
	issueViewFields = "number,title,body,state,author,labels,assignees,milestone,url,createdAt,updatedAt"
	issueListFields = "number,title,state,author,labels,url,createdAt,updatedAt"
)

// Run field constants.
const (
	runListFields = "databaseId,name,displayTitle,status,conclusion,event,headBranch,url,createdAt,updatedAt"
	runViewFields = "databaseId,name,displayTitle,status,conclusion,event,headBranch,headSha,url,createdAt,updatedAt,jobs"
)

// Search field constants.
const (
	searchPRFields     = "number,title,state,author,repository,url,createdAt,updatedAt"
	searchIssueFields  = "number,title,state,author,repository,url,createdAt,updatedAt"
	searchRepoFields   = "fullName,description,url,stargazersCount,language,updatedAt"
	searchCodeFields   = "path,repository,sha,textMatches,url"
	searchCommitFields = "sha,commit,author,repository,url,committer"
)

// ListIssuesOpts holds options for listing issues.
type ListIssuesOpts struct {
	State, Author, Assignee, Label, Milestone, Search string
	Limit                                             int
}

// ListRunsOpts holds options for listing workflow runs.
type ListRunsOpts struct {
	Branch, Status, Workflow string
	Limit                    int
}

// ListCachesOpts holds options for listing caches.
type ListCachesOpts struct {
	Limit       int
	Sort, Order string
}

// SearchPRsOpts holds options for searching pull requests.
type SearchPRsOpts struct {
	Repo, Owner, State, Author, Label string
	Limit                             int
}

// SearchIssuesOpts holds options for searching issues.
type SearchIssuesOpts struct {
	Repo, Owner, State, Author, Label string
	Limit                             int
}

// SearchReposOpts holds options for searching repositories.
type SearchReposOpts struct {
	Owner, Language, Topic, Stars string
	Limit                         int
}

// SearchCodeOpts holds options for searching code.
type SearchCodeOpts struct {
	Repo, Owner, Language, Extension, Filename string
	Limit                                      int
}

// SearchCommitsOpts holds options for searching commits.
type SearchCommitsOpts struct {
	Repo, Owner, Author string
	Limit               int
}

// ViewIssue retrieves details for an issue.
func (c *Client) ViewIssue(_ context.Context, owner, repo string, number int) (string, error) {
	out, err := c.runner.Run("gh", "issue", "view", "-R", repoFlag(owner, repo), "--json", issueViewFields, strconv.Itoa(number))
	if err != nil {
		return "", fmt.Errorf("gh issue view failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// ListIssues lists issues for a repository.
func (c *Client) ListIssues(_ context.Context, owner, repo string, opts ListIssuesOpts) (string, error) {
	args := []string{"issue", "list", "-R", repoFlag(owner, repo), "--json", issueListFields, "--limit", strconv.Itoa(clampLimit(opts.Limit))}
	if opts.State != "" {
		args = append(args, "--state", opts.State)
	}
	if opts.Author != "" {
		args = append(args, "--author", opts.Author)
	}
	if opts.Assignee != "" {
		args = append(args, "--assignee", opts.Assignee)
	}
	if opts.Label != "" {
		args = append(args, "--label", opts.Label)
	}
	if opts.Milestone != "" {
		args = append(args, "--milestone", opts.Milestone)
	}
	if opts.Search != "" {
		args = append(args, "--search", opts.Search)
	}
	out, err := c.runner.Run("gh", args...)
	if err != nil {
		return "", fmt.Errorf("gh issue list failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// CommentIssue adds a comment to an issue.
func (c *Client) CommentIssue(_ context.Context, owner, repo string, number int, body string) (string, error) {
	out, err := c.runner.Run("gh", "issue", "comment", "-R", repoFlag(owner, repo), "--body", body, strconv.Itoa(number))
	if err != nil {
		return "", fmt.Errorf("gh issue comment failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

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

// PRReviews retrieves top-level review submissions on a pull request.
func (c *Client) PRReviews(_ context.Context, owner, repo string, number int, limit int) (string, error) {
	out, err := c.runner.Run("gh", "pr", "view", "-R", repoFlag(owner, repo),
		"--json", "reviews", "--jq",
		fmt.Sprintf(".reviews[:%d] | map({author,authorAssociation,body,state,submittedAt})", clampLimit(limit)),
		strconv.Itoa(number))
	if err != nil {
		return "", fmt.Errorf("gh pr reviews failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// PRReviewComments retrieves inline review comments on a pull request.
// Uses the REST API via `gh api` because `gh pr view --json` does not expose inline comments.
// Clamped limit maps to the per_page query param (max 100 per GitHub REST API).
func (c *Client) PRReviewComments(_ context.Context, owner, repo string, number int, limit int) (string, error) {
	endpoint := fmt.Sprintf("repos/%s/pulls/%d/comments?per_page=%d", repoFlag(owner, repo), number, clampLimit(limit))
	jq := "map({id, in_reply_to_id, pull_request_review_id, user: {login: .user.login, type: .user.type}, body, path, line, original_line, side, diff_hunk, created_at})"
	out, err := c.runner.Run("gh", "api", "--jq", jq, "--", endpoint)
	if err != nil {
		return "", fmt.Errorf("gh pr review comments failed: %s", strings.TrimSpace(string(out)))
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

// ListRuns lists workflow runs for a repository.
func (c *Client) ListRuns(_ context.Context, owner, repo string, opts ListRunsOpts) (string, error) {
	args := []string{"run", "list", "-R", repoFlag(owner, repo), "--json", runListFields, "--limit", strconv.Itoa(clampLimit(opts.Limit))}
	if opts.Branch != "" {
		args = append(args, "--branch", opts.Branch)
	}
	if opts.Status != "" {
		args = append(args, "--status", opts.Status)
	}
	if opts.Workflow != "" {
		args = append(args, "--workflow", opts.Workflow)
	}
	out, err := c.runner.Run("gh", args...)
	if err != nil {
		return "", fmt.Errorf("gh run list failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// ViewRun retrieves details for a workflow run. If logFailed is true, returns
// the failed log output instead of JSON.
func (c *Client) ViewRun(_ context.Context, owner, repo string, runID string, logFailed bool) (string, error) {
	var args []string
	if logFailed {
		args = []string{"run", "view", "-R", repoFlag(owner, repo), "--log-failed", "--", runID}
	} else {
		args = []string{"run", "view", "-R", repoFlag(owner, repo), "--json", runViewFields, "--", runID}
	}
	out, err := c.runner.Run("gh", args...)
	if err != nil {
		return "", fmt.Errorf("gh run view failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// ViewRunJobLog fetches the log output for a single workflow job by ID.
// If tailLines > 0, only the last tailLines lines are returned.
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

// Rerun re-runs a workflow run.
func (c *Client) Rerun(_ context.Context, owner, repo string, runID string, failedOnly bool) (string, error) {
	args := []string{"run", "rerun", "-R", repoFlag(owner, repo)}
	if failedOnly {
		args = append(args, "--failed")
	}
	args = append(args, "--", runID)
	out, err := c.runner.Run("gh", args...)
	if err != nil {
		return "", fmt.Errorf("gh run rerun failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// CancelRun cancels a workflow run.
func (c *Client) CancelRun(_ context.Context, owner, repo string, runID string) (string, error) {
	out, err := c.runner.Run("gh", "run", "cancel", "-R", repoFlag(owner, repo), "--", runID)
	if err != nil {
		return "", fmt.Errorf("gh run cancel failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// ListCaches lists caches for a repository.
func (c *Client) ListCaches(_ context.Context, owner, repo string, opts ListCachesOpts) (string, error) {
	args := []string{"cache", "list", "-R", repoFlag(owner, repo), "--limit", strconv.Itoa(clampLimit(opts.Limit))}
	if opts.Sort != "" {
		args = append(args, "--sort", opts.Sort)
	}
	if opts.Order != "" {
		args = append(args, "--order", opts.Order)
	}
	out, err := c.runner.Run("gh", args...)
	if err != nil {
		return "", fmt.Errorf("gh cache list failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// DeleteCache deletes a cache from a repository.
func (c *Client) DeleteCache(_ context.Context, owner, repo string, cacheID string) (string, error) {
	out, err := c.runner.Run("gh", "cache", "delete", "-R", repoFlag(owner, repo), "--", cacheID)
	if err != nil {
		return "", fmt.Errorf("gh cache delete failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// ListPRFiles lists files changed by a pull request.
// Requests one extra item (up to 100) so callers can detect truncation.
func (c *Client) ListPRFiles(_ context.Context, owner, repo string, number, limit int) (string, error) {
	perPage := limit + 1
	if perPage > 100 {
		perPage = 100
	}
	out, err := c.runner.Run("gh", "api",
		fmt.Sprintf("repos/%s/%s/pulls/%s/files?per_page=%s", owner, repo, strconv.Itoa(number), strconv.Itoa(perPage)),
	)
	if err != nil {
		return "", fmt.Errorf("gh api pulls/%d/files failed: %s", number, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// ListBranches lists branches in a repository.
// Requests one extra item (up to 100) so callers can detect truncation.
func (c *Client) ListBranches(_ context.Context, owner, repo string, limit int) (string, error) {
	perPage := limit + 1
	if perPage > 100 {
		perPage = 100
	}
	out, err := c.runner.Run("gh", "api",
		fmt.Sprintf("repos/%s/%s/branches?per_page=%s", owner, repo, strconv.Itoa(perPage)),
	)
	if err != nil {
		return "", fmt.Errorf("gh api branches failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// SearchPRs searches for pull requests.
func (c *Client) SearchPRs(_ context.Context, query string, opts SearchPRsOpts) (string, error) {
	args := []string{"search", "prs", "--json", searchPRFields, "--limit", strconv.Itoa(clampLimit(opts.Limit))}
	if opts.Repo != "" {
		args = append(args, "--repo", opts.Repo)
	}
	if opts.Owner != "" {
		args = append(args, "--owner", opts.Owner)
	}
	if opts.State != "" {
		args = append(args, "--state", opts.State)
	}
	if opts.Author != "" {
		args = append(args, "--author", opts.Author)
	}
	if opts.Label != "" {
		args = append(args, "--label", opts.Label)
	}
	args = append(args, "--", query)
	out, err := c.runner.Run("gh", args...)
	if err != nil {
		return "", fmt.Errorf("gh search prs failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// SearchIssues searches for issues.
func (c *Client) SearchIssues(_ context.Context, query string, opts SearchIssuesOpts) (string, error) {
	args := []string{"search", "issues", "--json", searchIssueFields, "--limit", strconv.Itoa(clampLimit(opts.Limit))}
	if opts.Repo != "" {
		args = append(args, "--repo", opts.Repo)
	}
	if opts.Owner != "" {
		args = append(args, "--owner", opts.Owner)
	}
	if opts.State != "" {
		args = append(args, "--state", opts.State)
	}
	if opts.Author != "" {
		args = append(args, "--author", opts.Author)
	}
	if opts.Label != "" {
		args = append(args, "--label", opts.Label)
	}
	args = append(args, "--", query)
	out, err := c.runner.Run("gh", args...)
	if err != nil {
		return "", fmt.Errorf("gh search issues failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// SearchRepos searches for repositories.
func (c *Client) SearchRepos(_ context.Context, query string, opts SearchReposOpts) (string, error) {
	args := []string{"search", "repos", "--json", searchRepoFields, "--limit", strconv.Itoa(clampLimit(opts.Limit))}
	if opts.Owner != "" {
		args = append(args, "--owner", opts.Owner)
	}
	if opts.Language != "" {
		args = append(args, "--language", opts.Language)
	}
	if opts.Topic != "" {
		args = append(args, "--topic", opts.Topic)
	}
	if opts.Stars != "" {
		args = append(args, "--stars", opts.Stars)
	}
	args = append(args, "--", query)
	out, err := c.runner.Run("gh", args...)
	if err != nil {
		return "", fmt.Errorf("gh search repos failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// SearchCode searches for code.
func (c *Client) SearchCode(_ context.Context, query string, opts SearchCodeOpts) (string, error) {
	args := []string{"search", "code", "--json", searchCodeFields, "--limit", strconv.Itoa(clampLimit(opts.Limit))}
	if opts.Repo != "" {
		args = append(args, "--repo", opts.Repo)
	}
	if opts.Owner != "" {
		args = append(args, "--owner", opts.Owner)
	}
	if opts.Language != "" {
		args = append(args, "--language", opts.Language)
	}
	if opts.Extension != "" {
		args = append(args, "--extension", opts.Extension)
	}
	if opts.Filename != "" {
		args = append(args, "--filename", opts.Filename)
	}
	args = append(args, "--", query)
	out, err := c.runner.Run("gh", args...)
	if err != nil {
		return "", fmt.Errorf("gh search code failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// SearchCommits searches for commits.
func (c *Client) SearchCommits(_ context.Context, query string, opts SearchCommitsOpts) (string, error) {
	args := []string{"search", "commits", "--json", searchCommitFields, "--limit", strconv.Itoa(clampLimit(opts.Limit))}
	if opts.Repo != "" {
		args = append(args, "--repo", opts.Repo)
	}
	if opts.Owner != "" {
		args = append(args, "--owner", opts.Owner)
	}
	if opts.Author != "" {
		args = append(args, "--author", opts.Author)
	}
	args = append(args, "--", query)
	out, err := c.runner.Run("gh", args...)
	if err != nil {
		return "", fmt.Errorf("gh search commits failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
