package events

import (
	"errors"
	"sync"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

var (
	ErrStreamLimit  = errors.New("event stream limit is reached")
	ErrShuttingDown = errors.New("event delivery is shutting down")
)

type Hub struct {
	mu       sync.Mutex
	streams  map[uint64]*Subscription
	nextID   uint64
	limit    int64
	buffer   int
	shutting bool
}

type Subscription struct {
	hub          *Hub
	id           uint64
	credentialID string
	events       chan contract.Invalidation
	done         chan struct{}
}

func New() *Hub {
	streamLimit, ok := contract.FixedLimitByName("event_streams")
	if !ok {
		panic("event_streams contract limit is missing")
	}
	bufferLimit, ok := contract.FixedLimitByName("event_buffered_invalidations")
	if !ok {
		panic("event_buffered_invalidations contract limit is missing")
	}
	return &Hub{
		streams: make(map[uint64]*Subscription),
		limit:   streamLimit.Maximum,
		buffer:  int(bufferLimit.Maximum),
	}
}

func (hub *Hub) Subscribe(credentialID string, terminal <-chan struct{}) (*Subscription, error) {
	hub.mu.Lock()
	if hub.shutting {
		hub.mu.Unlock()
		return nil, ErrShuttingDown
	}
	if int64(len(hub.streams)) >= hub.limit {
		hub.mu.Unlock()
		return nil, ErrStreamLimit
	}
	id := hub.nextID
	hub.nextID++
	subscription := &Subscription{
		hub: hub, id: id, credentialID: credentialID,
		events: make(chan contract.Invalidation, hub.buffer), done: make(chan struct{}),
	}
	hub.streams[id] = subscription
	hub.mu.Unlock()
	if terminal != nil {
		go func() {
			select {
			case <-terminal:
				subscription.Close()
			case <-subscription.done:
			}
		}()
	}
	return subscription, nil
}

func (hub *Hub) Publish(event contract.Invalidation) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.shutting {
		return
	}
	for id, subscription := range hub.streams {
		select {
		case subscription.events <- event:
		default:
			hub.closeLocked(id)
		}
	}
}

func (hub *Hub) InvalidateCredential(credentialID *string) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	for id, subscription := range hub.streams {
		if credentialID == nil || subscription.credentialID == *credentialID {
			hub.closeLocked(id)
		}
	}
}

func (hub *Hub) Status() contract.LimitStatus {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	inUse := int64(len(hub.streams))
	return contract.LimitStatus{InUse: inUse, Limit: hub.limit, Saturated: inUse >= hub.limit}
}

func (hub *Hub) Shutdown() {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.shutting {
		return
	}
	hub.shutting = true
	for id := range hub.streams {
		hub.closeLocked(id)
	}
}

func (subscription *Subscription) Events() <-chan contract.Invalidation { return subscription.events }
func (subscription *Subscription) Done() <-chan struct{}                { return subscription.done }
func (subscription *Subscription) Close() {
	subscription.hub.mu.Lock()
	defer subscription.hub.mu.Unlock()
	subscription.hub.closeLocked(subscription.id)
}

func (hub *Hub) closeLocked(id uint64) {
	subscription, ok := hub.streams[id]
	if !ok {
		return
	}
	delete(hub.streams, id)
	close(subscription.done)
	close(subscription.events)
}
