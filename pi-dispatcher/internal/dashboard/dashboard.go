package dashboard

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/averycrespi/agent-tools/pi-dispatcher/internal/inspect"
	"github.com/averycrespi/agent-tools/pi-dispatcher/internal/store"
)

//go:embed index.html
var indexHTML []byte

//go:embed favicon.svg
var faviconSVG []byte

type Dashboard struct {
	store        *store.Store
	pollInterval time.Duration
}

type TaskSummary = inspect.TaskSummary
type TaskDetail = inspect.TaskDetail
type RunSummary = inspect.RunSummary
type AgentOptionsSummary = inspect.AgentOptionsSummary
type LogWindow = inspect.LogWindow

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
		out = append(out, inspect.TaskSummaryView(summary))
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
	view := inspect.TaskSummaryFromTask(task)
	var latest *RunSummary
	var responsePreview string
	var responseTruncated bool
	if err == nil {
		latest = inspect.RunSummaryView(run)
		view.LatestRun = latest
		responsePreview, responseTruncated = inspect.ResponsePreviewFromPiEvents(run.PiEventsPath)
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
	limit, err := parseIntQuery(r, "limit", inspect.DefaultLogLimit)
	if err != nil || limit < 0 {
		writeError(w, http.StatusBadRequest, "limit must be a non-negative integer")
		return
	}
	if limit > inspect.MaxLogLimit {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("limit must be less than or equal to %d", inspect.MaxLogLimit))
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
	window, err := inspect.ReadLogWindow(path, stream, offset, limit)
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
	_, _ = w.Write([]byte("Use the authenticated Pi Dispatcher Dashboard URL printed by pd dashboard."))
}

func (d *Dashboard) handleFavicon(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	_, _ = w.Write(faviconSVG)
}

func (d *Dashboard) handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
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
