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
	prViewFields  = "number,title,body,state,author,baseRefName,headRefName,url,isDraft,mergeable,reviewDecision,statusCheckRollup,labels,assignees,createdAt,updatedAt"
	prListFields  = "number,title,state,author,headRefName,url,isDraft,createdAt,updatedAt"
	prCheckFields = "name,state,description,targetUrl,startedAt,completedAt"
)

// CreatePROpts holds options for creating a pull request.
type CreatePROpts struct {
	Title, Body, Base, Head string
	Draft                   bool
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
	Title, Body, Base                   string
	AddLabels, RemoveLabels             []string
	AddReviewers, RemoveReviewers       []string
	AddAssignees, RemoveAssignees       []string
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
