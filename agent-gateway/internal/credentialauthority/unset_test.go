package credentialauthority

import (
	"errors"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
	"github.com/stretchr/testify/require"
)

func TestUnsetRequirementsRefuseBeforeMaterialAccess(t *testing.T) {
	transports := []contract.Transport{
		contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/bin/true", Arguments: []string{}, WorkingDirectory: "/tmp", Environment: map[string]string{}, SecretEnvironment: map[string]string{"TOKEN": "api"}},
		contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: "https://resource.example/mcp", ProtocolMode: contract.ProtocolModern, Authentication: contract.BearerAuthentication{Mode: contract.AuthenticationBearer}},
	}
	oauthCandidate, _, _, _, _ := oauthActivation(t, contract.TokenEndpointAuthNone, 60)
	oauthTransport, err := servers.DecodeTransport(oauthCandidate.Server.Transport)
	require.NoError(t, err)
	transports = append(transports, oauthTransport)
	for _, transport := range transports {
		authority := servers.AuthorityMetadata{RegistrationRevision: "0", CredentialRevisions: contract.CredentialRevisions{StaticCredential: "0", OAuthClient: "0", OAuthTokens: "0"}}
		candidate := activationCandidate(t, transport, authority)
		coordinator := &resolverCoordinator{err: errors.New("unrelated ambient material must not be read")}
		resolver, err := New(&resolverRepository{server: candidate.Server, authority: authority}, coordinator, testInstallationID, func() time.Time { return testNow })
		require.NoError(t, err)
		outcome := resolver.Resolve(t.Context(), candidate)
		require.Equal(t, contract.ServerCredentialAbsent, outcome.CredentialState)
		require.Equal(t, contract.RuntimeAuthenticationRequired, outcome.State)
		require.Equal(t, contract.ReasonCredentialAbsent, *outcome.Reason)
		require.Nil(t, outcome.Lease)
		require.Zero(t, coordinator.calls)
		authority.CredentialRevisions.StaticCredential = "2"
		authority.CredentialRevisions.OAuthTokens = "2"
		candidate.Authority = authority
		outcome = resolver.Resolve(t.Context(), candidate)
		require.NotEqual(t, contract.ServerCredentialAbsent, outcome.CredentialState)
		require.Nil(t, outcome.Lease)
		require.Zero(t, coordinator.calls)
	}
}
