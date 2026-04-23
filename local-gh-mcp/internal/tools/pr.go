package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/format"
	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/gh"
	gomcp "github.com/mark3labs/mcp-go/mcp"
)

func prNumberOnlySchema(prDesc string) gomcp.ToolInputSchema {
	return gomcp.ToolInputSchema{
		Type: "object",
		Properties: map[string]any{
			"owner":     map[string]any{"type": "string", "description": "Repository owner."},
			"repo":      map[string]any{"type": "string", "description": "Repository name."},
			"pr_number": map[string]any{"type": "number", "description": prDesc},
		},
		Required: []string{"owner", "repo", "pr_number"},
	}
}

func (h *Handler) prTools() []gomcp.Tool {
	return []gomcp.Tool{
		{
			Name:        "gh_create_pr",
			Description: "Create a new pull request. Fails if a PR already exists for the head branch. Returns the PR URL.",
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
				Required: []string{"owner", "repo", "title", "body"},
			},
		},
		{
			Name:        "gh_view_pr",
			Description: "View PR metadata and description as structured markdown. For the diff, use gh_diff_pr; for CI status, use gh_list_pr_checks; for conversation, use gh_list_pr_comments/reviews/review_comments.",
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
					"pr_number": map[string]any{
						"type":        "number",
						"description": "Pull request number",
					},
					"max_body_length": map[string]any{
						"type":        "number",
						"default":     2000,
						"description": "Max body length in chars (default 2000, max 50000).",
					},
				},
				Required: []string{"owner", "repo", "pr_number"},
			},
		},
		{
			Name:        "gh_list_prs",
			Description: "List PRs in a single repository. Use this when you know owner/repo. For cross-repo queries or GitHub search DSL filters (is:open, author:@me, etc.), use gh_search_prs instead.",
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
						"enum":        []string{"open", "closed", "merged", "all"},
						"description": "Filter by state.",
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
			Name:        "gh_diff_pr",
			Description: "View a PR's diff. Returns a file summary table followed by the full unified diff. Large PRs can be long; if you only need which files changed, consider the file summary alone.",
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
					"pr_number": map[string]any{
						"type":        "number",
						"description": "Pull request number",
					},
				},
				Required: []string{"owner", "repo", "pr_number"},
			},
		},
		{
			Name:        "gh_comment_pr",
			Description: "Post a conversation comment on a PR (issue-style, not tied to a line of the diff). For inline review comments, use gh_review_pr instead.",
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
					"pr_number": map[string]any{
						"type":        "number",
						"description": "Pull request number",
					},
					"body": map[string]any{
						"type":        "string",
						"description": "Comment body",
					},
				},
				Required: []string{"owner", "repo", "pr_number", "body"},
			},
		},
		{
			Name:        "gh_review_pr",
			Description: "Submit a review: approve, request_changes, or comment. Requires owner, repo, PR number, and event. A body is optional for approve and comment; request_changes requires a body.",
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
					"pr_number": map[string]any{
						"type":        "number",
						"description": "Pull request number",
					},
					"event": map[string]any{
						"type":        "string",
						"enum":        []string{"approve", "request_changes", "comment"},
						"description": "Review event type.",
					},
					"body": map[string]any{
						"type":        "string",
						"description": "Review body",
					},
				},
				Required: []string{"owner", "repo", "pr_number", "event"},
			},
		},
		{
			Name:        "gh_merge_pr",
			Description: "Merge a pull request. Method defaults to the repo's default; specify merge/squash/rebase explicitly to override. Set auto=true to enable auto-merge when checks pass.",
			Annotations: annDestructive,
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
					"pr_number": map[string]any{
						"type":        "number",
						"description": "Pull request number",
					},
					"method": map[string]any{
						"type":        "string",
						"enum":        []string{"merge", "squash", "rebase"},
						"description": "Merge method.",
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
				Required: []string{"owner", "repo", "pr_number"},
			},
		},
		{
			Name:        "gh_edit_pr",
			Description: "Edit PR metadata (title, body, base, labels, reviewers, assignees). Cannot change state or draft status — use gh_close_pr/gh_ready_pr/gh_draft_pr/gh_reopen_pr for those.",
			Annotations: annIdempotent,
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
					"pr_number": map[string]any{
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
				Required: []string{"owner", "repo", "pr_number"},
			},
		},
		{
			Name:        "gh_list_pr_checks",
			Description: "List CI status checks for a PR. Returns markdown bullets per check with state (success/failure/pending) and link.",
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
					"pr_number": map[string]any{
						"type":        "number",
						"description": "Pull request number",
					},
				},
				Required: []string{"owner", "repo", "pr_number"},
			},
		},
		{
			Name:        "gh_close_pr",
			Description: "Close a PR without merging. Optionally attach a closing comment. To reopen later, use gh_reopen_pr.",
			Annotations: annDestructive,
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
					"pr_number": map[string]any{
						"type":        "number",
						"description": "Pull request number",
					},
					"comment": map[string]any{
						"type":        "string",
						"description": "Comment to add when closing",
					},
				},
				Required: []string{"owner", "repo", "pr_number"},
			},
		},
		{
			Name:        "gh_list_pr_comments",
			Description: "List conversation (issue-style) comments on a pull request. Does NOT include review summaries or inline diff comments — for those, use gh_list_pr_reviews and gh_list_pr_review_comments. Returns markdown-formatted comment list.",
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
					"pr_number": map[string]any{
						"type":        "number",
						"description": "Pull request number",
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
				Required: []string{"owner", "repo", "pr_number"},
			},
		},
		{
			Name:        "gh_list_pr_reviews",
			Description: "List top-level review submissions on a pull request (approve, request-changes, comment) with their state, body, author, and submission date. For inline diff comments use gh_list_pr_review_comments. Returns markdown.",
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
					"pr_number": map[string]any{
						"type":        "number",
						"description": "Pull request number",
					},
					"max_body_length": map[string]any{
						"type":        "number",
						"default":     2000,
						"description": "Max body length per review in chars (default 2000, max 50000).",
					},
					"limit": map[string]any{
						"type":        "number",
						"default":     30,
						"description": "Max reviews to return (default 30, max 100).",
					},
				},
				Required: []string{"owner", "repo", "pr_number"},
			},
		},
		{
			Name:        "gh_list_pr_review_comments",
			Description: "List inline review comments on a pull request's diff (comments attached to specific file and line). Grouped by file and threaded by reply. For top-level review summaries use gh_list_pr_reviews; for issue-style comments use gh_list_pr_comments. Returns markdown.",
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
					"pr_number": map[string]any{
						"type":        "number",
						"description": "Pull request number",
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
				Required: []string{"owner", "repo", "pr_number"},
			},
		},
		{
			Name:        "gh_list_pr_files",
			Description: "List files touched by a pull request with +/- counts per file (no diff content). Use gh_diff_pr if you need diff hunks. Results truncated at `limit` (default 30, max 100).",
			Annotations: annRead,
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"owner":     map[string]any{"type": "string", "description": "Repository owner."},
					"repo":      map[string]any{"type": "string", "description": "Repository name."},
					"pr_number": map[string]any{"type": "number", "description": "Pull request number."},
					"limit":     map[string]any{"type": "number", "default": 30, "description": "Max files shown (default 30, max 100)."},
				},
				Required: []string{"owner", "repo", "pr_number"},
			},
		},
		{
			Name:        "gh_ready_pr",
			Description: "Mark a draft pull request as ready for review (`gh pr ready`). See also gh_draft_pr to convert back, gh_close_pr / gh_reopen_pr for state transitions.",
			Annotations: annIdempotent,
			InputSchema: prNumberOnlySchema("Pull request number to mark ready."),
		},
		{
			Name:        "gh_draft_pr",
			Description: "Convert a pull request back to draft (`gh pr ready --undo`). See also gh_ready_pr.",
			Annotations: annIdempotent,
			InputSchema: prNumberOnlySchema("Pull request number to convert to draft."),
		},
		{
			Name:        "gh_reopen_pr",
			Description: "Reopen a closed pull request (`gh pr reopen`). See also gh_close_pr.",
			Annotations: annIdempotent,
			InputSchema: prNumberOnlySchema("Pull request number to reopen."),
		},
	}
}

