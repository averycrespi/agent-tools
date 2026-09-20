package invocation

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/activity"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

const maxTrafficRecordBytes int64 = 16384

var (
	ErrTrafficCapacity = errors.New("traffic capacity refused before mutation")
	ErrTrafficDeadline = errors.New("traffic deadline refused before mutation")
	ErrTrafficFault    = errors.New("traffic storage fault; restart validation required")
)

// TrafficConfig owns validated persistence bounds; production exposes only the
// combined byte budget. Active pins bound concurrent live invocations independently
// of retained history. Timeouts bound cooperative work, never abandon settlement.
type TrafficConfig struct {
	BudgetBytes     int64
	RetainedRecords int64
	QueueRecords    int
	QueueBytes      int64
	BatchRecords    int
	BatchBytes      int64
	ActiveRecords   int
	Readers         int
	Dwell           time.Duration
	QueueLifetime   time.Duration
	WriteLifetime   time.Duration
	ReadLifetime    time.Duration
}

func DefaultTrafficConfig() TrafficConfig {
	return TrafficConfig{BudgetBytes: 4294967296, RetainedRecords: 1000000,
		QueueRecords: 128, QueueBytes: 2 << 20, BatchRecords: 32, BatchBytes: 512 << 10,
		ActiveRecords: 1024, Readers: 2, Dwell: 2 * time.Millisecond,
		QueueLifetime: 250 * time.Millisecond, WriteLifetime: 2 * time.Second, ReadLifetime: time.Second}
}

func (c TrafficConfig) valid() bool {
	return c.BudgetBytes >= 1<<20 && c.BudgetBytes <= 16<<30 &&
		c.RetainedRecords > 0 && c.RetainedRecords <= 1000000 &&
		c.QueueRecords > 0 && c.QueueRecords <= 1024 && c.QueueBytes >= 16384 && c.QueueBytes <= 16<<20 &&
		c.BatchRecords > 0 && c.BatchRecords <= c.QueueRecords && c.BatchBytes >= 16384 && c.BatchBytes <= c.QueueBytes &&
		c.ActiveRecords >= c.BatchRecords && c.ActiveRecords <= 4096 && c.Readers > 0 && c.Readers <= 4 &&
		c.Dwell > 0 && c.Dwell <= 10*time.Millisecond && c.QueueLifetime >= c.Dwell && c.QueueLifetime <= time.Second &&
		c.WriteLifetime > 0 && c.WriteLifetime <= 5*time.Second && c.ReadLifetime > 0 && c.ReadLifetime <= time.Second
}

// TrafficReceipt has no public fields or serialization. Its immutable evidence
// is useful only in its original process/store. Neither an ID nor a history row
// can create one. Dispatch still requires the authority owner's confirmation.
type TrafficReceipt struct {
	evidence PreparedAdmission
	owner    *TrafficStore
	request  context.Context
}

type trafficPin struct{ dispatched, completing bool }
type trafficResult struct {
	receipt *TrafficReceipt
	err     error
}
type trafficRequest struct {
	ctx            context.Context
	expires        time.Time
	prepared       PreparedAdmission
	completion     *activity.Completion
	diagnosticJSON any
	receipt        *TrafficReceipt
	bytes          int64
	result         chan trafficResult
}

// TrafficStore is the composition-owned MCP evidence store. Its worker owns
// evidence only; there are deliberately no execution callbacks or retry paths.
type TrafficStore struct {
	db              *sql.DB
	readerDB        *sql.DB
	path            string
	config          TrafficConfig
	mu              sync.Mutex
	closed, faulted bool
	draining        bool
	queued          int
	queuedBytes     int64
	terminalQueued  int
	quotaRefusals   int64
	pins            map[*TrafficReceipt]*trafficPin
	pendingPins     int
	admissions      chan *trafficRequest
	terminals       chan *trafficRequest
	stop            chan struct{}
	done            chan struct{}
	readSlots       chan struct{}
	readGate        sync.RWMutex
	admissionGate   sync.RWMutex
	writerGate      sync.Mutex
	closeOnce       sync.Once
	closeErr        error
	// Tests inject failures/barriers only at the actual owning boundary.
	fault func(string) error
}

func (s *TrafficStore) BudgetBytes() int64 { return s.config.BudgetBytes }

func (s *TrafficStore) Healthy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && !s.faulted && !s.draining
}

func (s *TrafficStore) BeginDrain() {
	s.mu.Lock()
	s.draining = true
	s.mu.Unlock()
}

func (s *TrafficStore) Admit(ctx context.Context, prepared PreparedAdmission) (*TrafficReceipt, error) {
	prepared.admission = cloneAdmissionEvidence(prepared.admission)
	if !validPreparedAdmission(prepared) {
		return nil, ErrInvalidInput
	}
	r := &trafficRequest{ctx: ctx, prepared: prepared, bytes: trafficCharge(prepared),
		expires: time.Now().Add(s.config.QueueLifetime), result: make(chan trafficResult, 1)}
	s.mu.Lock()
	if s.closed || s.faulted || s.draining {
		s.mu.Unlock()
		return nil, ErrTrafficFault
	}
	if ctx.Err() != nil {
		s.mu.Unlock()
		return nil, errors.Join(ErrTrafficDeadline, ctx.Err())
	}
	if s.queued >= s.config.QueueRecords || s.queuedBytes+r.bytes > s.config.QueueBytes ||
		r.bytes > s.config.BatchBytes || r.bytes > maxTrafficRecordBytes || len(s.pins)+s.pendingPins >= s.config.ActiveRecords {
		s.quotaRefusals++
		s.mu.Unlock()
		return nil, ErrTrafficCapacity
	}
	s.queued++
	s.queuedBytes += r.bytes
	s.pendingPins++
	s.admissions <- r
	s.mu.Unlock()
	// Cancellation cannot abandon an accepted batch's settlement.
	result := <-r.result
	return result.receipt, result.err
}

