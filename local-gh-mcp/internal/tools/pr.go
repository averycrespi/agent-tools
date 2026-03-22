package tools

import (
	"context"
	"fmt"

	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/gh"
	gomcp "github.com/mark3labs/mcp-go/mcp"
)

func (h *Handler) prTools() []gomcp.Tool {
	return []gomcp.Tool{
		{
			Name:        "gh_create_pr",
			Description: "Create a new pull request",
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
					"title": map[string]any{
						"type":        "string",
						"description": "Pull request title",
					},
					"body": map[string]any{
						"type":        "string",
						"description": "Pull request body",
					},
					"base": map[string]any{
						"type":        "string",
						"description": "Base branch name",
					},
					"head": map[string]any{
						"type":        "string",
						"description": "Head branch name",
					},
					"draft": map[string]any{
						"type":        "boolean",
						"description": "Create as draft PR",
					},
					"labels": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Labels to add",
					},
					"reviewers": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Reviewers to request",
					},
					"assignees": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Assignees to add",
					},
				},
				Required: []string{"owner", "repo", "title"},
			},
		},
		{
			Name:        "gh_view_pr",
			Description: "View details of a pull request",
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
						"description": "Pull request number",
					},
				},
				Required: []string{"owner", "repo", "number"},
			},
		},
		{
			Name:        "gh_list_prs",
			Description: "List pull requests for a repository",
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
						"description": "Filter by state: open, closed, merged, all",
					},
					"author": map[string]any{
						"type":        "string",
						"description": "Filter by author",
					},
					"label": map[string]any{
						"type":        "string",
						"description": "Filter by label",
					},
					"base": map[string]any{
						"type":        "string",
						"description": "Filter by base branch",
					},
					"head": map[string]any{
						"type":        "string",
						"description": "Filter by head branch",
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
			Name:        "gh_diff_pr",
			Description: "Get the diff of a pull request",
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
						"description": "Pull request number",
					},
				},
				Required: []string{"owner", "repo", "number"},
			},
		},
		{
			Name:        "gh_comment_pr",
			Description: "Add a comment to a pull request",
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
						"description": "Pull request number",
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
			Name:        "gh_review_pr",
			Description: "Submit a review on a pull request",
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
						"description": "Pull request number",
					},
					"event": map[string]any{
						"type":        "string",
						"description": "Review event: approve, request_changes, comment",
					},
					"body": map[string]any{
						"type":        "string",
						"description": "Review body",
					},
				},
				Required: []string{"owner", "repo", "number", "event"},
			},
		},
		{
			Name:        "gh_merge_pr",
			Description: "Merge a pull request",
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
						"description": "Pull request number",
					},
					"method": map[string]any{
						"type":        "string",
						"description": "Merge method: merge, squash, rebase",
					},
					"delete_branch": map[string]any{
						"type":        "boolean",
						"description": "Delete branch after merge",
					},
					"auto": map[string]any{
						"type":        "boolean",
						"description": "Enable auto-merge",
					},
				},
				Required: []string{"owner", "repo", "number"},
			},
		},
		{
			Name:        "gh_edit_pr",
			Description: "Edit a pull request",
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
						"description": "Pull request number",
					},
					"title": map[string]any{
						"type":        "string",
						"description": "New title",
					},
					"body": map[string]any{
						"type":        "string",
						"description": "New body",
					},
					"base": map[string]any{
						"type":        "string",
						"description": "New base branch",
					},
					"add_labels": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Labels to add",
					},
					"remove_labels": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Labels to remove",
					},
					"add_reviewers": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Reviewers to add",
					},
					"remove_reviewers": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Reviewers to remove",
					},
					"add_assignees": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Assignees to add",
					},
					"remove_assignees": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Assignees to remove",
					},
				},
				Required: []string{"owner", "repo", "number"},
			},
		},
		{
			Name:        "gh_check_pr",
			Description: "View status checks for a pull request",
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
						"description": "Pull request number",
					},
				},
				Required: []string{"owner", "repo", "number"},
			},
		},
		{
			Name:        "gh_close_pr",
			Description: "Close a pull request",
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
						"description": "Pull request number",
					},
					"comment": map[string]any{
						"type":        "string",
						"description": "Comment to add when closing",
					},
				},
				Required: []string{"owner", "repo", "number"},
			},
		},
	}
}

func (h *Handler) handleCreatePR(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	title := stringFromArgs(args, "title")
	if title == "" {
		return gomcp.NewToolResultError("title is required"), nil
	}
	opts := gh.CreatePROpts{
		Title:     title,
		Body:      stringFromArgs(args, "body"),
		Base:      stringFromArgs(args, "base"),
		Head:      stringFromArgs(args, "head"),
		Draft:     boolFromArgs(args, "draft"),
		Labels:    stringSliceFromArgs(args, "labels"),
		Reviewers: stringSliceFromArgs(args, "reviewers"),
		Assignees: stringSliceFromArgs(args, "assignees"),
	}
	out, err := h.gh.CreatePR(ctx, owner, repo, opts)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return gomcp.NewToolResultText(out), nil
}

