package main

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	"github.com/stretchr/testify/require"
)

func TestDocumentedUpstreamExamples(t *testing.T) {
	contents, err := os.ReadFile("../../docs/operators/upstream-servers.md")
	require.NoError(t, err)
	blocks := regexp.MustCompile("(?s)```json\\n(.*?)\\n```").FindAllSubmatch(contents, -1)
	require.Len(t, blocks, 4, "three server definitions and one complete credential replacement")
	for index, block := range blocks {
		var object map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(block[1], &object))
		if transport, ok := object["transport"]; ok {
			require.Len(t, object, 4)
			require.JSONEq(t, "false", string(object["enabled"]), "examples must not launch before deliberate enablement")
			require.NoError(t, validateServerTransportInput(transport), "example %d", index)
			continue
		}
		kind, canonical, err := validateCredentialReplacementInput(block[1])
		require.NoError(t, err)
		require.Equal(t, contract.ServerCredentialStatic, kind)
		var replacement contract.StaticCredentialReplacement
		require.NoError(t, controlclient.DecodeResponse(canonical, &replacement))
		require.Equal(t, map[string]string{"bearer": "synthetic-example-not-a-real-token"}, replacement.Values)
	}
}
