package tools

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/gh"
	gomcp "github.com/mark3labs/mcp-go/mcp"
)

// GHClient defines the GitHub operations needed by MCP tool handlers.
// Must match gh.Client method signatures exactly.
type GHClient interface {
	// PR operations
	CreatePR(ctx context.Context, owner, repo string, opts gh.CreatePROpts) (string, error)
	ViewPR(ctx context.Context, owner, repo string, number int) (string, error)
	ListPRs(ctx context.Context, owner, repo string, opts gh.ListPROpts) (string, error)
	DiffPR(ctx context.Context, owner, repo string, number int) (string, error)
	CommentPR(ctx context.Context, owner, repo string, number int, body string) (string, error)
	ReviewPR(ctx context.Context, owner, repo string, number int, event, body string) (string, error)
	MergePR(ctx context.Context, owner, repo string, number int, opts gh.MergePROpts) (string, error)
	EditPR(ctx context.Context, owner, repo string, number int, opts gh.EditPROpts) (string, error)
	CheckPR(ctx context.Context, owner, repo string, number int) (string, error)
	ClosePR(ctx context.Context, owner, repo string, number int, comment string) (string, error)
	ReadyPR(ctx context.Context, owner, repo string, number int) (string, error)
	DraftPR(ctx context.Context, owner, repo string, number int) (string, error)
	ReopenPR(ctx context.Context, owner, repo string, number int) (string, error)
	// Issue operations
	ViewIssue(ctx context.Context, owner, repo string, number int) (string, error)
	ListIssues(ctx context.Context, owner, repo string, opts gh.ListIssuesOpts) (string, error)
	CommentIssue(ctx context.Context, owner, repo string, number int, body string) (string, error)
	// Comment listing operations
	PRComments(ctx context.Context, owner, repo string, number int, limit int) (string, error)
	PRReviews(ctx context.Context, owner, repo string, number int, limit int) (string, error)
	PRReviewComments(ctx context.Context, owner, repo string, number int, limit int) (string, error)
	IssueComments(ctx context.Context, owner, repo string, number int, limit int) (string, error)
	// Run operations - NOTE: runID is string
	ListRuns(ctx context.Context, owner, repo string, opts gh.ListRunsOpts) (string, error)
	ViewRun(ctx context.Context, owner, repo string, runID string, logFailed bool) (string, error)
	ViewRunJobLog(ctx context.Context, owner, repo string, jobID int64, tailLines int) (string, error)
	Rerun(ctx context.Context, owner, repo string, runID string, failedOnly bool) (string, error)
	CancelRun(ctx context.Context, owner, repo string, runID string) (string, error)
	// Cache operations - NOTE: cacheID is string
	ListCaches(ctx context.Context, owner, repo string, opts gh.ListCachesOpts) (string, error)
	DeleteCache(ctx context.Context, owner, repo string, cacheID string) (string, error)
	// Search operations
	SearchPRs(ctx context.Context, query string, opts gh.SearchPRsOpts) (string, error)
	SearchIssues(ctx context.Context, query string, opts gh.SearchIssuesOpts) (string, error)
	SearchRepos(ctx context.Context, query string, opts gh.SearchReposOpts) (string, error)
	SearchCode(ctx context.Context, query string, opts gh.SearchCodeOpts) (string, error)
	SearchCommits(ctx context.Context, query string, opts gh.SearchCommitsOpts) (string, error)
	// PR files
	ListPRFiles(ctx context.Context, owner, repo string, number, limit int) (string, error)
	// Context operations
	ViewUser(ctx context.Context) (string, error)
	// Branch operations
	ListBranches(ctx context.Context, owner, repo string, limit, page int) (string, error)
	// Release operations
	ListReleases(ctx context.Context, owner, repo string, limit int) (string, error)
	ViewRelease(ctx context.Context, owner, repo, tag string) (string, error)
}

// Handler manages MCP tool definitions and dispatches calls to the GH client.
type Handler struct {
	gh GHClient
}

// NewHandler creates a Handler with the given GH client.
func NewHandler(gh GHClient) *Handler {
	return &Handler{gh: gh}
}