func (h *Handler) handleCreatePR(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	if errResult := requireStringFields("gh_create_pr", args, "title", "body"); errResult != nil {
		return errResult, nil
	}
	title := stringFromArgs(args, "title")
	body := stringFromArgs(args, "body")
	opts := gh.CreatePROpts{
		Title:     title,
		Body:      body,
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
	number := intFromArgs(args, "pr_number")
	if number == 0 {
		return gomcp.NewToolResultError("pr_number is required"), nil
	}
	maxBody := clampMaxBodyLength(intFromArgs(args, "max_body_length"))
	out, err := h.gh.ViewPR(ctx, owner, repo, number)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var pr format.PRView
	if err := json.Unmarshal([]byte(out), &pr); err != nil {
		return parseError("gh_view_pr", err, out), nil
	}
	return gomcp.NewToolResultText(format.FormatPRView(pr, maxBody)), nil
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
		Limit:  intFromArgs(args, "limit"),
	}
	out, err := h.gh.ListPRs(ctx, owner, repo, opts)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var items []format.PRListItem
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		return parseError("gh_list_prs", err, out), nil
	}
	var lines []string
	for _, item := range items {
		lines = append(lines, format.FormatPRListItem(item))
	}
	if len(lines) == 0 {
		return gomcp.NewToolResultText("No pull requests found."), nil
	}
	return gomcp.NewToolResultText(strings.Join(lines, "\n")), nil
}

