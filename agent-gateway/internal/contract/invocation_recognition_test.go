package contract

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInvocationRecognitionIsDetailOnlySidecar(t *testing.T) {
	recognition := &InvocationRecognition{Server: &InvocationServerRecognition{ServerID: "server", DisplayName: "Current"}, Grant: &InvocationGrantRecognition{GrantID: "grant", Description: ""}}
	item := Invocation{Recognition: recognition}
	encoded, err := json.Marshal(item)
	require.NoError(t, err)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &fields))
	require.JSONEq(t, `{"server":{"server_id":"server","display_name":"Current"},"grant":{"grant_id":"grant","description":""}}`, string(fields["recognition"]))
	item.Recognition = &InvocationRecognition{}
	encoded, err = json.Marshal(item)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encoded, &fields))
	require.JSONEq(t, `{"server":null,"grant":null}`, string(fields["recognition"]))
	encoded, err = json.Marshal(InvocationPage{Items: []InvocationSummary{item.InvocationSummary}})
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "recognition")
	encoded, err = json.Marshal(InvocationAuditRecord{})
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "recognition")
}
