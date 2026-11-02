package invocation

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/downstream"
	"github.com/stretchr/testify/require"
)

func TestTrafficServicePersistsSafeFailureDiagnostics(t *testing.T) {
	const canary = "RAW-HEADER-BODY-CREDENTIAL-ARGUMENT-CANARY"
	for _, metadata := range []string{
		`{"version":1,"category":"authentication","phase":"response_status","http_status":401}`,
		`{"version":2,"category":"response_contract","phase":"response_validation","validation":{"schema":"models_result","version":1,"violations":[{"code":"missing","path":"$.models.[].name","rule":"required"}],"truncated":false}}`,
		`{"version":1,"category":"authentication","phase":"response_status","raw":"` + canary + `"}`,
	} {
		t.Run(metadata, func(t *testing.T) {
			_, audits, authority, _, credential := newAdmissionCoordinatorWithoutTraffic(t, nil)
			traffic, _ := trafficFixture(t, nil, nil)
			audits.traffic = traffic
			lease, err := authority.Authenticate(t.Context(), credential.Bearer)
			require.NoError(t, err)
			defer lease.Release()
			calls := 0
			service, err := newService(audits, authority, func(string) (callTarget, bool) {
				return serviceCallTarget(nil, func(context.Context) (executionLease, error) {
					return &serviceExecutionLease{execute: func(json.RawMessage) downstream.CallResult {
						calls++
						return downstream.CallResult{Response: downstream.Response{Result: json.RawMessage(`{"content":[{"type":"text","text":"` + canary + `"}],"isError":true,"_meta":{"` + contract.FailureDiagnosticMetaKey + `":` + metadata + `}}`)}}
					}}, nil
				}), true
			})
			require.NoError(t, err)
			response := service.Call(t.Context(), lease, CallRequest{Params: callParams(`{"name":"namespace.tool","arguments":{"token":"` + canary + `"}}`), WireValid: true})
			require.Equal(t, 1, calls)
			require.Equal(t, contract.DownstreamFailure, response.ErrorCode)
			require.NotNil(t, response.Diagnostics)
			waitTraffic(t, traffic)
			item, err := audits.Get(t.Context(), response.InvocationID)
			require.NoError(t, err)
			require.Equal(t, response.Diagnostics, item.Diagnostics)
			history, err := traffic.History(t.Context(), 0, 10)
			require.NoError(t, err)
			require.Len(t, history.Records, 1)
			raw, err := json.Marshal(history.Records)
			require.NoError(t, err)
			require.NotContains(t, string(raw), canary)
			require.Contains(t, string(raw), "[REDACTED]")
		})
	}
}

func TestTrafficDiagnosticsRestart(t *testing.T) {
	traffic, owner := trafficFixture(t, nil, nil)
	observation := recordMCP(t, traffic, trafficPrepared(1))
	completion := trafficCompletion()
	completion.Class = contract.TerminalDownstreamFailure
	diagnostic := &contract.FailureDiagnostics{GatewayObserved: contract.FailureObservation{Source: "protocol", Reason: "rpc_error"}}
	require.NoError(t, traffic.ObserveMCPCompletion(observation, completion, diagnostic))
	waitTraffic(t, traffic)
	require.NoError(t, traffic.Close())
	reopened, err := OpenTraffic(t.Context(), owner, invocationTestInstallationID, invocationID(90), traffic.config)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	history, err := reopened.History(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, history.Records, 1)
	require.Equal(t, diagnostic, history.Records[0].Diagnostics)
	require.Equal(t, invocationID(1), history.Records[0].InvocationID)
}

func TestTrafficInvalidDiagnosticsSettleWithoutTerminal(t *testing.T) {
	traffic, _ := trafficFixture(t, nil, nil)
	observation := recordMCP(t, traffic, trafficPrepared(1))
	diagnostic := &contract.FailureDiagnostics{GatewayObserved: contract.FailureObservation{Source: "tool", Reason: "reported_error"}}
	require.ErrorIs(t, traffic.ObserveMCPCompletion(observation, trafficCompletion(), diagnostic), ErrInvalidInput)
	history, err := traffic.History(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Nil(t, history.Records[0].TerminalClass)
	require.Nil(t, history.Records[0].Diagnostics)
	require.True(t, traffic.Healthy())
}
