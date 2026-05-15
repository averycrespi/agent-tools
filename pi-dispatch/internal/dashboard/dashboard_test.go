package dashboard

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/pi-dispatch/internal/store"
	"github.com/stretchr/testify/require"
)

func TestAPITasksReturnsSummaries(t *testing.T) {
	st, task := testStore(t)
	dash := New(st)

	rec := httptest.NewRecorder()
	dash.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Tasks []TaskSummary `json:"tasks"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Tasks, 1)
	require.Equal(t, task.ID, payload.Tasks[0].ID)
	require.Equal(t, string(store.StatusRunning), payload.Tasks[0].Status)
	require.NotNil(t, payload.Tasks[0].LatestRun)
	require.Equal(t, "run-test", payload.Tasks[0].LatestRun.ID)
}

func TestAPITaskDetailReturnsTaskRunAndEvents(t *testing.T) {
	st, task := testStore(t)
	dash := New(st)

	rec := httptest.NewRecorder()
	dash.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID, nil))

	require.Equal(t, http.StatusOK, rec.Code)
	var payload TaskDetail
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, task.ID, payload.Task.ID)
	require.NotNil(t, payload.LatestRun)
	require.Equal(t, "run-test", payload.LatestRun.ID)
}

func TestAPITaskEventsSupportsAfterAndLimit(t *testing.T) {
	st, task := testStore(t)
	dash := New(st)

	rec := httptest.NewRecorder()
	dash.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/events?after_id=1&limit=1", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Events []Event `json:"events"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Events, 1)
	require.Equal(t, int64(2), payload.Events[0].ID)
}

func TestAPITaskLogsReadsBoundedWindow(t *testing.T) {
	st, task := testStore(t)
	dash := New(st)

	rec := httptest.NewRecorder()
	dash.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/logs?stream=stdout&offset=6&limit=5", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	var payload LogWindow
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, "stdout", payload.Stream)
	require.Equal(t, int64(6), payload.Offset)
	require.Equal(t, int64(11), payload.NextOffset)
	require.Equal(t, "world", payload.Content)
}

func TestAPITaskLogsRejectsBadStream(t *testing.T) {
	st, task := testStore(t)
	dash := New(st)

	rec := httptest.NewRecorder()
	dash.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/logs?stream=combined", nil))

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestEventsSSESendsSnapshot(t *testing.T) {
	st, _ := testStore(t)
	dash := New(st)
	dash.pollInterval = time.Millisecond
	server := httptest.NewServer(dash.Handler())
	t.Cleanup(server.Close)

	resp, err := server.Client().Get(server.URL + "/events")
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	buf := make([]byte, 256)
	n, err := resp.Body.Read(buf)
	require.NoError(t, err)
	body := string(buf[:n])
	require.Contains(t, body, "event: snapshot")
	require.Contains(t, body, `"task_count":1`)
	server.CloseClientConnections()
}

func testStore(t *testing.T) (*store.Store, store.Task) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "pd.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	dir := t.TempDir()
	stdout := filepath.Join(dir, "stdout.log")
	stderr := filepath.Join(dir, "stderr.log")
	require.NoError(t, os.WriteFile(stdout, []byte("hello world"), 0o600))
	require.NoError(t, os.WriteFile(stderr, []byte("oops"), 0o600))

	now := time.Now()
	task := store.Task{ID: "pd-test", RepoPath: "/repo", RepoName: "repo", Branch: "pd/test", WorktreePath: "/wt", PromptSource: "arg", Prompt: "hello", PromptPreview: "hello", Status: store.StatusRunning, CreatedAt: now, UpdatedAt: now}
	run := store.Run{ID: "run-test", TaskID: task.ID, Attempt: 1, SupervisorPID: 123, Status: store.StatusRunning, StartedAt: now, EndedAt: sql.NullTime{}, ControlSocketPath: "/sock", StdoutLogPath: stdout, StderrLogPath: stderr, PiEventsPath: "/events"}
	require.NoError(t, st.CreateTaskWithRun(context.Background(), task, run))
	require.NoError(t, st.AddEvent(context.Background(), store.Event{TaskID: task.ID, RunID: run.ID, Timestamp: now, Type: "one"}))
	require.NoError(t, st.AddEvent(context.Background(), store.Event{TaskID: task.ID, RunID: run.ID, Timestamp: now, Type: "two"}))
	return st, task
}
