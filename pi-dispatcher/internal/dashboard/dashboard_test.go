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

	"github.com/averycrespi/agent-tools/pi-dispatcher/internal/store"
	"github.com/stretchr/testify/require"
)

func compactHTMLTestString(value string) string {
	value = strings.Join(strings.Fields(value), "")
	value = strings.ReplaceAll(value, ";", "")
	value = strings.ReplaceAll(value, `"`, `'`)
	return value
}

func TestIndexIncludesExplorerUI(t *testing.T) {
	st, _ := testStore(t)
	dash := New(st)

	rec := httptest.NewRecorder()
	dash.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	compactBody := compactHTMLTestString(body)
	require.Contains(t, body, "Pi Dispatcher")
	require.Contains(t, body, "favicon.svg")
	require.Contains(t, body, "Search tasks")
	require.NotContains(t, body, "eventClass")
	require.NotContains(t, body, "event-type")
	require.NotContains(t, body, "evt-good")
	require.NotContains(t, body, "loadMoreEvents")
	require.NotContains(t, body, "eventsHasMore")
	require.NotContains(t, body, "Load more events")
	require.NotContains(t, body, "events-panel")
	require.Contains(t, compactBody, "grid-template-columns:max-contentminmax(0,1fr)")
	require.Contains(t, compactBody, ".grid>.muted{white-space:nowrap}")
	require.Contains(t, compactBody, "box-shadow:inset4px00var(--product-accent)")
	require.Contains(t, body, ".logbar #logstate")
	require.Contains(t, body, "stdout")
	require.Contains(t, body, "status-dot")
	require.Contains(t, body, "Disconnected")
	require.Contains(t, body, "Connected")
	require.Contains(t, body, `data-tab="overview"`)
	require.Contains(t, body, `data-tab="prompt"`)
	require.Contains(t, body, `data-tab="response"`)
	require.NotContains(t, body, `data-tab="events"`)
	require.Contains(t, body, `data-tab="logs"`)
	require.Contains(t, body, "location.hash")
	require.Contains(t, body, "setTab(state.tab)")
	require.Contains(t, body, `<div id="empty-detail" class="empty">No task selected</div>`)
	require.NotContains(t, body, `<h2 id="detail-title">Select a task</h2>`)
	require.Contains(t, body, "promptText(d.task)")
	require.Contains(t, body, "responseText(d)")
	require.Contains(t, compactBody, "returntask.prompt||promptPreview(task)")
	require.Contains(t, compactBody, "$('prompt').textContent=promptText(d.task)||'Nopromptrecorded.'")
	require.Contains(t, compactBody, "$('response').textContent=response||'Noresponserecorded.'")
	require.NotContains(t, compactBody, "['Prompt',promptText(d.task)]")
	require.NotContains(t, compactBody, "meta.push(['Response',response])")
	require.Contains(t, compactBody, "getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())}")
	require.Contains(t, compactBody, "${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}")
	require.NotContains(t, body, "toLocaleString()")
	require.Contains(t, compactBody, "['TaskID',d.task.id]")
	require.Contains(t, compactBody, "['RunID',r.id||'']")
	require.Contains(t, compactBody, "['Started',fmt(r.started_at)]")
	require.Contains(t, compactBody, "['Updated',fmt(d.task.updated_at)]")
	require.Contains(t, compactBody, "['Ended',fmt(r.ended_at)]")
	require.Contains(t, compactBody, "['MaxDuration',formatDuration(r.max_duration_seconds)]")
	require.Less(t, strings.Index(compactBody, "['Attempt',r.attempt||'']"), strings.Index(compactBody, "['Started',fmt(r.started_at)]"))
	require.Less(t, strings.Index(compactBody, "['Started',fmt(r.started_at)]"), strings.Index(compactBody, "['Updated',fmt(d.task.updated_at)]"))
	require.Less(t, strings.Index(compactBody, "['Updated',fmt(d.task.updated_at)]"), strings.Index(compactBody, "['Ended',fmt(r.ended_at)]"))
	require.Less(t, strings.Index(compactBody, "['Ended',fmt(r.ended_at)]"), strings.Index(compactBody, "['SupervisorPID',r.supervisor_pid||'']"))
	require.NotContains(t, body, "run:r.id||''")
	require.NotContains(t, body, "$('detail-title').textContent=state.selected")
	require.Contains(t, compactBody, "['Stdout',r.stdout_log_path||'']")
	require.Contains(t, compactBody, "['Stderr',r.stderr_log_path||'']")
	require.Contains(t, compactBody, "['Events',r.pi_events_path||'']")
	require.Contains(t, compactBody, "['AgentProvider',agent.provider||'']")
	require.Contains(t, compactBody, "['AgentModel',agent.model||'']")
	require.Contains(t, compactBody, "['AgentThinking',agent.thinking||'']")
	require.Contains(t, compactBody, "['AgentTools',formatList(agent.tools)]")
	require.Contains(t, compactBody, "['Env',formatList(r.env_var_names)]")
	require.Contains(t, compactBody, "meta.filter(([,v])=>v!==''&&v!==undefined&&v!==null)")
	require.NotContains(t, body, "pi_argv")
	require.NotContains(t, body, "system_prompt")
	require.NotContains(t, body, "append_system_prompt")
	require.NotContains(t, body, "session:r.pi_session_file")
	require.Less(t, strings.Index(compactBody, "['Stdout',r.stdout_log_path||'']"), strings.Index(compactBody, "['Stderr',r.stderr_log_path||'']"))
	require.Less(t, strings.Index(compactBody, "['Stderr',r.stderr_log_path||'']"), strings.Index(compactBody, "['Events',r.pi_events_path||'']"))
	require.Contains(t, compactBody, "api('api/tasks')")
	require.Contains(t, compactBody, "newEventSource('events')")
	require.Contains(t, compactBody, "if(state.selected)awaitrefreshSelectedTask()")
	require.NotContains(t, body, "if(state.selected)await selectTask(state.selected)")
	require.NotContains(t, body, "api('/api/tasks')")
	require.NotContains(t, body, "new EventSource('/events')")
	require.NotContains(t, body, "Dispatch Board")
	require.NotContains(t, body, "Read-only Explorer for pd tasks, runs, events, and logs.")
	require.NotContains(t, body, "Shows persisted state only")
	require.NotContains(t, body, "Pi Dispatcher Dashboard")
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
	require.Contains(t, rec.Body.String(), "Pi Dispatcher")
	require.NotContains(t, rec.Body.String(), "Pi Dispatcher Dashboard")
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
	require.Equal(t, task.Prompt, payload.Task.Prompt)
	require.False(t, payload.Task.PromptTruncated)
	require.NotNil(t, payload.LatestRun)
	require.Equal(t, "run-test", payload.LatestRun.ID)
	require.Equal(t, "gpt-5", payload.LatestRun.AgentOptions.Model)
	require.Equal(t, []string{"OPENAI_API_KEY", "EMPTY"}, payload.LatestRun.EnvVarNames)
	body := rec.Body.String()
	require.NotContains(t, body, "pi_argv")
	require.NotContains(t, body, "secret system prompt")
	require.NotContains(t, body, "system_prompt")
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
	require.Equal(t, task.Prompt, payload.Task.Prompt)
	require.True(t, payload.Task.PromptTruncated)
}

func TestAPITaskEventsRouteIsNotExposed(t *testing.T) {
	st, task := testStore(t)
	dash := New(st)

	rec := httptest.NewRecorder()
	dash.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/events", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "Pi Dispatcher")
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
	run := store.Run{ID: "run-test", TaskID: task.ID, Attempt: 1, SupervisorPID: 123, Status: store.StatusRunning, StartedAt: now, EndedAt: sql.NullTime{}, AgentOptionsJSON: `{"model":"gpt-5","system_prompt":"secret system prompt"}`, PiArgvJSON: `["pi","--mode","rpc","--model","gpt-5","--system-prompt","secret system prompt"]`, EnvVarNamesJSON: `["OPENAI_API_KEY","EMPTY"]`, ControlSocketPath: "/sock", StdoutLogPath: stdout, StderrLogPath: stderr, PiEventsPath: "/events"}
	require.NoError(t, st.CreateTaskWithRun(context.Background(), task, run))
	return st, task
}
