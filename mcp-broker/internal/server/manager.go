package server

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)

// Tool represents a discovered MCP tool with its schema and metadata.
// OutputSchema, Annotations, and Meta are passed through verbatim from the
// upstream backend so clients see the tool exactly as the backend declared it.
type Tool struct {
	Name         string                `json:"name"`
	Description  string                `json:"description"`
	InputSchema  map[string]any        `json:"inputSchema,omitempty"`
	OutputSchema *mcp.ToolOutputSchema `json:"outputSchema,omitempty"`
	Annotations  *mcp.ToolAnnotation   `json:"annotations,omitempty"`
	Meta         *mcp.Meta             `json:"_meta,omitempty"`
}

// toBrokerTool converts an upstream mcp.Tool into the broker's Tool form,
// preserving annotations, output schema, and _meta verbatim.
func toBrokerTool(t mcp.Tool) Tool {
	schema := make(map[string]any)
	if t.InputSchema.Properties != nil {
		schema["type"] = t.InputSchema.Type
		schema["properties"] = t.InputSchema.Properties
	}
	if t.InputSchema.Required != nil {
		schema["required"] = t.InputSchema.Required
	}

	out := Tool{
		Name:        t.Name,
		Description: t.Description,
		InputSchema: schema,
		Meta:        t.Meta,
	}
	if t.OutputSchema.Type != "" {
		os := t.OutputSchema
		out.OutputSchema = &os
	}
	// Upstream mcp.Tool always carries an annotations value (zero or
	// otherwise); only emit the pointer when at least one field is set so
	// we don't add `"annotations": {}` for tools that declared none.
	if a := t.Annotations; a.Title != "" || a.ReadOnlyHint != nil || a.DestructiveHint != nil || a.IdempotentHint != nil || a.OpenWorldHint != nil {
		out.Annotations = &a
	}
	return out
}

// ToolResult holds the result of a tool call.
type ToolResult struct {
	Content any
	IsError bool
}

// Backend is the interface for communicating with an MCP server.
// Implementations handle stdio and HTTP transports.
type Backend interface {
	ListTools(ctx context.Context) ([]Tool, error)
	CallTool(ctx context.Context, name string, arguments map[string]any) (*ToolResult, error)
	Close() error
}

// toolEntry maps a prefixed tool name to its backend and original name.
type toolEntry struct {
	backend      Backend
	originalName string
	tool         Tool
}

// Manager manages connections to backend MCP servers.
type Manager struct {
	backends map[string]Backend
	tools    map[string]toolEntry
	logger   *slog.Logger
}

// NewManager creates a Manager and connects to all configured backends.
func NewManager(ctx context.Context, servers map[string]config.ServerConfig, logger *slog.Logger) (*Manager, error) {
	m := &Manager{
		backends: make(map[string]Backend),
		tools:    make(map[string]toolEntry),
		logger:   logger,
	}

	for name, srv := range servers {
		backend, err := connect(ctx, name, srv, logger)
		if err != nil {
			logger.Error("failed to connect to backend", "name", name, "error", err)
			continue
		}
		m.backends[name] = backend
		logger.Info("connected to backend", "name", name)
	}

	if err := m.discover(ctx); err != nil {
		return nil, fmt.Errorf("discovering tools: %w", err)
	}

	return m, nil
}

// connect creates a Backend for the given server config.
func connect(ctx context.Context, name string, srv config.ServerConfig, logger *slog.Logger) (Backend, error) {
	switch srv.Type {
	case "streamable-http":
		return newHTTPBackend(ctx, name, srv)
	case "sse":
		return newSSEBackend(ctx, name, srv)
	default:
		return newStdioBackend(ctx, name, srv, logger)
	}
}

// discover calls tools/list on each backend and builds the prefixed tool registry.
func (m *Manager) discover(ctx context.Context) error {
	for name, backend := range m.backends {
		tools, err := backend.ListTools(ctx)
		if err != nil {
			if m.logger != nil {
				m.logger.Error("failed to list tools", "backend", name, "error", err)
			}
			continue
		}
		for _, tool := range tools {
			prefixed := name + "." + tool.Name
			m.tools[prefixed] = toolEntry{
				backend:      backend,
				originalName: tool.Name,
				tool: Tool{
					Name:         prefixed,
					Description:  tool.Description,
					InputSchema:  tool.InputSchema,
					OutputSchema: tool.OutputSchema,
					Annotations:  tool.Annotations,
					Meta:         tool.Meta,
				},
			}
		}
		if m.logger != nil {
			m.logger.Info("discovered tools", "backend", name, "count", len(tools))
		}
	}
	return nil
}

// Tools returns all discovered tools across all backends.
func (m *Manager) Tools() []Tool {
	tools := make([]Tool, 0, len(m.tools))
	for _, entry := range m.tools {
		tools = append(tools, entry.tool)
	}
	return tools
}

// ToolDescription returns the description for a named tool, or "" if not found.
func (m *Manager) ToolDescription(name string) string {
	if entry, ok := m.tools[name]; ok {
		return entry.tool.Description
	}
	return ""
}

// Call proxies a tool call to the appropriate backend.
func (m *Manager) Call(ctx context.Context, tool string, args map[string]any) (*ToolResult, error) {
	entry, ok := m.tools[tool]
	if !ok {
		return nil, fmt.Errorf("unknown tool: %s", tool)
	}
	return entry.backend.CallTool(ctx, entry.originalName, args)
}

// Close shuts down all backend connections.
func (m *Manager) Close() error {
	var errs []error
	for name, backend := range m.backends {
		if err := backend.Close(); err != nil {
			errs = append(errs, fmt.Errorf("closing %s: %w", name, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("errors closing backends: %v", errs)
	}
	return nil
}

// expandEnv substitutes $VAR and ${VAR} references in values from the process environment.
func expandEnv(env map[string]string) map[string]string {
	if env == nil {
		return nil
	}
	result := make(map[string]string, len(env))
	for k, v := range env {
		result[k] = os.ExpandEnv(v)
	}
	return result
}
