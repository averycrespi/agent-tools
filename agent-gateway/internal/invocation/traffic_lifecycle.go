package invocation

import (
	"context"
	"sync"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

// trafficLifecycle keeps the stable facade independent of optional file I/O.
// There is one opener, no reopen, and close joins actual settlement, not a timer.
type trafficLifecycle struct {
	once   sync.Once
	done   chan struct{}
	target *TrafficStore // guarded by the facade's mu
	state  string
}

// NewOptionalTraffic constructs a disabled facade without touching history files.
// StartOpening may be called once after the security graph has been constructed.
func NewOptionalTraffic(config TrafficConfig) *TrafficStore {
	return &TrafficStore{config: config, recorded: newRecordedActivity(), optional: &trafficLifecycle{done: make(chan struct{}), state: "disabled"}}
}

// StartOpening owns one asynchronous validation attempt. A nil opener explicitly
// leaves history disabled. The owner must join Close before releasing the installation.
func (s *TrafficStore) StartOpening(open func() (*TrafficStore, error)) {
	s.optional.once.Do(func() {
		s.mu.Lock()
		if open == nil || s.draining || s.closed {
			close(s.optional.done)
			s.mu.Unlock()
			return
		}
		s.optional.state = "opening"
		s.mu.Unlock()
		go func() {
			defer close(s.optional.done)
			target, err := open()
			s.mu.Lock()
			if err != nil {
				s.optional.state = "unavailable"
			} else if !s.draining && !s.closed {
				target.invalidate = s.invalidate
				s.optional.target = target
				s.optional.state = "ready"
				target = nil
			}
			s.mu.Unlock()
			// Drain won: never publish this fully validated late generation.
			if target != nil {
				s.closeErr = target.Close()
			}
		}()
	})
}

func (s *TrafficStore) optionalTarget() *TrafficStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	return s.optional.target
}

func (s *TrafficStore) optionalStatus(ctx context.Context) contract.TrafficStatus {
	s.mu.Lock()
	target, state, dropped := s.optional.target, s.optional.state, s.quotaRefusals
	stopped := s.closed || s.draining
	delivery := s.deliveryLocked()
	s.mu.Unlock()
	if target != nil {
		status := target.Status(ctx)
		status.QuotaRefusals += dropped
		status.Delivery.Discarded += delivery.Discarded
		if status.Delivery.Discarded > contract.RecordedActivityMaxCount {
			status.Delivery.Discarded = contract.RecordedActivityMaxCount
		}
		if stopped {
			status.Ready = false
			status.State = "disabled"
		}
		return status
	}
	if stopped {
		state = "disabled"
	}
	return contract.TrafficStatus{State: state, Delivery: delivery, DatabaseMeasurement: contract.ByteMeasurement{State: "unavailable"}, WALMeasurement: contract.ByteMeasurement{State: "unavailable"}, FreeSpaceMeasurement: contract.ByteMeasurement{State: "unavailable"}, PressureReason: "history_unavailable", BudgetBytes: s.config.BudgetBytes, QuotaRefusals: dropped, RollingHistory: true, UnknownCompletionPossible: true}
}
