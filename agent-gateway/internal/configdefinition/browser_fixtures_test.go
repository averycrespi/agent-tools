package configdefinition

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBrowserResourceDefinitionsUsePublicV2Contract(t *testing.T) {
	raw, err := os.ReadFile("../../web/tests/configuration-fixtures.json")
	require.NoError(t, err)
	var fixtures map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &fixtures))
	require.Len(t, fixtures, 8)
	for _, kind := range []string{"agent", "mcp_server", "mcp_grant", "http_credential", "http_grant", "git_credential", "git_repository", "git_grant"} {
		t.Run(kind, func(t *testing.T) {
			definition, err := DecodeDefinition(fixtures[kind])
			require.NoError(t, err, "mocked browser inputs must be valid real-backend definitions")
			require.Equal(t, kind, definition.Kind)
			require.Equal(t, "portable", definition.Key)
		})
	}
}
