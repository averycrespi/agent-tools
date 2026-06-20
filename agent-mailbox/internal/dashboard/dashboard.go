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

	"github.com/averycrespi/agent-tools/agent-mailbox/internal/store"
)

//go:embed index.html
var indexHTML []byte

//go:embed favicon.svg
var faviconSVG []byte

type Dashboard struct {
	store        *store.Store
	pollInterval time.Duration
}

func New(st *store.Store) *Dashboard {
	return &Dashboard{store: st, pollInterval: 2 * time.Second}
}

func (d *Dashboard) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/messages", d.handleMessages)
	mux.HandleFunc("GET /api/messages/{id}", d.handleMessageDetail)
	mux.HandleFunc("POST /api/messages/{id}/ack", d.handleAck)
	mux.HandleFunc("POST /api/messages/{id}/resolve", d.handleResolve)
	mux.HandleFunc("GET /events", d.handleEvents)
	mux.HandleFunc("GET /unauthorized", d.handleUnauthorized)
	mux.HandleFunc("GET /favicon.svg", d.handleFavicon)
	mux.HandleFunc("GET /", d.handleIndex)
	return mux
}

func (d *Dashboard) handleMessages(w http.ResponseWriter, r *http.Request) {
	params, err := listParams(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := d.store.ListMessages(r.Context(), params)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (d *Dashboard) handleMessageDetail(w http.ResponseWriter, r *http.Request) {
	detail, err := d.store.GetMessage(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (d *Dashboard) handleAck(w http.ResponseWriter, r *http.Request) {
	d.lifecycle(w, r, d.store.AckMessage)
}

func (d *Dashboard) handleResolve(w http.ResponseWriter, r *http.Request) {
	d.lifecycleResolve(w, r)
}

func (d *Dashboard) lifecycle(w http.ResponseWriter, r *http.Request, fn func(context.Context, string, string) (store.Message, bool, error)) {
	actor := r.URL.Query().Get("actor")
	msg, changed, err := fn(r.Context(), r.PathValue("id"), actor)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": msg, "changed": changed})
}

func (d *Dashboard) lifecycleResolve(w http.ResponseWriter, r *http.Request) {
	msg, changed, err := d.store.ResolveMessageWithResolution(r.Context(), r.PathValue("id"), r.URL.Query().Get("actor"), r.URL.Query().Get("resolution"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": msg, "changed": changed})
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
		result, err := d.store.ListMessages(r.Context(), store.ListMessagesParams{Limit: store.DefaultLimit})
		if err != nil {
			_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", jsonString(map[string]string{"error": err.Error()}))
			flusher.Flush()
			return false
		}
		_, _ = fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", jsonString(result))
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
	_, _ = w.Write([]byte("Use the authenticated Agent Mailbox dashboard URL printed by agent-mailbox dashboard."))
}

func (d *Dashboard) handleFavicon(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	_, _ = w.Write(faviconSVG)
}

func (d *Dashboard) handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
}

func listParams(r *http.Request) (store.ListMessagesParams, error) {
	q := r.URL.Query()
	limit, err := intQuery(r, "limit", store.DefaultLimit)
	if err != nil {
		return store.ListMessagesParams{}, err
	}
	offset, err := intQuery(r, "offset", 0)
	if err != nil {
		return store.ListMessagesParams{}, err
	}
	p := store.ListMessagesParams{Status: store.Status(q.Get("status")), Channel: q.Get("channel"), Sender: q.Get("sender"), Severity: store.Severity(q.Get("severity")), Limit: limit, Offset: offset}
	if raw := q.Get("requires_response"); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return store.ListMessagesParams{}, fmt.Errorf("requires_response must be boolean")
		}
		p.RequiresResponse = &parsed
	}
	return p, nil
}

func intQuery(r *http.Request, name string, def int) (int, error) {
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

func writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "message not found")
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
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
