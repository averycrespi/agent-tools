package main

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestInvocationCLIShowsDiagnosticProvenance(t *testing.T) {
	status, retry := 429, 12
	item := contract.Invocation{InvocationSummary: contract.InvocationSummary{Outcome: contract.InvocationOutcome{Class: contract.InvocationOutcomeDownstreamFailure}}}
	raw, err := json.Marshal(item)
	require.NoError(t, err)
	legacy, err := invocationItemTable(raw)
	require.NoError(t, err)
	require.Len(t, legacy.Rows, 1)
	item.Diagnostics = &contract.FailureDiagnostics{GatewayObserved: contract.FailureObservation{Source: "tool", Reason: "reported_error"}, ServerReported: &contract.ServerFailureDiagnostic{Version: 1, Category: "rate_limit", Phase: "response_status", HTTPStatus: &status, RetryAfterSeconds: &retry}}
	raw, err = json.Marshal(item)
	require.NoError(t, err)
	table, err := invocationItemTable(raw)
	require.NoError(t, err)
	text := fmt.Sprint(table.Rows)
	for _, expected := range []string{"Gateway observed", "Server reported (unverified)", "rate_limit", "response_status", "HTTP 429", "retry guidance 12s (not permission)"} {
		require.Contains(t, text, expected)
	}
	item.Diagnostics.ServerReported.Category = "RAW-SECRET"
	raw, err = json.Marshal(item)
	require.NoError(t, err)
	_, err = invocationItemTable(raw)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "RAW-SECRET")
}
