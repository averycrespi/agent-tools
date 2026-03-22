package tools

import (
	"context"

	gomcp "github.com/mark3labs/mcp-go/mcp"
)

func (h *Handler) searchTools() []gomcp.Tool {
	return nil
}

func (h *Handler) handleSearchPRs(_ context.Context, _ gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	return gomcp.NewToolResultError("not implemented"), nil
}

func (h *Handler) handleSearchIssues(_ context.Context, _ gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	return gomcp.NewToolResultError("not implemented"), nil
}

func (h *Handler) handleSearchRepos(_ context.Context, _ gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	return gomcp.NewToolResultError("not implemented"), nil
}

func (h *Handler) handleSearchCode(_ context.Context, _ gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	return gomcp.NewToolResultError("not implemented"), nil
}

func (h *Handler) handleSearchCommits(_ context.Context, _ gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	return gomcp.NewToolResultError("not implemented"), nil
}
