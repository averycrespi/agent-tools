package tools

import (
	"context"
	"testing"

	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/gh"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSearchPRs_Success(t *testing.T) {
	h := NewHandler(&mockGHClient{
		searchPRsFunc: func(_ context.Context, query string, opts gh.SearchPRsOpts) (string, error) {
			assert.Equal(t, "fix bug", query)
			assert.Equal(t, "octocat", opts.Owner)
			return `[{"number":1}]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_search_prs"
	req.Params.Arguments = map[string]any{
		"query": "fix bug",
		"owner": "octocat",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

func TestSearchPRs_MissingQuery(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_search_prs"
	req.Params.Arguments = map[string]any{
		"owner": "octocat",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestSearchIssues_Success(t *testing.T) {
	h := NewHandler(&mockGHClient{
		searchIssuesFunc: func(_ context.Context, query string, opts gh.SearchIssuesOpts) (string, error) {
			assert.Equal(t, "memory leak", query)
			assert.Equal(t, "open", opts.State)
			return `[{"number":5}]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_search_issues"
	req.Params.Arguments = map[string]any{
		"query": "memory leak",
		"state": "open",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

func TestSearchRepos_Success(t *testing.T) {
	h := NewHandler(&mockGHClient{
		searchReposFunc: func(_ context.Context, query string, opts gh.SearchReposOpts) (string, error) {
			assert.Equal(t, "kubernetes", query)
			assert.Equal(t, "go", opts.Language)
			return `[{"fullName":"k8s/k8s"}]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_search_repos"
	req.Params.Arguments = map[string]any{
		"query":    "kubernetes",
		"language": "go",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

func TestSearchCode_Success(t *testing.T) {
	h := NewHandler(&mockGHClient{
		searchCodeFunc: func(_ context.Context, query string, opts gh.SearchCodeOpts) (string, error) {
			assert.Equal(t, "func main", query)
			assert.Equal(t, "go", opts.Language)
			assert.Equal(t, "main.go", opts.Filename)
			return `[{"path":"main.go"}]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_search_code"
	req.Params.Arguments = map[string]any{
		"query":    "func main",
		"language": "go",
		"filename": "main.go",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

func TestSearchCommits_Success(t *testing.T) {
	h := NewHandler(&mockGHClient{
		searchCommitsFunc: func(_ context.Context, query string, opts gh.SearchCommitsOpts) (string, error) {
			assert.Equal(t, "initial commit", query)
			assert.Equal(t, "octocat", opts.Author)
			return `[{"sha":"abc123"}]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_search_commits"
	req.Params.Arguments = map[string]any{
		"query":  "initial commit",
		"author": "octocat",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

func TestToolCount(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	tools := h.Tools()
	assert.Equal(t, 24, len(tools), "expected 24 total tools")
}
