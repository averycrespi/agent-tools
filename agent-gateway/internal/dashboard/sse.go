package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

const (
	sseBufferSize = 32
	sseKeepalive  = 15 * time.Second
)

// EventKind is the SSE event type sent in the "event:" frame field.
//
// WHY: a named type prevents untrusted strings from reaching the SSE frame
// format. The compiler enforces at every call site that only declared constants
// are passed — no runtime validation required.
type EventKind string

const (
	// EventApproval is fired when an approval request is added or resolved.
	EventApproval EventKind = "approval"
	// EventRequest is fired when a proxied request is audited.
	EventRequest EventKind = "request"
)

// Event is a single SSE event broadcast to all subscribers.
type Event struct {
	Kind EventKind
	ID   ulid.ULID
	Data any
}

// sseBroker manages SSE subscribers and fan-out.
type sseBroker struct {
	mu          sync.Mutex
	subscribers map[string]chan []byte // id → channel
}

func newSSEBroker() *sseBroker {
	return &sseBroker{
		subscribers: make(map[string]chan []byte),
	}
}

// Subscribe registers a new subscriber and returns its event channel. The
// subscriber is automatically deregistered when ctx is cancelled.
func (b *sseBroker) Subscribe(ctx context.Context) <-chan []byte {
	ch := make(chan []byte, sseBufferSize)
	id := fmt.Sprintf("%p", ch)

	b.mu.Lock()
	b.subscribers[id] = ch
	b.mu.Unlock()

	go func() {
		<-ctx.Done()
		b.mu.Lock()
		delete(b.subscribers, id)
		b.mu.Unlock()
	}()

	return ch
}

// Broadcast sends ev to all current subscribers. If a subscriber's buffer is
// full the event is dropped (drop-on-full).
func (b *sseBroker) Broadcast(ev Event) {
	frame := marshalEvent(ev)

	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subscribers {
		select {
		case ch <- frame:
		default:
			// Drop if buffer full.
		}
	}
}

// marshalEvent serialises ev into an SSE frame:
//
//	id: <ulid>
//	event: <kind>
//	data: <json>\n\n
func marshalEvent(ev Event) []byte {
	dataBytes, _ := json.Marshal(ev.Data)
	frame := fmt.Sprintf("id: %s\nevent: %s\ndata: %s\n\n",
		ev.ID.String(), ev.Kind, string(dataBytes))
	return []byte(frame)
}

// handleEvents is the HTTP handler for GET /api/events (SSE).
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := s.sse.Subscribe(r.Context())

	// Send initial keepalive.
	_, _ = fmt.Fprint(w, ": keepalive\n\n")
	flusher.Flush()

	heartbeat := time.NewTicker(sseKeepalive)
	defer heartbeat.Stop()

	for {
		select {
		case msg := <-ch:
			_, _ = w.Write(msg)
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}
