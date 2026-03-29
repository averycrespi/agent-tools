package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/format"
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
					"max_body_length": map[string]any{
						"type":        "number",
						"description": "Max body length in chars (default 2000, max 50000)",
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
		{
			Name:        "gh_list_issue_comments",
			Description: "List comments on an issue. Returns markdown-formatted comment list.",
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

func (h *Handler) handleListIssueComments(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
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
	out, err := h.gh.IssueComments(ctx, owner, repo, number, limit)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var comments []format.Comment
	if err := json.Unmarshal([]byte(out), &comments); err != nil {
		return gomcp.NewToolResultError(fmt.Sprintf("failed to parse comments JSON: %v", err)), nil
	}
	return gomcp.NewToolResultText(format.FormatComments(comments, maxBody)), nil
}
