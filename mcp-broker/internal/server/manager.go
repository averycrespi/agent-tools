package server

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)

// Tool represents a discovered MCP tool with its schema.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema,omitempty"`
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
func NewManager(ctx context.Context, servers []config.ServerConfig, logger *slog.Logger) (*Manager, error) {
	m := &Manager{
		backends: make(map[string]Backend),
		tools:    make(map[string]toolEntry),
		logger:   logger,
	}

	for _, srv := range servers {
		backend, err := connect(ctx, srv, logger)
		if err != nil {
			// Log and skip failed backends rather than failing entirely
			logger.Error("failed to connect to backend", "name", srv.Name, "error", err)
			continue
		}
		m.backends[srv.Name] = backend
		logger.Info("connected to backend", "name", srv.Name)
	}

	if err := m.discover(ctx); err != nil {
		return nil, fmt.Errorf("discovering tools: %w", err)
	}

	return m, nil
}

// connect creates a Backend for the given server config.
func connect(ctx context.Context, srv config.ServerConfig, logger *slog.Logger) (Backend, error) {
	switch srv.Type {
	case "http":
		return newHTTPBackend(ctx, srv)
	default:
		// stdio is the default
		return newStdioBackend(ctx, srv, logger)
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
					Name:        prefixed,
					Description: tool.Description,
					InputSchema: tool.InputSchema,
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

// expandEnv substitutes $VAR references in env values from the process environment.
func expandEnv(env map[string]string) map[string]string {
	if env == nil {
		return nil
	}
	result := make(map[string]string, len(env))
	for k, v := range env {
		if strings.HasPrefix(v, "$") {
			result[k] = os.Getenv(v[1:])
		} else {
			result[k] = v
		}
	}
	return result
}

// Stubs for backend implementations (replaced in Task 7)
func newStdioBackend(_ context.Context, _ config.ServerConfig, _ *slog.Logger) (Backend, error) {
	return nil, fmt.Errorf("stdio backend not yet implemented")
}

func newHTTPBackend(_ context.Context, _ config.ServerConfig) (Backend, error) {
	return nil, fmt.Errorf("http backend not yet implemented")
}
