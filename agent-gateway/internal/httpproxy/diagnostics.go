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

func (e *Engine) observeRejection(started time.Time, stage diagnostics.Stage, cause diagnostics.Cause, err error, writer http.ResponseWriter) {
	id, resource := "", ""
	var secrets []string
	if w, ok := writer.(*failureWriter); ok {
		id, resource, secrets = w.id, w.resource, w.secrets
	}
	detail := diagnostics.Snapshot("proxy", "admit", "", err, secrets...)
	if detail.Resource != "" && detail.Resource != resource {
		detail.Explanation = "selected_resource=" + detail.Resource + "; " + detail.Explanation
	}
	detail.Resource = diagnostics.Text(resource, 160, secrets...)
	e.observeProxy(started, diagnostics.HTTPProxyRejected, stage, cause, id, detail)
}

func (e *Engine) observeProxy(started time.Time, event diagnostics.Event, stage diagnostics.Stage, cause diagnostics.Cause, id string, details ...diagnostics.Detail) {
	if e.options.Diagnostics == nil {
		return
	}
	var detail diagnostics.Detail
	if len(details) != 0 {
		detail = details[0]
	}
	e.options.Diagnostics.HTTPProxy(diagnostics.Facts{
		Detail: detail,
		Event:  event, Stage: stage, Cause: cause, ProxyID: id,
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
