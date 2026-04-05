package dashboard

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/server"
)

type pendingRequest struct {
	ID        string         `json:"id"`
	Tool      string         `json:"tool"`
	Args      map[string]any `json:"args"`
	Timestamp time.Time      `json:"timestamp"`
	decision  chan string
}

type decidedRequest struct {
	ID           string         `json:"id"`
	Tool         string         `json:"tool"`
	Args         map[string]any `json:"args"`
	Decision     string         `json:"decision"`
	DenialReason string         `json:"denial_reason,omitempty"`
	Timestamp    time.Time      `json:"timestamp"`
	DecidedAt    time.Time      `json:"decided_at"`
}

// ToolLister provides the list of discovered tools.
type ToolLister interface {
	Tools() []server.Tool
}

// AuditQuerier provides audit log queries.
type AuditQuerier interface {
	Query(ctx context.Context, opts audit.QueryOpts) ([]audit.Record, int, error)
}

// Dashboard serves the web UI and manages the approval flow.
type Dashboard struct {
	mu      sync.Mutex
	pending map[string]*pendingRequest
	decided []decidedRequest
	clients []chan []byte
	tools   ToolLister
	auditor AuditQuerier
	logger  *slog.Logger
}

// New creates a Dashboard.
func New(tools ToolLister, auditor AuditQuerier, logger *slog.Logger) *Dashboard {
	return &Dashboard{
		pending: make(map[string]*pendingRequest),
		tools:   tools,
		auditor: auditor,
		logger:  logger,
	}
}

// Handler returns the HTTP handler for the dashboard.
func (d *Dashboard) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /events", d.handleEvents)
	mux.HandleFunc("POST /api/decide", d.handleDecide)
	mux.HandleFunc("GET /api/pending", d.handlePending)
	mux.HandleFunc("GET /api/tools", d.handleTools)
	mux.HandleFunc("GET /api/audit", d.handleAudit)
	mux.HandleFunc("GET /unauthorized", d.handleUnauthorized)
	mux.HandleFunc("GET /", d.handleIndex)
	return mux
}

// Review blocks until a human approves or denies the request via the web UI.
// Returns (approved, denialReason, err). On explicit denial: denialReason="user".
// On context cancellation: returns (false, "", ctx.Err()).
func (d *Dashboard) Review(ctx context.Context, tool string, args map[string]any) (bool, string, error) {
	id := generateID()
	ch := make(chan string, 1) // sends "" for approval, "user" for denial

	pr := &pendingRequest{
		ID:        id,
		Tool:      tool,
		Args:      args,
		Timestamp: time.Now(),
		decision:  ch,
	}

	d.mu.Lock()
	d.pending[id] = pr
	d.mu.Unlock()

	if d.logger != nil {
		d.logger.Info("approval requested", "tool", tool, "request_id", id)
	}
	d.broadcast(newRequestEvent(pr))

	select {
	case denialReason := <-ch:
		approved := denialReason == ""
		return approved, denialReason, nil
	case <-ctx.Done():
		d.mu.Lock()
		delete(d.pending, id)
		d.mu.Unlock()
		d.broadcast(removedEvent(id))
		return false, "", ctx.Err()
	}
}

func (d *Dashboard) handleDecide(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		ID       string `json:"id"`
		Decision string `json:"decision"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	d.mu.Lock()
	pr, ok := d.pending[payload.ID]
	if ok {
		delete(d.pending, payload.ID)
	}
	d.mu.Unlock()

	if !ok {
		http.Error(w, "unknown request ID", http.StatusNotFound)
		return
	}

	approved := payload.Decision == "approve"
	denialReason := ""
	if !approved {
		denialReason = "user"
	}

	pr.decision <- denialReason

	decision := "denied"
	if approved {
		decision = "approved"
	}
	dr := decidedRequest{
		ID:           pr.ID,
		Tool:         pr.Tool,
		Args:         pr.Args,
		Decision:     decision,
		DenialReason: denialReason,
		Timestamp:    pr.Timestamp,
		DecidedAt:    time.Now(),
	}
	d.mu.Lock()
	d.decided = append(d.decided, dr)
	d.mu.Unlock()

	d.broadcast(decidedEvent(dr))
	w.WriteHeader(http.StatusOK)
}

func (d *Dashboard) handlePending(w http.ResponseWriter, _ *http.Request) {
	d.mu.Lock()
	items := make([]*pendingRequest, 0, len(d.pending))
	for _, pr := range d.pending {
		items = append(items, pr)
	}
	d.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(items)
}

func (d *Dashboard) handleTools(w http.ResponseWriter, _ *http.Request) {
	var tools []server.Tool
	if d.tools != nil {
		tools = d.tools.Tools()
		sort.Slice(tools, func(i, j int) bool {
			return tools[i].Name < tools[j].Name
		})
	}
	if tools == nil {
		tools = []server.Tool{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"tools": tools})
}

func (d *Dashboard) handleAudit(w http.ResponseWriter, r *http.Request) {
	if d.auditor == nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"records": []audit.Record{}, "total": 0})
		return
	}

	opts := audit.QueryOpts{}
	if v := r.URL.Query().Get("tool"); v != "" {
		opts.Tool = v
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			opts.Limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			opts.Offset = n
		}
	}

	records, total, err := d.auditor.Query(r.Context(), opts)
	if err != nil {
		if d.logger != nil {
			d.logger.Error("audit query failed", "error", err)
		}
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"records": records, "total": total})
}

func (d *Dashboard) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan []byte, 16)
	d.mu.Lock()
	d.clients = append(d.clients, ch)
	d.mu.Unlock()

	defer func() {
		d.mu.Lock()
		for i, c := range d.clients {
			if c == ch {
				d.clients = append(d.clients[:i], d.clients[i+1:]...)
				break
			}
		}
		d.mu.Unlock()
	}()

	_, _ = fmt.Fprintf(w, ": keepalive\n\n")
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case msg := <-ch:
			_, _ = fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (d *Dashboard) handleUnauthorized(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, `<!DOCTYPE html>
<html><head><title>Unauthorized - MCP Broker</title>
<style>body{font-family:system-ui,sans-serif;max-width:600px;margin:80px auto;padding:0 20px;color:#333}
h1{color:#c00}code{background:#f4f4f4;padding:2px 6px;border-radius:3px}</style>
</head><body>
<h1>Unauthorized</h1>
<p>You need to authenticate to access the MCP Broker dashboard.</p>
<p>Open the authenticated URL printed in the broker's startup output:</p>
<pre>Dashboard: http://localhost:PORT/dashboard/?token=TOKEN</pre>
<p>This sets a cookie so you won't need to do this again.</p>
</body></html>`)
}

func (d *Dashboard) handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
}

func (d *Dashboard) broadcast(data []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, ch := range d.clients {
		select {
		case ch <- data:
		default:
		}
	}
}

type sseEvent struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

func newRequestEvent(pr *pendingRequest) []byte {
	b, _ := json.Marshal(sseEvent{Type: "new", Data: pr})
	return b
}

func removedEvent(id string) []byte {
	b, _ := json.Marshal(sseEvent{Type: "removed", Data: map[string]string{"id": id}})
	return b
}

func decidedEvent(dr decidedRequest) []byte {
	b, _ := json.Marshal(sseEvent{Type: "decided", Data: dr})
	return b
}

func generateID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

//go:embed index.html
var indexHTML []byte
