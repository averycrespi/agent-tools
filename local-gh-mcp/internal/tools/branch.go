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
			Description: "List branches in a GitHub repository, newest first. Each entry shows the branch name and its HEAD commit SHA. Results truncated at `limit` (default 30, max 100).",
			Annotations: annRead,
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"owner": map[string]any{"type": "string", "description": "Repository owner."},
					"repo":  map[string]any{"type": "string", "description": "Repository name."},
					"limit": map[string]any{"type": "number", "default": 30, "description": "Max branches shown (default 30, max 100)."},
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
	limit := clampLimit(intFromArgs(args, "limit"))
	raw, err := h.gh.ListBranches(ctx, owner, repo, limit)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var branches []format.Branch
	if err := json.Unmarshal([]byte(raw), &branches); err != nil {
		return parseError("gh_list_branches", err, raw), nil
	}
	return gomcp.NewToolResultText(format.FormatBranches(branches, limit)), nil
}
