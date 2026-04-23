package tools

import (
	"context"
	"encoding/json"

	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/format"
	gomcp "github.com/mark3labs/mcp-go/mcp"
)

func (h *Handler) releaseTools() []gomcp.Tool {
	return []gomcp.Tool{
		{
			Name:        "gh_list_releases",
			Description: "List releases in a repository, newest first. Shows tag, title, publish date, and draft/pre-release flags. Use gh_view_release for full notes and assets.",
			Annotations: annRead,
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"owner": map[string]any{"type": "string", "description": "Repository owner."},
					"repo":  map[string]any{"type": "string", "description": "Repository name."},
					"limit": map[string]any{"type": "number", "default": 30, "description": "Max releases shown (default 30, max 100)."},
				},
				Required: []string{"owner", "repo"},
			},
		},
		{
			Name:        "gh_view_release",
			Description: "Show a single release with notes and assets. Omit `tag` to get the latest release. Assets are listed by name and size; download URLs are not surfaced (signed URLs expire).",
			Annotations: annRead,
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"owner":           map[string]any{"type": "string", "description": "Repository owner."},
					"repo":            map[string]any{"type": "string", "description": "Repository name."},
					"tag":             map[string]any{"type": "string", "description": "Release tag (optional; omit for the latest release)."},
					"max_body_length": map[string]any{"type": "number", "default": 2000, "description": "Max release-notes body characters (default 2000)."},
				},
				Required: []string{"owner", "repo"},
			},
		},
	}
}

func (h *Handler) handleListReleases(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	limit := clampLimit(intFromArgsOr(args, "limit", defaultLimit))
	raw, err := h.gh.ListReleases(ctx, owner, repo, limit)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var releases []format.Release
	if err := json.Unmarshal([]byte(raw), &releases); err != nil {
		return parseError("gh_list_releases", err, raw), nil
	}
	return gomcp.NewToolResultText(format.FormatReleases(releases, limit)), nil
}

func (h *Handler) handleViewRelease(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	tag := stringFromArgs(args, "tag")
	maxBody := clampMaxBodyLength(intFromArgsOr(args, "max_body_length", defaultMaxBodyLength))
	raw, err := h.gh.ViewRelease(ctx, owner, repo, tag)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var rel format.Release
	if err := json.Unmarshal([]byte(raw), &rel); err != nil {
		return parseError("gh_view_release", err, raw), nil
	}
	return gomcp.NewToolResultText(format.FormatRelease(rel, maxBody)), nil
}