func (h *Handler) handleDiffPR(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	number := intFromArgs(args, "pr_number")
	if number == 0 {
		return gomcp.NewToolResultError("pr_number is required"), nil
	}
	out, err := h.gh.DiffPR(ctx, owner, repo, number)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return gomcp.NewToolResultText(format.FormatDiff(out)), nil
}

func (h *Handler) handleCommentPR(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	number := intFromArgs(args, "pr_number")
	if number == 0 {
		return gomcp.NewToolResultError("pr_number is required"), nil
	}
	if errResult := requireStringFields("gh_comment_pr", args, "body"); errResult != nil {
		return errResult, nil
	}
	body := stringFromArgs(args, "body")
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
	number := intFromArgs(args, "pr_number")
	if number == 0 {
		return gomcp.NewToolResultError("pr_number is required"), nil
	}
	if errResult := requireStringFields("gh_review_pr", args, "event"); errResult != nil {
		return errResult, nil
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
	number := intFromArgs(args, "pr_number")
	if number == 0 {
		return gomcp.NewToolResultError("pr_number is required"), nil
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
	number := intFromArgs(args, "pr_number")
	if number == 0 {
		return gomcp.NewToolResultError("pr_number is required"), nil
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

func (h *Handler) handleListPRChecks(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	number := intFromArgs(args, "pr_number")
	if number == 0 {
		return gomcp.NewToolResultError("pr_number is required"), nil
	}
	out, err := h.gh.CheckPR(ctx, owner, repo, number)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var checks []format.Check
	if err := json.Unmarshal([]byte(out), &checks); err != nil {
		return parseError("gh_list_pr_checks", err, out), nil
	}
	return gomcp.NewToolResultText(format.FormatCheckList(checks)), nil
}

func (h *Handler) handleClosePR(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	number := intFromArgs(args, "pr_number")
	if number == 0 {
		return gomcp.NewToolResultError("pr_number is required"), nil
	}
	comment := stringFromArgs(args, "comment")
	out, err := h.gh.ClosePR(ctx, owner, repo, number, comment)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return gomcp.NewToolResultText(out), nil
}

func (h *Handler) handleListPRComments(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	number := intFromArgs(args, "pr_number")
	if number == 0 {
		return gomcp.NewToolResultError("pr_number is required"), nil
	}
	maxBody := clampMaxBodyLength(intFromArgs(args, "max_body_length"))
	limit := intFromArgs(args, "limit")
	out, err := h.gh.PRComments(ctx, owner, repo, number, limit)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var comments []format.Comment
	if err := json.Unmarshal([]byte(out), &comments); err != nil {
		return parseError("gh_list_pr_comments", err, out), nil
	}
	return gomcp.NewToolResultText(format.FormatComments(comments, maxBody)), nil
}

func (h *Handler) handleListPRReviews(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	number := intFromArgs(args, "pr_number")
	if number == 0 {
		return gomcp.NewToolResultError("pr_number is required"), nil
	}
	maxBody := clampMaxBodyLength(intFromArgs(args, "max_body_length"))
	limit := intFromArgs(args, "limit")
	out, err := h.gh.PRReviews(ctx, owner, repo, number, limit)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var reviews []format.Review
	if err := json.Unmarshal([]byte(out), &reviews); err != nil {
		return parseError("gh_list_pr_reviews", err, out), nil
	}
	return gomcp.NewToolResultText(format.FormatReviews(reviews, maxBody)), nil
}

func (h *Handler) handleListPRReviewComments(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	number := intFromArgs(args, "pr_number")
	if number == 0 {
		return gomcp.NewToolResultError("pr_number is required"), nil
	}
	maxBody := clampMaxBodyLength(intFromArgs(args, "max_body_length"))
	limit := intFromArgs(args, "limit")
	out, err := h.gh.PRReviewComments(ctx, owner, repo, number, limit)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var comments []format.ReviewComment
	if err := json.Unmarshal([]byte(out), &comments); err != nil {
		return parseError("gh_list_pr_review_comments", err, out), nil
	}
	return gomcp.NewToolResultText(format.FormatReviewComments(comments, maxBody)), nil
}

func (h *Handler) handleReadyPR(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	number := intFromArgs(args, "pr_number")
	if number == 0 {
		return gomcp.NewToolResultError("pr_number is required"), nil
	}
	if _, err := h.gh.ReadyPR(ctx, owner, repo, number); err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return gomcp.NewToolResultText(fmt.Sprintf("PR #%d in %s/%s marked ready for review", number, owner, repo)), nil
}

func (h *Handler) handleDraftPR(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	number := intFromArgs(args, "pr_number")
	if number == 0 {
		return gomcp.NewToolResultError("pr_number is required"), nil
	}
	if _, err := h.gh.DraftPR(ctx, owner, repo, number); err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return gomcp.NewToolResultText(fmt.Sprintf("PR #%d in %s/%s converted to draft", number, owner, repo)), nil
}

func (h *Handler) handleReopenPR(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	number := intFromArgs(args, "pr_number")
	if number == 0 {
		return gomcp.NewToolResultError("pr_number is required"), nil
	}
	if _, err := h.gh.ReopenPR(ctx, owner, repo, number); err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return gomcp.NewToolResultText(fmt.Sprintf("PR #%d in %s/%s reopened", number, owner, repo)), nil
}

func (h *Handler) handleListPRFiles(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	number := intFromArgs(args, "pr_number")
	if number == 0 {
		return gomcp.NewToolResultError("pr_number is required"), nil
	}
	limit := clampLimit(intFromArgs(args, "limit"))
	raw, err := h.gh.ListPRFiles(ctx, owner, repo, number, limit)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var files []format.PRFile
	if err := json.Unmarshal([]byte(raw), &files); err != nil {
		return parseError("gh_list_pr_files", err, raw), nil
	}
	return gomcp.NewToolResultText(format.FormatPRFiles(files, limit)), nil
}
