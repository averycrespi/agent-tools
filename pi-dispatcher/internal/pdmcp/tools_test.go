package pdmcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/pi-dispatcher/internal/store"
)

func TestToolDefinitionsAreReadOnlyLocal(t *testing.T) {
	h := NewHandler(nil)
	tools := h.Tools()

	require.Equal(t, []string{"list_tasks", "get_task", "get_task_logs"}, toolNames(tools))
	for _, tool := range tools {
		require.NotNil(t, tool.Annotations.ReadOnlyHint, tool.Name)
		require.True(t, *tool.Annotations.ReadOnlyHint, tool.Name)
		require.NotNil(t, tool.Annotations.OpenWorldHint, tool.Name)
		require.False(t, *tool.Annotations.OpenWorldHint, tool.Name)
	}
}

func TestListTasksReturnsSummariesWithLatestRun(t *testing.T) {
	st, task := testStore(t)
	h := NewHandler(st)
	req := gomcp.CallToolRequest{}
	req.Params.Name = "list_tasks"

	result, err := h.Handle(context.Background(), req)

	require.NoError(t, err)
	require.False(t, result.IsError)
	var payload struct {
		Tasks []TaskSummary `json:"tasks"`
	}
	decodeToolText(t, result, &payload)
	require.Len(t, payload.Tasks, 1)
	require.Equal(t, task.ID, payload.Tasks[0].ID)
	require.Equal(t, "run-test", payload.Tasks[0].LatestRun.ID)
	require.Equal(t, "visible preview", payload.Tasks[0].PromptPreview)
}

func TestGetTaskReturnsDetailResponsePreviewAndOmitsSensitiveFields(t *testing.T) {
	st, task := testStore(t)
	h := NewHandler(st)
	req := gomcp.CallToolRequest{}
	req.Params.Name = "get_task"
	req.Params.Arguments = map[string]any{"task_id": task.ID}

	result, err := h.Handle(context.Background(), req)

	require.NoError(t, err)
	require.False(t, result.IsError)
	body := toolText(t, result)
	require.Contains(t, body, "latest assistant answer")
	require.NotContains(t, body, "secret full prompt")
	require.NotContains(t, body, "secret system prompt")
	require.NotContains(t, body, "pi_argv")
	require.NotContains(t, body, "OPENAI_API_KEY=secret")

	var detail TaskDetail
	require.NoError(t, json.Unmarshal([]byte(body), &detail))
	require.Equal(t, task.ID, detail.Task.ID)
	require.Equal(t, "latest assistant answer", detail.ResponsePreview)
	require.Equal(t, []string{"OPENAI_API_KEY"}, detail.LatestRun.EnvVarNames)
}

func TestGetTaskLogsReturnsBoundedWindow(t *testing.T) {
	st, task := testStore(t)
	tests := []struct {
		name       string
		stream     string
		offset     float64
		limit      float64
		wantNext   int64
		wantSize   int64
		wantOutput string
	}{
		{name: "stdout", stream: "stdout", offset: 2, limit: 4, wantNext: 6, wantSize: 10, wantOutput: "cdef"},
		{name: "stderr", stream: "stderr", offset: 0, limit: 6, wantNext: 6, wantSize: 14, wantOutput: "stderr"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandler(st)
			req := gomcp.CallToolRequest{}
			req.Params.Name = "get_task_logs"
			req.Params.Arguments = map[string]any{"task_id": task.ID, "stream": tt.stream, "offset": tt.offset, "limit": tt.limit}

			result, err := h.Handle(context.Background(), req)

			require.NoError(t, err)
			require.False(t, result.IsError)
			var window LogWindow
			decodeToolText(t, result, &window)
			require.Equal(t, tt.stream, window.Stream)
			require.EqualValues(t, tt.offset, window.Offset)
			require.EqualValues(t, tt.wantNext, window.NextOffset)
			require.EqualValues(t, tt.wantSize, window.Size)
			require.Equal(t, tt.wantOutput, window.Content)
		})
	}
}

