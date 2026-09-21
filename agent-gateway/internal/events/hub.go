package events

import (
	"errors"
	"sync"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

var (
	ErrStreamLimit  = errors.New("event stream limit is reached")
	ErrShuttingDown = errors.New("event delivery is shutting down")
)

type Hub struct {
	mu        sync.Mutex
	streams   map[uint64]*Subscription
	nextID    uint64
	limit     int64
	buffer    int
	shutting  bool
	coalesced map[contract.InvalidationKind]uint64
	timer     *time.Timer
	timerWork sync.WaitGroup
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
		streams:   make(map[uint64]*Subscription),
		coalesced: make(map[contract.InvalidationKind]uint64),
		limit:     streamLimit.Maximum,
		buffer:    int(bufferLimit.Maximum),
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
	if event.Kind == contract.InvalidationInvocations || event.Kind == contract.InvalidationSystemStatus {
		if len(hub.streams) == 0 {
			return
		}
		hub.coalesced[event.Kind] = hub.nextID - 1
		if hub.timer == nil {
			hub.timerWork.Add(1)
			hub.timer = time.AfterFunc(250*time.Millisecond, hub.flushCoalesced)
		}
		return
	}
	hub.publishLocked(event)
}

func (hub *Hub) flushCoalesced() {
	defer hub.timerWork.Done()
	hub.mu.Lock()
	defer hub.mu.Unlock()
	hub.timer = nil
	if hub.shutting {
		return
	}
	for kind, through := range hub.coalesced {
		for id, subscription := range hub.streams {
			if id > through {
				continue
			}
			select {
			case subscription.events <- contract.Invalidation{Kind: kind}:
			default:
				hub.closeLocked(id)
			}
		}
		delete(hub.coalesced, kind)
	}
}

func (hub *Hub) publishLocked(event contract.Invalidation) {
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
	if !hub.shutting {
		hub.shutting = true
		if hub.timer != nil && hub.timer.Stop() {
			hub.timerWork.Done()
		}
		for id := range hub.streams {
			hub.closeLocked(id)
		}
	}
	hub.mu.Unlock()
	hub.timerWork.Wait()
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
