package tools

import (
	"context"
	"encoding/json"

	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/format"
	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/gh"
	gomcp "github.com/mark3labs/mcp-go/mcp"
)

func (h *Handler) cacheTools() []gomcp.Tool {
	return []gomcp.Tool{
		{
			Name:        "gh_list_caches",
			Description: "List GitHub Actions caches for a repository as markdown bullets (id, key, size, ref, created/accessed dates). Sort by created_at, last_accessed_at, or size_in_bytes. Useful for finding stale or oversized caches before deletion.",
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
					"limit": map[string]any{
						"type":        "number",
						"default":     30,
						"description": "Max results (default 30, max 100).",
					},
					"sort": map[string]any{
						"type":        "string",
						"enum":        []string{"created_at", "last_accessed_at", "size_in_bytes"},
						"description": "Sort key.",
					},
					"order": map[string]any{
						"type":        "string",
						"enum":        []string{"asc", "desc"},
						"description": "Sort order.",
					},
				},
				Required: []string{"owner", "repo"},
			},
		},
		{
			Name:        "gh_delete_cache",
			Description: "Delete a GitHub Actions cache by its numeric ID (obtained from gh_list_caches). Irreversible — the cache must be rebuilt from the next run.",
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
	var caches []format.Cache
	if err := json.Unmarshal([]byte(out), &caches); err != nil {
		return parseError("gh_list_caches", err, out), nil
	}
	return gomcp.NewToolResultText(format.FormatCaches(caches)), nil
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