func TestGetTaskResponsePreviewIsBounded(t *testing.T) {
	st, task := testStore(t)
	run, err := st.LatestRun(context.Background(), task.ID)
	require.NoError(t, err)
	longResponse := strings.Repeat("x", 4001)
	require.NoError(t, os.WriteFile(run.PiEventsPath, []byte(`{"message":{"role":"assistant","content":"`+longResponse+`"}}`+"\n"), 0o600))
	h := NewHandler(st)
	req := gomcp.CallToolRequest{}
	req.Params.Name = "get_task"
	req.Params.Arguments = map[string]any{"task_id": task.ID}

	result, err := h.Handle(context.Background(), req)

	require.NoError(t, err)
	require.False(t, result.IsError)
	var detail TaskDetail
	decodeToolText(t, result, &detail)
	require.True(t, detail.ResponseTruncated)
	require.Len(t, detail.ResponsePreview, 4000)
}

func TestGetTaskErrorsAreToolErrors(t *testing.T) {
	st, _ := testStore(t)
	h := NewHandler(st)
	req := gomcp.CallToolRequest{}
	req.Params.Name = "get_task"
	req.Params.Arguments = map[string]any{"task_id": "missing"}

	result, err := h.Handle(context.Background(), req)

	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Contains(t, toolText(t, result), "task not found")
}

func TestGetTaskLogsValidationErrorsAreToolErrors(t *testing.T) {
	st, task := testStore(t)
	tests := []struct {
		name    string
		args    map[string]any
		message string
	}{
		{
			name:    "invalid stream",
			args:    map[string]any{"task_id": task.ID, "stream": "events"},
			message: "stream must be stdout or stderr",
		},
		{
			name:    "negative offset",
			args:    map[string]any{"task_id": task.ID, "offset": float64(-1)},
			message: "offset must be a non-negative integer",
		},
		{
			name:    "fractional offset",
			args:    map[string]any{"task_id": task.ID, "offset": 1.5},
			message: "offset must be a non-negative integer",
		},
		{
			name:    "negative limit",
			args:    map[string]any{"task_id": task.ID, "limit": float64(-1)},
			message: "limit must be a non-negative integer",
		},
		{
			name:    "fractional limit",
			args:    map[string]any{"task_id": task.ID, "limit": 1.5},
			message: "limit must be a non-negative integer",
		},
		{
			name:    "oversized limit",
			args:    map[string]any{"task_id": task.ID, "limit": float64(1024*1024 + 1)},
			message: "limit must be less than or equal to 1048576",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandler(st)
			req := gomcp.CallToolRequest{}
			req.Params.Name = "get_task_logs"
			req.Params.Arguments = tt.args

			result, err := h.Handle(context.Background(), req)

			require.NoError(t, err)
			require.True(t, result.IsError)
			require.Contains(t, toolText(t, result), tt.message)
		})
	}
}

func TestGetTaskLogsReadFailureReturnsToolError(t *testing.T) {
	st, task := testStore(t)
	run, err := st.LatestRun(context.Background(), task.ID)
	require.NoError(t, err)
	require.NoError(t, os.Remove(run.StdoutLogPath))
	h := NewHandler(st)
	req := gomcp.CallToolRequest{}
	req.Params.Name = "get_task_logs"
	req.Params.Arguments = map[string]any{"task_id": task.ID}

	result, err := h.Handle(context.Background(), req)

	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Contains(t, toolText(t, result), "no such file")
}

func TestStoreReadFailuresReturnToolErrors(t *testing.T) {
	boom := errors.New("store unavailable")
	h := NewHandler(failingStore{err: boom})
	for _, name := range []string{"list_tasks", "get_task", "get_task_logs"} {
		t.Run(name, func(t *testing.T) {
			req := gomcp.CallToolRequest{}
			req.Params.Name = name
			req.Params.Arguments = map[string]any{"task_id": "task-test"}

			result, err := h.Handle(context.Background(), req)

			require.NoError(t, err)
			require.True(t, result.IsError)
			require.Contains(t, toolText(t, result), boom.Error())
		})
	}
}

