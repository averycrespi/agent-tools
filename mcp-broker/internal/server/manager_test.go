package server

import (
	"context"
	"testing"

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
