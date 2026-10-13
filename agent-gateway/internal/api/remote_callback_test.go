package api

import (
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestConfiguredCallbackSelection(t *testing.T) {
	for _, test := range []struct {
		origin, callback string
		valid            bool
	}{
		{"https://gateway.example:8443", "https://gateway.example:8443/oauth/callback", true},
		{"https://gateway.example:8443", "https://gateway.example/oauth/callback", false},
		{"https://gateway.example:8443", "https://other.example:8443/oauth/callback", false},
		{"https://gateway.example:8443", "https://gateway.example:8443/other", false},
		{"https://gateway.example:8443", "http://localhost:3118/callback", false},
		{"http://127.0.0.1:8210", "https://gateway.example:8443/oauth/callback", false},
		{"http://127.0.0.1:8210", "http://localhost:3118/callback", true},
	} {
		handler := New(Options{Origin: test.origin})
		transport := contract.StreamableHTTPTransport{Authentication: contract.OAuthAuthentication{CallbackURI: &test.callback}}
		err := handler.validateCallbackSelection(transport)
		if test.valid {
			require.NoError(t, err)
		} else {
			require.Error(t, err)
		}
	}
}
