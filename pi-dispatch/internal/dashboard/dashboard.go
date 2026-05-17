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

	pdconfig "github.com/averycrespi/agent-tools/pi-dispatch/internal/config"
	"github.com/averycrespi/agent-tools/pi-dispatch/internal/store"
)

const (
	defaultLogLimit       = 64 * 1024
	maxPiEventsScanBytes  = 512 * 1024
	maxResponsePreviewLen = 4000
)

//go:embed index.html
var indexHTML []byte

//go:embed favicon.svg
var faviconSVG []byte

type Dashboard struct {
	store        *store.Store
	pollInterval time.Duration
}

type TaskSummary struct {
	ID              string      `json:"id"`
	RepoPath        string      `json:"repo_path"`
	RepoName        string      `json:"repo_name"`
	Branch          string      `json:"branch"`
	WorktreePath    string      `json:"worktree_path"`
	PromptSource    string      `json:"prompt_source"`
	PromptPreview   string      `json:"prompt_preview"`
	PromptTruncated bool        `json:"prompt_truncated"`
	Status          string      `json:"status"`
	CreatedAt       time.Time   `json:"created_at"`
	UpdatedAt       time.Time   `json:"updated_at"`
	LatestRun       *RunSummary `json:"latest_run,omitempty"`
}

type TaskDetail struct {
	Task              TaskSummary `json:"task"`
	LatestRun         *RunSummary `json:"latest_run,omitempty"`
	ResponsePreview   string      `json:"response_preview,omitempty"`
	ResponseTruncated bool        `json:"response_truncated"`
}

type RunSummary struct {
	ID                string              `json:"id"`
	TaskID            string              `json:"task_id"`
	Attempt           int                 `json:"attempt"`
	SupervisorPID     int                 `json:"supervisor_pid"`
	PiSessionFile     string              `json:"pi_session_file"`
	Status            string              `json:"status"`
	StartedAt         time.Time           `json:"started_at"`
	EndedAt           *time.Time          `json:"ended_at,omitempty"`
	ExitCode          *int64              `json:"exit_code,omitempty"`
	ErrorMessage      string              `json:"error_message"`
	AgentOptions      AgentOptionsSummary `json:"agent_options"`
	ControlSocketPath string              `json:"control_socket_path"`
	StdoutLogPath     string              `json:"stdout_log_path"`
	StderrLogPath     string              `json:"stderr_log_path"`
	PiEventsPath      string              `json:"pi_events_path"`
}

type AgentOptionsSummary struct {
	Provider                  string   `json:"provider,omitempty"`
	Model                     string   `json:"model,omitempty"`
	Thinking                  string   `json:"thinking,omitempty"`
	Tools                     []string `json:"tools,omitempty"`
	DisableBuiltinTools       bool     `json:"disable_builtin_tools,omitempty"`
	DisableAllTools           bool     `json:"disable_all_tools,omitempty"`
	Extensions                []string `json:"extensions,omitempty"`
	DisableExtensionDiscovery bool     `json:"disable_extension_discovery,omitempty"`
	Skills                    []string `json:"skills,omitempty"`
	DisableSkillDiscovery     bool     `json:"disable_skill_discovery,omitempty"`
	DisableContextFiles       bool     `json:"disable_context_files,omitempty"`
	SessionDir                string   `json:"session_dir,omitempty"`
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
	mux.HandleFunc("GET /api/tasks/{id}/logs", d.handleTaskLogs)
	mux.HandleFunc("GET /events", d.handleEvents)
	mux.HandleFunc("GET /unauthorized", d.handleUnauthorized)
	mux.HandleFunc("GET /favicon.svg", d.handleFavicon)
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
	view := TaskSummary{ID: task.ID, RepoPath: task.RepoPath, RepoName: task.RepoName, Branch: task.Branch, WorktreePath: task.WorktreePath, PromptSource: task.PromptSource, PromptPreview: task.PromptPreview, PromptTruncated: promptTruncated(task.Prompt, task.PromptPreview), Status: string(task.Status), CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt}
	var latest *RunSummary
	var responsePreview string
	var responseTruncated bool
	if err == nil {
		latest = runSummaryView(run)
		view.LatestRun = latest
		responsePreview, responseTruncated = responsePreviewFromPiEvents(run.PiEventsPath)
	}
	writeJSON(w, http.StatusOK, TaskDetail{Task: view, LatestRun: latest, ResponsePreview: responsePreview, ResponseTruncated: responseTruncated})
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
	_, _ = w.Write([]byte("Use the authenticated Pi Dispatch Dashboard URL printed by pd dashboard."))
}

func (d *Dashboard) handleFavicon(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	_, _ = w.Write(faviconSVG)
}

