package diagnostics

import (
	"context"
	"io"
	"log"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Adapter is the sole diagnostic encoder and sink writer. Callers retain it
// until Done closes, even if Finish's bounded wait expires.
type Adapter struct {
	level       Level
	sink        io.Writer
	queue       chan Facts
	done        chan struct{}
	abort       chan struct{}
	mu          sync.Mutex
	stopped     bool
	finish      sync.Once
	terminal    boundedBytes
	dropped     atomic.Uint64
	invalid     atomic.Uint64
	failed      atomic.Bool
	calls       atomic.Uint64
	process     string
	now         func() time.Time
	suppression map[suppressionKey]suppressionState
}

func New(sink io.Writer, level Level) *Adapter {
	adapter := &Adapter{now: time.Now, suppression: make(map[suppressionKey]suppressionState), level: level, sink: sink, queue: make(chan Facts, QueueRecords-1), done: make(chan struct{}), abort: make(chan struct{}), process: time.Now().UTC().Format("20060102T150405.000000000")}
	go adapter.run()
	return adapter
}
func (adapter *Adapter) DebugEnabled() bool { return adapter != nil && adapter.level == Debug }
func (adapter *Adapter) CallID() uint64 {
	if adapter == nil {
		return 0
	}
	return NextID(&adapter.calls)
}
func (adapter *Adapter) Done() <-chan struct{}     { return adapter.done }
func (adapter *Adapter) TerminalOutput() io.Writer { return &adapter.terminal }

func (adapter *Adapter) Storage(facts Facts) {
	if adapter == nil {
		return
	}
	if facts.Event < StorageWait || facts.Event > StorageLatch {
		increment(&adapter.invalid)
		return
	}
	adapter.Observe(facts)
}
func (adapter *Adapter) Authority(facts Facts) {
	if adapter == nil {
		return
	}
	if facts.Event < AuthorityWait || facts.Event > AuthorityReject {
		increment(&adapter.invalid)
		return
	}
	adapter.Observe(facts)
}
func (adapter *Adapter) Invocation(facts Facts) {
	if adapter == nil {
		return
	}
	if facts.Event < InvocationAdmission || facts.Event > TerminalAnnotation {
		increment(&adapter.invalid)
		return
	}
	adapter.Observe(facts)
}

func (adapter *Adapter) Reconciliation(facts Facts) {
	if adapter == nil {
		return
	}
	if facts.Event != ReconciliationDisplaced && facts.Event != ReconciliationSettlementFailure && !upstreamEvent(facts.Event) {
		increment(&adapter.invalid)
		return
	}
	adapter.Observe(facts)
}

func (adapter *Adapter) Observe(facts Facts) {
	if adapter == nil {
		return
	}
	if !validFacts(facts) {
		increment(&adapter.invalid)
		return
	}
	if facts.Event >= InvocationAdmission && facts.Event <= StorageReject && adapter.level != Debug {
		return
	}
	if (facts.Event <= Shutdown || facts.Event == ReconciliationDisplaced) && adapter.level < Info {
		return
	}
	if upstreamEvent(facts.Event) && facts.Event != UpstreamRecovered && facts.Event != OAuthRefreshComplete && adapter.level < upstreamLevel(facts.Event) {
		return
	}
	// Lock contention is also loss: producers never wait for another producer.
	if !adapter.mu.TryLock() {
		increment(&adapter.dropped)
		return
	}
	defer adapter.mu.Unlock()
	if adapter.stopped {
		return
	}
	if adapter.suppress(&facts) || upstreamEvent(facts.Event) && adapter.level < upstreamLevel(facts.Event) {
		return
	}
	select {
	case adapter.queue <- facts:
	default:
		increment(&adapter.dropped)
	}
}

// Finish fences producers, submits the existing single-owner terminal problem,
// then waits at most one second TOTAL. It never closes an inherited sink. A
// partial or blocked Write cannot be cancelled; no replacement writer is made.
// terminal must only render bounded local problem data through its argument.
func (adapter *Adapter) Finish(terminal func(io.Writer)) bool {
	adapter.finish.Do(func() {
		timer := time.NewTimer(FlushDeadline)
		defer timer.Stop()
		adapter.mu.Lock()
		adapter.stopped = true
		clear(adapter.suppression)
		adapter.mu.Unlock()
		if terminal != nil {
			terminal(&adapter.terminal)
		}
		close(adapter.queue)
		select {
		case <-adapter.done:
		case <-timer.C:
			adapter.failed.Store(true)
			close(adapter.abort)
		}
	})
	select {
	case <-adapter.done:
		return !adapter.failed.Load()
	default:
		return false
	}
}

// HTTPErrorLog suppresses the standard server's arbitrary request/error text.
// HTTP diagnostics must use fixed typed events, never raw logger messages.
func HTTPErrorLog() *log.Logger { return log.New(io.Discard, "", 0) }

func increment(counter *atomic.Uint64) { _ = NextID(counter) }

func validFacts(f Facts) bool {
	if upstreamEvent(f.Event) {
		return validUpstream(f)
	}
	if f.Phase != PhaseUnknown || f.Reason != ReasonUnknown || f.Disposition != DispositionUnknown || f.Retry != 0 || f.Delay != 0 || f.Suppressed != 0 {
		return false
	}
	if f.Event != ReconciliationDisplaced && f.Event != ReconciliationSettlementFailure && (f.Upstream != 0 || f.Attempt != 0) {
		return false
	}
	if f.Event < Startup || f.Event >= Loss || f.Cause > UnknownOutcome || f.Stage > IntentCleanup || f.Writer > TerminalWriter || f.Duration < 0 || f.Owned < 0 || f.Owned > 1 || f.Waiting < 0 || f.Waiting > 32 || f.Limit < 0 || f.Limit > 32 {
		return false
	}
	if f.InvocationID != "" {
		if f.Event < InvocationAdmission || f.Event > TerminalAnnotation || len(f.InvocationID) != 26 || f.InvocationID[0] > '7' {
			return false
		}
		for _, c := range f.InvocationID {
			if c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' && c != 'I' && c != 'L' && c != 'O' && c != 'U' {
				continue
			}
			return false
		}
	}
	if !validFieldSubset(f) {
		return false
	}
	switch f.Event {
	case Startup, Readiness, Drain, ReconciliationDisplaced:
		return f.Cause == None && f.Duration == 0
	case Shutdown:
		return f.Cause == Success
	case LifecycleFailure:
		return f.Cause == Unavailable
	case ReconciliationSettlementFailure:
		return f.Duration == 0 && (f.Cause == Capacity || f.Cause == Unavailable || f.Cause == Stopped)
	case InvocationAdmission:
		switch f.Cause {
		case Success:
			return f.InvocationID != ""
		case Rejected:
			return true
		case Unavailable, Stopped:
			return f.InvocationID == ""
		default:
			return false
		}
	case ExecutionStart:
		return f.InvocationID != "" && f.Cause == None && f.Duration == 0
	case ExecutionResult:
		return f.InvocationID != "" && (f.Cause == Success || f.Cause == Rejected || f.Cause == UnknownOutcome)
	case TerminalAnnotation:
		return f.InvocationID != "" && (f.Cause == Success || f.Cause == Unavailable)
	case AuthorityWait, StorageWait:
		return f.Cause == None && f.Duration == 0
	case AuthorityAcquire, AuthorityRelease, StorageAcquire, StorageRelease:
		return f.Cause == Success
	case AuthorityReject, StorageReject:
		return f.Cause == Capacity || f.Cause == Expired || f.Cause == Cancelled || f.Cause == Stopped || f.Cause == Latched || f.Cause == Unavailable
	case DurabilityFailure, StorageLatch:
		return f.Cause == Latched && f.Stage != NoStage
	default:
		return false
	}
}

