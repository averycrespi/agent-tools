package server

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)

// httpBackend communicates with an MCP server via Streamable HTTP.
type httpBackend struct {
	client *client.Client
}

func newHTTPBackend(ctx context.Context, srv config.ServerConfig) (*httpBackend, error) {
	var opts []transport.StreamableHTTPCOption
	if headers := expandEnv(srv.Headers); len(headers) > 0 {
		opts = append(opts, transport.WithHTTPHeaders(headers))
	}

	c, err := client.NewStreamableHttpClient(srv.URL, opts...)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client for %q: %w", srv.Name, err)
	}

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "mcp-broker",
		Version: "0.1.0",
	}

	if _, err := c.Initialize(ctx, initReq); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("initialize HTTP server %q: %w", srv.Name, err)
	}

	return &httpBackend{client: c}, nil
}

func (b *httpBackend) ListTools(ctx context.Context) ([]Tool, error) {
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

func (b *httpBackend) CallTool(ctx context.Context, name string, arguments map[string]any) (*ToolResult, error) {
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

func (b *httpBackend) Close() error {
	return b.client.Close()
}