// Tools returns all MCP tool definitions.
func (h *Handler) Tools() []gomcp.Tool {
	var tools []gomcp.Tool
	tools = append(tools, h.contextTools()...)
	tools = append(tools, h.prTools()...)
	tools = append(tools, h.issueTools()...)
	tools = append(tools, h.runTools()...)
	tools = append(tools, h.cacheTools()...)
	tools = append(tools, h.searchTools()...)
	tools = append(tools, h.branchTools()...)
	tools = append(tools, h.releaseTools()...)
	return tools
}

// Handle dispatches an MCP tool call to the appropriate handler.
func (h *Handler) Handle(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	switch req.Params.Name {
	case "gh_whoami":
		return h.handleWhoami(ctx, req)
	case "gh_create_pr":
		return h.handleCreatePR(ctx, req)
	case "gh_view_pr":
		return h.handleViewPR(ctx, req)
	case "gh_list_prs":
		return h.handleListPRs(ctx, req)
	case "gh_diff_pr":
		return h.handleDiffPR(ctx, req)
	case "gh_comment_pr":
		return h.handleCommentPR(ctx, req)
	case "gh_review_pr":
		return h.handleReviewPR(ctx, req)
	case "gh_merge_pr":
		return h.handleMergePR(ctx, req)
	case "gh_edit_pr":
		return h.handleEditPR(ctx, req)
	case "gh_list_pr_checks":
		return h.handleListPRChecks(ctx, req)
	case "gh_close_pr":
		return h.handleClosePR(ctx, req)
	case "gh_ready_pr":
		return h.handleReadyPR(ctx, req)
	case "gh_draft_pr":
		return h.handleDraftPR(ctx, req)
	case "gh_reopen_pr":
		return h.handleReopenPR(ctx, req)
	case "gh_view_issue":
		return h.handleViewIssue(ctx, req)
	case "gh_list_issues":
		return h.handleListIssues(ctx, req)
	case "gh_comment_issue":
		return h.handleCommentIssue(ctx, req)
	case "gh_list_pr_comments":
		return h.handleListPRComments(ctx, req)
	case "gh_list_pr_reviews":
		return h.handleListPRReviews(ctx, req)
	case "gh_list_pr_review_comments":
		return h.handleListPRReviewComments(ctx, req)
	case "gh_list_issue_comments":
		return h.handleListIssueComments(ctx, req)
	case "gh_list_runs":
		return h.handleListRuns(ctx, req)
	case "gh_view_run":
		return h.handleViewRun(ctx, req)
	case "gh_view_run_job_logs":
		return h.handleViewRunJobLogs(ctx, req)
	case "gh_rerun_run":
		return h.handleRerunRun(ctx, req)
	case "gh_cancel_run":
		return h.handleCancelRun(ctx, req)
	case "gh_list_caches":
		return h.handleListCaches(ctx, req)
	case "gh_delete_cache":
		return h.handleDeleteCache(ctx, req)
	case "gh_search_prs":
		return h.handleSearchPRs(ctx, req)
	case "gh_search_issues":
		return h.handleSearchIssues(ctx, req)
	case "gh_search_repos":
		return h.handleSearchRepos(ctx, req)
	case "gh_search_code":
		return h.handleSearchCode(ctx, req)
	case "gh_search_commits":
		return h.handleSearchCommits(ctx, req)
	case "gh_list_pr_files":
		return h.handleListPRFiles(ctx, req)
	case "gh_list_branches":
		return h.handleListBranches(ctx, req)
	case "gh_list_releases":
		return h.handleListReleases(ctx, req)
	case "gh_view_release":
		return h.handleViewRelease(ctx, req)
	default:
		return gomcp.NewToolResultError(fmt.Sprintf("unknown tool: %s", req.Params.Name)), nil
	}
}

// parseError logs the raw gh output at error level and returns a terse
// tool-result error. Parse failures are server bugs; we don't leak internals
// to agents, but operators need the raw output to diagnose.
func parseError(toolName string, err error, raw string) *gomcp.CallToolResult {
	slog.Error("failed to parse gh output",
		"tool", toolName,
		"err", err,
		"raw", raw)
	return gomcp.NewToolResultError("internal error: unable to parse gh output; check server logs")
}

// Shared helpers

func intFromArgs(args map[string]any, key string) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

