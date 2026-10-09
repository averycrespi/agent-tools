package invocation

import (
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/activity"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
)

const (
	maxTrafficRecordBytes     int64 = contract.HTTPTrafficAdmissionBytes + httpTrafficChargeBase
	maxTrafficCompletionBytes int64 = 128 + contract.FailureDiagnosticMaxBytes
)

var (
	ErrTrafficCapacity = errors.New("traffic capacity refused before mutation")
	ErrTrafficDeadline = errors.New("traffic deadline refused before mutation")
	ErrTrafficFault    = errors.New("traffic storage unavailable; safe validation required")
)

// TrafficConfig bounds optional history, never live execution occupancy.
type TrafficConfig struct {
	BudgetBytes     int64
	RetainedRecords int64
	QueueRecords    int
	QueueBytes      int64
	BatchRecords    int
	BatchBytes      int64
	Readers         int
	Dwell           time.Duration
	QueueLifetime   time.Duration
	WriteLifetime   time.Duration
	ReadLifetime    time.Duration
}

func DefaultTrafficConfig() TrafficConfig {
	return TrafficConfig{BudgetBytes: 4294967296, RetainedRecords: 1000000,
		QueueRecords: 128, QueueBytes: 2 << 20, BatchRecords: 32, BatchBytes: 512 << 10,
		Readers: 2, Dwell: 2 * time.Millisecond,
		QueueLifetime: 250 * time.Millisecond, WriteLifetime: 2 * time.Second, ReadLifetime: time.Second}
}
func (c TrafficConfig) valid() bool {
	return c.BudgetBytes >= 1<<20 && c.BudgetBytes <= 16<<30 &&
		c.RetainedRecords > 0 && c.RetainedRecords <= 1000000 &&
		c.QueueRecords > 0 && c.QueueRecords <= 1024 && c.QueueBytes >= 16384 && c.QueueBytes <= 16<<20 &&
		c.BatchRecords > 0 && c.BatchRecords <= c.QueueRecords && c.BatchBytes >= 16384 && c.BatchBytes <= c.QueueBytes &&
		c.Readers > 0 && c.Readers <= 4 &&
		c.Dwell > 0 && c.Dwell <= 10*time.Millisecond && c.QueueLifetime >= c.Dwell && c.QueueLifetime <= time.Second &&
		c.WriteLifetime > 0 && c.WriteLifetime <= 5*time.Second && c.ReadLifetime > 0 && c.ReadLifetime <= time.Second
}

// TrafficObservation is an immutable sanitized snapshot. It contains no authority,
// context, callbacks, secrets or cleanup obligations. It is useful even if its
// initial enqueue was lost: a terminal observation includes this complete snapshot.
type TrafficObservation struct {
	prepared      PreparedAdmission
	httpAdmission string
	gitAdmission  string
	recorded      recordedEvent
	bytes         int64
}

type trafficRequest struct {
	observation    TrafficObservation
	expires        time.Time
	completion     *activity.Completion
	diagnosticJSON any
	httpCompletion string
	gitCompletion  string
	recorded       recordedEvent
	bytes          int64
}

// TrafficStore has one bounded nonblocking observation queue and one writer.
// No caller waits for persistence and an uncertain write never gets replayed.
type TrafficStore struct {
	diagnostics              diagnostics.TrafficObserver
	lossPending              diagnostics.TrafficLossCounts
	optional                 *trafficLifecycle
	db                       *sql.DB
	readerDB                 *sql.DB
	path                     string
	config                   TrafficConfig
	recorded                 *recordedActivity
	invalidate               func(contract.Invalidation)
	mu                       sync.Mutex
	closed, faulted          bool
	draining                 bool
	queued                   int
	queuedBytes              int64
	quotaRefusals            int64
	accepted, acknowledged   uint64
	completionQueued         int
	pressureReason           string
	observations             chan *trafficRequest
	stop                     chan struct{}
	done                     chan struct{}
	readSlots                chan struct{}
	readGate                 sync.RWMutex
	writerGate               sync.Mutex
	closeOnce                sync.Once
	closeErr                 error
	installation, generation string
	incident                 *contract.TrafficIncident
	lastAcknowledged         string
	recoverable              bool
	recoveryAt               time.Time
	failureRevision          uint64
	recoveryDelay            time.Duration
	pendingConnection        *sql.Conn // writer-gate owned; never pooled while unsettled
	// Tests inject failures/barriers only at the actual owning boundary.
	fault func(string) error
}

