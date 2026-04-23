package tools

import (
	"context"
	"strings"
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

func TestListRuns_FormatsMarkdown(t *testing.T) {
	h := NewHandler(&mockGHClient{
		listRunsFunc: func(_ context.Context, owner, repo string, opts gh.ListRunsOpts) (string, error) {
			return `[{"databaseId":100,"name":"CI","displayTitle":"Fix tests","status":"completed","conclusion":"success","event":"push","headBranch":"main","updatedAt":"2025-01-02T00:00:00Z"}]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_list_runs"
	req.Params.Arguments = map[string]any{
		"owner": "octocat",
		"repo":  "hello-world",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "**#100** Fix tests")
	assert.Contains(t, text, "completed/success")
	assert.Contains(t, text, "main")
}

func TestViewRun_FormatsMarkdown(t *testing.T) {
	h := NewHandler(&mockGHClient{
		viewRunFunc: func(_ context.Context, owner, repo string, runID string, logFailed bool) (string, error) {
			assert.False(t, logFailed)
			return `{"databaseId":100,"name":"CI","displayTitle":"Fix tests","status":"completed","conclusion":"success","event":"push","headBranch":"main","headSha":"abc1234567890","url":"https://github.com/octocat/hello-world/actions/runs/100","createdAt":"2025-01-01T00:00:00Z","updatedAt":"2025-01-02T00:00:00Z","jobs":[{"databaseId":12345,"name":"build","status":"completed","conclusion":"success","url":""}]}`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_view_run"
	req.Params.Arguments = map[string]any{
		"owner":  "octocat",
		"repo":   "hello-world",
		"run_id": "100",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "# Run #100: Fix tests")
	assert.Contains(t, text, "completed/success")
	assert.Contains(t, text, "## Jobs (1)")
	assert.Contains(t, text, "(job_id: 12345)")
}

func TestViewRun_LogFailed_Passthrough(t *testing.T) {
	h := NewHandler(&mockGHClient{
		viewRunFunc: func(_ context.Context, owner, repo string, runID string, logFailed bool) (string, error) {
			assert.True(t, logFailed)
			return "build  2025-01-01 FAIL step 3\nerror: exit code 1", nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_view_run"
	req.Params.Arguments = map[string]any{
		"owner":      "octocat",
		"repo":       "hello-world",
		"run_id":     "100",
		"log_failed": true,
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "error: exit code 1")
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
	req.Params.Name = "gh_rerun_run"
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

func TestGhViewRunJobLogs(t *testing.T) {
	var capturedJobID int64
	var capturedTail int
	mock := &mockGHClient{
		viewRunJobLogFunc: func(_ context.Context, _, _ string, jobID int64, tail int) (string, error) {
			capturedJobID, capturedTail = jobID, tail
			return "line 1\nline 2\nline 3\n", nil
		},
	}
	h := NewHandler(mock)
	res, err := h.Handle(context.Background(), gomcp.CallToolRequest{
		Params: gomcp.CallToolParams{Name: "gh_view_run_job_logs", Arguments: map[string]any{
			"owner": "x", "repo": "y", "job_id": float64(12345),
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedJobID != 12345 {
		t.Errorf("job_id not threaded: %d", capturedJobID)
	}
	if capturedTail != 500 {
		t.Errorf("default tail_lines should be 500, got %d", capturedTail)
	}
	if !strings.Contains(textOf(res), "line 1") {
		t.Errorf("log body missing, got: %s", textOf(res))
	}
}

func TestGhViewRunJobLogsRespectsTailLines(t *testing.T) {
	var capturedTail int
	mock := &mockGHClient{
		viewRunJobLogFunc: func(_ context.Context, _, _ string, _ int64, tail int) (string, error) {
			capturedTail = tail
			return "", nil
		},
	}
	h := NewHandler(mock)
	_, _ = h.Handle(context.Background(), gomcp.CallToolRequest{
		Params: gomcp.CallToolParams{Name: "gh_view_run_job_logs", Arguments: map[string]any{
			"owner": "x", "repo": "y", "job_id": float64(1), "tail_lines": float64(100),
		}},
	})
	if capturedTail != 100 {
		t.Errorf("tail_lines not threaded: %d", capturedTail)
	}
}

func TestListRuns_StatusEnum(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	for _, tool := range h.runTools() {
		if tool.Name != "gh_list_runs" {
			continue
		}
		prop := tool.InputSchema.Properties["status"].(map[string]any)
		enum, ok := prop["enum"].([]string)
		require.True(t, ok, "status must declare an enum")
		// Full set from gh run list --status.
		expected := []string{
			"queued", "in_progress", "completed", "waiting", "requested", "pending",
			"cancelled", "failure", "skipped", "stale", "startup_failure", "success",
			"timed_out", "action_required", "neutral",
		}
		assert.ElementsMatch(t, expected, enum)
		return
	}
	t.Fatal("gh_list_runs not found")
}

func TestRunToolAnnotations(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	tools := h.runTools()
	byName := make(map[string]gomcp.Tool, len(tools))
	for _, tool := range tools {
		byName[tool.Name] = tool
	}

	cases := []struct {
		name     string
		expected gomcp.ToolAnnotation
	}{
		{"gh_list_runs", annRead},
		{"gh_view_run", annRead},
		{"gh_view_run_job_logs", annRead},
		{"gh_rerun_run", annAdditive},
		{"gh_cancel_run", annDestructive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool, ok := byName[tc.name]
			require.True(t, ok, "tool %s not registered", tc.name)
			assert.Equal(t, tc.expected, tool.Annotations)
		})
	}
}
