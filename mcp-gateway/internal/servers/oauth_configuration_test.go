package servers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOAuthCompatibilityConfigurationRoundTripAndClearing(t *testing.T) {
	const original = `{"kind":"streamable_http","url":"https://resource.example/mcp","protocol_mode":"modern","authentication":{"mode":"oauth","registration":{"mode":"dynamic","issuer":null},"trusted_origins":[],"request_offline_access":false}}`
	for _, extra := range []string{"", `,"callback_uri":null,"auth_server_metadata_url":null,"scopes":null`, `,"callback_uri":"http://localhost:3118/callback","auth_server_metadata_url":"https://metadata.example/custom/config?tenant=fixture","scopes":["write","read","write"]`, `,"scopes":[]`} {
		raw := strings.TrimSuffix(original, "}}") + extra + "}}"
		transport, err := DecodeTransport([]byte(raw))
		require.NoError(t, err)
		auth := transport.(contract.StreamableHTTPTransport).Authentication.(contract.OAuthAuthentication)
		switch {
		case strings.Contains(extra, `"scopes":[]`):
			require.NotNil(t, auth.Scopes)
			assert.Empty(t, *auth.Scopes)
		case strings.Contains(extra, "localhost"):
			require.NotNil(t, auth.CallbackURI)
			assert.Equal(t, "http://localhost:3118/callback", *auth.CallbackURI)
			require.NotNil(t, auth.Scopes)
			assert.Equal(t, []string{"read", "write"}, *auth.Scopes)
		default:
			assert.Nil(t, auth.CallbackURI)
			assert.Nil(t, auth.AuthServerMetadataURL)
			assert.Nil(t, auth.Scopes)
		}
		encoded, err := json.Marshal(transport)
		require.NoError(t, err)
		roundTrip, err := DecodeTransport(encoded)
		require.NoError(t, err)
		assert.Equal(t, transport, roundTrip)
		if extra == "" {
			assert.JSONEq(t, original, string(encoded))
		}
	}
	for _, extra := range []string{
		`,"callback_uri":123`, `,"callback_uri":"http://example.test:3118/callback"`,
		`,"callback_uri":"http://localhost:3118/callback?"`, `,"auth_server_metadata_url":[]`,
		`,"auth_server_metadata_url":"http://metadata.example/config"`, `,"auth_server_metadata_url":"https://metadata.example/config#"`,
		`,"auth_server_metadata_url":"https://user:secret@metadata.example/config"`,
		`,"scopes":"read write"`, `,"scopes":[null]`, `,"scopes":["read write"]`, `,"scopes":["read\\write"]`,
	} {
		_, err := DecodeTransport([]byte(strings.TrimSuffix(original, "}}") + extra + "}}"))
		require.Error(t, err, extra)
	}
}
