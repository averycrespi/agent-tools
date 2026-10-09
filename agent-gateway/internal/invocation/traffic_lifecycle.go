package invocation

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

// trafficLifecycle keeps the stable facade independent of optional file I/O.
// There is one opener owner; retries wait for actual failed-attempt settlement.
type trafficLifecycle struct {
	once   sync.Once
	done   chan struct{}
	stop   chan struct{}
	target *TrafficStore // guarded by the facade's mu
	state  string
}

// NewOptionalTraffic constructs a disabled facade without touching history files.
// StartOpening may be called once after the security graph has been constructed.
func NewOptionalTraffic(config TrafficConfig) *TrafficStore {
	return &TrafficStore{config: config, recorded: newRecordedActivity(), optional: &trafficLifecycle{done: make(chan struct{}), stop: make(chan struct{}), state: "disabled"}}
}

// StartOpening owns asynchronous validation with bounded transient backoff. A nil opener explicitly
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
			delay := time.Second
			for {
				target, err := open()
				if err != nil {
					// The opener has settled and released failed-attempt ownership.
					// Uncertain schema migration is never replayed here.
					s.failTraffic(err, "opening", "not_started")
					s.mu.Lock()
					s.optional.state = "unavailable"
					retry := s.recoverable && !errors.Is(err, ErrTrafficFault) && !s.draining && !s.closed
					if !retry && s.incident != nil {
						s.incident.Recovery = "operator_action_required"
					}
					s.mu.Unlock()
					if target != nil {
						s.closeErr = target.Close()
						return
					}
					if !retry {
						return
					}
					timer := time.NewTimer(delay)
					select {
					case <-s.optional.stop:
						timer.Stop()
						return
					case <-timer.C:
					}
					delay = min(delay*2, 30*time.Second)
					continue
				}
				s.mu.Lock()
				if !s.draining && !s.closed {
					target.mu.Lock()
					target.invalidate, target.diagnostics = s.invalidate, s.diagnostics
					target.incident = s.incidentLocked()
					if target.incident != nil {
						target.incident.Recovery = "degraded"
					}
					target.mu.Unlock()
					s.faulted = false
					s.optional.target, s.optional.state = target, "ready"
					target = nil
				}
				s.mu.Unlock()
				// Drain won: never publish this fully validated late generation.
				if target != nil {
					s.closeErr = target.Close()
				}
				return
			}
		}()
	})
}

// SetTrafficDiagnostics binds the startup-owned bounded adapter before opening.
func (s *TrafficStore) SetTrafficDiagnostics(observer diagnostics.TrafficObserver) {
	s.mu.Lock()
	s.diagnostics = observer
	s.mu.Unlock()
	s.reportLoss()
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
	incident, health := s.incidentLocked(), s.healthLocked()
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
	if incident == nil {
		health = state
	}
	return contract.TrafficStatus{State: state, Health: health, Incident: incident, Delivery: delivery, DatabaseMeasurement: contract.ByteMeasurement{State: "unavailable"}, WALMeasurement: contract.ByteMeasurement{State: "unavailable"}, FreeSpaceMeasurement: contract.ByteMeasurement{State: "unavailable"}, PressureReason: "history_unavailable", BudgetBytes: s.config.BudgetBytes, QuotaRefusals: dropped, RollingHistory: true, UnknownCompletionPossible: true}
}
