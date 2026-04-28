package tools

import (
	"context"
	"fmt"
	"strconv"
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

func TestViewRun_LogFailed_TailLines(t *testing.T) {
	// Build 100 log lines; expect only the last 3 in the output.
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = "line" + strconv.Itoa(i)
	}
	logs := strings.Join(lines, "\n")
	h := NewHandler(&mockGHClient{
		viewRunFunc: func(_ context.Context, _, _ string, _ string, _ bool) (string, error) {
			return logs, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_view_run"
	req.Params.Arguments = map[string]any{
		"owner":      "octocat",
		"repo":       "hello-world",
		"run_id":     "100",
		"log_failed": true,
		"tail_lines": float64(3),
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Equal(t, "# Run 100 failed-job logs (last 3 lines per job)\n\nline97\nline98\nline99", text)
}

// TestViewRun_LogFailed_NoFailedJobs guards the empty-state hint: when
// log_failed=true and gh returns nothing (run had no failures), the wrapper
// must emit a friendly message rather than returning an empty body that
// looks like a broken tool.
func TestViewRun_LogFailed_NoFailedJobs(t *testing.T) {
	h := NewHandler(&mockGHClient{
		viewRunFunc: func(_ context.Context, _, _ string, _ string, _ bool) (string, error) {
			return "", nil
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
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "No failed jobs in run 12345")
	assert.Contains(t, text, "log_failed=false", "should hint at the alternative invocation")
}

// TestViewRun_LogFailed_MaxBytes verifies the byte cap kicks in after the
// line tail, truncating verbose lines that the line cap can't catch.
func TestViewRun_LogFailed_MaxBytes(t *testing.T) {
	// 50 lines × ~200 bytes each = ~10k bytes. Cap at 1000 bytes.
	lines := make([]string, 50)
	for i := range lines {
		lines[i] = strings.Repeat("x", 200)
	}
	logs := strings.Join(lines, "\n")
	h := NewHandler(&mockGHClient{
		viewRunFunc: func(_ context.Context, _, _ string, _ string, _ bool) (string, error) {
			return logs, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_view_run"
	req.Params.Arguments = map[string]any{
		"owner":      "octocat",
		"repo":       "hello-world",
		"run_id":     "100",
		"log_failed": true,
		"max_bytes":  float64(1000),
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.LessOrEqual(t, len(text), 1100, "byte cap should hold within a small overhead for the truncation marker")
	assert.Contains(t, text, "[truncated")
	assert.Contains(t, text, "bytes]")
}

// TestGhViewRunJobLogs_MaxBytes verifies max_bytes truncation on the
// per-job log handler.
func TestGhViewRunJobLogs_MaxBytes(t *testing.T) {
	logs := strings.Repeat("x", 5000)
	h := NewHandler(&mockGHClient{
		viewRunJobLogFunc: func(_ context.Context, _, _ string, _ int64, _ int) (string, error) {
			return logs, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_view_run_job_logs"
	req.Params.Arguments = map[string]any{
		"owner": "x", "repo": "y", "job_id": float64(1),
		"max_bytes": float64(500),
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "[truncated")
	assert.LessOrEqual(t, len(text), 700, "header + truncation marker + 500-byte body")
}

// TestViewRun_LogFailed_KeepsRecentLines is the regression guard for max_bytes
// trim direction. Failure context typically lives at the END of a log; if the
// byte cap drops the tail (old buggy behaviour) triage breaks.
func TestViewRun_LogFailed_KeepsRecentLines(t *testing.T) {
	// 200 lines × ~30 bytes each = ~6000 bytes. Cap at 500 — only the last
	// few lines should survive, including the LAST one with the error.
	lines := make([]string, 200)
	for i := range lines {
		lines[i] = fmt.Sprintf("line-%03d-padding-padding-padding", i)
	}
	lines[199] = "FATAL ERROR HERE"
	logs := strings.Join(lines, "\n")
	h := NewHandler(&mockGHClient{
		viewRunFunc: func(_ context.Context, _, _ string, _ string, _ bool) (string, error) {
			return logs, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_view_run"
	req.Params.Arguments = map[string]any{
		"owner":      "octocat",
		"repo":       "hello-world",
		"run_id":     "100",
		"log_failed": true,
		"max_bytes":  float64(500),
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "FATAL ERROR HERE", "max_bytes must preserve the END of the tail (most-recent line)")
	assert.NotContains(t, text, "line-000-", "first lines should be dropped, not the last")
	assert.Contains(t, text, "[truncated — showing last")
}

func TestViewRun_LogFailed_HeaderShape(t *testing.T) {
	h := NewHandler(&mockGHClient{
		viewRunFunc: func(_ context.Context, _, _ string, _ string, _ bool) (string, error) {
			return "build  2025-01-01 FAIL step 3\nerror: exit code 1", nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_view_run"
	req.Params.Arguments = map[string]any{
		"owner":      "octocat",
		"repo":       "hello-world",
		"run_id":     "12345",
		"log_failed": true,
		"tail_lines": float64(50),
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.True(t, strings.HasPrefix(text, "# Run 12345 failed-job logs (last 50 lines per job)\n\n"),
		"output must begin with the run-log header anchor; got: %q", text)
}

func TestGhViewRunJobLogs_HeaderShape(t *testing.T) {
	h := NewHandler(&mockGHClient{
		viewRunJobLogFunc: func(_ context.Context, _, _ string, _ int64, _ int) (string, error) {
			return "line 1\nline 2\nline 3\n", nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_view_run_job_logs"
	req.Params.Arguments = map[string]any{
		"owner":      "x",
		"repo":       "y",
		"job_id":     float64(99),
		"tail_lines": float64(25),
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.True(t, strings.HasPrefix(text, "# Job 99 logs (last 25 lines)\n\n"),
		"output must begin with the job-log header anchor; got: %q", text)
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
	if capturedTail != 200 {
		t.Errorf("default tail_lines should be 200, got %d", capturedTail)
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
		// Full accepted set from `gh run list --status` flag validator.
		// `pending` is not accepted by gh and is intentionally omitted.
		expected := []string{
			"queued", "completed", "in_progress", "requested", "waiting",
			"action_required", "cancelled", "failure", "neutral", "skipped",
			"stale", "startup_failure", "success", "timed_out",
		}
		assert.ElementsMatch(t, expected, enum)
		return
	}
	t.Fatal("gh_list_runs not found")
}

func TestGhListRunsActorEventFilters(t *testing.T) {
	var capturedActor, capturedEvent string
	h := NewHandler(&mockGHClient{
		listRunsFunc: func(_ context.Context, owner, repo string, opts gh.ListRunsOpts) (string, error) {
			capturedActor = opts.Actor
			capturedEvent = opts.Event
			return `[]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_list_runs"
	req.Params.Arguments = map[string]any{
		"owner": "octocat",
		"repo":  "hello-world",
		"actor": "octocat",
		"event": "push",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Equal(t, "octocat", capturedActor)
	assert.Equal(t, "push", capturedEvent)
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

func TestListRuns_Empty(t *testing.T) {
	h := NewHandler(&mockGHClient{
		listRunsFunc: func(_ context.Context, _, _ string, _ gh.ListRunsOpts) (string, error) {
			return `[]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_list_runs"
	req.Params.Arguments = map[string]any{"owner": "octocat", "repo": "hello-world"}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "No workflow runs found.", emptyResultText(t, result))
}
