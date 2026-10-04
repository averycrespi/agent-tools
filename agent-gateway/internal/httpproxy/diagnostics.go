package httpproxy

import (
	"context"
	"errors"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
)

func (e *Engine) observeRejection(started time.Time, stage diagnostics.Stage, cause diagnostics.Cause) {
	if e.options.Diagnostics == nil {
		return
	}
	e.options.Diagnostics.HTTPProxy(diagnostics.Facts{
		Event: diagnostics.HTTPProxyRejected, Stage: stage, Cause: cause,
		Duration: min(max(time.Since(started), 0), contract.DiagnosticElapsedMaximum),
	})
}

func proxyFailureCause(err error) diagnostics.Cause {
	switch {
	case errors.Is(err, context.Canceled):
		return diagnostics.Cancelled
	case errors.Is(err, context.DeadlineExceeded):
		return diagnostics.Expired
	default:
		return diagnostics.Unavailable
	}
}
