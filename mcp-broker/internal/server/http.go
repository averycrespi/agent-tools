package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)

// isUnauthorized returns true if the error indicates a 401 response,
// whether from a plain client (ErrUnauthorized) or an OAuth-aware client
// (OAuthAuthorizationRequiredError).
func isUnauthorized(err error) bool {
	return client.IsOAuthAuthorizationRequiredError(err) || errors.Is(err, transport.ErrUnauthorized)
}

// httpBackend communicates with an MCP server via Streamable HTTP or SSE.
type httpBackend struct {
	client *client.Client
}

func newHTTPBackend(ctx context.Context, name string, srv config.ServerConfig) (*httpBackend, error) {
	var opts []transport.StreamableHTTPCOption
	if headers := expandEnv(srv.Headers); len(headers) > 0 {
		opts = append(opts, transport.WithHTTPHeaders(headers))
	}

	// Try plain client first.
	c, err := client.NewStreamableHttpClient(srv.URL, opts...)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client for %q: %w", name, err)
	}

	if err := initializeClient(ctx, c, name); err == nil {
		return &httpBackend{client: c}, nil
	} else if !isUnauthorized(err) {
		return nil, err // initializeClient already closed c
	}

	// Server requires OAuth — reconnect with OAuth support.
	oauthCfg := oauthConfig(name)
	c, err = client.NewOAuthStreamableHttpClient(srv.URL, oauthCfg, opts...)
	if err != nil {
		return nil, fmt.Errorf("create OAuth HTTP client for %q: %w", name, err)
	}
	if err := initializeOAuthClient(ctx, c, name); err != nil {
		return nil, err
	}
	return &httpBackend{client: c}, nil
}

func newSSEBackend(ctx context.Context, name string, srv config.ServerConfig) (*httpBackend, error) {
	var opts []transport.ClientOption
	if headers := expandEnv(srv.Headers); len(headers) > 0 {
		opts = append(opts, transport.WithHeaders(headers))
	}

	// Try plain client first.
	c, err := client.NewSSEMCPClient(srv.URL, opts...)
	if err != nil {
		return nil, fmt.Errorf("create SSE client for %q: %w", name, err)
	}

	needsOAuth := false
	if err := c.Start(ctx); err != nil {
		_ = c.Close()
		if !isUnauthorized(err) {
			return nil, fmt.Errorf("start SSE client for %q: %w", name, err)
		}
		needsOAuth = true
	}
	if !needsOAuth {
		if err := initializeClient(ctx, c, name); err == nil {
			return &httpBackend{client: c}, nil
		} else if !isUnauthorized(err) {
			return nil, err
		}
		// initializeClient closes c on error
	}

	// Server requires OAuth — reconnect with OAuth support.
	oauthCfg := oauthConfig(name)
	c, err = client.NewOAuthSSEClient(srv.URL, oauthCfg, opts...)
	if err != nil {
		return nil, fmt.Errorf("create OAuth SSE client for %q: %w", name, err)
	}
	if err := c.Start(ctx); err != nil {
		if !isUnauthorized(err) {
			_ = c.Close()
			return nil, fmt.Errorf("start OAuth SSE client for %q: %w", name, err)
		}
		if err := runOAuthFlow(ctx, err, name); err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("OAuth flow for %q: %w", name, err)
		}
		if err := c.Start(ctx); err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("start OAuth SSE client for %q after auth: %w", name, err)
		}
	}
	if err := initializeOAuthClient(ctx, c, name); err != nil {
		return nil, err
	}
	return &httpBackend{client: c}, nil
}

// initializeClient sends the MCP Initialize handshake.
// On error, it closes the client before returning.
func initializeClient(ctx context.Context, c *client.Client, name string) error {
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "mcp-broker",
		Version: "0.1.0",
	}

	if _, err := c.Initialize(ctx, initReq); err != nil {
		_ = c.Close()
		return fmt.Errorf("initialize server %q: %w", name, err)
	}
	return nil
}

func (b *httpBackend) ListTools(ctx context.Context) ([]Tool, error) {
	req := mcp.ListToolsRequest{}
	resp, err := b.client.ListTools(ctx, req)
	if err != nil && isUnauthorized(err) {
		resp, err = b.client.ListTools(ctx, req) // single retry — see CallTool
	}
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
	if err != nil && isUnauthorized(err) {
		// Retry once: mcp-go's getValidToken re-runs refresh, which often
		// succeeds after a transient Atlassian refresh failure. If the
		// retry also fails, surface the error — user restarts the broker.
		resp, err = b.client.CallTool(ctx, req)
	}
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
