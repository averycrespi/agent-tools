package mcpingress

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestCallFailureDiagnosticsPreserveOutcomeAndRejectUnsafeClaims(t *testing.T) {
	for _, code := range []contract.AgentCallErrorCode{contract.ToolUnavailable, contract.DownstreamFailure, contract.OutcomeUnknown} {
		for _, source := range []string{"tool", "RAW-SECRET"} {
			response := ToolsCallResponse{ErrorCode: code, InvocationID: "01J60000000000000000000003", Diagnostics: &contract.FailureDiagnostics{GatewayObserved: contract.FailureObservation{Source: source, Reason: "reported_error"}}}
			raw, err := encodeToolsCallResponse(context.Background(), json.RawMessage(`1`), response)
			require.NoError(t, err)
			require.Contains(t, string(raw), `"code":"`+string(code)+`"`)
			require.NotContains(t, string(raw), "RAW-SECRET")
			require.Equal(t, code == contract.DownstreamFailure && source == "tool", containsDiagnostic(t, raw))
			if code == contract.OutcomeUnknown {
				require.Contains(t, string(raw), `"outcomeUnknown":true`)
			}
		}
	}
}
func containsDiagnostic(t *testing.T, raw []byte) bool {
	t.Helper()
	var envelope struct {
		Error struct {
			Data contract.AgentCallErrorData `json:"data"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(raw, &envelope))
	return envelope.Error.Data.Diagnostics != nil
}
