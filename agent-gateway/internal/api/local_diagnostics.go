package api

import "github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"

func (handler *Handler) observeLocalFailure(operation, resource, effects string, err error, secrets ...string) {
	if err == nil || handler.diagnostics == nil {
		return
	}
	detail := diagnostics.Snapshot("control-api", operation, resource, err, secrets...)
	detail.Explanation = effects + "; " + detail.Explanation
	handler.diagnostics.HTTPProxy(diagnostics.Facts{Event: diagnostics.OperatorFailure, Detail: detail})
}
