package keyring

import (
	"context"
	"fmt"
	"sync"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
)

type cleanupFailure struct {
	err                                error
	resource, kind, phase              string
	total, attempted, deleted, removed int
}

func (failure *cleanupFailure) Error() string { return failure.err.Error() }
func (failure *cleanupFailure) Unwrap() error { return failure.err }
func (failure *cleanupFailure) OperatorDetail() diagnostics.Detail {
	detail := diagnostics.Snapshot("keyring", "cleanup retained generations", failure.resource, failure.err)
	detail.Explanation = fmt.Sprintf("kind=%s phase=%s candidates=%d attempted=%d physical_delete_ack=%d bookkeeping_delete_ack=%d; %s", failure.kind, failure.phase, failure.total, failure.attempted, failure.deleted, failure.removed, detail.Explanation)
	detail.Effect = "preserve retained generations; inspect current authority"
	return detail
}

type cleanupWarning struct {
	namespace Namespace
	effects   string
	err       error
	secret    string
}

// Bind once during construction, before any coordinator operation starts.
func (coordinator *Coordinator) SetDiagnostics(observer diagnostics.HTTPProxyObserver) {
	coordinator.diagnostics = observer
}
func (coordinator *Coordinator) noteCleanup(namespace Namespace, effects string, err error, secret []byte) {
	if err == nil || coordinator.diagnostics == nil {
		return
	}
	if len(coordinator.cleanupWarnings) == 4 {
		if coordinator.cleanupOmitted < 1<<53-1 {
			coordinator.cleanupOmitted++
		}
		return
	}
	coordinator.cleanupWarnings = append(coordinator.cleanupWarnings, cleanupWarning{namespace: namespace, effects: effects, err: err, secret: string(secret)})
}
func (coordinator *Coordinator) observeCleanup(warnings []cleanupWarning, omitted uint64, masking ...string) {
	if coordinator.diagnostics == nil {
		return
	}
	for _, warning := range warnings {
		secrets := append([]string{warning.secret}, masking...)
		detail := diagnostics.Snapshot("keyring", "cleanup after cutover", warning.namespace.owner, warning.err, secrets...)
		clear(secrets)
		detail.Explanation = fmt.Sprintf("%s; cleanup=unconfirmed omitted_warnings=%d; %s", warning.effects, omitted, detail.Explanation)
		coordinator.diagnostics.HTTPProxy(diagnostics.Facts{Event: diagnostics.OperatorFailure, Detail: detail})
	}
	clear(warnings)
}

type cleanupDeferralKey struct{}
type deferredCleanup struct {
	mu       sync.Mutex
	warnings []ownedCleanupWarning
	omitted  uint64
}
type ownedCleanupWarning struct {
	coordinator *Coordinator
	warning     cleanupWarning
}

// DeferCleanupDiagnostics transfers at most four causal observations to the
// enclosing operation. Finish once, after all its admissions and synchronous
// keyring calls have released, with constituent source-known credential values.
// It introduces no worker, sink or timer, and never formats while collecting.
func DeferCleanupDiagnostics(ctx context.Context) (context.Context, func(...string)) {
	pending := &deferredCleanup{}
	return context.WithValue(ctx, cleanupDeferralKey{}, pending), func(masking ...string) {
		pending.mu.Lock()
		warnings, omitted := pending.warnings, pending.omitted
		pending.warnings, pending.omitted = nil, 0
		pending.mu.Unlock()
		for _, entry := range warnings {
			entry.coordinator.observeCleanup([]cleanupWarning{entry.warning}, omitted, masking...)
		}
		clear(warnings)
	}
}

func (coordinator *Coordinator) deliverCleanup(ctx context.Context, warnings []cleanupWarning, omitted uint64) {
	pending, ok := ctx.Value(cleanupDeferralKey{}).(*deferredCleanup)
	if !ok {
		coordinator.observeCleanup(warnings, omitted)
		return
	}
	pending.mu.Lock()
	defer pending.mu.Unlock()
	pending.omitted = min(uint64(1<<53-1), pending.omitted+omitted)
	for _, warning := range warnings {
		if len(pending.warnings) < 4 {
			pending.warnings = append(pending.warnings, ownedCleanupWarning{coordinator: coordinator, warning: warning})
		} else {
			pending.omitted = min(uint64(1<<53-1), pending.omitted+1)
		}
	}
	clear(warnings)
}
