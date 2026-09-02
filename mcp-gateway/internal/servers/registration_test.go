package servers

import (
	"context"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublicOAuthRegistrationPublishesExactRevisionedAuthority(t *testing.T) {
	repository, _, _ := newRepository(t, new(sequenceReader))
	created := mustCreateOAuthServer(t, repository, "public-registration", contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic})
	authority := testRegistrationAuthority(contract.RegistrationDynamic, contract.TokenEndpointAuthNone)
	published, err := repository.PublishPublicRegistration(context.Background(), RegistrationFence{
		ServerID: created.ID, ExpectedDesiredRevision: "1", ExpectedRegistrationRevision: "0", ExpectedOAuthClientRevision: "0",
	}, authority)
	require.NoError(t, err)
	assert.Equal(t, "1", published.Revision)
	read, err := repository.OAuthRegistration(context.Background(), created.ID)
	require.NoError(t, err)
	assert.Equal(t, published, read)
	metadata, err := repository.Authority(context.Background(), created.ID)
	require.NoError(t, err)
	assert.Equal(t, "1", metadata.RegistrationRevision)
	assert.Equal(t, "0", metadata.CredentialRevisions.OAuthClient)
	assert.Nil(t, metadata.OAuthClientHandle)

	_, err = repository.PublishPublicRegistration(context.Background(), RegistrationFence{
		ServerID: created.ID, ExpectedDesiredRevision: "1", ExpectedRegistrationRevision: "0", ExpectedOAuthClientRevision: "0",
	}, authority)
	assert.ErrorIs(t, err, ErrStaleRevision)
}

func mustCreateOAuthServer(t *testing.T, repository *Repository, namespace string, registration contract.OAuthRegistration) Server {
	t.Helper()
	created, err := repository.Create(context.Background(), CreateRequest{Definition: Definition{
		Namespace: namespace, DisplayName: namespace, Enabled: false,
		Transport: contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: "https://resource.example/mcp", ProtocolMode: contract.ProtocolModern, Authentication: contract.OAuthAuthentication{Mode: contract.AuthenticationOAuth, Registration: registration, TrustedOrigins: []string{}, RequestOfflineAccess: false}},
	}, Idempotency: idempotency(namespace, namespace, "")})
	require.NoError(t, err)
	return created.Server
}

func testRegistrationAuthority(mode contract.RegistrationMode, method contract.TokenEndpointAuthMethod) OAuthRegistrationAuthority {
	return OAuthRegistrationAuthority{Revision: "0", Mode: mode, Issuer: "https://issuer.example", ClientID: "client-id", CallbackURL: "http://127.0.0.1:8210/oauth/callback", ResourceURL: "https://resource.example/mcp", TokenEndpointAuthMethod: method, CreatedAt: testTime.Format(time.RFC3339Nano)}
}
