package client

import (
	"context"
	"fmt"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// Tool is a discovered MCP tool with its schema.
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
}

// ContentBlock is a single element in a tool result.
type ContentBlock struct {
	Type string
	Text string
}

// ToolResult holds the output of a tool call.
type ToolResult struct {
	Content []ContentBlock
	IsError bool
}

// Client can discover and call tools on the MCP broker.
type Client interface {
	ListTools(ctx context.Context) ([]Tool, error)
	CallTool(ctx context.Context, name string, args map[string]any) (*ToolResult, error)
	Close() error
}

type mcpClientImpl struct {
	c *mcpclient.Client
}

// New connects to the broker at endpoint (e.g. "http://localhost:8200/mcp")
// and authenticates with token.
func New(ctx context.Context, endpoint, token string) (Client, error) {
	opts := []transport.StreamableHTTPCOption{
		transport.WithHTTPHeaders(map[string]string{
			"Authorization": "Bearer " + token,
		}),
	}

	c, err := mcpclient.NewStreamableHttpClient(endpoint, opts...)
	if err != nil {
		return nil, fmt.Errorf("create MCP client: %w", err)
	}

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "broker-cli",
		Version: "0.1.0",
	}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("initialize MCP client: %w", err)
	}

	return &mcpClientImpl{c: c}, nil
}

func (m *mcpClientImpl) ListTools(ctx context.Context) ([]Tool, error) {
	resp, err := m.c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}

	tools := make([]Tool, len(resp.Tools))
	for i, t := range resp.Tools {
		schema := make(map[string]any)
		if t.InputSchema.Properties != nil {
			schema["type"] = t.InputSchema.Type
			schema["properties"] = t.InputSchema.Properties
		}
		if t.InputSchema.Required != nil {
			req := make([]any, len(t.InputSchema.Required))
			for i, r := range t.InputSchema.Required {
				req[i] = r
			}
			schema["required"] = req
		}
		tools[i] = Tool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		}
	}
	return tools, nil
}

func (m *mcpClientImpl) CallTool(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args

	resp, err := m.c.CallTool(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("call tool %q: %w", name, err)
	}

	result := &ToolResult{IsError: resp.IsError}
	for _, block := range resp.Content {
		switch v := block.(type) {
		case mcp.TextContent:
			result.Content = append(result.Content, ContentBlock{Type: "text", Text: v.Text})
		}
	}
	return result, nil
}

func (m *mcpClientImpl) Close() error {
	return m.c.Close()
}
