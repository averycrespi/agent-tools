// Package diagnostics defines bounded, payload-free process diagnostics.
package diagnostics

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

const (
	QueueRecords  = contract.DiagnosticQueueRecords
	RecordBytes   = contract.DiagnosticRecordBytes
	FlushDeadline = contract.DiagnosticFlushDeadline
)

type Level uint8

const (
	Warn Level = iota
	Info
	Debug
)

func ParseLevel(value string) (Level, bool) {
	switch value {
	case "warn":
		return Warn, true
	case "info":
		return Info, true
	case "debug":
		return Debug, true
	}
	return Warn, false
}

type Event uint8

const (
	Startup Event = iota + 1
	Readiness
	Drain
	Shutdown
	LifecycleFailure
	InvocationAdmission
	ExecutionStart
	ExecutionResult
	TerminalAnnotation
	AuthorityWait
	AuthorityAcquire
	AuthorityRelease
	AuthorityReject
	StorageWait
	StorageAcquire
	StorageRelease
	StorageReject
	DurabilityFailure
	StorageLatch
	ReconciliationDisplaced
	ReconciliationSettlementFailure
	UpstreamAttemptStart
	UpstreamAttemptComplete
	UpstreamRetryScheduled
	UpstreamRetryReset
	UpstreamUnhealthy
	UpstreamRecovered
	OAuthRequired
	OAuthCompleted
	OAuthExpired
	OAuthFailed
	OAuthRefreshComplete
	OAuthRefreshFailed
	OAuthStage
	CatalogPollScheduled
	Loss
)

var eventNames = [...]string{"", "startup", "readiness", "drain", "shutdown", "lifecycle_failure", "invocation_admission", "execution_start", "execution_result", "terminal_annotation", "authority_wait", "authority_acquire", "authority_release", "authority_reject", "storage_wait", "storage_acquire", "storage_release", "storage_reject", "durability_failure", "storage_latch", "reconciliation_displaced", "reconciliation_settlement_failure", "upstream_attempt_start", "upstream_attempt_complete", "upstream_retry_scheduled", "upstream_retry_reset", "upstream_unhealthy", "upstream_recovered", "oauth_required", "oauth_completed", "oauth_expired", "oauth_failed", "oauth_refresh_complete", "oauth_refresh_failed", "oauth_stage", "catalog_poll_scheduled", "diagnostic_loss"}

type Cause uint8

const (
	None Cause = iota
	Success
	Rejected
	Capacity
	Expired
	Cancelled
	Stopped
	Latched
	Unavailable
	UnknownOutcome
)

var causeNames = [...]string{"", "success", "rejected", "capacity", "expired", "cancelled", "stopped", "latched", "unavailable", "unknown_outcome"}

type Stage uint8

const (
	NoStage Stage = iota
	SizeCheck
	IdentityCheck
	IntentArm
	TransactionBegin
	TransactionBody
	TransactionCommit
	TransactionRollback
	IntentCleanup
)

var stageNames = [...]string{"", "size_check", "identity_check", "intent_arm", "transaction_begin", "transaction_body", "transaction_commit", "transaction_rollback", "intent_cleanup"}

type WriterKind uint8

const (
	Foreign WriterKind = iota
	AdmissionWriter
	TerminalWriter
)

var writerNames = [...]string{"foreign", "invocation_admission", "terminal_annotation"}

// Facts has no arbitrary keys, error, payload, or resource identity slots. The
// adapter validates event-specific subsets before retaining even these fields.
type Facts struct {
	Upstream     uint64
	Attempt      uint64
	Phase        Phase
	Reason       Reason
	Disposition  Disposition
	Retry        uint64
	Delay        time.Duration
	Suppressed   uint64
	Event        Event
	Cause        Cause
	Stage        Stage
	Writer       WriterKind
	Call         uint64
	Mutation     uint64
	InvocationID string
	Duration     time.Duration
	Owned        int
	Waiting      int
	Limit        int
}

type StorageObserver interface {
	DebugEnabled() bool
	Storage(Facts)
}
type AuthorityObserver interface {
	DebugEnabled() bool
	Authority(Facts)
}
type InvocationObserver interface {
	DebugEnabled() bool
	Invocation(Facts)
}
type ReconciliationObserver interface {
	Reconciliation(Facts)
}

type Observer interface {
	ReconciliationObserver
	StorageObserver
	AuthorityObserver
	InvocationObserver
}

type correlationKey struct{}
type Correlation struct {
	Call   uint64
	Writer WriterKind
}

func WithCall(ctx context.Context, call uint64) context.Context {
	return context.WithValue(ctx, correlationKey{}, Correlation{Call: call, Writer: AdmissionWriter})
}
func WithTerminal(ctx context.Context) context.Context {
	value := FromContext(ctx)
	value.Writer = TerminalWriter
	return context.WithValue(ctx, correlationKey{}, value)
}
func FromContext(ctx context.Context) Correlation {
	value, _ := ctx.Value(correlationKey{}).(Correlation)
	return value
}

// Diagnostic identities use no audit entropy. Saturation omits further identity
// rather than reusing an ID; it can never prevent a call from executing.
func NextID(counter *atomic.Uint64) uint64 {
	for {
		old := counter.Load()
		if old == ^uint64(0) {
			return 0
		}
		if counter.CompareAndSwap(old, old+1) {
			return old + 1
		}
	}
}
