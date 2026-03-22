package tools

import (
	"context"
	"testing"

	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/gh"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListRuns_Success(t *testing.T) {
	h := NewHandler(&mockGHClient{
		listRunsFunc: func(_ context.Context, owner, repo string, opts gh.ListRunsOpts) (string, error) {
			assert.Equal(t, "octocat", owner)
			assert.Equal(t, "hello-world", repo)
			assert.Equal(t, "main", opts.Branch)
			return `[{"databaseId":123}]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_list_runs"
	req.Params.Arguments = map[string]any{
		"owner":  "octocat",
		"repo":   "hello-world",
		"branch": "main",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

func TestListRuns_MissingRepo(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_list_runs"
	req.Params.Arguments = map[string]any{
		"owner": "octocat",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestViewRun_Success(t *testing.T) {
	h := NewHandler(&mockGHClient{
		viewRunFunc: func(_ context.Context, owner, repo string, runID string, logFailed bool) (string, error) {
			assert.Equal(t, "octocat", owner)
			assert.Equal(t, "hello-world", repo)
			assert.Equal(t, "12345", runID)
			assert.True(t, logFailed)
			return "failed log output", nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_view_run"
	req.Params.Arguments = map[string]any{
		"owner":      "octocat",
		"repo":       "hello-world",
		"run_id":     "12345",
		"log_failed": true,
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

func TestViewRun_MissingRunID(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_view_run"
	req.Params.Arguments = map[string]any{
		"owner": "octocat",
		"repo":  "hello-world",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestRerun_Success(t *testing.T) {
	h := NewHandler(&mockGHClient{
		rerunFunc: func(_ context.Context, owner, repo string, runID string, failedOnly bool) (string, error) {
			assert.Equal(t, "octocat", owner)
			assert.Equal(t, "hello-world", repo)
			assert.Equal(t, "12345", runID)
			assert.True(t, failedOnly)
			return "rerun triggered", nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_rerun"
	req.Params.Arguments = map[string]any{
		"owner":       "octocat",
		"repo":        "hello-world",
		"run_id":      "12345",
		"failed_only": true,
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

func TestCancelRun_Success(t *testing.T) {
	h := NewHandler(&mockGHClient{
		cancelRunFunc: func(_ context.Context, owner, repo string, runID string) (string, error) {
			assert.Equal(t, "octocat", owner)
			assert.Equal(t, "hello-world", repo)
			assert.Equal(t, "12345", runID)
			return "run cancelled", nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_cancel_run"
	req.Params.Arguments = map[string]any{
		"owner":  "octocat",
		"repo":   "hello-world",
		"run_id": "12345",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}