func (h *Handler) handleViewPR(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	number := intFromArgs(args, "number")
	if number == 0 {
		return gomcp.NewToolResultError("number is required"), nil
	}
	out, err := h.gh.ViewPR(ctx, owner, repo, number)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return gomcp.NewToolResultText(out), nil
}

func (h *Handler) handleListPRs(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	opts := gh.ListPROpts{
		State:  stringFromArgs(args, "state"),
		Author: stringFromArgs(args, "author"),
		Label:  stringFromArgs(args, "label"),
		Base:   stringFromArgs(args, "base"),
		Head:   stringFromArgs(args, "head"),
		Search: stringFromArgs(args, "search"),
		Limit:  intFromArgs(args, "limit"),
	}
	out, err := h.gh.ListPRs(ctx, owner, repo, opts)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return gomcp.NewToolResultText(out), nil
}

func (h *Handler) handleDiffPR(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	number := intFromArgs(args, "number")
	if number == 0 {
		return gomcp.NewToolResultError("number is required"), nil
	}
	out, err := h.gh.DiffPR(ctx, owner, repo, number)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return gomcp.NewToolResultText(out), nil
}

func (h *Handler) handleCommentPR(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
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
	out, err := h.gh.CommentPR(ctx, owner, repo, number, body)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return gomcp.NewToolResultText(out), nil
}

func (h *Handler) handleReviewPR(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	number := intFromArgs(args, "number")
	if number == 0 {
		return gomcp.NewToolResultError("number is required"), nil
	}
	event := stringFromArgs(args, "event")
	switch event {
	case "approve", "request_changes", "comment":
		// valid
	default:
		return gomcp.NewToolResultError(fmt.Sprintf("invalid event %q: must be approve, request_changes, or comment", event)), nil
	}
	// Convert underscore form to hyphen form expected by gh CLI.
	if event == "request_changes" {
		event = "request-changes"
	}
	body := stringFromArgs(args, "body")
	out, err := h.gh.ReviewPR(ctx, owner, repo, number, event, body)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return gomcp.NewToolResultText(out), nil
}

func (h *Handler) handleMergePR(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	number := intFromArgs(args, "number")
	if number == 0 {
		return gomcp.NewToolResultError("number is required"), nil
	}
	method := stringFromArgs(args, "method")
	switch method {
	case "merge", "squash", "rebase", "":
		// valid
	default:
		return gomcp.NewToolResultError(fmt.Sprintf("invalid method %q: must be merge, squash, or rebase", method)), nil
	}
	opts := gh.MergePROpts{
		Method:       method,
		DeleteBranch: boolFromArgs(args, "delete_branch"),
		Auto:         boolFromArgs(args, "auto"),
	}
	out, err := h.gh.MergePR(ctx, owner, repo, number, opts)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return gomcp.NewToolResultText(out), nil
}

func (h *Handler) handleEditPR(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	number := intFromArgs(args, "number")
	if number == 0 {
		return gomcp.NewToolResultError("number is required"), nil
	}
	opts := gh.EditPROpts{
		Title:           stringFromArgs(args, "title"),
		Body:            stringFromArgs(args, "body"),
		Base:            stringFromArgs(args, "base"),
		AddLabels:       stringSliceFromArgs(args, "add_labels"),
		RemoveLabels:    stringSliceFromArgs(args, "remove_labels"),
		AddReviewers:    stringSliceFromArgs(args, "add_reviewers"),
		RemoveReviewers: stringSliceFromArgs(args, "remove_reviewers"),
		AddAssignees:    stringSliceFromArgs(args, "add_assignees"),
		RemoveAssignees: stringSliceFromArgs(args, "remove_assignees"),
	}
	out, err := h.gh.EditPR(ctx, owner, repo, number, opts)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return gomcp.NewToolResultText(out), nil
}

func (h *Handler) handleCheckPR(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	number := intFromArgs(args, "number")
	if number == 0 {
		return gomcp.NewToolResultError("number is required"), nil
	}
	out, err := h.gh.CheckPR(ctx, owner, repo, number)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return gomcp.NewToolResultText(out), nil
}

func (h *Handler) handleClosePR(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	number := intFromArgs(args, "number")
	if number == 0 {
		return gomcp.NewToolResultError("number is required"), nil
	}
	comment := stringFromArgs(args, "comment")
	out, err := h.gh.ClosePR(ctx, owner, repo, number, comment)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return gomcp.NewToolResultText(out), nil
}
