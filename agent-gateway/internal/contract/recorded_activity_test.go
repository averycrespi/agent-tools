package contract

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordedActivityContract(t *testing.T) {
	route, ok := RouteForPath("/api/v2/recorded-activity")
	require.True(t, ok)
	assert.Equal(t, AuthorityAdmin, route.Authority)
	assert.Equal(t, []string{"GET"}, route.Methods)
	var matches []ResourceMechanic
	for _, mechanic := range ResourceMechanics() {
		if mechanic.Pattern == route.Pattern {
			matches = append(matches, mechanic)
		}
	}
	require.Equal(t, []ResourceMechanic{{Pattern: route.Pattern, Method: "GET", RequestSchema: "None", SuccessSchema: "RecordedActivitySummary", SuccessStatuses: []int{200}}}, matches)
	assert.Equal(t, 60, RecordedActivityBuckets)
	assert.Equal(t, 15, RecordedActivityWindow)
	assert.Equal(t, uint64(9007199254740991), RecordedActivityMaxCount)
	encoded, err := json.Marshal(RecordedActivityBucket{Coverage: "unavailable"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"start":"","end":"","observed_start":null,"coverage":"unavailable","counts":null}`, string(encoded))
	encoded, err = json.Marshal(RecordedProtocols{})
	require.NoError(t, err)
	var protocols map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &protocols))
	assert.Len(t, protocols, 4)
	for _, protocol := range []string{"mcp", "http_request", "connect", "http_unclassified"} {
		assert.Contains(t, protocols, protocol)
	}
}