func (s *TrafficStore) BudgetBytes() int64 { return s.config.BudgetBytes }
func (s *TrafficStore) Healthy() bool {
	if s.optional != nil {
		target := s.optionalTarget()
		return target != nil && target.Healthy()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && !s.faulted && !s.draining
}
func (s *TrafficStore) BeginDrain() {
	s.mu.Lock()
	if !s.draining && s.optional != nil {
		close(s.optional.stop)
	}
	s.draining = true
	var target *TrafficStore
	if s.optional != nil {
		target = s.optional.target
	}
	s.mu.Unlock()
	if target != nil {
		target.BeginDrain()
	}
}

func (s *TrafficStore) ObserveMCP(prepared PreparedAdmission) *TrafficObservation {
	prepared.admission = cloneAdmissionEvidence(prepared.admission)
	if !validPreparedAdmission(prepared) {
		s.drop(diagnostics.TrafficInvalid, false)
		return nil
	}
	observation := &TrafficObservation{prepared: prepared, recorded: mcpRecordedAdmission(prepared), bytes: trafficCharge(prepared)}
	if observation.bytes > 16384 {
		s.drop(diagnostics.TrafficOversized, false)
		return nil
	}
	s.observeInitial(observation)
	return observation
}

func (s *TrafficStore) observeInitial(observation *TrafficObservation) {
	_ = s.enqueueObservation(&trafficRequest{observation: *observation, recorded: observation.recorded, bytes: observation.bytes})
}

// A refused enqueue drops only capture. The caller retains its sanitized snapshot
// independently of this result and can submit one self-contained terminal later.
func (s *TrafficStore) enqueueObservation(r *trafficRequest) error {
	if s == nil {
		return ErrTrafficFault
	}
	s.mu.Lock()
	if !s.closed && !s.faulted && !s.draining && s.optional != nil && s.optional.target != nil {
		target := s.optional.target
		s.mu.Unlock()
		return target.enqueueObservation(r)
	}
	defer func() { s.mu.Unlock(); s.reportLoss() }()
	refuse := func(err error, reason diagnostics.TrafficLossReason) error {
		s.dropLocked()
		s.lossLocked(reason, r.terminal())
		return err
	}
	if s.closed || s.faulted || s.draining {
		return refuse(ErrTrafficFault, diagnostics.TrafficUnavailable)
	}
	if s.optional != nil {
		reason := diagnostics.TrafficUnavailable
		if s.optional.state == "disabled" {
			reason = diagnostics.TrafficDisabled
		}
		return refuse(ErrTrafficFault, reason)
	}
	if r.bytes > s.config.BatchBytes || r.bytes > maxTrafficRecordBytes+maxTrafficCompletionBytes {
		return refuse(ErrTrafficCapacity, diagnostics.TrafficOversized)
	}
	if s.queued >= s.config.QueueRecords || s.queuedBytes+r.bytes > s.config.QueueBytes {
		return refuse(ErrTrafficCapacity, diagnostics.TrafficQueueFull)
	}
	r.expires = time.Now().Add(s.config.QueueLifetime)
	s.queued++
	s.queuedBytes += r.bytes
	if s.accepted < contract.RecordedActivityMaxCount {
		s.accepted++
	}
	if r.terminal() {
		s.completionQueued++
	}
	s.observations <- r
	return nil
}
func (s *TrafficStore) drop(reason diagnostics.TrafficLossReason, terminal bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.dropLocked()
	s.lossLocked(reason, terminal)
	s.mu.Unlock()
	s.reportLoss()
}
func (s *TrafficStore) dropLocked() {
	if s.quotaRefusals < int64(contract.RecordedActivityMaxCount) {
		s.quotaRefusals++
	}
	if s.incident != nil && s.incident.Recovery != "recovered" {
		if s.incident.Discarded < contract.RecordedActivityMaxCount {
			s.incident.Discarded++
		}
		if s.incident.Affected < contract.RecordedActivityMaxCount {
			s.incident.Affected++
		}
	}
}

func (s *TrafficStore) ObserveMCPCompletion(observation *TrafficObservation, completion activity.Completion, diagnostic *contract.FailureDiagnostics) error {
	diagnosticJSON, err := encodeFailureDiagnostics(completion.Class, diagnostic)
	if err != nil || observation == nil || observation.httpAdmission != "" || observation.gitAdmission != "" || !validTrafficCompletion(observation.prepared, completion) {
		s.drop(diagnostics.TrafficInvalid, true)
		return ErrInvalidInput
	}
	return s.enqueueObservation(&trafficRequest{observation: *observation, completion: &completion, diagnosticJSON: diagnosticJSON,
		recorded: recordedTerminal(recordedMCP, string(completion.Class)), bytes: observation.bytes + maxTrafficCompletionBytes})
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
	if s.optional != nil {
		s.closeOnce.Do(func() {
			s.BeginDrain()
			s.StartOpening(nil)
			<-s.optional.done
			s.mu.Lock()
			s.closed = true
			target := s.optional.target
			s.mu.Unlock()
			if target != nil {
				s.closeErr = target.Close()
			}
		})
		return s.closeErr
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		close(s.stop)
		s.mu.Unlock()
		<-s.done
		if s.pendingConnection != nil {
			// Close does not assert rollback or clean settlement.
			s.closeErr = errors.Join(ErrTrafficFault, s.pendingConnection.Close())
		}
		s.readGate.Lock()
		s.closeErr = errors.Join(s.closeErr, s.readerDB.Close(), s.db.Close())
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
