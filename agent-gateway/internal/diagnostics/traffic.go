package diagnostics

import (
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

type TrafficObserver interface{ Traffic(Facts) }

// Closed scalar slots; zero means absent outside traffic events.
var trafficCauses = [...]string{"", "unknown", "permission", "missing", "integrity", "full", "io", "locked", "deadline", "capacity", "ownership"}
var trafficStages = [...]string{"", "opening", "validation", "reservation", "begin", "statement", "commit", "rollback", "acknowledgment", "read"}
var trafficSettlements = [...]string{"", "not_started", "rolled_back", "committed", "uncertain"}

func TrafficFacts(recovered bool, cause, stage, settlement string, code int) Facts {
	f := Facts{Event: TrafficFailure, SQLiteCode: code}
	if recovered {
		f.Event = TrafficRecovered
	}
	for i, v := range trafficCauses {
		if v == cause {
			f.TrafficCause = uint8(i)
		}
	}
	for i, v := range trafficStages {
		if v == stage {
			f.TrafficStage = uint8(i)
		}
	}
	for i, v := range trafficSettlements {
		if v == settlement {
			f.Settlement = uint8(i)
		}
	}
	return f
}
func trafficEvent(event Event) bool { return event == TrafficFailure || event == TrafficRecovered }
func validTraffic(f Facts) bool {
	base := f
	base.Event, base.TrafficCause, base.TrafficStage, base.Settlement, base.SQLiteCode = 0, 0, 0, 0, 0
	return base == (Facts{}) && f.TrafficCause > 0 && int(f.TrafficCause) < len(trafficCauses) && f.TrafficStage > 0 && int(f.TrafficStage) < len(trafficStages) && f.Settlement > 0 && int(f.Settlement) < len(trafficSettlements) && f.SQLiteCode >= 0 && f.SQLiteCode <= 65535
}
func (adapter *Adapter) Traffic(f Facts) {
	if adapter == nil {
		return
	}
	if !trafficEvent(f.Event) {
		increment(&adapter.invalid)
		return
	}
	adapter.Observe(f)
}

// Two fixed slots bound failure/recovery flapping without allocating per cause,
// identity or incident. Status remains authoritative when a transition is lost.
func (adapter *Adapter) suppressTraffic(f Facts) bool {
	if !trafficEvent(f.Event) {
		return false
	}
	index := 0
	if f.Event == TrafficRecovered {
		index = 1
	}
	now := adapter.now()
	if !adapter.trafficLast[index].IsZero() && now.Sub(adapter.trafficLast[index]) < contract.DiagnosticSummaryInterval {
		return true
	}
	adapter.trafficLast[index] = now
	return false
}
