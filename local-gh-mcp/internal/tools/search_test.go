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

func TestSearchPRs_RendersBodyExcerpt(t *testing.T) {
	h := NewHandler(&mockGHClient{
		searchPRsFunc: func(_ context.Context, query string, opts gh.SearchPRsOpts) (string, error) {
			return `[{"number":1,"title":"Fix bug","state":"OPEN","author":{"login":"alice"},"repository":{"nameWithOwner":"octocat/hello-world"},"body":"First line.\n\nSecond line.","updatedAt":"2025-01-02T00:00:00Z"}]`, nil
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
	assert.Contains(t, text, "\n  > First line. Second line.")
}

func TestSearchPRs_BodyExcerptClampedToMax(t *testing.T) {
	body := ""
	for i := 0; i < 1000; i++ {
		body += "a"
	}
	h := NewHandler(&mockGHClient{
		searchPRsFunc: func(_ context.Context, query string, opts gh.SearchPRsOpts) (string, error) {
			return `[{"number":1,"title":"T","state":"OPEN","author":{"login":"a"},"repository":{"nameWithOwner":"o/r"},"body":"` + body + `","updatedAt":"2025-01-02T00:00:00Z"}]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_search_prs"
	req.Params.Arguments = map[string]any{
		"query":           "x",
		"max_body_length": 5000, // clamped down to 500
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "[truncated — showing 500 of 1000 bytes]")
}

func TestSearchPRs_MaxBodyLengthSchema(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	for _, tool := range h.searchTools() {
		if tool.Name != "gh_search_prs" {
			continue
		}
		prop, ok := tool.InputSchema.Properties["max_body_length"].(map[string]any)
		require.True(t, ok, "max_body_length property missing or wrong shape")
		assert.Equal(t, "number", prop["type"])
		assert.Equal(t, 200, prop["default"])
		return
	}
	t.Fatal("gh_search_prs not found")
}

func TestSearchIssues_RendersBodyExcerpt(t *testing.T) {
	h := NewHandler(&mockGHClient{
		searchIssuesFunc: func(_ context.Context, query string, opts gh.SearchIssuesOpts) (string, error) {
			return `[{"number":1,"title":"Bug","state":"OPEN","author":{"login":"alice"},"repository":{"nameWithOwner":"octocat/hello-world"},"body":"Steps:\n\n  1. Run","updatedAt":"2025-01-02T00:00:00Z"}]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_search_issues"
	req.Params.Arguments = map[string]any{
		"query": "bug",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "\n  > Steps: 1. Run")
}

func TestSearchIssues_MaxBodyLengthSchema(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	for _, tool := range h.searchTools() {
		if tool.Name != "gh_search_issues" {
			continue
		}
		prop, ok := tool.InputSchema.Properties["max_body_length"].(map[string]any)
		require.True(t, ok, "max_body_length property missing or wrong shape")
		assert.Equal(t, "number", prop["type"])
		assert.Equal(t, 200, prop["default"])
		return
	}
	t.Fatal("gh_search_issues not found")
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

// runConflictCase runs a search tool with the given args and asserts a
// conflict error is returned with the expected per-flag message. fnFlag is
// used for the error string; it must match the handler's flag-side name.
func runConflictCase(t *testing.T, h *Handler, toolName string, args map[string]any, flag string) {
	t.Helper()
	req := gomcp.CallToolRequest{}
	req.Params.Name = toolName
	req.Params.Arguments = args
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	require.True(t, result.IsError, "expected conflict error result")
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, flag+" set both via flag and query; pick one")
}

func TestSearchPRs_ConflictDetection(t *testing.T) {
	cases := []struct {
		name    string
		flag    string
		args    map[string]any
		qualKey string
	}{
		{"state vs state:", "state", map[string]any{"query": "state:open", "state": "open"}, "state"},
		{"state vs is:", "state", map[string]any{"query": "is:open", "state": "open"}, "state"},
		{"repo vs repo:", "repo", map[string]any{"query": "repo:o/r", "repo": "o/r"}, "repo"},
		{"owner vs owner:", "owner", map[string]any{"query": "owner:o", "owner": "o"}, "owner"},
		{"owner vs org:", "owner", map[string]any{"query": "org:o", "owner": "o"}, "owner"},
		{"author vs author:", "author", map[string]any{"query": "author:a", "author": "a"}, "author"},
		{"label vs label:", "label", map[string]any{"query": "label:bug", "label": "bug"}, "label"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHandler(&mockGHClient{})
			runConflictCase(t, h, "gh_search_prs", tc.args, tc.flag)
		})
	}
}

func TestSearchPRs_NoConflictBaseline(t *testing.T) {
	h := NewHandler(&mockGHClient{
		searchPRsFunc: func(_ context.Context, query string, opts gh.SearchPRsOpts) (string, error) {
			return `[]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_search_prs"
	req.Params.Arguments = map[string]any{
		"query": "review-requested:@me",
		"state": "open",
		"repo":  "o/r",
		"owner": "o",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

func TestSearchIssues_ConflictDetection(t *testing.T) {
	cases := []struct {
		name string
		flag string
		args map[string]any
	}{
		{"state vs state:", "state", map[string]any{"query": "state:open", "state": "open"}},
		{"state vs is:", "state", map[string]any{"query": "is:open", "state": "open"}},
		{"repo vs repo:", "repo", map[string]any{"query": "repo:o/r", "repo": "o/r"}},
		{"owner vs owner:", "owner", map[string]any{"query": "owner:o", "owner": "o"}},
		{"owner vs org:", "owner", map[string]any{"query": "org:o", "owner": "o"}},
		{"author vs author:", "author", map[string]any{"query": "author:a", "author": "a"}},
		{"label vs label:", "label", map[string]any{"query": "label:bug", "label": "bug"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHandler(&mockGHClient{})
			runConflictCase(t, h, "gh_search_issues", tc.args, tc.flag)
		})
	}
}

func TestSearchIssues_NoConflictBaseline(t *testing.T) {
	h := NewHandler(&mockGHClient{
		searchIssuesFunc: func(_ context.Context, query string, opts gh.SearchIssuesOpts) (string, error) {
			return `[]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_search_issues"
	req.Params.Arguments = map[string]any{
		"query": "memory leak",
		"state": "open",
		"label": "bug",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

func TestSearchRepos_ConflictDetection(t *testing.T) {
	cases := []struct {
		name string
		flag string
		args map[string]any
	}{
		{"owner vs owner:", "owner", map[string]any{"query": "owner:o", "owner": "o"}},
		{"owner vs org:", "owner", map[string]any{"query": "org:o", "owner": "o"}},
		{"owner vs user:", "owner", map[string]any{"query": "user:o", "owner": "o"}},
		{"language vs language:", "language", map[string]any{"query": "language:go", "language": "go"}},
		{"topic vs topic:", "topic", map[string]any{"query": "topic:cli", "topic": "cli"}},
		{"stars vs stars:", "stars", map[string]any{"query": "stars:>100", "stars": ">100"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHandler(&mockGHClient{})
			runConflictCase(t, h, "gh_search_repos", tc.args, tc.flag)
		})
	}
}

func TestSearchRepos_NoConflictBaseline(t *testing.T) {
	h := NewHandler(&mockGHClient{
		searchReposFunc: func(_ context.Context, query string, opts gh.SearchReposOpts) (string, error) {
			return `[]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_search_repos"
	req.Params.Arguments = map[string]any{
		"query":    "kubernetes",
		"owner":    "k8s",
		"language": "go",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

func TestSearchCode_ConflictDetection(t *testing.T) {
	cases := []struct {
		name string
		flag string
		args map[string]any
	}{
		{"repo vs repo:", "repo", map[string]any{"query": "repo:o/r foo", "repo": "o/r"}},
		{"owner vs owner:", "owner", map[string]any{"query": "owner:o foo", "owner": "o"}},
		{"owner vs org:", "owner", map[string]any{"query": "org:o foo", "owner": "o"}},
		{"language vs language:", "language", map[string]any{"query": "language:go foo", "language": "go"}},
		{"extension vs extension:", "extension", map[string]any{"query": "extension:go foo", "extension": "go"}},
		{"filename vs filename:", "filename", map[string]any{"query": "filename:main.go foo", "filename": "main.go"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHandler(&mockGHClient{})
			runConflictCase(t, h, "gh_search_code", tc.args, tc.flag)
		})
	}
}

func TestSearchCode_NoConflictBaseline(t *testing.T) {
	h := NewHandler(&mockGHClient{
		searchCodeFunc: func(_ context.Context, query string, opts gh.SearchCodeOpts) (string, error) {
			return `[]`, nil
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

func TestSearchCommits_ConflictDetection(t *testing.T) {
	cases := []struct {
		name string
		flag string
		args map[string]any
	}{
		{"repo vs repo:", "repo", map[string]any{"query": "repo:o/r fix", "repo": "o/r"}},
		{"owner vs owner:", "owner", map[string]any{"query": "owner:o fix", "owner": "o"}},
		{"owner vs org:", "owner", map[string]any{"query": "org:o fix", "owner": "o"}},
		{"author vs author:", "author", map[string]any{"query": "author:a fix", "author": "a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHandler(&mockGHClient{})
			runConflictCase(t, h, "gh_search_commits", tc.args, tc.flag)
		})
	}
}

func TestSearchCommits_NoConflictBaseline(t *testing.T) {
	h := NewHandler(&mockGHClient{
		searchCommitsFunc: func(_ context.Context, query string, opts gh.SearchCommitsOpts) (string, error) {
			return `[]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_search_commits"
	req.Params.Arguments = map[string]any{
		"query":  "initial",
		"author": "octocat",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

func TestToolCount(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	tools := h.Tools()
	assert.Equal(t, 37, len(tools), "expected 37 total tools")
}