// requirePositiveInt reads a required positive integer argument. Returns a
// terminal tool-error result if the value is missing, zero, or negative —
// callers can `return result, nil` immediately when the second value is non-nil.
func requirePositiveInt(args map[string]any, key string) (int, *gomcp.CallToolResult) {
	n := intFromArgs(args, key)
	if n <= 0 {
		return 0, gomcp.NewToolResultError(fmt.Sprintf("%s must be a positive integer", key))
	}
	return n, nil
}

// requirePositiveIntString reads a required string argument expected to encode
// a positive integer (precision-safe for int64 IDs that exceed JSON number
// precision). Returns the trimmed string on success; rejects missing, empty,
// whitespace, non-numeric, zero, and negative values.
func requirePositiveIntString(args map[string]any, key string) (string, *gomcp.CallToolResult) {
	s := strings.TrimSpace(stringFromArgs(args, key))
	if s == "" {
		return "", gomcp.NewToolResultError(fmt.Sprintf("%s must be a positive integer", key))
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return "", gomcp.NewToolResultError(fmt.Sprintf("%s must be a positive integer", key))
	}
	return s, nil
}

// validateEnum is defense-in-depth handler-side validation for params that
// declare an `enum` in their schema. Empty values pass through so optional
// filters can fall back to gh defaults; callers reject empty separately when
// the param is required. Returns a terminal tool-error result on mismatch.
func validateEnum(key, value string, allowed []string) *gomcp.CallToolResult {
	if value == "" {
		return nil
	}
	for _, a := range allowed {
		if value == a {
			return nil
		}
	}
	return gomcp.NewToolResultError(fmt.Sprintf("invalid %s %q: must be one of %s", key, value, strings.Join(allowed, ", ")))
}

func stringFromArgs(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return v
}

func boolFromArgs(args map[string]any, key string) bool {
	v, _ := args[key].(bool)
	return v
}

func stringSliceFromArgs(args map[string]any, key string) []string {
	val, ok := args[key]
	if !ok {
		return nil
	}
	if arr, ok := val.([]any); ok {
		result := make([]string, 0, len(arr))
		for _, item := range arr {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	}
	return nil
}

const (
	defaultLimit         = 30
	maxLimit             = 100
	defaultMaxBodyLength = 2000
	maxMaxBodyLength     = 50000
	defaultDiffMaxBytes  = 50000
	maxDiffMaxBytes      = 500000
	defaultLogTailLines  = 200
	maxLogTailLines      = 5000
	defaultLogMaxBytes   = 50000
	maxLogMaxBytes       = 500000
)

func clampLimit(v int) int {
	if v <= 0 {
		return defaultLimit
	}
	if v > maxLimit {
		return maxLimit
	}
	return v
}

func clampMaxBodyLength(v int) int {
	if v <= 0 {
		return defaultMaxBodyLength
	}
	if v > maxMaxBodyLength {
		return maxMaxBodyLength
	}
	return v
}

func clampDiffMaxBytes(v int) int {
	if v <= 0 {
		return defaultDiffMaxBytes
	}
	if v > maxDiffMaxBytes {
		return maxDiffMaxBytes
	}
	return v
}

func clampLogTailLines(v int) int {
	if v <= 0 {
		return defaultLogTailLines
	}
	if v > maxLogTailLines {
		return maxLogTailLines
	}
	return v
}

func clampLogMaxBytes(v int) int {
	if v <= 0 {
		return defaultLogMaxBytes
	}
	if v > maxLogMaxBytes {
		return maxLogMaxBytes
	}
	return v
}

// tailLines returns the last n lines of s, joined with "\n". Trailing newlines on s are
// trimmed before splitting so the count is by content lines, not blank trailing entries.
// Returns s unchanged if n <= 0 or the input has fewer than n lines.
func tailLines(s string, n int) string {
	if n <= 0 {
		return s
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

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

func requireOwnerRepo(args map[string]any) (string, string, *gomcp.CallToolResult) {
	owner, _ := args["owner"].(string)
	repo, _ := args["repo"].(string)
	if err := gh.ValidateOwnerRepo(owner, repo); err != nil {
		return "", "", gomcp.NewToolResultError(err.Error())
	}
	return owner, repo, nil
}

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
