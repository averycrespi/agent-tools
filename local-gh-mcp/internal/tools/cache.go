package tools

import (
	"context"

	gomcp "github.com/mark3labs/mcp-go/mcp"
)

func (h *Handler) cacheTools() []gomcp.Tool {
	return nil
}

func (h *Handler) handleListCaches(_ context.Context, _ gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	return gomcp.NewToolResultError("not implemented"), nil
}

func (h *Handler) handleDeleteCache(_ context.Context, _ gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	return gomcp.NewToolResultError("not implemented"), nil
}
