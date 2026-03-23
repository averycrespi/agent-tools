package tools

import (
	"context"

	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/gh"
	gomcp "github.com/mark3labs/mcp-go/mcp"
)

func (h *Handler) cacheTools() []gomcp.Tool {
	return []gomcp.Tool{
		{
			Name:        "gh_list_caches",
			Description: "List caches for a repository",
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
					"limit": map[string]any{
						"type":        "number",
						"description": "Max results (default 30, max 100)",
					},
					"sort": map[string]any{
						"type":        "string",
						"description": "Sort field",
					},
					"order": map[string]any{
						"type":        "string",
						"description": "Sort order: asc, desc",
					},
				},
				Required: []string{"owner", "repo"},
			},
		},
		{
			Name:        "gh_delete_cache",
			Description: "Delete a cache from a repository",
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
					"cache_id": map[string]any{
						"type":        "string",
						"description": "Cache ID to delete",
					},
				},
				Required: []string{"owner", "repo", "cache_id"},
			},
		},
	}
}

func (h *Handler) handleListCaches(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	opts := gh.ListCachesOpts{
		Limit: intFromArgs(args, "limit"),
		Sort:  stringFromArgs(args, "sort"),
		Order: stringFromArgs(args, "order"),
	}
	out, err := h.gh.ListCaches(ctx, owner, repo, opts)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return gomcp.NewToolResultText(out), nil
}

func (h *Handler) handleDeleteCache(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	cacheID := stringFromArgs(args, "cache_id")
	if cacheID == "" {
		return gomcp.NewToolResultError("cache_id is required"), nil
	}
	out, err := h.gh.DeleteCache(ctx, owner, repo, cacheID)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return gomcp.NewToolResultText(out), nil
}
