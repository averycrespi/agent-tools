package tools

import (
	"context"
	"testing"

	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/gh"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListCaches_Success(t *testing.T) {
	h := NewHandler(&mockGHClient{
		listCachesFunc: func(_ context.Context, owner, repo string, opts gh.ListCachesOpts) (string, error) {
			assert.Equal(t, "octocat", owner)
			assert.Equal(t, "hello-world", repo)
			assert.Equal(t, "size_in_bytes", opts.Sort)
			return `[{"id":42,"key":"npm-cache","ref":"refs/heads/main","sizeInBytes":1048576,"createdAt":"2026-04-01T10:00:00Z","lastAccessedAt":"2026-04-15T12:00:00Z"}]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_list_caches"
	req.Params.Arguments = map[string]any{
		"owner": "octocat",
		"repo":  "hello-world",
		"sort":  "size_in_bytes",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(gomcp.TextContent)
	require.True(t, ok)
	for _, want := range []string{"**42**", "`npm-cache`", "refs/heads/main", "1.0 MiB", "2026-04-01", "2026-04-15"} {
		assert.Contains(t, text.Text, want)
	}
	assert.NotContains(t, text.Text, "\t", "output must be markdown, not TSV")
}

func TestListCaches_Empty(t *testing.T) {
	h := NewHandler(&mockGHClient{
		listCachesFunc: func(_ context.Context, _, _ string, _ gh.ListCachesOpts) (string, error) {
			return `[]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_list_caches"
	req.Params.Arguments = map[string]any{
		"owner": "octocat",
		"repo":  "hello-world",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(gomcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "No caches found.", text.Text)
}

func TestListCaches_MissingOwner(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_list_caches"
	req.Params.Arguments = map[string]any{
		"repo": "hello-world",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestDeleteCache_Success(t *testing.T) {
	h := NewHandler(&mockGHClient{
		deleteCacheFunc: func(_ context.Context, owner, repo string, cacheID string) (string, error) {
			assert.Equal(t, "octocat", owner)
			assert.Equal(t, "hello-world", repo)
			assert.Equal(t, "abc123", cacheID)
			return "cache deleted", nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_delete_cache"
	req.Params.Arguments = map[string]any{
		"owner":    "octocat",
		"repo":     "hello-world",
		"cache_id": "abc123",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

func TestDeleteCache_MissingCacheID(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_delete_cache"
	req.Params.Arguments = map[string]any{
		"owner": "octocat",
		"repo":  "hello-world",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestListCaches_SortOrderEnums(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	for _, tool := range h.cacheTools() {
		if tool.Name != "gh_list_caches" {
			continue
		}
		sortProp := tool.InputSchema.Properties["sort"].(map[string]any)
		sortEnum, ok := sortProp["enum"].([]string)
		require.True(t, ok, "sort must declare an enum")
		assert.ElementsMatch(t, []string{"created_at", "last_accessed_at", "size_in_bytes"}, sortEnum)

		orderProp := tool.InputSchema.Properties["order"].(map[string]any)
		orderEnum, ok := orderProp["enum"].([]string)
		require.True(t, ok, "order must declare an enum")
		assert.ElementsMatch(t, []string{"asc", "desc"}, orderEnum)
		return
	}
	t.Fatal("gh_list_caches not found")
}

func TestCacheToolAnnotations(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	tools := h.cacheTools()
	byName := make(map[string]gomcp.Tool, len(tools))
	for _, tool := range tools {
		byName[tool.Name] = tool
	}

	cases := []struct {
		name     string
		expected gomcp.ToolAnnotation
	}{
		{"gh_list_caches", annRead},
		{"gh_delete_cache", annDestructive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool, ok := byName[tc.name]
			require.True(t, ok, "tool %s not registered", tc.name)
			assert.Equal(t, tc.expected, tool.Annotations)
		})
	}
}