func TestGetTaskLogsLatestRunFailureReturnsToolError(t *testing.T) {
	boom := errors.New("latest run unavailable")
	h := NewHandler(latestRunFailingStore{err: boom})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "get_task_logs"
	req.Params.Arguments = map[string]any{"task_id": "task-test"}

	result, err := h.Handle(context.Background(), req)

	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Contains(t, toolText(t, result), boom.Error())
}

func TestUnknownToolReturnsToolError(t *testing.T) {
	h := NewHandler(nil)
	req := gomcp.CallToolRequest{}
	req.Params.Name = "unknown"

	result, err := h.Handle(context.Background(), req)

	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Contains(t, toolText(t, result), "unknown tool")
}

type failingStore struct {
	err error
}

func (s failingStore) ListTaskSummaries(context.Context) ([]store.TaskSummary, error) {
	return nil, s.err
}

func (s failingStore) GetTask(context.Context, string) (store.Task, error) {
	return store.Task{}, s.err
}

func (s failingStore) LatestRun(context.Context, string) (store.Run, error) {
	return store.Run{}, s.err
}

type latestRunFailingStore struct {
	err error
}

func (s latestRunFailingStore) ListTaskSummaries(context.Context) ([]store.TaskSummary, error) {
	return nil, nil
}

func (s latestRunFailingStore) GetTask(context.Context, string) (store.Task, error) {
	return store.Task{ID: "task-test"}, nil
}

func (s latestRunFailingStore) LatestRun(context.Context, string) (store.Run, error) {
	return store.Run{}, s.err
}

func toolNames(tools []gomcp.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

func decodeToolText(t *testing.T, result *gomcp.CallToolResult, out any) {
	t.Helper()
	require.NoError(t, json.Unmarshal([]byte(toolText(t, result)), out))
}

func toolText(t *testing.T, result *gomcp.CallToolResult) string {
	t.Helper()
	require.NotEmpty(t, result.Content)
	text, ok := result.Content[0].(gomcp.TextContent)
	require.True(t, ok)
	return text.Text
}

func testStore(t *testing.T) (*store.Store, store.Task) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "pd.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "stdout.log")
	stderrPath := filepath.Join(dir, "stderr.log")
	piEventsPath := filepath.Join(dir, "pi-events.jsonl")
	require.NoError(t, os.WriteFile(stdoutPath, []byte("abcdefghij"), 0o600))
	require.NoError(t, os.WriteFile(stderrPath, []byte("stderr content"), 0o600))
	require.NoError(t, os.WriteFile(piEventsPath, []byte(`{"type":"agent_end","messages":[{"role":"user","content":"question"},{"role":"assistant","content":[{"type":"text","text":"latest assistant answer"}]}]}`+"\n"), 0o600))

	now := time.Now().UTC().Truncate(time.Second)
	task := store.Task{
		ID:                    "task-test",
		RepoPath:              "/repo",
		RepoName:              "repo",
		Branch:                "pd/test",
		WorktreePath:          "/worktree",
		PromptSource:          "arg",
		Prompt:                "visible preview with secret full prompt",
		PromptPreview:         "visible preview",
		Status:                store.StatusRunning,
		WorktreeCleanupPolicy: store.CleanupPolicyNever,
		WorktreeCleanupStatus: store.CleanupStatusNotRequested,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	run := store.Run{
		ID:                 "run-test",
		TaskID:             task.ID,
		Attempt:            1,
		Status:             store.StatusRunning,
		StartedAt:          now,
		AgentOptionsJSON:   `{"provider":"openai","model":"gpt-5","system_prompt":"secret system prompt"}`,
		PiArgvJSON:         `["--system-prompt","secret system prompt"]`,
		EnvVarNamesJSON:    `["OPENAI_API_KEY"]`,
		ControlSocketPath:  filepath.Join(dir, "control.sock"),
		StdoutLogPath:      stdoutPath,
		StderrLogPath:      stderrPath,
		PiEventsPath:       piEventsPath,
		MaxDurationSeconds: 60,
	}
	require.NoError(t, st.CreateTaskWithRun(context.Background(), task, run))
	return st, task
}
