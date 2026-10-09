package oauth

import "github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"

type disconnectCause struct {
	phase string
	err   error
}

func (failure *disconnectCause) Error() string { return failure.phase + ": " + failure.err.Error() }
func (failure *disconnectCause) Unwrap() error { return failure.err }
func withDisconnectCause(phase string, err error) error {
	return &disconnectCause{phase: phase, err: err}
}

func (service *DisconnectService) SetDiagnostics(observer diagnostics.HTTPProxyObserver) {
	service.diagnostics = observer
}
func (service *DisconnectService) observeDisconnect(serverID, facts string, err error, secrets ...string) {
	if service.diagnostics == nil {
		return
	}
	detail := diagnostics.Snapshot("oauth", "disconnect and cleanup", serverID, err, secrets...)
	detail.Explanation = facts + detail.Explanation
	detail.Effect = "inspect local authority and remote provider; no replay"
	service.diagnostics.HTTPProxy(diagnostics.Facts{Event: diagnostics.OperatorFailure, Detail: detail})
}
