package invocation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
	"github.com/stretchr/testify/require"
)

func TestInvocationFailureWithoutCaptureAndBoundedMasking(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		var output bytes.Buffer
		adapter := diagnostics.New(&output, diagnostics.Warn)
		service := &Service{diagnostics: adapter}
		arguments := strictjson.Value{Type: strictjson.ValueArray, Array: []strictjson.Value{{Type: strictjson.ValueString, String: "actual-argument-secret"}}}
		if oversized {
			arguments.Array = append(arguments.Array, make([]strictjson.Value, 128)...)
		}
		service.observeCallFailure(serviceCallTarget(nil, nil), downstream.CallResult{Failure: downstream.FailureStartUncertain, Err: errors.Join(errors.New("socket closed actual-argument-secret"), errors.New("cleanup failed"))}, &arguments)
		require.True(t, adapter.Finish(nil))
		require.NotContains(t, output.String(), "actual-argument-secret")
		require.Contains(t, output.String(), `"component":"mcp"`)
		require.Contains(t, output.String(), `"effect":"start_uncertain"`)
		require.NotContains(t, output.String(), "invocation_id")
		if oversized {
			require.Contains(t, output.String(), "detail withheld: masking input exceeds bound")
		} else {
			require.Contains(t, output.String(), "socket closed")
			require.Contains(t, output.String(), "cleanup failed")
		}
	}
}

func TestInvocationDiagnosticPrivacyAndUnknownOutcome(t *testing.T) {
	const canary = "sensitive-header-url-argument-downstream-ERROR-canary"
	for _, test := range []struct {
		name   string
		result downstream.CallResult
		want   contract.InvocationTerminalClass
	}{
		{"success", downstream.CallResult{Response: downstream.Response{Result: json.RawMessage(`{"content":[{"type":"text","text":"` + canary + `"}]}`)}}, contract.TerminalSucceeded},
		{"tool error", downstream.CallResult{Response: downstream.Response{Error: &downstream.RPCError{Code: -32000, Message: canary, Data: json.RawMessage(`{"` + canary + `":"` + canary + `"}`)}}}, contract.TerminalDownstreamFailure},
		{"metadata error", downstream.CallResult{Response: downstream.Response{Result: json.RawMessage(`{"content":[{"type":"text","text":"` + canary + `"}],"isError":true,"_meta":{"io.github.averycrespi.agent-tools/failure":{"version":1,"category":"` + canary + `","phase":"exchange"}}}`)}}, contract.TerminalDownstreamFailure},
		{"unknown", downstream.CallResult{Failure: downstream.FailureStartUncertain, Err: errors.New("connection reset after dispatch: " + canary + "\n{\"event\":\"forged\"}")}, contract.TerminalOutcomeUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, audits, authority, _, credential := newAdmissionCoordinator(t, nil)
			var output bytes.Buffer
			level := diagnostics.Debug
			if test.want == contract.TerminalOutcomeUnknown {
				level = diagnostics.Warn
			}
			adapter := diagnostics.New(&output, level)
			defer func() { adapter.Finish(nil); <-adapter.Done() }()
			audits.store.SetDiagnostics(adapter)
			authority.SetDiagnostics(adapter)
			lease, err := authority.Authenticate(t.Context(), credential.Bearer)
			require.NoError(t, err)
			defer lease.Release()
			executions := 0
			service, err := newService(audits, authority, func(string) (callTarget, bool) {
				return serviceCallTarget(nil, func(context.Context) (executionLease, error) {
					return &serviceExecutionLease{execute: func(json.RawMessage) downstream.CallResult { executions++; return test.result }}, nil
				}), true
			})
			require.NoError(t, err)
			service.SetDiagnostics(adapter)
			response := service.Call(t.Context(), lease, CallRequest{Params: callParams(`{"name":"namespace.tool","arguments":{"` + canary + `":"` + canary + `"}}`), WireValid: true})
			require.Equal(t, 1, executions)
			record := onlyInvocationRecord(t, audits)
			require.Equal(t, test.want, *record.TerminalClass)
			if test.want == contract.TerminalOutcomeUnknown {
				require.Equal(t, contract.OutcomeUnknown, response.ErrorCode)
			}
			require.True(t, adapter.Finish(nil))
			<-adapter.Done()
			for _, secret := range []string{canary, credential.Bearer, "namespace.tool", "\n{\"event\":\"forged\"}"} {
				require.NotContains(t, output.String(), secret)
			}
			if test.want == contract.TerminalOutcomeUnknown {
				require.Contains(t, output.String(), `"event":"operator_failure"`)
				require.Contains(t, output.String(), "connection reset after dispatch")
				require.Contains(t, output.String(), `"effect":"start_uncertain"`)
			} else {
				require.Contains(t, output.String(), `"invocation_id":"`+record.InvocationID+`"`)
			}
		})
	}
}
