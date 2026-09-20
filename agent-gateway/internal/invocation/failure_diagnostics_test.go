package invocation

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/downstream"
	"github.com/stretchr/testify/require"
)

func TestSafeFailureDiagnosticsClassification(t *testing.T) {
	for _, test := range []struct {
		result         downstream.CallResult
		source, reason string
		code           contract.AgentCallErrorCode
	}{
		{downstream.CallResult{Failure: downstream.FailurePreStart, Err: errors.New("RAW-SECRET")}, "transport", "prestart", contract.ToolUnavailable},
		{downstream.CallResult{Failure: downstream.FailureStartUncertain, Err: errors.New("RAW-SECRET")}, "transport", "handoff_uncertain", contract.OutcomeUnknown},
		{downstream.CallResult{Failure: downstream.FailureResponseInvalid, Err: errors.New("RAW-SECRET")}, "protocol", "invalid_response", contract.DownstreamFailure},
		{downstream.CallResult{Response: downstream.Response{Error: &downstream.RPCError{Message: "RAW-SECRET"}}}, "protocol", "rpc_error", contract.DownstreamFailure},
		{downstream.CallResult{Response: downstream.Response{Result: json.RawMessage(`{"content":"RAW-SECRET"}`)}}, "result_validation", "result_shape", contract.DownstreamFailure},
	} {
		result := SanitizeCallResult(test.result)
		require.Equal(t, test.code, result.ErrorCode)
		require.Equal(t, contract.FailureObservation{Source: test.source, Reason: test.reason}, result.Diagnostics.GatewayObserved)
		require.True(t, result.Diagnostics.ValidFor(result.TerminalClass))
		raw, err := json.Marshal(result)
		require.NoError(t, err)
		require.NotContains(t, string(raw), "RAW-SECRET")
	}
}

func TestSafeFailureDiagnosticsPersistWithoutPayloads(t *testing.T) {
	const canary = "RAW-HEADER-BODY-CREDENTIAL-ARGUMENT-CANARY"
	for index, metadata := range []string{
		`{"version":1,"category":"authentication","phase":"response_status","http_status":401}`,
		`{"version":2,"category":"response_contract","phase":"response_validation","validation":{"schema":"models_result","version":1,"violations":[{"code":"missing","path":"$.models.[].name","rule":"required"}],"truncated":false}}`,
		`{"version":99,"category":"authentication","phase":"response_status"}`,
		`{"version":1,"category":"` + canary + `","phase":"response_status"}`,
		`{"version":1,"category":"authentication","phase":"response_status","raw":"` + canary + `"}`,
		`null`, `[]`,
	} {
		t.Run(metadata, func(t *testing.T) {
			_, audits, authority, _, credential := newAdmissionCoordinator(t, nil)
			lease, err := authority.Authenticate(t.Context(), credential.Bearer)
			require.NoError(t, err)
			defer lease.Release()
			executions := 0
			raw := json.RawMessage(`{"content":[{"type":"text","text":"` + canary + `"}],"isError":true,"_meta":{"arbitrary":"` + canary + `","` + contract.FailureDiagnosticMetaKey + `":` + metadata + `}}`)
			service, err := newService(audits, authority, func(string) (callTarget, bool) {
				return serviceCallTarget(nil, func(context.Context) (executionLease, error) {
					return &serviceExecutionLease{execute: func(json.RawMessage) downstream.CallResult {
						executions++
						return downstream.CallResult{Response: downstream.Response{Result: raw}}
					}}, nil
				}), true
			})
			require.NoError(t, err)
			response := service.Call(t.Context(), lease, CallRequest{Params: callParams(`{"name":"namespace.tool","arguments":{"token":"` + canary + `"}}`), WireValid: true})
			require.Equal(t, 1, executions)
			require.Equal(t, contract.DownstreamFailure, response.ErrorCode)
			require.Equal(t, "tool", response.Diagnostics.GatewayObserved.Source)
			require.Equal(t, index <= 1, response.Diagnostics.ServerReported != nil)
			item, err := audits.Get(t.Context(), response.InvocationID)
			require.NoError(t, err)
			require.Equal(t, response.Diagnostics, item.Diagnostics)
			require.NoError(t, audits.ValidateStartup(t.Context()))
			encoded, err := json.Marshal(item)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), canary)
			require.Contains(t, string(encoded), "[REDACTED]")
			record := onlyInvocationRecord(t, audits)
			encoded, err = json.Marshal(record)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), canary)
		})
	}
}
