package invocation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/stretchr/testify/require"
)

func TestInvocationDiagnosticPrivacyAndUnknownOutcome(t *testing.T) {
	const canary = "sensitive-header-url-argument-downstream-ERROR-canary"
	for _, test := range []struct {
		name   string
		result downstream.CallResult
		want   contract.InvocationTerminalClass
	}{
		{"success", downstream.CallResult{Response: downstream.Response{Result: json.RawMessage(`{"content":[{"type":"text","text":"` + canary + `"}]}`)}}, contract.TerminalSucceeded},
		{"tool error", downstream.CallResult{Response: downstream.Response{Error: &downstream.RPCError{Code: -32000, Message: canary, Data: json.RawMessage(`{"` + canary + `":"` + canary + `"}`)}}}, contract.TerminalDownstreamFailure},
		{"unknown", downstream.CallResult{Failure: downstream.FailureStartUncertain, Err: errors.New(canary + "\n{\"event\":\"forged\"}")}, contract.TerminalOutcomeUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, audits, authority, _, credential := newAdmissionCoordinator(t, nil)
			var output bytes.Buffer
			adapter := diagnostics.New(&output, diagnostics.Debug)
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
			for _, secret := range []string{canary, credential.Bearer, "namespace.tool", "forged"} {
				require.NotContains(t, output.String(), secret)
			}
			require.Contains(t, output.String(), `"invocation_id":"`+record.InvocationID+`"`)
			if test.want == contract.TerminalOutcomeUnknown {
				require.Contains(t, output.String(), `"cause":"unknown_outcome"`)
			}
		})
	}
}