func validFieldSubset(f Facts) bool {
	switch {
	case f.Event <= LifecycleFailure || f.Event == ReconciliationDisplaced || f.Event == ReconciliationSettlementFailure:
		return f.Call == 0 && f.Mutation == 0 && f.InvocationID == "" && f.Stage == NoStage && f.Writer == Foreign && f.Owned == 0 && f.Waiting == 0 && f.Limit == 0
	case f.Event <= TerminalAnnotation:
		return f.Call != 0 && f.Mutation == 0 && f.Stage == NoStage && f.Writer == Foreign && f.Owned == 0 && f.Waiting == 0 && f.Limit == 0
	case f.Event <= AuthorityReject:
		return f.Mutation != 0 && f.InvocationID == "" && f.Stage == NoStage && f.Writer == Foreign && f.Limit == 32
	case f.Event <= StorageReject:
		return f.Mutation != 0 && f.InvocationID == "" && f.Stage == NoStage && f.Limit == 31 && f.Waiting <= 31 && (f.Writer == Foreign || f.Call != 0)
	default:
		return f.Mutation != 0 && f.InvocationID == "" && f.Owned == 0 && f.Waiting == 0 && f.Limit == 0 && (f.Writer == Foreign || f.Call != 0)
	}
}

type boundedBytes struct {
	data    [RecordBytes]byte
	size    int
	invalid bool
}

func (buffer *boundedBytes) Write(data []byte) (int, error) {
	if len(data) > len(buffer.data)-buffer.size {
		buffer.invalid = true
		return len(data), nil
	}
	copy(buffer.data[buffer.size:], data)
	buffer.size += len(data)
	return len(data), nil
}
func (adapter *Adapter) write(data []byte) bool {
	select {
	case <-adapter.abort:
		return false
	default:
	}
	n, err := adapter.sink.Write(data)
	ok := err == nil && n == len(data)
	if !ok {
		adapter.failed.Store(true)
	}
	return ok
}

