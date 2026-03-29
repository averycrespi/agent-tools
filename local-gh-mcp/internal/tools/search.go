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

func (h *Handler) searchTools() []gomcp.Tool {
	return []gomcp.Tool{
		{
			Name:        "gh_search_prs",
			Description: "Search for pull requests. Returns markdown bullet list.",
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Search query",
					},
					"repo": map[string]any{
						"type":        "string",
						"description": "Filter by repository (owner/repo)",
					},
					"owner": map[string]any{
						"type":        "string",
						"description": "Filter by owner",
					},
					"state": map[string]any{
						"type":        "string",
						"description": "Filter by state: open, closed, merged",
					},
					"author": map[string]any{
						"type":        "string",
						"description": "Filter by author",
					},
					"label": map[string]any{
						"type":        "string",
						"description": "Filter by label",
					},
					"limit": map[string]any{
						"type":        "number",
						"description": "Max results (default 30, max 100)",
					},
				},
				Required: []string{"query"},
			},
		},
		{
			Name:        "gh_search_issues",
			Description: "Search for issues. Returns markdown bullet list.",
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Search query",
					},
					"repo": map[string]any{
						"type":        "string",
						"description": "Filter by repository (owner/repo)",
					},
					"owner": map[string]any{
						"type":        "string",
						"description": "Filter by owner",
					},
					"state": map[string]any{
						"type":        "string",
						"description": "Filter by state: open, closed",
					},
					"author": map[string]any{
						"type":        "string",
						"description": "Filter by author",
					},
					"label": map[string]any{
						"type":        "string",
						"description": "Filter by label",
					},
					"limit": map[string]any{
						"type":        "number",
						"description": "Max results (default 30, max 100)",
					},
				},
				Required: []string{"query"},
			},
		},
		{
			Name:        "gh_search_repos",
			Description: "Search for repositories. Returns markdown bullet list.",
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Search query",
					},
					"owner": map[string]any{
						"type":        "string",
						"description": "Filter by owner",
					},
					"language": map[string]any{
						"type":        "string",
						"description": "Filter by language",
					},
					"topic": map[string]any{
						"type":        "string",
						"description": "Filter by topic",
					},
					"stars": map[string]any{
						"type":        "string",
						"description": "Filter by star count (e.g. >100)",
					},
					"limit": map[string]any{
						"type":        "number",
						"description": "Max results (default 30, max 100)",
					},
				},
				Required: []string{"query"},
			},
		},
		{
			Name:        "gh_search_code",
			Description: "Search for code. Returns markdown bullet list.",
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Search query",
					},
					"repo": map[string]any{
						"type":        "string",
						"description": "Filter by repository (owner/repo)",
					},
					"owner": map[string]any{
						"type":        "string",
						"description": "Filter by owner",
					},
					"language": map[string]any{
						"type":        "string",
						"description": "Filter by language",
					},
					"extension": map[string]any{
						"type":        "string",
						"description": "Filter by file extension",
					},
					"filename": map[string]any{
						"type":        "string",
						"description": "Filter by filename",
					},
					"limit": map[string]any{
						"type":        "number",
						"description": "Max results (default 30, max 100)",
					},
				},
				Required: []string{"query"},
			},
		},
		{
			Name:        "gh_search_commits",
			Description: "Search for commits. Returns markdown bullet list.",
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Search query",
					},
					"repo": map[string]any{
						"type":        "string",
						"description": "Filter by repository (owner/repo)",
					},
					"owner": map[string]any{
						"type":        "string",
						"description": "Filter by owner",
					},
					"author": map[string]any{
						"type":        "string",
						"description": "Filter by author",
					},
					"limit": map[string]any{
						"type":        "number",
						"description": "Max results (default 30, max 100)",
					},
				},
				Required: []string{"query"},
			},
		},
	}
}

func (h *Handler) handleSearchPRs(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	query := stringFromArgs(args, "query")
	if query == "" {
		return gomcp.NewToolResultError("query is required"), nil
	}
	opts := gh.SearchPRsOpts{
		Repo:   stringFromArgs(args, "repo"),
		Owner:  stringFromArgs(args, "owner"),
		State:  stringFromArgs(args, "state"),
		Author: stringFromArgs(args, "author"),
		Label:  stringFromArgs(args, "label"),
		Limit:  intFromArgs(args, "limit"),
	}
	out, err := h.gh.SearchPRs(ctx, query, opts)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var items []format.SearchPRItem
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		return gomcp.NewToolResultError(fmt.Sprintf("failed to parse search PRs JSON: %v", err)), nil
	}
	var lines []string
	for _, item := range items {
		lines = append(lines, format.FormatSearchPRItem(item))
	}
	if len(lines) == 0 {
		return gomcp.NewToolResultText("No pull requests found."), nil
	}
	return gomcp.NewToolResultText(strings.Join(lines, "\n")), nil
}

