package tools

import (
	"context"

	gomcp "github.com/mark3labs/mcp-go/mcp"
)

func (h *Handler) issueTools() []gomcp.Tool {
	return nil
}

func (h *Handler) handleViewIssue(_ context.Context, _ gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	return gomcp.NewToolResultError("not implemented"), nil
}

func (h *Handler) handleListIssues(_ context.Context, _ gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	return gomcp.NewToolResultError("not implemented"), nil
}

func (h *Handler) handleCommentIssue(_ context.Context, _ gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	return gomcp.NewToolResultError("not implemented"), nil
}
