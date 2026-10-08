package diagnostics

import (
	"time"

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

type trafficRepeat struct {
	last  time.Time
	count uint64
}

// A bounded history coalesces only identical causes/resources/settlements. New
// information is never hidden by an unrelated failure earlier in the minute.
func (adapter *Adapter) suppressTraffic(f *Facts) bool {
	if !trafficEvent(f.Event) {
		return false
	}
	if adapter.trafficHistory == nil {
		adapter.trafficHistory = make(map[Facts]trafficRepeat)
	}
	now := adapter.now()
	key := *f
	prior, exists := adapter.trafficHistory[key]
	if exists && now.Sub(prior.last) < contract.DiagnosticSummaryInterval {
		prior.count = min(prior.count+1, maxCount)
		adapter.trafficHistory[key] = prior
		return true
	}
	if !exists && len(adapter.trafficHistory) >= 32 {
		var oldest Facts
		var at time.Time
		for k, v := range adapter.trafficHistory {
			if at.IsZero() || v.last.Before(at) {
				oldest, at = k, v.last
			}
		}
		delete(adapter.trafficHistory, oldest)
	}
	f.Suppressed = prior.count
	adapter.trafficHistory[key] = trafficRepeat{last: now}
	return false
}
