package invocation

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
)

// A failure is classified where its transaction stage and settlement are known.
// Neither a deadline nor the absence of rows proves that a transaction settled.
type trafficFailure struct {
	err                      error
	stage, cause, settlement string
	code                     int
	recoverable              bool
}

func (f *trafficFailure) Error() string { return "traffic storage operation failed" }
func (f *trafficFailure) Unwrap() error { return f.err }

func classifyTraffic(err error, stage, settlement string) *trafficFailure {
	f := &trafficFailure{err: err, stage: stage, cause: "unknown", settlement: settlement}
	var code sqlite3.ExtendedErrorCode
	if errors.As(err, &code) {
		f.code = int(code)
	}
	switch {
	case errors.Is(err, gatewaypaths.ErrUnsafePath):
		f.cause = "ownership"
	case errors.Is(err, os.ErrPermission):
		f.cause = "permission"
	case errors.Is(err, os.ErrNotExist):
		f.cause = "missing"
	case errors.Is(err, sqlite3.CORRUPT), errors.Is(err, sqlite3.NOTADB), errors.Is(err, ErrInvalidState):
		f.cause = "integrity"
	case errors.Is(err, sqlite3.FULL):
		f.cause = "full"
		f.recoverable = true
	case errors.Is(err, sqlite3.IOERR):
		f.cause = "io"
		f.recoverable = true
	case errors.Is(err, sqlite3.BUSY), errors.Is(err, sqlite3.LOCKED):
		f.cause = "locked"
		f.recoverable = true
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled), errors.Is(err, sqlite3.INTERRUPT), errors.Is(err, ErrTrafficDeadline):
		f.cause = "deadline"
		f.recoverable = true
	case errors.Is(err, ErrTrafficCapacity):
		f.cause = "capacity"
		f.recoverable = true
	}
	// Unknown commit/rollback outcomes require complete revalidation even when
	// the original synchronous connection has returned to autocommit.
	if settlement == "uncertain" {
		f.recoverable = true
	}
	if f.cause == "integrity" || f.cause == "permission" || f.cause == "missing" || f.cause == "ownership" {
		f.recoverable = false
	}
	return f
}

func trafficAutocommit(conn *sql.Conn) bool {
	settled := false
	err := conn.Raw(func(raw any) error {
		c, ok := raw.(driver.Conn)
		if ok {
			settled = c.Raw().GetAutocommit()
		}
		return nil
	})
	return err == nil && settled
}

// finishTrafficConnection never returns an unresolved connection to the pool.
// The sole writer retains it through revalidation and shutdown, without replay.
func (s *TrafficStore) finishTrafficConnection(conn *sql.Conn) {
	if trafficAutocommit(conn) {
		_ = conn.Close()
		return
	}
	s.pendingConnection = conn
}

func (s *TrafficStore) failTraffic(err error, stage, settlement string) {
	if err == nil {
		return
	}
	var f *trafficFailure
	if !errors.As(err, &f) {
		f = classifyTraffic(err, stage, settlement)
	}
	if f.settlement != "uncertain" && (errors.Is(err, ErrIdentityUnavailable) || errors.Is(err, ErrTrafficCapacity)) {
		return
	}
	s.mu.Lock()
	if s.closed || s.draining || s.faulted && !s.recoverable {
		s.mu.Unlock()
		return
	}
	if s.incident == nil || s.incident.Recovery == "recovered" {
		s.incident = &contract.TrafficIncident{FirstFailure: time.Now().UTC().Format(time.RFC3339Nano), Cause: f.cause, Stage: f.stage, Settlement: f.settlement, SQLiteCode: f.code}
	}
	s.failureRevision++
	s.faulted = true
	s.incident.RecoveryCause, s.incident.RecoveryStage = f.cause, f.stage
	s.recoverable = f.recoverable
	s.incident.Recovery = "operator_action_required"
	if f.recoverable {
		s.incident.Recovery = "recovering"
	}
	if s.recoveryDelay == 0 {
		s.recoveryDelay = time.Second
	}
	s.recoveryAt = time.Now().Add(s.recoveryDelay)
	observer := s.diagnostics
	facts := diagnostics.TrafficFacts(false, f.cause, f.stage, f.settlement, f.code)
	s.mu.Unlock()
	if observer != nil {
		observer.Traffic(facts)
	}
}

// recoverTraffic runs only under the existing writer gate, never a replacement
// writer. The caller's timer schedules bounded attempts, not batch retries.
func (s *TrafficStore) recoverTraffic(now time.Time) {
	s.writerGate.Lock()
	defer s.writerGate.Unlock()
	s.mu.Lock()
	if s.closed || s.draining || !s.faulted || !s.recoverable || now.Before(s.recoveryAt) {
		s.mu.Unlock()
		return
	}
	revision := s.failureRevision
	s.mu.Unlock()
	if s.pendingConnection != nil {
		if !trafficAutocommit(s.pendingConnection) {
			s.deferRecovery()
			return
		}
		_ = s.pendingConnection.Close()
		s.pendingConnection = nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := s.inject("revalidation")
	if err == nil {
		err = trafficFiles(s.path, s.config)
	}
	if err == nil {
		err = s.validateTraffic(ctx, s.installation, s.generation)
	}
	if err != nil {
		s.failTraffic(err, "validation", "not_started")
		s.deferRecovery()
		return
	}
	s.mu.Lock()
	if !s.closed && !s.draining && revision == s.failureRevision {
		s.faulted = false
		s.recoverable = false
		s.recoveryDelay = 0
		// Revalidation restores eligibility; a fresh acknowledged submission is
		// required before claiming observed recording recovery.
		s.incident.Recovery = "degraded"
	}
	s.mu.Unlock()
}
func (s *TrafficStore) deferRecovery() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recoveryDelay = min(max(s.recoveryDelay*2, time.Second), 30*time.Second)
	s.recoveryAt = time.Now().Add(s.recoveryDelay)
}
func (s *TrafficStore) incidentLocked() *contract.TrafficIncident {
	if s.incident == nil {
		return nil
	}
	snapshot := *s.incident
	return &snapshot
}
func (s *TrafficStore) healthLocked() string {
	if s.incident != nil {
		return s.incident.Recovery
	}
	if s.faulted {
		return "operator_action_required"
	}
	return "healthy"
}
