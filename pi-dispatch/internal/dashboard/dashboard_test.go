package dashboard

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/pi-dispatch/internal/store"
	"github.com/stretchr/testify/require"
)

func TestIndexIncludesExplorerUI(t *testing.T) {
	st, _ := testStore(t)
	dash := New(st)

	rec := httptest.NewRecorder()
	dash.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "Pi Dispatch")
	require.Contains(t, body, "favicon.svg")
	require.Contains(t, body, "Search tasks")
	require.Contains(t, body, "eventClass")
	require.Contains(t, body, "event-type")
	require.Contains(t, body, "evt-good")
	require.Contains(t, body, "loadMoreEvents")
	require.Contains(t, body, "eventsHasMore")
	require.Contains(t, body, "Load more events")
	require.NotContains(t, body, "events-panel').addEventListener('scroll'")
	require.Contains(t, body, "grid-template-columns:minmax(100px,140px) minmax(0,1fr)")
	require.Contains(t, body, "box-shadow:inset 4px 0 0 var(--accent)")
	require.Contains(t, body, ".logbar #logstate")
	require.Contains(t, body, "stdout")
	require.Contains(t, body, "status-dot")
	require.Contains(t, body, "Disconnected")
	require.Contains(t, body, "Connected")
	require.Contains(t, body, `data-tab="overview"`)
	require.Contains(t, body, `data-tab="events"`)
	require.Contains(t, body, `data-tab="logs"`)
	require.Contains(t, body, "location.hash")
	require.Contains(t, body, "setTab(state.tab)")
	require.Contains(t, body, "promptText(d.task)")
	require.Contains(t, body, "responseText(d)")
	require.Contains(t, body, "api('api/tasks')")
	require.Contains(t, body, "new EventSource('events')")
	require.Contains(t, body, "if(state.selected)await refreshSelectedTask()")
	require.NotContains(t, body, "if(state.selected)await selectTask(state.selected)")
	require.NotContains(t, body, "api('/api/tasks')")
	require.NotContains(t, body, "new EventSource('/events')")
	require.NotContains(t, body, "Dispatch Board")
	require.NotContains(t, body, "Read-only Explorer for pd tasks, runs, events, and logs.")
	require.NotContains(t, body, "Shows persisted state only")
	require.NotContains(t, body, "Pi Dispatch Dashboard")
	require.NotContains(t, body, "Raw Logs")
	require.NotContains(t, body, "Event Timeline")
}

func TestFaviconServesDashboardIcon(t *testing.T) {
	st, _ := testStore(t)
	dash := New(st)

	rec := httptest.NewRecorder()
	dash.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/favicon.svg", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "image/svg+xml", rec.Header().Get("Content-Type"))
	require.Contains(t, rec.Body.String(), "Pi Dispatch")
	require.NotContains(t, rec.Body.String(), "Pi Dispatch Dashboard")
}

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
	require.False(t, payload.Task.PromptTruncated)
	require.NotNil(t, payload.LatestRun)
	require.Equal(t, "run-test", payload.LatestRun.ID)
}

func TestAPITaskDetailIncludesLastAssistantResponseFromPiEvents(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "pd.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	dir := t.TempDir()
	piEventsPath := filepath.Join(dir, "pi-events.jsonl")
	require.NoError(t, os.WriteFile(piEventsPath, []byte(strings.Join([]string{
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Earlier response."}]}}`,
		`{"type":"agent_end","messages":[{"role":"user","content":[{"type":"text","text":"say hi"}]},{"role":"assistant","content":[{"type":"text","text":"Hi!"}]}]}`,
		`{"type":"response","command":"get_state","success":true,"data":{"sessionFile":"/sandbox/session.jsonl"}}`,
	}, "\n")+"\n"), 0o600))

	now := time.Now()
	task := store.Task{ID: "pd-response", RepoPath: "/repo", RepoName: "repo", Branch: "pd/response", WorktreePath: "/wt", PromptSource: "arg", Prompt: "say hi", PromptPreview: "say hi", Status: store.StatusSucceeded, CreatedAt: now, UpdatedAt: now}
	run := store.Run{ID: "run-response", TaskID: task.ID, Attempt: 1, Status: store.StatusSucceeded, StartedAt: now, ControlSocketPath: "/sock", StdoutLogPath: "/stdout", StderrLogPath: "/stderr", PiEventsPath: piEventsPath}
	require.NoError(t, st.CreateTaskWithRun(context.Background(), task, run))
	dash := New(st)

	rec := httptest.NewRecorder()
	dash.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID, nil))

	require.Equal(t, http.StatusOK, rec.Code)
	var payload TaskDetail
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, "Hi!", payload.ResponsePreview)
	require.False(t, payload.ResponseTruncated)
}