func (adapter *Adapter) run() {
	defer close(adapter.done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var lastLoss time.Time
	loss := func() bool {
		now := time.Now()
		if !lastLoss.IsZero() && now.Sub(lastLoss) < time.Second {
			return true
		}
		dropped, invalid := adapter.dropped.Swap(0), adapter.invalid.Swap(0)
		if dropped == 0 && invalid == 0 {
			return true
		}
		if !adapter.encode(Facts{Event: Loss}, dropped, invalid) {
			return false
		}
		lastLoss = time.Now()
		return true
	}
	for {
		select {
		case <-adapter.abort:
			return
		default:
		}
		select {
		case <-adapter.abort:
			return
		case <-ticker.C:
			if !loss() {
				return
			}
		case facts, ok := <-adapter.queue:
			if !ok {
				if !loss() {
					return
				}
				if adapter.terminal.invalid {
					adapter.failed.Store(true)
				} else if adapter.terminal.size > 0 {
					_ = adapter.write(adapter.terminal.data[:adapter.terminal.size])
				}
				return
			}
			if !adapter.encode(facts, 0, 0) || !loss() {
				return
			}
		}
	}
}

func (adapter *Adapter) encode(f Facts, dropped, invalid uint64) bool {
	var buffer boundedBytes
	handler := slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug, ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
		if attr.Key == slog.MessageKey {
			attr.Key = "event"
		}
		return attr
	}})
	level := slog.LevelDebug
	if f.Event <= Shutdown || f.Event == ReconciliationDisplaced {
		level = slog.LevelInfo
	}
	if f.Event == LifecycleFailure || f.Event == DurabilityFailure || f.Event == StorageLatch {
		level = slog.LevelError
	}
	if f.Event == Loss || f.Event == ReconciliationSettlementFailure {
		level = slog.LevelWarn
	}
	if upstreamEvent(f.Event) {
		switch upstreamLevel(f.Event) {
		case Warn:
			level = slog.LevelWarn
		case Info:
			level = slog.LevelInfo
		case Debug:
			level = slog.LevelDebug
		}
	}
	record := slog.NewRecord(time.Now().UTC(), level, eventNames[f.Event], 0)
	record.AddAttrs(slog.Int("schema_version", 1), slog.String("process_id", adapter.process))
	if f.Upstream != 0 {
		record.AddAttrs(slog.Uint64("upstream_ref", f.Upstream))
	}
	if f.Attempt != 0 {
		record.AddAttrs(slog.Uint64("attempt_ref", f.Attempt))
	}
	if upstreamEvent(f.Event) {
		record.AddAttrs(slog.String("phase", phaseNames[f.Phase]), slog.String("reason", reasonNames[f.Reason]), slog.String("disposition", dispositionNames[f.Disposition]))
		if f.Event == UpstreamRetryScheduled {
			record.AddAttrs(slog.Uint64("retry_attempt", f.Retry))
		}
		if f.Event == UpstreamRetryScheduled || f.Event == CatalogPollScheduled {
			record.AddAttrs(slog.Int64("delay_ms", f.Delay.Milliseconds()))
		}
		if f.Suppressed != 0 {
			record.AddAttrs(slog.Uint64("suppressed", f.Suppressed))
		}
	}
	if f.Cause != None {
		record.AddAttrs(slog.String("cause", causeNames[f.Cause]))
	}
	if f.Stage != NoStage {
		record.AddAttrs(slog.String("stage", stageNames[f.Stage]))
	}
	if f.Call != 0 {
		record.AddAttrs(slog.Uint64("call_id", f.Call))
	}
	if f.Mutation != 0 {
		record.AddAttrs(slog.Uint64("mutation_id", f.Mutation))
	}
	if f.InvocationID != "" {
		record.AddAttrs(slog.String("invocation_id", f.InvocationID))
	}
	if f.Duration != 0 {
		record.AddAttrs(slog.Int64("duration_ms", f.Duration.Milliseconds()))
	}
	if f.Event >= AuthorityWait && f.Event <= StorageReject {
		record.AddAttrs(slog.Int("owned", f.Owned), slog.Int("waiting", f.Waiting), slog.Int("limit", f.Limit))
	}
	if f.Event >= StorageWait && f.Event <= StorageLatch {
		record.AddAttrs(slog.String("writer_kind", writerNames[f.Writer]))
	}
	if f.Event == Loss {
		record.AddAttrs(slog.Uint64("dropped", dropped), slog.Uint64("invalid", invalid))
	}
	if err := handler.Handle(context.Background(), record); err != nil || buffer.invalid || buffer.size == 0 {
		return false
	}
	return adapter.write(buffer.data[:buffer.size])
}

var _ Observer = (*Adapter)(nil)
