package tools

import (
	"context"

	gomcp "github.com/mark3labs/mcp-go/mcp"
)

func (h *Handler) runTools() []gomcp.Tool {
	return nil
}

func (h *Handler) handleListRuns(_ context.Context, _ gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	return gomcp.NewToolResultError("not implemented"), nil
}

func (h *Handler) handleViewRun(_ context.Context, _ gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	return gomcp.NewToolResultError("not implemented"), nil
}

func (h *Handler) handleRerun(_ context.Context, _ gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	return gomcp.NewToolResultError("not implemented"), nil
}

func (h *Handler) handleCancelRun(_ context.Context, _ gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	return gomcp.NewToolResultError("not implemented"), nil
}
