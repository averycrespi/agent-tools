package tools

import (
	"context"
	"encoding/json"

	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/format"
	gomcp "github.com/mark3labs/mcp-go/mcp"
)

func (h *Handler) branchTools() []gomcp.Tool {
	return []gomcp.Tool{
		{
			Name:        "gh_list_branches",
			Description: "List branches in a GitHub repository, alphabetical by branch name (the underlying GitHub REST endpoint does not expose recency sorting). Each entry shows the branch name and its HEAD commit SHA. Results truncated at `limit` (default 30, max 100); use `page` to retrieve later pages.",
			Annotations: annRead,
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"owner": map[string]any{"type": "string", "description": "Repository owner."},
					"repo":  map[string]any{"type": "string", "description": "Repository name."},
					"limit": map[string]any{"type": "number", "minimum": 1, "default": 30, "description": "Max branches shown (default 30, max 100; values <= 0 are rejected)."},
					"page":  map[string]any{"type": "number", "default": 1, "minimum": 1, "description": "1-indexed page number (default 1)."},
				},
				Required: []string{"owner", "repo"},
			},
		},
	}
}

func (h *Handler) handleListBranches(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	limit, errResult := validateLimit(args)
	if errResult != nil {
		return errResult, nil
	}
	limit = clampLimit(limit)
	page := intFromArgs(args, "page")
	if page < 1 {
		page = 1
	}
	raw, err := h.gh.ListBranches(ctx, owner, repo, limit, page)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var branches []format.Branch
	if err := json.Unmarshal([]byte(raw), &branches); err != nil {
		return parseError("gh_list_branches", err, raw), nil
	}
	if len(branches) == 0 {
		return gomcp.NewToolResultText("No branches found."), nil
	}
	return gomcp.NewToolResultText(format.FormatBranches(branches, limit)), nil
}