func (h *Handler) handleSearchIssues(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	query := stringFromArgs(args, "query")
	if query == "" {
		return gomcp.NewToolResultError("query is required"), nil
	}
	opts := gh.SearchIssuesOpts{
		Repo:   stringFromArgs(args, "repo"),
		Owner:  stringFromArgs(args, "owner"),
		State:  stringFromArgs(args, "state"),
		Author: stringFromArgs(args, "author"),
		Label:  stringFromArgs(args, "label"),
		Limit:  intFromArgs(args, "limit"),
	}
	out, err := h.gh.SearchIssues(ctx, query, opts)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var items []format.SearchPRItem
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		return gomcp.NewToolResultError(fmt.Sprintf("failed to parse search issues JSON: %v", err)), nil
	}
	var lines []string
	for _, item := range items {
		lines = append(lines, format.FormatSearchPRItem(item))
	}
	if len(lines) == 0 {
		return gomcp.NewToolResultText("No issues found."), nil
	}
	return gomcp.NewToolResultText(strings.Join(lines, "\n")), nil
}

func (h *Handler) handleSearchRepos(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	query := stringFromArgs(args, "query")
	if query == "" {
		return gomcp.NewToolResultError("query is required"), nil
	}
	opts := gh.SearchReposOpts{
		Owner:    stringFromArgs(args, "owner"),
		Language: stringFromArgs(args, "language"),
		Topic:    stringFromArgs(args, "topic"),
		Stars:    stringFromArgs(args, "stars"),
		Limit:    intFromArgs(args, "limit"),
	}
	out, err := h.gh.SearchRepos(ctx, query, opts)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var items []format.SearchRepoItem
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		return gomcp.NewToolResultError(fmt.Sprintf("failed to parse search repos JSON: %v", err)), nil
	}
	var lines []string
	for _, item := range items {
		lines = append(lines, format.FormatSearchRepoItem(item))
	}
	if len(lines) == 0 {
		return gomcp.NewToolResultText("No repositories found."), nil
	}
	return gomcp.NewToolResultText(strings.Join(lines, "\n")), nil
}

func (h *Handler) handleSearchCode(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	query := stringFromArgs(args, "query")
	if query == "" {
		return gomcp.NewToolResultError("query is required"), nil
	}
	opts := gh.SearchCodeOpts{
		Repo:      stringFromArgs(args, "repo"),
		Owner:     stringFromArgs(args, "owner"),
		Language:  stringFromArgs(args, "language"),
		Extension: stringFromArgs(args, "extension"),
		Filename:  stringFromArgs(args, "filename"),
		Limit:     intFromArgs(args, "limit"),
	}
	out, err := h.gh.SearchCode(ctx, query, opts)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var items []format.SearchCodeItem
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		return gomcp.NewToolResultError(fmt.Sprintf("failed to parse search code JSON: %v", err)), nil
	}
	var lines []string
	for _, item := range items {
		lines = append(lines, format.FormatSearchCodeItem(item))
	}
	if len(lines) == 0 {
		return gomcp.NewToolResultText("No code results found."), nil
	}
	return gomcp.NewToolResultText(strings.Join(lines, "\n")), nil
}

func (h *Handler) handleSearchCommits(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	query := stringFromArgs(args, "query")
	if query == "" {
		return gomcp.NewToolResultError("query is required"), nil
	}
	opts := gh.SearchCommitsOpts{
		Repo:   stringFromArgs(args, "repo"),
		Owner:  stringFromArgs(args, "owner"),
		Author: stringFromArgs(args, "author"),
		Limit:  intFromArgs(args, "limit"),
	}
	out, err := h.gh.SearchCommits(ctx, query, opts)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var items []format.SearchCommitItem
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		return gomcp.NewToolResultError(fmt.Sprintf("failed to parse search commits JSON: %v", err)), nil
	}
	var lines []string
	for _, item := range items {
		lines = append(lines, format.FormatSearchCommitItem(item))
	}
	if len(lines) == 0 {
		return gomcp.NewToolResultText("No commits found."), nil
	}
	return gomcp.NewToolResultText(strings.Join(lines, "\n")), nil
}
