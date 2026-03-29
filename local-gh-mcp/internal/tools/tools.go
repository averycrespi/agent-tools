package tools

import (
	"context"
	"fmt"

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
	// Issue operations
	ViewIssue(ctx context.Context, owner, repo string, number int) (string, error)
	ListIssues(ctx context.Context, owner, repo string, opts gh.ListIssuesOpts) (string, error)
	CommentIssue(ctx context.Context, owner, repo string, number int, body string) (string, error)
	// Comment listing operations
	PRComments(ctx context.Context, owner, repo string, number int, limit int) (string, error)
	IssueComments(ctx context.Context, owner, repo string, number int, limit int) (string, error)
	// Run operations - NOTE: runID is string
	ListRuns(ctx context.Context, owner, repo string, opts gh.ListRunsOpts) (string, error)
	ViewRun(ctx context.Context, owner, repo string, runID string, logFailed bool) (string, error)
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
	tools = append(tools, h.prTools()...)
	tools = append(tools, h.issueTools()...)
	tools = append(tools, h.runTools()...)
	tools = append(tools, h.cacheTools()...)
	tools = append(tools, h.searchTools()...)
	return tools
}

// Handle dispatches an MCP tool call to the appropriate handler.
func (h *Handler) Handle(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	switch req.Params.Name {
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
	case "gh_check_pr":
		return h.handleCheckPR(ctx, req)
	case "gh_close_pr":
		return h.handleClosePR(ctx, req)
	case "gh_view_issue":
		return h.handleViewIssue(ctx, req)
	case "gh_list_issues":
		return h.handleListIssues(ctx, req)
	case "gh_comment_issue":
		return h.handleCommentIssue(ctx, req)
	case "gh_list_pr_comments":
		return h.handleListPRComments(ctx, req)
	case "gh_list_issue_comments":
		return h.handleListIssueComments(ctx, req)
	case "gh_list_runs":
		return h.handleListRuns(ctx, req)
	case "gh_view_run":
		return h.handleViewRun(ctx, req)
	case "gh_rerun":
		return h.handleRerun(ctx, req)
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
	default:
		return gomcp.NewToolResultError(fmt.Sprintf("unknown tool: %s", req.Params.Name)), nil
	}
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

func requireOwnerRepo(args map[string]any) (string, string, *gomcp.CallToolResult) {
	owner, _ := args["owner"].(string)
	repo, _ := args["repo"].(string)
	if err := gh.ValidateOwnerRepo(owner, repo); err != nil {
		return "", "", gomcp.NewToolResultError(err.Error())
	}
	return owner, repo, nil
}