// Confirm consumes the receipt's one dispatch disposition. Callers must hold
// current authority and use the live request context. It never executes work.
func (s *TrafficStore) Confirm(ctx context.Context, receipt *TrafficReceipt) bool {
	return s.confirmCandidate(ctx, receipt, "", func() bool { return true })
}

func (s *TrafficStore) confirmCandidate(ctx context.Context, receipt *TrafficReceipt, invocationID string, detach func() bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	pin, ok := s.pins[receipt]
	if !ok || receipt.owner != s || pin.dispatched || pin.completing || s.closed || s.faulted || s.draining || ctx.Err() != nil || receipt.request.Err() != nil {
		return false
	}
	evidence := receipt.evidence.admission.Authorization
	if evidence == nil || evidence.Decision != contract.DecisionAllow {
		return false
	}
	if invocationID != "" && receipt.evidence.InvocationID != invocationID || detach == nil || !detach() {
		return false
	}
	pin.dispatched = true
	return true
}

// Release settles a no-dispatch disposition. It cannot release a live dispatch
// or a completion attempt; those remain pinned through Complete settlement.
func (s *TrafficStore) Release(receipt *TrafficReceipt) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pin, ok := s.pins[receipt]; ok && !pin.dispatched && !pin.completing {
		delete(s.pins, receipt)
	}
}

// finishWithoutTerminal settles an execution whose local result deliberately
// has no terminal claim (for example control commit uncertainty). It is called
// only after execution returns, never as a timeout or cancellation shortcut.
func (s *TrafficStore) finishWithoutTerminal(receipt *TrafficReceipt) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pin, ok := s.pins[receipt]; ok && pin.dispatched && !pin.completing {
		delete(s.pins, receipt)
	}
}

// Complete makes exactly one synchronous best-effort attempt. Its return value
// is evidence persistence, never a replacement for the caller's known result.
func (s *TrafficStore) Complete(ctx context.Context, receipt *TrafficReceipt, completion activity.Completion) error {
	return s.complete(ctx, receipt, completion, nil)
}

func (s *TrafficStore) complete(ctx context.Context, receipt *TrafficReceipt, completion activity.Completion, diagnostic *contract.FailureDiagnostics) error {
	// Encode before queueing so the writer owns bounded immutable evidence.
	diagnosticJSON, diagnosticErr := encodeFailureDiagnostics(completion.Class, diagnostic)
	s.mu.Lock()
	pin, ok := s.pins[receipt]
	if !ok || receipt.owner != s || !pin.dispatched || pin.completing {
		s.mu.Unlock()
		return ErrInvalidInput
	}
	pin.completing = true
	// Every refusal settles this sole attempt and releases the pin.
	refuse := func(err error) error { delete(s.pins, receipt); s.mu.Unlock(); return err }
	if diagnosticErr != nil || !validTrafficCompletion(receipt.evidence, completion) {
		return refuse(ErrInvalidInput)
	}
	if s.closed || s.faulted || s.draining {
		return refuse(ErrTrafficFault)
	}
	if ctx.Err() != nil {
		return refuse(errors.Join(ErrTrafficDeadline, ctx.Err()))
	}
	if s.terminalQueued >= s.config.QueueRecords {
		return refuse(ErrTrafficCapacity)
	}
	r := &trafficRequest{ctx: ctx, receipt: receipt, completion: &completion, diagnosticJSON: diagnosticJSON, bytes: 128 + contract.FailureDiagnosticMaxBytes,
		expires: time.Now().Add(s.config.QueueLifetime), result: make(chan trafficResult, 1)}
	s.terminalQueued++
	s.terminals <- r
	s.mu.Unlock()
	return (<-r.result).err
}

func validTrafficCompletion(p PreparedAdmission, c activity.Completion) bool {
	if p.admission.Authorization == nil || p.admission.Authorization.Decision != contract.DecisionAllow {
		return false
	}
	if _, err := contract.ParseInvocationTerminalClass(string(c.Class)); err != nil {
		return false
	}
	at, ok := parseCanonicalInvocationTimestamp(c.CompletedAt)
	evaluated, valid := parseCanonicalInvocationTimestamp(p.admission.Authorization.EvaluatedAt)
	return ok && valid && !at.Before(evaluated)
}

func trafficCharge(p PreparedAdmission) int64 {
	// Includes fixed row/index/terminal allowance; this is a conservative logical
	// retention charge, separate from the hard physical DB+WAL reservation.
	values, _ := admissionSQLValues(p)
	total := int64(1024 + contract.FailureDiagnosticMaxBytes)
	for _, v := range values {
		if text, ok := v.(string); ok {
			total += int64(len(text))
		}
	}
	return total
}

func (s *TrafficStore) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		close(s.stop)
		s.mu.Unlock()
		<-s.done
		s.readGate.Lock()
		s.closeErr = errors.Join(s.readerDB.Close(), s.db.Close())
		s.readGate.Unlock()
		trafficOwners.Lock()
		delete(trafficOwners.paths, filepath.Dir(s.path))
		trafficOwners.Unlock()
	})
	return s.closeErr
}

func (s *TrafficStore) inject(point string) error {
	if s.fault != nil {
		return s.fault(point)
	}
	return nil
}
