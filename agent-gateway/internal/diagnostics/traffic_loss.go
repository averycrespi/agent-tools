package diagnostics

import (
	"fmt"
	"sync/atomic"
)

type TrafficLossReason uint8

const (
	TrafficInvalid TrafficLossReason = iota
	TrafficOversized
	TrafficUnavailable
	TrafficQueueFull
	TrafficQueueExpired
	TrafficReservation
	TrafficIdentity
	TrafficDisabled
	TrafficWriterLoss
	trafficLossKinds
)

var trafficLossNames = [...]string{"invalid", "oversized", "history_unavailable", "queue_full", "queue_expired", "physical_reservation", "identity_refusal", "capture_disabled", "writer_settlement"}

type TrafficLossObserver interface {
	TrafficLoss(TrafficLossReason, bool, uint64)
}
type TrafficLossCounts [trafficLossKinds][2]uint64
type trafficLossCounters [trafficLossKinds][2]atomic.Uint64

// Producers submit fixed scalar classifications, never one record per refusal.
// The adapter's existing worker/ticker owns periodic and terminal summaries.
func (adapter *Adapter) TrafficLoss(reason TrafficLossReason, terminal bool, count uint64) {
	if adapter == nil {
		return
	}
	if reason >= trafficLossKinds {
		increment(&adapter.invalid)
		return
	}
	stage := 0
	if terminal {
		stage = 1
	}
	counter := &adapter.trafficLosses[reason][stage]
	for {
		value := counter.Load()
		if value >= maxCount || counter.CompareAndSwap(value, value+min(count, maxCount-value)) {
			return
		}
	}
}

func (adapter *Adapter) reportTrafficLoss(reported *[trafficLossKinds][2]uint64) bool {
	for reason := range trafficLossKinds {
		initial, terminal := adapter.trafficLosses[reason][0].Load(), adapter.trafficLosses[reason][1].Load()
		if initial == reported[reason][0] && terminal == reported[reason][1] {
			continue
		}
		detail := Detail{Component: "traffic", Operation: "capture loss summary", Explanation: fmt.Sprintf("reason=%s initial_submissions=%d terminal_submissions=%d cumulative=true; submissions_are_not_executions_or_distinct_rows", trafficLossNames[reason], initial, terminal), Effect: "application dispatch unaffected; no replay"}
		if !adapter.encode(Facts{Event: OperatorFailure, Detail: detail}, 0, 0) {
			return false
		}
		reported[reason] = [2]uint64{initial, terminal}
	}
	return true
}
