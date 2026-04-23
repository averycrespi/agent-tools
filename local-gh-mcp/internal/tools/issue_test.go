package tools

import (
	"context"
	"testing"

	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/gh"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestViewIssue_Success(t *testing.T) {
	h := NewHandler(&mockGHClient{
		viewIssueFunc: func(_ context.Context, owner, repo string, number int) (string, error) {
			assert.Equal(t, "octocat", owner)
			assert.Equal(t, "hello-world", repo)
			assert.Equal(t, 7, number)
			return `{"number":7,"title":"Bug report"}`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_view_issue"
	req.Params.Arguments = map[string]any{
		"owner":        "octocat",
		"repo":         "hello-world",
		"issue_number": float64(7),
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

func TestViewIssue_MissingNumber(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_view_issue"
	req.Params.Arguments = map[string]any{
		"owner": "octocat",
		"repo":  "hello-world",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestListIssues_Success(t *testing.T) {
	h := NewHandler(&mockGHClient{
		listIssuesFunc: func(_ context.Context, owner, repo string, opts gh.ListIssuesOpts) (string, error) {
			assert.Equal(t, "octocat", owner)
			assert.Equal(t, "hello-world", repo)
			assert.Equal(t, "open", opts.State)
			return `[{"number":1}]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_list_issues"
	req.Params.Arguments = map[string]any{
		"owner": "octocat",
		"repo":  "hello-world",
		"state": "open",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

func TestListIssues_MissingOwner(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_list_issues"
	req.Params.Arguments = map[string]any{
		"repo": "hello-world",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestCommentIssue_Success(t *testing.T) {
	h := NewHandler(&mockGHClient{
		commentIssueFunc: func(_ context.Context, owner, repo string, number int, body string) (string, error) {
			assert.Equal(t, "octocat", owner)
			assert.Equal(t, "hello-world", repo)
			assert.Equal(t, 3, number)
			assert.Equal(t, "Nice find!", body)
			return "comment added", nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_comment_issue"
	req.Params.Arguments = map[string]any{
		"owner":        "octocat",
		"repo":         "hello-world",
		"issue_number": float64(3),
		"body":         "Nice find!",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

func TestCommentIssue_MissingBody(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_comment_issue"
	req.Params.Arguments = map[string]any{
		"owner":        "octocat",
		"repo":         "hello-world",
		"issue_number": float64(3),
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestViewIssue_FormatsMarkdown(t *testing.T) {
	h := NewHandler(&mockGHClient{
		viewIssueFunc: func(_ context.Context, owner, repo string, number int) (string, error) {
			return `{"number":100,"title":"Bug report","body":"Steps to reproduce","state":"OPEN","author":{"login":"alice"},"labels":[{"name":"bug"}],"milestone":null,"createdAt":"2025-01-01T00:00:00Z","updatedAt":"2025-01-02T00:00:00Z"}`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_view_issue"
	req.Params.Arguments = map[string]any{
		"owner":        "octocat",
		"repo":         "hello-world",
		"issue_number": float64(100),
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "# Issue #100: Bug report (OPEN)")
	assert.Contains(t, text, "@alice")
	assert.Contains(t, text, "bug")
	assert.Contains(t, text, "Steps to reproduce")
}

func TestListIssues_FormatsMarkdown(t *testing.T) {
	h := NewHandler(&mockGHClient{
		listIssuesFunc: func(_ context.Context, owner, repo string, opts gh.ListIssuesOpts) (string, error) {
			return `[{"number":10,"title":"Bug report","state":"OPEN","author":{"login":"alice"},"labels":[{"name":"bug"}],"updatedAt":"2025-01-02T00:00:00Z"},{"number":11,"title":"Feature request","state":"CLOSED","author":{"login":"bob"},"labels":[],"updatedAt":"2025-01-03T00:00:00Z"}]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_list_issues"
	req.Params.Arguments = map[string]any{
		"owner": "octocat",
		"repo":  "hello-world",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "**#10** Bug report")
	assert.Contains(t, text, "@alice")
	assert.Contains(t, text, "bug")
	assert.Contains(t, text, "**#11** Feature request")
	assert.Contains(t, text, "CLOSED")
}

func TestIssueToolAnnotations(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	tools := h.issueTools()
	byName := make(map[string]gomcp.Tool, len(tools))
	for _, tool := range tools {
		byName[tool.Name] = tool
	}

	cases := []struct {
		name     string
		expected gomcp.ToolAnnotation
	}{
		{"gh_view_issue", annRead},
		{"gh_list_issues", annRead},
		{"gh_comment_issue", annAdditive},
		{"gh_list_issue_comments", annRead},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool, ok := byName[tc.name]
			require.True(t, ok, "tool %s not registered", tc.name)
			assert.Equal(t, tc.expected, tool.Annotations)
		})
	}
}

func TestListIssueComments_Success(t *testing.T) {
	h := NewHandler(&mockGHClient{
		issueCommentsFunc: func(_ context.Context, owner, repo string, number int, limit int) (string, error) {
			return `[{"author":{"login":"alice"},"authorAssociation":"NONE","body":"Thanks!","createdAt":"2025-01-01T00:00:00Z","isMinimized":false,"minimizedReason":""}]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_list_issue_comments"
	req.Params.Arguments = map[string]any{
		"owner":        "octocat",
		"repo":         "hello-world",
		"issue_number": float64(100),
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "## Comments (1)")
	assert.Contains(t, text, "@alice")
	assert.Contains(t, text, "Thanks!")
}

func TestListIssues_StateEnum(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	for _, tool := range h.issueTools() {
		if tool.Name != "gh_list_issues" {
			continue
		}
		prop, ok := tool.InputSchema.Properties["state"].(map[string]any)
		require.True(t, ok, "state property missing or wrong shape")
		enum, ok := prop["enum"].([]string)
		require.True(t, ok, "state must declare an enum")
		assert.ElementsMatch(t, []string{"open", "closed", "all"}, enum)
		return
	}
	t.Fatal("gh_list_issues not found")
}
