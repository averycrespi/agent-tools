package tools

import (
	"context"
	"encoding/json"

	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/format"
	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/gh"
	gomcp "github.com/mark3labs/mcp-go/mcp"
)

func (h *Handler) issueTools() []gomcp.Tool {
	return []gomcp.Tool{
		{
			Name:        "gh_view_issue",
			Description: "View issue metadata and description as structured markdown. For comments, use gh_list_issue_comments.",
			Annotations: annRead,
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
					"issue_number": map[string]any{
						"type":        "number",
						"minimum":     1,
						"description": "Issue number",
					},
					"max_body_length": map[string]any{
						"type":        "number",
						"default":     2000,
						"description": "Max body length in chars (default 2000, max 50000).",
					},
				},
				Required: []string{"owner", "repo", "issue_number"},
			},
		},
		{
			Name:        "gh_list_issues",
			Description: "List issues in a single repository. Use this when you know owner/repo. For cross-repo queries or GitHub search DSL filters, use gh_search_issues instead.",
			Annotations: annRead,
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
						"enum":        []string{"open", "closed", "all"},
						"default":     "open",
						"description": "Filter by state (default open).",
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
					"limit": map[string]any{
						"type":        "number",
						"default":     30,
						"description": "Max results (default 30, max 100).",
					},
				},
				Required: []string{"owner", "repo"},
			},
		},
		{
			Name:        "gh_comment_issue",
			Description: "Post a comment on an issue. Returns the comment URL on success.",
			Annotations: annAdditive,
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
					"issue_number": map[string]any{
						"type":        "number",
						"minimum":     1,
						"description": "Issue number",
					},
					"body": map[string]any{
						"type":        "string",
						"description": "Comment body",
					},
				},
				Required: []string{"owner", "repo", "issue_number", "body"},
			},
		},
		{
			Name:        "gh_list_issue_comments",
			Description: "For the issue body itself, use gh_view_issue. List comments on an issue. Returns markdown-formatted comment list.",
			Annotations: annRead,
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
					"issue_number": map[string]any{
						"type":        "number",
						"minimum":     1,
						"description": "Issue number",
					},
					"max_body_length": map[string]any{
						"type":        "number",
						"default":     2000,
						"description": "Max body length per comment in chars (default 2000, max 50000).",
					},
					"limit": map[string]any{
						"type":        "number",
						"default":     30,
						"description": "Max comments to return (default 30, max 100).",
					},
				},
				Required: []string{"owner", "repo", "issue_number"},
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
	number, errResult := requirePositiveInt(args, "issue_number")
	if errResult != nil {
		return errResult, nil
	}
	maxBody := clampMaxBodyLength(intFromArgs(args, "max_body_length"))
	out, err := h.gh.ViewIssue(ctx, owner, repo, number)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var issue format.IssueView
	if err := json.Unmarshal([]byte(out), &issue); err != nil {
		return parseError("gh_view_issue", err, out), nil
	}
	return gomcp.NewToolResultText(format.FormatIssueView(issue, maxBody)), nil
}

func (h *Handler) handleListIssues(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	state := stringFromArgs(args, "state")
	if errResult := validateEnum("state", state, []string{"open", "closed", "all"}); errResult != nil {
		return errResult, nil
	}
	limit := clampLimit(intFromArgs(args, "limit"))
	opts := gh.ListIssuesOpts{
		State:     state,
		Author:    stringFromArgs(args, "author"),
		Assignee:  stringFromArgs(args, "assignee"),
		Label:     stringFromArgs(args, "label"),
		Milestone: stringFromArgs(args, "milestone"),
		Limit:     limit,
	}
	out, err := h.gh.ListIssues(ctx, owner, repo, opts)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var items []format.IssueListItem
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		return parseError("gh_list_issues", err, out), nil
	}
	if len(items) == 0 {
		return gomcp.NewToolResultText("No issues found."), nil
	}
	return gomcp.NewToolResultText(format.FormatIssueList(items, limit)), nil
}

func (h *Handler) handleCommentIssue(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	number, errResult := requirePositiveInt(args, "issue_number")
	if errResult != nil {
		return errResult, nil
	}
	if errResult := requireStringFields("gh_comment_issue", args, "body"); errResult != nil {
		return errResult, nil
	}
	body := stringFromArgs(args, "body")
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
	number, errResult := requirePositiveInt(args, "issue_number")
	if errResult != nil {
		return errResult, nil
	}
	maxBody := clampMaxBodyLength(intFromArgs(args, "max_body_length"))
	limit := clampLimit(intFromArgs(args, "limit"))
	out, err := h.gh.IssueComments(ctx, owner, repo, number, limit)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var comments []format.Comment
	if err := json.Unmarshal([]byte(out), &comments); err != nil {
		return parseError("gh_list_issue_comments", err, out), nil
	}
	if len(comments) == 0 {
		return gomcp.NewToolResultText("No comments found."), nil
	}
	return gomcp.NewToolResultText(format.FormatComments(comments, maxBody, limit)), nil
}