func (d *Dashboard) handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
}

func taskSummaryView(summary store.TaskSummary) TaskSummary {
	view := TaskSummary{ID: summary.Task.ID, RepoPath: summary.Task.RepoPath, RepoName: summary.Task.RepoName, Branch: summary.Task.Branch, WorktreePath: summary.Task.WorktreePath, PromptSource: summary.Task.PromptSource, PromptPreview: summary.Task.PromptPreview, PromptTruncated: promptTruncated(summary.Task.Prompt, summary.Task.PromptPreview), Status: string(summary.Task.Status), CreatedAt: summary.Task.CreatedAt, UpdatedAt: summary.Task.UpdatedAt}
	if summary.LatestRun.Valid {
		view.LatestRun = runSummaryView(summary.LatestRun.Run)
	}
	return view
}

func promptTruncated(prompt, preview string) bool {
	normalized := strings.TrimSpace(strings.Join(strings.Fields(prompt), " "))
	return len(normalized) > len(preview)
}

func responsePreviewFromPiEvents(piEventsPath string) (string, bool) {
	piEvents, err := readTail(piEventsPath, maxPiEventsScanBytes)
	if err != nil {
		return "", false
	}
	response, ok := lastAssistantResponseFromPiEvents(piEvents)
	if !ok {
		return "", false
	}
	if len(response) <= maxResponsePreviewLen {
		return response, false
	}
	return response[:maxResponsePreviewLen], true
}

func lastAssistantResponseFromPiEvents(data []byte) (string, bool) {
	lines := strings.Split(strings.TrimRight(string(data), "\r\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		text, ok := assistantResponseFromPiEvent([]byte(lines[i]))
		if ok {
			return text, true
		}
	}
	return "", false
}

func assistantResponseFromPiEvent(data []byte) (string, bool) {
	var event struct {
		Message  messagePayload   `json:"message"`
		Messages []messagePayload `json:"messages"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return "", false
	}
	if text, ok := assistantResponseFromMessage(event.Message); ok {
		return text, true
	}
	for i := len(event.Messages) - 1; i >= 0; i-- {
		if text, ok := assistantResponseFromMessage(event.Messages[i]); ok {
			return text, true
		}
	}
	return "", false
}

func assistantResponseFromMessage(message messagePayload) (string, bool) {
	if message.Role != "assistant" {
		return "", false
	}
	text := strings.TrimSpace(messageText(message.Content))
	return text, text != ""
}

type messagePayload struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func messageText(content json.RawMessage) string {
	var text string
	if err := json.Unmarshal(content, &text); err == nil {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &parts); err != nil {
		return ""
	}
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Type == "text" && part.Text != "" {
			out = append(out, part.Text)
		}
	}
	return strings.Join(out, "\n")
}

func runSummaryView(run store.Run) *RunSummary {
	view := &RunSummary{ID: run.ID, TaskID: run.TaskID, Attempt: run.Attempt, SupervisorPID: run.SupervisorPID, PiSessionFile: run.PiSessionFile, Status: string(run.Status), StartedAt: run.StartedAt, ErrorMessage: run.ErrorMessage, AgentOptions: decodeAgentOptions(run.AgentOptionsJSON), ControlSocketPath: run.ControlSocketPath, StdoutLogPath: run.StdoutLogPath, StderrLogPath: run.StderrLogPath, PiEventsPath: run.PiEventsPath}
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

func decodeAgentOptions(data string) AgentOptionsSummary {
	if data == "" {
		return AgentOptionsSummary{}
	}
	var agent pdconfig.AgentOptions
	if err := json.Unmarshal([]byte(data), &agent); err != nil {
		return AgentOptionsSummary{}
	}
	return AgentOptionsSummary{Provider: agent.Provider, Model: agent.Model, Thinking: agent.Thinking, Tools: agent.Tools, DisableBuiltinTools: agent.DisableBuiltinTools, DisableAllTools: agent.DisableAllTools, Extensions: agent.Extensions, DisableExtensionDiscovery: agent.DisableExtensionDiscovery, Skills: agent.Skills, DisableSkillDiscovery: agent.DisableSkillDiscovery, DisableContextFiles: agent.DisableContextFiles, SessionDir: agent.SessionDir}
}

func readTail(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path) //nolint:gosec // path comes from pd-owned task state.
	if err != nil {
		return nil, err
	}
	defer file.Close() //nolint:errcheck
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	offset := int64(0)
	if size > limit {
		offset = size - limit
		size = limit
	}
	buf := make([]byte, size)
	n, err := file.ReadAt(buf, offset)
	if err != nil && !strings.Contains(err.Error(), "EOF") {
		return nil, err
	}
	return buf[:n], nil
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
