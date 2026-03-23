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
		"owner":  "octocat",
		"repo":   "hello-world",
		"number": float64(7),
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
		"owner":  "octocat",
		"repo":   "hello-world",
		"number": float64(3),
		"body":   "Nice find!",
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
		"owner":  "octocat",
		"repo":   "hello-world",
		"number": float64(3),
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}
