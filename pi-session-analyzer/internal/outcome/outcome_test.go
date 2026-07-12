package outcome

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClassifyCallUsesDeterministicOutcomePrecedence(t *testing.T) {
	t.Parallel()

	truth, falsity := true, false
	tests := []struct {
		name     string
		callName string
		results  []Result
		want     Classification
	}{
		{name: "confirmed wins", callName: "mcp_call", results: []Result{{IsError: &falsity}, {Content: "mcp_call failed"}, {IsError: &truth}}, want: ConfirmedError},
		{name: "inferred before success", callName: "mcp_call", results: []Result{{IsError: &falsity}, {Content: "fetch failed"}}, want: InferredError},
		{name: "result name scopes inference", callName: "other", results: []Result{{Name: "mcp_call", Content: "mcp error"}}, want: InferredError},
		{name: "non MCP text remains unknown", callName: "bash", results: []Result{{Content: "fetch failed"}}, want: Unknown},
		{name: "explicit success", callName: "bash", results: []Result{{IsError: &falsity}}, want: Success},
		{name: "no result", callName: "bash", want: Unknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, ClassifyCall(tt.callName, tt.results))
		})
	}
}

func TestInferredMCPFailureRequiresUnlabeledNarrowPhrase(t *testing.T) {
	t.Parallel()

	truth := true
	require.True(t, IsInferredMCPFailure("mcp_call", "", nil, "MCP_CALL failed: unavailable"))
	require.True(t, IsInferredMCPFailure("", "mcp_call", nil, "fetch failed"))
	require.False(t, IsInferredMCPFailure("mcp_call", "", &truth, "mcp error"))
	require.False(t, IsInferredMCPFailure("mcp_call", "", nil, "ordinary failure"))
	require.False(t, IsInferredMCPFailure("bash", "", nil, "mcp error"))
}
