package dashboard

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/pi-dispatch/internal/store"
)

const defaultLogLimit = 64 * 1024

//go:embed index.html
var indexHTML []byte

type Dashboard struct {
	store        *store.Store
	pollInterval time.Duration
}

type TaskSummary struct {
	ID            string      `json:"id"`
	RepoPath      string      `json:"repo_path"`
	RepoName      string      `json:"repo_name"`
	Branch        string      `json:"branch"`
	WorktreePath  string      `json:"worktree_path"`
	TemplateName  string      `json:"template_name"`
	PromptSource  string      `json:"prompt_source"`
	PromptPreview string      `json:"prompt_preview"`
	Status        string      `json:"status"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
	LatestRun     *RunSummary `json:"latest_run,omitempty"`
}

type TaskDetail struct {
	Task      TaskSummary `json:"task"`
	LatestRun *RunSummary `json:"latest_run,omitempty"`
}

type RunSummary struct {
	ID                string     `json:"id"`
	TaskID            string     `json:"task_id"`
	Attempt           int        `json:"attempt"`
	SupervisorPID     int        `json:"supervisor_pid"`
	PiSessionFile     string     `json:"pi_session_file"`
	Status            string     `json:"status"`
	StartedAt         time.Time  `json:"started_at"`
	EndedAt           *time.Time `json:"ended_at,omitempty"`
	ExitCode          *int64     `json:"exit_code,omitempty"`
	ErrorMessage      string     `json:"error_message"`
	ControlSocketPath string     `json:"control_socket_path"`
	StdoutLogPath     string     `json:"stdout_log_path"`
	StderrLogPath     string     `json:"stderr_log_path"`
	PiEventsPath      string     `json:"pi_events_path"`
}

type Event struct {
	ID          int64     `json:"id"`
	TaskID      string    `json:"task_id"`
	RunID       string    `json:"run_id"`
	Timestamp   time.Time `json:"timestamp"`
	Type        string    `json:"type"`
	Message     string    `json:"message"`
	PayloadJSON string    `json:"payload_json"`
}

type LogWindow struct {
	Stream     string `json:"stream"`
	Offset     int64  `json:"offset"`
	NextOffset int64  `json:"next_offset"`
	Size       int64  `json:"size"`
	Content    string `json:"content"`
}

func New(st *store.Store) *Dashboard {
	return &Dashboard{store: st, pollInterval: 2 * time.Second}
}

func (d *Dashboard) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tasks", d.handleTasks)
	mux.HandleFunc("GET /api/tasks/{id}", d.handleTaskDetail)
	mux.HandleFunc("GET /api/tasks/{id}/events", d.handleTaskEvents)
	mux.HandleFunc("GET /api/tasks/{id}/logs", d.handleTaskLogs)
	mux.HandleFunc("GET /events", d.handleEvents)
	mux.HandleFunc("GET /unauthorized", d.handleUnauthorized)
	mux.HandleFunc("GET /", d.handleIndex)
	return mux
}

func (d *Dashboard) handleTasks(w http.ResponseWriter, r *http.Request) {
	summaries, err := d.store.ListTaskSummaries(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]TaskSummary, 0, len(summaries))
	for _, summary := range summaries {
		out = append(out, taskSummaryView(summary))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": out})
}

func (d *Dashboard) handleTaskDetail(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	task, err := d.store.GetTask(r.Context(), taskID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "task not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	run, err := d.store.LatestRun(r.Context(), taskID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	view := TaskSummary{ID: task.ID, RepoPath: task.RepoPath, RepoName: task.RepoName, Branch: task.Branch, WorktreePath: task.WorktreePath, TemplateName: task.TemplateName, PromptSource: task.PromptSource, PromptPreview: task.PromptPreview, Status: string(task.Status), CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt}
	var latest *RunSummary
	if err == nil {
		latest = runSummaryView(run)
		view.LatestRun = latest
	}
	writeJSON(w, http.StatusOK, TaskDetail{Task: view, LatestRun: latest})
}

func (d *Dashboard) handleTaskEvents(w http.ResponseWriter, r *http.Request) {
	afterID, err := parseInt64Query(r, "after_id", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	limit, err := parseIntQuery(r, "limit", 100)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	events, err := d.store.ListEventsAfter(r.Context(), r.PathValue("id"), afterID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]Event, 0, len(events))
	for _, event := range events {
		out = append(out, eventView(event))
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
}

func (d *Dashboard) handleTaskLogs(w http.ResponseWriter, r *http.Request) {
	stream := r.URL.Query().Get("stream")
	if stream == "" {
		stream = "stdout"
	}
	if stream != "stdout" && stream != "stderr" {
		writeError(w, http.StatusBadRequest, "stream must be stdout or stderr")
		return
	}
	offset, err := parseInt64Query(r, "offset", 0)
	if err != nil || offset < 0 {
		writeError(w, http.StatusBadRequest, "offset must be a non-negative integer")
		return
	}
	limit, err := parseIntQuery(r, "limit", defaultLogLimit)
	if err != nil || limit < 0 {
		writeError(w, http.StatusBadRequest, "limit must be a non-negative integer")
		return
	}
	run, err := d.store.LatestRun(context.Background(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "task not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	path := run.StdoutLogPath
	if stream == "stderr" {
		path = run.StderrLogPath
	}
	window, err := readLogWindow(path, stream, offset, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, window)
}

func (d *Dashboard) handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	sendSnapshot := func() bool {
		summaries, err := d.store.ListTaskSummaries(r.Context())
		if err != nil {
			_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", jsonString(map[string]string{"error": err.Error()}))
			flusher.Flush()
			return false
		}
		_, _ = fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", jsonString(map[string]int{"task_count": len(summaries)}))
		flusher.Flush()
		return true
	}
	if !sendSnapshot() {
		return
	}
	ticker := time.NewTicker(d.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if !sendSnapshot() {
				return
			}
		}
	}
}

func (d *Dashboard) handleUnauthorized(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte("Use the authenticated Dispatch Board URL printed by pd dashboard."))
}

func (d *Dashboard) handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
}

func taskSummaryView(summary store.TaskSummary) TaskSummary {
	view := TaskSummary{ID: summary.Task.ID, RepoPath: summary.Task.RepoPath, RepoName: summary.Task.RepoName, Branch: summary.Task.Branch, WorktreePath: summary.Task.WorktreePath, TemplateName: summary.Task.TemplateName, PromptSource: summary.Task.PromptSource, PromptPreview: summary.Task.PromptPreview, Status: string(summary.Task.Status), CreatedAt: summary.Task.CreatedAt, UpdatedAt: summary.Task.UpdatedAt}
	if summary.LatestRun.Valid {
		view.LatestRun = runSummaryView(summary.LatestRun.Run)
	}
	return view
}

func runSummaryView(run store.Run) *RunSummary {
	view := &RunSummary{ID: run.ID, TaskID: run.TaskID, Attempt: run.Attempt, SupervisorPID: run.SupervisorPID, PiSessionFile: run.PiSessionFile, Status: string(run.Status), StartedAt: run.StartedAt, ErrorMessage: run.ErrorMessage, ControlSocketPath: run.ControlSocketPath, StdoutLogPath: run.StdoutLogPath, StderrLogPath: run.StderrLogPath, PiEventsPath: run.PiEventsPath}
	if run.EndedAt.Valid {
		ended := run.EndedAt.Time
		view.EndedAt = &ended
	}
	if run.ExitCode.Valid {
		exitCode := run.ExitCode.Int64
		view.ExitCode = &exitCode
	}
	return view
}

func eventView(event store.Event) Event {
	return Event{ID: event.ID, TaskID: event.TaskID, RunID: event.RunID, Timestamp: event.Timestamp, Type: event.Type, Message: event.Message, PayloadJSON: event.PayloadJSON}
}

func readLogWindow(path string, stream string, offset int64, limit int) (LogWindow, error) {
	file, err := os.Open(path) //nolint:gosec // log path comes from pd-owned task state.
	if err != nil {
		return LogWindow{}, err
	}
	defer file.Close() //nolint:errcheck
	info, err := file.Stat()
	if err != nil {
		return LogWindow{}, err
	}
	if offset > info.Size() {
		offset = info.Size()
	}
	buf := make([]byte, limit)
	n, err := file.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, os.ErrClosed) && n == 0 {
		if !strings.Contains(err.Error(), "EOF") {
			return LogWindow{}, err
		}
	}
	return LogWindow{Stream: stream, Offset: offset, NextOffset: offset + int64(n), Size: info.Size(), Content: string(buf[:n])}, nil
}

func parseIntQuery(r *http.Request, name string, def int) (int, error) {
	value := r.URL.Query().Get(name)
	if value == "" {
		return def, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return parsed, nil
}

func parseInt64Query(r *http.Request, name string, def int64) (int64, error) {
	value := r.URL.Query().Get(name)
	if value == "" {
		return def, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return parsed, nil
}

func jsonString(payload any) string {
	data, err := json.Marshal(payload)
	if err != nil {
		return `{"error":"encode failed"}`
	}
	return string(data)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
