package keyring

import (
	"fmt"

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
func (coordinator *Coordinator) observeCleanup(warnings []cleanupWarning, omitted uint64) {
	if coordinator.diagnostics == nil {
		return
	}
	for _, warning := range warnings {
		detail := diagnostics.Snapshot("keyring", "cleanup after cutover", warning.namespace.owner, warning.err, warning.secret)
		detail.Explanation = fmt.Sprintf("%s; cleanup=unconfirmed omitted_warnings=%d; %s", warning.effects, omitted, detail.Explanation)
		coordinator.diagnostics.HTTPProxy(diagnostics.Facts{Event: diagnostics.OperatorFailure, Detail: detail})
	}
	clear(warnings)
}
