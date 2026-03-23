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
			assert.Equal(t, "size", opts.Sort)
			return `[{"id":1}]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_list_caches"
	req.Params.Arguments = map[string]any{
		"owner": "octocat",
		"repo":  "hello-world",
		"sort":  "size",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
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
