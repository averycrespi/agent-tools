package server

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)

// stdioBackend communicates with an MCP server via stdio.
type stdioBackend struct {
	client *client.Client
}

func newStdioBackend(ctx context.Context, srv config.ServerConfig, logger *slog.Logger) (*stdioBackend, error) {
	env := expandEnv(srv.Env)
	envSlice := os.Environ()
	for k, v := range env {
		envSlice = append(envSlice, k+"="+v)
	}

	c, err := client.NewStdioMCPClient(srv.Command, envSlice, srv.Args...)
	if err != nil {
		return nil, fmt.Errorf("spawn stdio server %q: %w", srv.Name, err)
	}

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "mcp-broker",
		Version: "0.1.0",
	}

	if _, err := c.Initialize(ctx, initReq); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("initialize stdio server %q: %w", srv.Name, err)
	}

	logger.Debug("stdio backend initialized", "name", srv.Name, "command", srv.Command)

	return &stdioBackend{client: c}, nil
}

func (b *stdioBackend) ListTools(ctx context.Context) ([]Tool, error) {
	req := mcp.ListToolsRequest{}
	resp, err := b.client.ListTools(ctx, req)
	if err != nil {
		return nil, err
	}

	tools := make([]Tool, len(resp.Tools))
	for i, t := range resp.Tools {
		schema := make(map[string]any)
		if t.InputSchema.Properties != nil {
			schema["type"] = t.InputSchema.Type
			schema["properties"] = t.InputSchema.Properties
		}
		if t.InputSchema.Required != nil {
			schema["required"] = t.InputSchema.Required
		}
		tools[i] = Tool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		}
	}
	return tools, nil
}

func (b *stdioBackend) CallTool(ctx context.Context, name string, arguments map[string]any) (*ToolResult, error) {
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = arguments

	resp, err := b.client.CallTool(ctx, req)
	if err != nil {
		return nil, err
	}

	return &ToolResult{
		Content: resp.Content,
		IsError: resp.IsError,
	}, nil
}

func (b *stdioBackend) Close() error {
	return b.client.Close()
}
