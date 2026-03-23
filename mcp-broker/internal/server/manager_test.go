package server

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// mockBackend implements the Backend interface for testing.
type mockBackend struct {
	mock.Mock
}

func (m *mockBackend) ListTools(ctx context.Context) ([]Tool, error) {
	args := m.Called(ctx)
	return args.Get(0).([]Tool), args.Error(1)
}

func (m *mockBackend) CallTool(ctx context.Context, name string, arguments map[string]any) (*ToolResult, error) {
	args := m.Called(ctx, name, arguments)
	return args.Get(0).(*ToolResult), args.Error(1)
}

func (m *mockBackend) Close() error {
	args := m.Called()
	return args.Error(0)
}

func TestManager_DiscoverTools_PrefixesNames(t *testing.T) {
	mb := new(mockBackend)
	mb.On("ListTools", mock.Anything).Return([]Tool{
		{Name: "search", Description: "Search things"},
		{Name: "get_pr", Description: "Get a PR"},
	}, nil)
	mb.On("Close").Return(nil)

	m := &Manager{
		backends: map[string]Backend{"github": mb},
		tools:    make(map[string]toolEntry),
	}

	err := m.discover(context.Background())
	require.NoError(t, err)

	tools := m.Tools()
	require.Len(t, tools, 2)

	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name] = true
	}
	require.True(t, names["github.search"])
	require.True(t, names["github.get_pr"])
}

func TestManager_Call_ProxiesToCorrectBackend(t *testing.T) {
	mb := new(mockBackend)
	mb.On("ListTools", mock.Anything).Return([]Tool{
		{Name: "search", Description: "Search"},
	}, nil)
	mb.On("CallTool", mock.Anything, "search", map[string]any{"q": "test"}).
		Return(&ToolResult{Content: "found it"}, nil)

	m := &Manager{
		backends: map[string]Backend{"github": mb},
		tools:    make(map[string]toolEntry),
	}

	err := m.discover(context.Background())
	require.NoError(t, err)

	result, err := m.Call(context.Background(), "github.search", map[string]any{"q": "test"})
	require.NoError(t, err)
	require.Equal(t, "found it", result.Content)

	mb.AssertExpectations(t)
}

func TestManager_Call_UnknownToolReturnsError(t *testing.T) {
	m := &Manager{
		backends: map[string]Backend{},
		tools:    make(map[string]toolEntry),
	}

	_, err := m.Call(context.Background(), "nonexistent.tool", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown tool")
}

func TestConnect_UnknownTypeDefaultsToStdio(t *testing.T) {
	// connect() with empty type should attempt stdio (which will fail without a real command,
	// but the error message confirms the routing)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, err := connect(ctx, "test", config.ServerConfig{Type: "", Command: "/nonexistent"}, logger)
	require.Error(t, err)
	require.Contains(t, err.Error(), "spawn stdio server")
}

func TestConnect_StreamableHTTPType(t *testing.T) {
	// connect() with "streamable-http" should attempt HTTP (which will fail without a real server,
	// but the error confirms routing)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, err := connect(ctx, "test", config.ServerConfig{Type: "streamable-http", URL: "http://localhost:1/nonexistent"}, logger)
	require.Error(t, err)
	require.Contains(t, err.Error(), "initialize server")
}

func TestConnect_StreamableHTTPFailsGracefully(t *testing.T) {
	// When connecting to a non-existent HTTP server, connect should fail
	// with an error that includes the server name for debugging.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	_, err := connect(ctx, "broken", config.ServerConfig{
		Type: "streamable-http",
		URL:  "http://127.0.0.1:1/nonexistent",
	}, logger)
	require.Error(t, err)
	require.Contains(t, err.Error(), "broken")
}

func TestExpandEnv_SubstitutesVariables(t *testing.T) {
	t.Setenv("MY_TOKEN", "secret123")
	env := map[string]string{
		"TOKEN":  "$MY_TOKEN",
		"STATIC": "plainvalue",
	}
	result := expandEnv(env)
	require.Equal(t, "secret123", result["TOKEN"])
	require.Equal(t, "plainvalue", result["STATIC"])
}

func TestExpandEnv_EmbeddedVariables(t *testing.T) {
	t.Setenv("MY_TOKEN", "ghp_abc123")
	env := map[string]string{
		"AUTH": "Bearer $MY_TOKEN",
	}
	result := expandEnv(env)
	require.Equal(t, "Bearer ghp_abc123", result["AUTH"])
}

func TestExpandEnv_BraceSyntax(t *testing.T) {
	t.Setenv("MY_TOKEN", "secret123")
	env := map[string]string{
		"TOKEN": "${MY_TOKEN}",
	}
	result := expandEnv(env)
	require.Equal(t, "secret123", result["TOKEN"])
}
