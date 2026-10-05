package httpproxy

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/remote"
)

func (e *Engine) observeCompletion(protocol diagnostics.Protocol, outcome string, started time.Time) {
	result := diagnostics.Unknown
	switch outcome {
	case "succeeded":
		result = diagnostics.Succeeded
	case "prestart_failure":
		result = diagnostics.PrestartFailure
	case "nonmutation":
		result = diagnostics.Nonmutation
	}
	e.options.Observations.Result(protocol, result)
	e.options.Observations.Latency(protocol, diagnostics.ExecutionStage, time.Since(started))
}

func (e *Engine) observeRejection(started time.Time, stage diagnostics.Stage, cause diagnostics.Cause, writers ...http.ResponseWriter) {
	id := ""
	if len(writers) != 0 {
		if w, ok := writers[0].(*failureWriter); ok {
			id = w.id
		}
	}
	e.observeProxy(started, diagnostics.HTTPProxyRejected, stage, cause, id)
}

func (e *Engine) observeProxy(started time.Time, event diagnostics.Event, stage diagnostics.Stage, cause diagnostics.Cause, id string) {
	if e.options.Diagnostics == nil {
		return
	}
	e.options.Diagnostics.HTTPProxy(diagnostics.Facts{
		Event: event, Stage: stage, Cause: cause, ProxyID: id,
		Duration: min(max(time.Since(started), 0), contract.DiagnosticElapsedMaximum),
	})
}

func proxyFailureCause(err error) diagnostics.Cause {
	switch {
	case errors.Is(err, context.Canceled):
		return diagnostics.Cancelled
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, remote.ErrProxyTimeout), transferCondition(err) == "timeout":
		return diagnostics.Expired
	default:
		return diagnostics.Unavailable
	}
}
