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

func TestSearchPRs_FormatsMarkdown(t *testing.T) {
	h := NewHandler(&mockGHClient{
		searchPRsFunc: func(_ context.Context, query string, opts gh.SearchPRsOpts) (string, error) {
			return `[{"number":1,"title":"Fix bug","state":"OPEN","author":{"login":"alice"},"repository":{"nameWithOwner":"octocat/hello-world"},"updatedAt":"2025-01-02T00:00:00Z"}]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_search_prs"
	req.Params.Arguments = map[string]any{
		"query": "fix bug",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "**octocat/hello-world#1** Fix bug")
	assert.Contains(t, text, "@alice")
	assert.Contains(t, text, "OPEN")
}

func TestSearchRepos_FormatsMarkdown(t *testing.T) {
	h := NewHandler(&mockGHClient{
		searchReposFunc: func(_ context.Context, query string, opts gh.SearchReposOpts) (string, error) {
			return `[{"fullName":"kubernetes/kubernetes","description":"Container orchestration","url":"https://github.com/kubernetes/kubernetes","stargazersCount":100000,"language":"Go","updatedAt":"2025-01-02T00:00:00Z"}]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_search_repos"
	req.Params.Arguments = map[string]any{
		"query": "kubernetes",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "**kubernetes/kubernetes**")
	assert.Contains(t, text, "100000 stars")
	assert.Contains(t, text, "Go")
}

func TestSearchCode_FormatsMarkdown(t *testing.T) {
	h := NewHandler(&mockGHClient{
		searchCodeFunc: func(_ context.Context, query string, opts gh.SearchCodeOpts) (string, error) {
			return `[{"path":"main.go","repository":{"nameWithOwner":"octocat/hello-world"},"sha":"abc1234","textMatches":[{"fragment":"func main()"}],"url":"https://github.com/octocat/hello-world/blob/abc1234/main.go"}]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_search_code"
	req.Params.Arguments = map[string]any{
		"query": "func main",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "**octocat/hello-world**")
	assert.Contains(t, text, "main.go")
	assert.Contains(t, text, "func main()")
}

func TestSearchCommits_FormatsMarkdown(t *testing.T) {
	h := NewHandler(&mockGHClient{
		searchCommitsFunc: func(_ context.Context, query string, opts gh.SearchCommitsOpts) (string, error) {
			return `[{"sha":"abc1234567890","commit":{"message":"Initial commit\n\nWith details"},"author":{"login":"alice"},"repository":{"nameWithOwner":"octocat/hello-world"},"url":"https://github.com/octocat/hello-world/commit/abc1234"}]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_search_commits"
	req.Params.Arguments = map[string]any{
		"query": "initial commit",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "**octocat/hello-world**")
	assert.Contains(t, text, "abc1234")
	assert.Contains(t, text, "Initial commit")
	assert.Contains(t, text, "@alice")
}

func TestSearchToolAnnotations(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	tools := h.searchTools()
	byName := make(map[string]gomcp.Tool, len(tools))
	for _, tool := range tools {
		byName[tool.Name] = tool
	}

	cases := []struct {
		name     string
		expected gomcp.ToolAnnotation
	}{
		{"gh_search_prs", annRead},
		{"gh_search_issues", annRead},
		{"gh_search_repos", annRead},
		{"gh_search_code", annRead},
		{"gh_search_commits", annRead},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool, ok := byName[tc.name]
			require.True(t, ok, "tool %s not registered", tc.name)
			assert.Equal(t, tc.expected, tool.Annotations)
		})
	}
}

func TestSearchPRs_StateEnum(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	for _, tool := range h.searchTools() {
		if tool.Name != "gh_search_prs" {
			continue
		}
		prop, ok := tool.InputSchema.Properties["state"].(map[string]any)
		require.True(t, ok, "state property missing or wrong shape")
		enum, ok := prop["enum"].([]string)
		require.True(t, ok, "state must declare an enum")
		assert.ElementsMatch(t, []string{"open", "closed", "merged", "all"}, enum)
		return
	}
	t.Fatal("gh_search_prs not found")
}

func TestSearchIssues_StateEnum(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	for _, tool := range h.searchTools() {
		if tool.Name != "gh_search_issues" {
			continue
		}
		prop, ok := tool.InputSchema.Properties["state"].(map[string]any)
		require.True(t, ok, "state property missing or wrong shape")
		enum, ok := prop["enum"].([]string)
		require.True(t, ok, "state must declare an enum")
		assert.ElementsMatch(t, []string{"open", "closed", "all"}, enum)
		return
	}
	t.Fatal("gh_search_issues not found")
}

func TestToolCount(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	tools := h.Tools()
	assert.Equal(t, 33, len(tools), "expected 33 total tools")
}