func TestAPITaskDetailDoesNotReadSessionFileForResponse(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "pd.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	dir := t.TempDir()
	piEventsPath := filepath.Join(dir, "pi-events.jsonl")
	require.NoError(t, os.WriteFile(piEventsPath, []byte(`{"type":"response","command":"get_state","success":true,"data":{"sessionFile":"/sandbox/session.jsonl"}}`+"\n"), 0o600))

	now := time.Now()
	task := store.Task{ID: "pd-response-session", RepoPath: "/repo", RepoName: "repo", Branch: "pd/response", WorktreePath: "/wt", PromptSource: "arg", Prompt: "say hi", PromptPreview: "say hi", Status: store.StatusSucceeded, CreatedAt: now, UpdatedAt: now}
	run := store.Run{ID: "run-response-session", TaskID: task.ID, Attempt: 1, Status: store.StatusSucceeded, StartedAt: now, ControlSocketPath: "/sock", StdoutLogPath: "/stdout", StderrLogPath: "/stderr", PiEventsPath: piEventsPath}
	require.NoError(t, st.CreateTaskWithRun(context.Background(), task, run))
	dash := New(st)

	rec := httptest.NewRecorder()
	dash.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID, nil))

	require.Equal(t, http.StatusOK, rec.Code)
	var payload TaskDetail
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Empty(t, payload.ResponsePreview)
	require.False(t, payload.ResponseTruncated)
}

func TestAPITaskDetailReportsTruncatedPrompt(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "pd.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	now := time.Now()
	task := store.Task{ID: "pd-long", RepoPath: "/repo", RepoName: "repo", Branch: "pd/long", WorktreePath: "/wt", PromptSource: "arg", Prompt: "this prompt is much longer than the preview stored for the task", PromptPreview: "this prompt is much", Status: store.StatusRunning, CreatedAt: now, UpdatedAt: now}
	run := store.Run{ID: "run-long", TaskID: task.ID, Attempt: 1, Status: store.StatusRunning, StartedAt: now, ControlSocketPath: "/sock", StdoutLogPath: "/stdout", StderrLogPath: "/stderr", PiEventsPath: "/events"}
	require.NoError(t, st.CreateTaskWithRun(context.Background(), task, run))
	dash := New(st)

	rec := httptest.NewRecorder()
	dash.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID, nil))

	require.Equal(t, http.StatusOK, rec.Code)
	var payload TaskDetail
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.True(t, payload.Task.PromptTruncated)
}

func TestAPITaskEventsSupportsAfterLimitAndHasMore(t *testing.T) {
	st, task := testStore(t)
	require.NoError(t, st.AddEvent(context.Background(), store.Event{TaskID: task.ID, RunID: "run-test", Timestamp: time.Now(), Type: "three"}))
	dash := New(st)

	rec := httptest.NewRecorder()
	dash.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/events?after_id=1&limit=1", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Events  []Event `json:"events"`
		HasMore bool    `json:"has_more"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Events, 1)
	require.Equal(t, int64(2), payload.Events[0].ID)
	require.True(t, payload.HasMore)
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
