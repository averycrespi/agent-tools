package tools

import (
	"context"

	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/gh"
	gomcp "github.com/mark3labs/mcp-go/mcp"
)

func (h *Handler) issueTools() []gomcp.Tool {
	return []gomcp.Tool{
		{
			Name:        "gh_view_issue",
			Description: "View details of an issue",
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
						"description": "Issue number",
					},
				},
				Required: []string{"owner", "repo", "number"},
			},
		},
		{
			Name:        "gh_list_issues",
			Description: "List issues for a repository",
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
					"state": map[string]any{
						"type":        "string",
						"description": "Filter by state: open, closed, all",
					},
					"author": map[string]any{
						"type":        "string",
						"description": "Filter by author",
					},
					"assignee": map[string]any{
						"type":        "string",
						"description": "Filter by assignee",
					},
					"label": map[string]any{
						"type":        "string",
						"description": "Filter by label",
					},
					"milestone": map[string]any{
						"type":        "string",
						"description": "Filter by milestone",
					},
					"search": map[string]any{
						"type":        "string",
						"description": "Search query",
					},
					"limit": map[string]any{
						"type":        "number",
						"description": "Max results (default 30, max 100)",
					},
				},
				Required: []string{"owner", "repo"},
			},
		},
		{
			Name:        "gh_comment_issue",
			Description: "Add a comment to an issue",
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
						"description": "Issue number",
					},
					"body": map[string]any{
						"type":        "string",
						"description": "Comment body",
					},
				},
				Required: []string{"owner", "repo", "number", "body"},
			},
		},
	}
}

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
	out, err := h.gh.ViewIssue(ctx, owner, repo, number)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return gomcp.NewToolResultText(out), nil
}

func (h *Handler) handleListIssues(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	opts := gh.ListIssuesOpts{
		State:     stringFromArgs(args, "state"),
		Author:    stringFromArgs(args, "author"),
		Assignee:  stringFromArgs(args, "assignee"),
		Label:     stringFromArgs(args, "label"),
		Milestone: stringFromArgs(args, "milestone"),
		Search:    stringFromArgs(args, "search"),
		Limit:     intFromArgs(args, "limit"),
	}
	out, err := h.gh.ListIssues(ctx, owner, repo, opts)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return gomcp.NewToolResultText(out), nil
}

func (h *Handler) handleCommentIssue(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	number := intFromArgs(args, "number")
	if number == 0 {
		return gomcp.NewToolResultError("number is required"), nil
	}
	body := stringFromArgs(args, "body")
	if body == "" {
		return gomcp.NewToolResultError("body is required"), nil
	}
	out, err := h.gh.CommentIssue(ctx, owner, repo, number, body)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return gomcp.NewToolResultText(out), nil
}
