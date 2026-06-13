package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

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
	Content           any
	StructuredContent any
	IsError           bool
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

// BackendStatus describes startup health for one configured backend.
type BackendStatus struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	Phase     string `json:"phase,omitempty"`
	Attempts  int    `json:"attempts"`
	Error     string `json:"error,omitempty"`
	ToolCount int    `json:"tool_count"`
}

// Manager manages connections to backend MCP servers.
type Manager struct {
	backends       map[string]Backend
	backendConfigs map[string]config.ServerConfig
	statuses       map[string]BackendStatus
	tools          map[string]toolEntry
	toolPatches    []config.ToolPatchConfig
	logger         *slog.Logger
}

var connectBackend = connectWithContexts

// NewManager creates a Manager and connects to all configured backends.
func NewManager(ctx context.Context, servers map[string]config.ServerConfig, toolPatches []config.ToolPatchConfig, logger *slog.Logger) (*Manager, error) {
	m := &Manager{
		backends:       make(map[string]Backend),
		backendConfigs: make(map[string]config.ServerConfig),
		statuses:       make(map[string]BackendStatus),
		tools:          make(map[string]toolEntry),
		toolPatches:    toolPatches,
		logger:         logger,
	}

	for name, srv := range servers {
		m.backendConfigs[name] = srv
		m.statuses[name] = BackendStatus{Name: name, Status: "failed"}
		backend, attempts, err := m.connectWithRetry(ctx, name, srv)
		if err != nil {
			if logger != nil {
				logger.Error("failed to connect to backend", "name", name, "attempts", attempts, "error", err)
			}
			m.statuses[name] = BackendStatus{Name: name, Status: "failed", Phase: "connect", Attempts: attempts, Error: summarizeStartupError(err)}
			continue
		}
		m.backends[name] = backend
		if logger != nil {
			logger.Info("connected to backend", "name", name)
		}
	}

	if err := m.discover(ctx); err != nil {
		return nil, fmt.Errorf("discovering tools: %w", err)
	}

	return m, nil
}

// connect creates a Backend for the given server config.
func connect(ctx context.Context, name string, srv config.ServerConfig, logger *slog.Logger) (Backend, error) {
	return connectWithContexts(ctx, ctx, name, srv, logger)
}

func connectWithContexts(lifetimeCtx context.Context, startupCtx context.Context, name string, srv config.ServerConfig, logger *slog.Logger) (Backend, error) {
	switch srv.Type {
	case "streamable-http":
		return newHTTPBackend(startupCtx, lifetimeCtx, name, srv)
	case "sse":
		return newSSEBackend(startupCtx, lifetimeCtx, name, srv)
	default:
		return newStdioBackend(startupCtx, name, srv, logger)
	}
}

// discover calls tools/list on each backend and builds the prefixed tool registry.
func (m *Manager) discover(ctx context.Context) error {
	for name, backend := range m.backends {
		srv := m.backendConfigs[name]
		tools, attempts, err := m.listToolsWithRetry(ctx, name, srv, backend)
		if err != nil {
			if m.logger != nil {
				m.logger.Error("failed to list tools", "backend", name, "attempts", attempts, "error", err)
			}
			if closeErr := backend.Close(); closeErr != nil && m.logger != nil {
				m.logger.Error("failed to close backend after discovery failure", "backend", name, "error", closeErr)
			}
			delete(m.backends, name)
			if m.statuses != nil {
				m.statuses[name] = BackendStatus{Name: name, Status: "failed", Phase: "list_tools", Attempts: attempts, Error: summarizeStartupError(err)}
			}
			continue
		}
		for _, tool := range tools {
			prefixed := name + "." + tool.Name
			tool.Name = prefixed
			patched, disabled := m.applyToolPatch(tool)
			if disabled {
				continue
			}
			m.tools[prefixed] = toolEntry{
				backend:      backend,
				originalName: tool.Name[len(name)+1:],
				tool:         patched,
			}
		}
		if m.statuses != nil {
			m.statuses[name] = BackendStatus{Name: name, Status: "connected", Attempts: attempts, ToolCount: len(tools)}
		}
		if m.logger != nil {
			m.logger.Info("discovered tools", "backend", name, "count", len(tools))
		}
	}
	return nil
}

func (m *Manager) applyToolPatch(tool Tool) (Tool, bool) {
	for _, patch := range m.toolPatches {
		matched, err := filepath.Match(patch.Tool, tool.Name)
		if err != nil || !matched {
			continue
		}
		if patch.Disabled {
			return tool, true
		}
		if patch.Annotations != nil {
			tool.Annotations = mergeAnnotations(tool.Annotations, patch.Annotations)
		}
		return tool, false
	}
	return tool, false
}

