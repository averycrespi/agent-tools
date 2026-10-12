package catalog

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// Keep downstream schema compatibility Gateway-owned, independent of any backend binary.
func TestTraversalNormalizesNestedSchemaCatalog(t *testing.T) {
	const empty = `{"type":"object","properties":{},"additionalProperties":false}`
	const nested = `{"type":"object","properties":{"items":{"type":"array","minItems":1,"maxItems":8,"items":{"oneOf":[{"type":"object","properties":{"kind":{"const":"choice"},"values":{"type":"object","minProperties":1,"additionalProperties":{"type":["string","null"]}}},"required":["kind","values"],"additionalProperties":false},{"type":"object","properties":{"kind":{"const":"score"},"value":{"type":"number","minimum":0,"maximum":1},"date":{"type":"string","pattern":"^[0-9]{4}-[0-9]{2}-[0-9]{2}$"}},"required":["kind","value"],"additionalProperties":false}]}}},"required":["items"],"additionalProperties":false}`
	page := json.RawMessage(`{"tools":[{"name":"list","inputSchema":` + empty + `,"outputSchema":` + nested + `},{"name":"judge","inputSchema":` + nested + `,"outputSchema":` + nested + `}]}`)
	candidate, err := NewTraverser().Traverse(t.Context(), &scriptedClient{results: []json.RawMessage{page}}, "fixture")
	require.NoError(t, err)
	require.Empty(t, candidate.Issues)
	normalized := NormalizeCandidate(candidate, NormalizeOptions{ServerID: "fixture"})
	require.Empty(t, normalized.Issues)
	require.Len(t, normalized.Tools, 2)
	for index, name := range []string{"list", "judge"} {
		tool := normalized.Tools[index]
		require.Equal(t, name, tool.Key.UpstreamName)
		require.Equal(t, "fixture."+name, tool.ExternalName)
		input := nested
		if index == 0 {
			input = empty
		}
		require.JSONEq(t, input, string(tool.Descriptor.InputSchema))
		require.JSONEq(t, nested, string(tool.Descriptor.OutputSchema))
	}
}
