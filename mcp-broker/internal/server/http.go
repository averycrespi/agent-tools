package server

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)

// httpBackend communicates with an MCP server via Streamable HTTP or SSE.
type httpBackend struct {
	client *client.Client
}

func newHTTPBackend(ctx context.Context, name string, srv config.ServerConfig) (*httpBackend, error) {
	oauthCfg := oauthConfig(name)

	var opts []transport.StreamableHTTPCOption
	if headers := expandEnv(srv.Headers); len(headers) > 0 {
		opts = append(opts, transport.WithHTTPHeaders(headers))
	}

	c, err := client.NewOAuthStreamableHttpClient(srv.URL, oauthCfg, opts...)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client for %q: %w", name, err)
	}

	if err := initializeOAuthClient(ctx, c, name); err != nil {
		return nil, err
	}

	return &httpBackend{client: c}, nil
}

func newSSEBackend(ctx context.Context, name string, srv config.ServerConfig) (*httpBackend, error) {
	oauthCfg := oauthConfig(name)

	var opts []transport.ClientOption
	if headers := expandEnv(srv.Headers); len(headers) > 0 {
		opts = append(opts, transport.WithHeaders(headers))
	}

	c, err := client.NewOAuthSSEClient(srv.URL, oauthCfg, opts...)
	if err != nil {
		return nil, fmt.Errorf("create SSE client for %q: %w", name, err)
	}

	if err := c.Start(ctx); err != nil {
		if !client.IsOAuthAuthorizationRequiredError(err) {
			_ = c.Close()
			return nil, fmt.Errorf("start SSE client for %q: %w", name, err)
		}
		if err := runOAuthFlow(ctx, err, callbackPort(name)); err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("OAuth flow for %q: %w", name, err)
		}
		if err := c.Start(ctx); err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("start SSE client for %q after auth: %w", name, err)
		}
	}

	if err := initializeOAuthClient(ctx, c, name); err != nil {
		return nil, err
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
