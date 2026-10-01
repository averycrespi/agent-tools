package invocation

import (
	"crypto/rand"
	"strconv"
	"sync"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

type recordedProtocol uint8

const (
	recordedMCP recordedProtocol = iota
	recordedHTTPRequest
	recordedConnect
	recordedHTTPUnclassified
)

type recordedKind uint8

const (
	recordedAllow recordedKind = iota
	recordedDeny
	recordedBlock
	recordedInvalidParams
	recordedUnknownTool
	recordedInvalidArguments
	recordedAuthorizationUnavailable
	recordedInvalidRequest
	recordedInterception
	recordedSucceeded
	recordedPrestartFailure
	recordedDownstreamFailure
	recordedUpstreamFailure
	recordedOutcomeUnknown
)

type recordedEvent struct {
	protocol recordedProtocol
	kind     recordedKind
}
type recordedMinute struct {
	minute int64
	counts contract.RecordedProtocols
}

// One bounded memory-only observer belongs to the selected traffic writer. No
// identifiers, evidence payloads, callbacks or persistence enter these buckets.
type recordedActivity struct {
	mu          sync.Mutex
	clock       func() (time.Time, time.Duration)
	epoch       string
	generation  uint64
	exhausted   bool
	start, last time.Time
	tick        time.Duration
	reason      string
	buckets     [contract.RecordedActivityBuckets]recordedMinute
}

func newRecordedActivity() *recordedActivity {
	origin := time.Now()
	return newRecordedActivityClock(func() (time.Time, time.Duration) {
		now := time.Now()
		return now.UTC(), now.Sub(origin)
	})
}

func newRecordedActivityClock(clock func() (time.Time, time.Duration)) *recordedActivity {
	now, tick := clock()
	return &recordedActivity{clock: clock, epoch: rand.Text(), generation: 1, start: now, last: now, tick: tick, reason: "process_start"}
}

func (a *recordedActivity) reset(now time.Time, reason string) {
	// Resume only at the next whole minute: the discontinuous interval is
	// unobserved, not a fresh bucket containing fabricated zeros.
	a.start = now.Truncate(time.Minute).Add(time.Minute)
	a.reason = reason
	if a.generation == ^uint64(0) {
		a.exhausted = true
	} else {
		a.generation++
	}
	a.buckets = [contract.RecordedActivityBuckets]recordedMinute{}
}

func (a *recordedActivity) now() time.Time {
	now, tick := a.clock()
	wall, elapsed := now.Sub(a.last), tick-a.tick
	if wall < 0 || elapsed < 0 || wall-elapsed > time.Second || elapsed-wall > time.Second {
		a.reset(now, "clock_reset")
	}
	a.last, a.tick = now, tick
	return now
}

func recordedCounter(counts *contract.RecordedProtocols, event recordedEvent) *uint64 {
	var values *contract.RecordedEvents
	switch event.protocol {
	case recordedMCP:
		values = &counts.MCP
	case recordedHTTPRequest:
		values = &counts.HTTPRequest
	case recordedConnect:
		values = &counts.Connect
	case recordedHTTPUnclassified:
		values = &counts.HTTPUnclassified
	default:
		return nil
	}
	switch event.kind {
	case recordedAllow:
		return &values.Admissions.Allow
	case recordedDeny:
		return &values.Admissions.Deny
	case recordedBlock:
		return &values.Admissions.Block
	case recordedInvalidParams:
		return &values.Admissions.InvalidParams
	case recordedUnknownTool:
		return &values.Admissions.UnknownTool
	case recordedInvalidArguments:
		return &values.Admissions.InvalidArguments
	case recordedAuthorizationUnavailable:
		return &values.Admissions.AuthorizationUnavailable
	case recordedInvalidRequest:
		return &values.Admissions.InvalidRequest
	case recordedInterception:
		return &values.Admissions.InterceptionSelected
	case recordedSucceeded:
		return &values.Completions.Succeeded
	case recordedPrestartFailure:
		return &values.Completions.PrestartFailure
	case recordedDownstreamFailure:
		return &values.Completions.DownstreamFailure
	case recordedUpstreamFailure:
		return &values.Completions.UpstreamFailure
	case recordedOutcomeUnknown:
		return &values.Completions.OutcomeUnknown
	default:
		return nil
	}
}

func (a *recordedActivity) record(event recordedEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	if a.exhausted || now.Before(a.start) {
		return
	}
	minute := now.Truncate(time.Minute).Unix() / 60
	index := (minute%contract.RecordedActivityBuckets + contract.RecordedActivityBuckets) % contract.RecordedActivityBuckets
	bucket := &a.buckets[index]
	if bucket.minute != minute {
		*bucket = recordedMinute{minute: minute}
	}
	count := recordedCounter(&bucket.counts, event)
	if count == nil || *count == contract.RecordedActivityMaxCount {
		a.reset(now, "counter_overflow")
		return
	}
	*count++
}

func (a *recordedActivity) snapshot() contract.RecordedActivitySummary {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	end := now.Truncate(time.Minute)
	start := end.Add(-contract.RecordedActivityWindow * time.Minute)
	result := contract.RecordedActivitySummary{
		Epoch: a.epoch + "-" + strconv.FormatUint(a.generation, 10), CollectionStart: a.start.Format(time.RFC3339Nano), AsOf: now.Format(time.RFC3339Nano),
		WindowStart: start.Format(time.RFC3339Nano), WindowEnd: end.Format(time.RFC3339Nano), BucketSeconds: 60, Coverage: "complete", EpochReason: a.reason,
		Buckets: make([]contract.RecordedActivityBucket, 0, contract.RecordedActivityWindow),
	}
	if a.start.After(start) {
		result.Coverage = "partial"
	}
	if a.exhausted || !a.start.Before(end) {
		result.Coverage = "unavailable"
	}
	for at := start; at.Before(end); at = at.Add(time.Minute) {
		stop := at.Add(time.Minute)
		bucket := contract.RecordedActivityBucket{Start: at.Format(time.RFC3339Nano), End: stop.Format(time.RFC3339Nano), Coverage: "unavailable"}
		if !a.exhausted && a.start.Before(stop) {
			observed := at
			bucket.Coverage = "complete"
			if a.start.After(at) {
				observed = a.start
				bucket.Coverage = "partial"
			}
			text := observed.Format(time.RFC3339Nano)
			bucket.ObservedStart = &text
			counts := contract.RecordedProtocols{}
			minute := at.Unix() / 60
			index := (minute%contract.RecordedActivityBuckets + contract.RecordedActivityBuckets) % contract.RecordedActivityBuckets
			if a.buckets[index].minute == minute {
				counts = a.buckets[index].counts
			}
			bucket.Counts = &counts
		}
		result.Buckets = append(result.Buckets, bucket)
	}
	return result
}

// RecordedActivity reads only bounded process-local acknowledgment counters.
func (s *TrafficStore) RecordedActivity() contract.RecordedActivitySummary {
	return s.recorded.snapshot()
}

func mcpRecordedAdmission(prepared PreparedAdmission) recordedEvent {
	event := recordedEvent{protocol: recordedMCP, kind: recordedAuthorizationUnavailable}
	switch prepared.admission.Class {
	case contract.AdmissionInvalidParams:
		event.kind = recordedInvalidParams
	case contract.AdmissionUnknownTool:
		event.kind = recordedUnknownTool
	case contract.AdmissionInvalidArguments:
		event.kind = recordedInvalidArguments
	case contract.AdmissionEvaluated:
		if prepared.admission.Authorization != nil {
			switch prepared.admission.Authorization.Decision {
			case contract.DecisionAllow:
				event.kind = recordedAllow
			case contract.DecisionDeny:
				event.kind = recordedDeny
			case contract.DecisionBlock:
				event.kind = recordedBlock
			}
		}
	}
	return event
}

func recordedTerminal(protocol recordedProtocol, outcome string) recordedEvent {
	event := recordedEvent{protocol: protocol, kind: recordedOutcomeUnknown}
	switch outcome {
	case "succeeded":
		event.kind = recordedSucceeded
	case "prestart_failure":
		event.kind = recordedPrestartFailure
	case "downstream_failure":
		event.kind = recordedDownstreamFailure
	case "upstream_failure":
		event.kind = recordedUpstreamFailure
	}
	return event
}

func httpRecordedAdmission(admission contract.HTTPTrafficAdmission) recordedEvent {
	event := recordedEvent{protocol: recordedHTTPUnclassified, kind: recordedInvalidRequest}
	if admission.Target == nil {
		return event
	}
	event.protocol = recordedHTTPRequest
	if admission.Target.Scheme == "" {
		event.protocol = recordedConnect
	}
	if admission.Decision != nil {
		switch {
		case admission.Decision.Allowed:
			event.kind = recordedAllow
		case admission.Decision.Transport == contract.HTTPTransportIntercept:
			event.kind = recordedInterception
		default:
			event.kind = recordedBlock
		}
	}
	return event
}
