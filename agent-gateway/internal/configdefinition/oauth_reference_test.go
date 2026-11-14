package configdefinition

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefinitionRequiresNamespaceDerivedOAuthSecretReference(t *testing.T) {
	oauth := strings.Replace(transportSpec, `{"mode":"none"}`, `{"mode":"oauth","registration":{"mode":"static","issuer":"https://issuer.example","client_id":"configured-client","token_endpoint_auth_method":"client_secret_basic"},"trusted_origins":[],"request_offline_access":false}`, 1)
	oauth = strings.Replace(oauth, `"oauth_client_secret":null,"oauth_authorization":false`, `"oauth_client_secret":"mcp/upstream/oauth/client_secret","oauth_authorization":true`, 1)
	valid := definition("mcp_server", "upstream", oauth)
	_, err := Normalize(valid)
	require.NoError(t, err)
	for _, wrong := range []string{"mcp/another/oauth/client_secret", "NOT_A_SECRET_REFERENCE", ""} {
		changed := valid
		changed.Spec = json.RawMessage(strings.Replace(oauth, "mcp/upstream/oauth/client_secret", wrong, 1))
		_, err := Normalize(changed)
		require.ErrorIs(t, err, ErrInvalid)
	}
}