func mergeAnnotations(base *mcp.ToolAnnotation, patch *config.ToolAnnotationsPatch) *mcp.ToolAnnotation {
	if patch == nil {
		return base
	}
	merged := mcp.ToolAnnotation{}
	if base != nil {
		merged = *base
	}
	if patch.Title != nil {
		merged.Title = *patch.Title
	}
	if patch.ReadOnlyHint != nil {
		merged.ReadOnlyHint = patch.ReadOnlyHint
	}
	if patch.DestructiveHint != nil {
		merged.DestructiveHint = patch.DestructiveHint
	}
	if patch.IdempotentHint != nil {
		merged.IdempotentHint = patch.IdempotentHint
	}
	if patch.OpenWorldHint != nil {
		merged.OpenWorldHint = patch.OpenWorldHint
	}
	return &merged
}

func (m *Manager) connectWithRetry(ctx context.Context, name string, srv config.ServerConfig) (Backend, int, error) {
	var backend Backend
	attempts, err := retryStartup(ctx, srv, func(lifetimeCtx context.Context, attemptCtx context.Context) error {
		var err error
		backend, err = connectBackend(lifetimeCtx, attemptCtx, name, srv, m.logger)
		return err
	})
	if err != nil {
		return nil, attempts, err
	}
	return backend, attempts, nil
}

func (m *Manager) listToolsWithRetry(ctx context.Context, name string, srv config.ServerConfig, backend Backend) ([]Tool, int, error) {
	var tools []Tool
	attempts, err := retryStartup(ctx, srv, func(_ context.Context, attemptCtx context.Context) error {
		var err error
		tools, err = backend.ListTools(attemptCtx)
		return err
	})
	if err != nil {
		return nil, attempts, err
	}
	return tools, attempts, nil
}

func retryStartup(ctx context.Context, srv config.ServerConfig, op func(context.Context, context.Context) error) (int, error) {
	policy := startupRetryPolicy(srv)
	var lastErr error
	for attempt := 1; attempt <= policy.maxAttempts; attempt++ {
		attemptCtx := ctx
		var cancel context.CancelFunc
		if policy.timeout > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, policy.timeout)
		}
		err := op(ctx, attemptCtx)
		if cancel != nil {
			cancel()
		}
		if err == nil {
			return attempt, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return attempt, ctx.Err()
		}
		if attempt == policy.maxAttempts || isNonRetryableStartupError(err) {
			return attempt, err
		}
		if policy.backoff > 0 {
			timer := time.NewTimer(policy.backoff)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return attempt, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return policy.maxAttempts, lastErr
}

type retryPolicy struct {
	maxAttempts int
	backoff     time.Duration
	timeout     time.Duration
}

func startupRetryPolicy(srv config.ServerConfig) retryPolicy {
	retries := 3
	if srv.StartupRetryCount != nil {
		retries = min(*srv.StartupRetryCount, config.MaxStartupRetryCount)
	}
	backoffMS := 1000
	if srv.StartupRetryBackoffMS != nil {
		backoffMS = *srv.StartupRetryBackoffMS
	}
	timeoutSeconds := 10
	if srv.StartupTimeoutSeconds != nil {
		timeoutSeconds = *srv.StartupTimeoutSeconds
	}
	return retryPolicy{
		maxAttempts: retries + 1,
		backoff:     time.Duration(backoffMS) * time.Millisecond,
		timeout:     time.Duration(timeoutSeconds) * time.Second,
	}
}

func isNonRetryableStartupError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "oauth flow") ||
		strings.Contains(msg, "authorization denied") ||
		strings.Contains(msg, "authorization required") ||
		strings.Contains(msg, "oauth callback") ||
		strings.Contains(msg, "oauth flow cancelled")
}

func summarizeStartupError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "startup attempt timed out"
	}
	if errors.Is(err, context.Canceled) {
		return "startup canceled"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "connection refused"):
		return "connection refused"
	case strings.Contains(msg, "no such file") || strings.Contains(msg, "executable file not found"):
		return "backend command unavailable"
	case strings.Contains(msg, "oauth"):
		return "OAuth flow failed"
	case strings.Contains(msg, "authorization"):
		return "authorization required"
	default:
		return "backend startup failed; see broker logs"
	}
}

// BackendStatuses returns startup status for every configured backend, sorted by name.
func (m *Manager) BackendStatuses() []BackendStatus {
	statuses := make([]BackendStatus, 0, len(m.statuses))
	for _, status := range m.statuses {
		statuses = append(statuses, status)
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })
	return statuses
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
