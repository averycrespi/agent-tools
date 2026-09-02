//go:build keyringnative

package material_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/credentialauthority"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/oauth"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/runtimes"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servercredentials"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeCompleteCredentialGenerationsAcquireAndClean(t *testing.T) {
	if os.Getenv("MCP_GATEWAY_KEYRING_NATIVE") != "1" {
		t.Skip("native keyring harness did not establish a disposable backend")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ownership, err := paths.Acquire(testutil.NewOwnerOnlyDataRoot(t))
	require.NoError(t, err)
	store, err := storage.Initialize(ctx, ownership, installationID)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
		require.NoError(t, ownership.MarkClean())
		require.NoError(t, ownership.Close())
	})
	provider, err := keyring.NewProvider(installationID)
	require.NoError(t, err)
	assert.Equal(t, contract.KeyringReady, provider.Probe(ctx).State)
	coordinator := keyring.NewCoordinator(provider, store, fixedClock{}, rand.Reader)

	staticTransport := contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/fixture/mcp", Arguments: []string{}, WorkingDirectory: "/", Environment: map[string]string{}, SecretEnvironment: map[string]string{"TOKEN": "api"}}
	staticMaterial, err := servercredentials.EncodeStaticGeneration(map[string]string{"api": staticCanary})
	require.NoError(t, err)
	staticNamespace, err := keyring.NewNamespace(installationID, serverID, keyring.RecordStaticCredential)
	require.NoError(t, err)
	staticPublished, err := coordinator.Replace(ctx, staticNamespace, staticMaterial)
	require.NoError(t, err)
	staticRepository := &repository{server: candidateServer(t, staticTransport), authority: servers.AuthorityMetadata{CredentialRevisions: contract.CredentialRevisions{StaticCredential: staticPublished.Revision}, StaticCredentialHandle: pointer(string(staticPublished.Handle))}}
	staticCandidate := runtimes.Candidate{Server: staticRepository.server, Authority: staticRepository.authority, RuntimeID: "01ARZ3NDEKTSV4RRFFQ69G5FAX", Generation: 1}
	staticResolver, err := credentialauthority.New(staticRepository, coordinator, installationID, func() time.Time { return now })
	require.NoError(t, err)
	staticOutcome := staticResolver.Resolve(ctx, staticCandidate)
	require.NotNil(t, staticOutcome.Lease)
	owner := runtimes.NewRuntimeOwner()
	staticKey, err := owner.Admit(staticCandidate, staticOutcome.Lease, nil)
	require.NoError(t, err)
	staticSecret, ok := owner.Material(staticKey, contract.ServerCredentialStatic)
	require.True(t, ok)
	assert.True(t, owner.Release(staticKey, true))
	assert.Equal(t, make([]byte, len(staticSecret)), staticSecret)
	require.NoError(t, provider.DeleteGeneration(ctx, staticNamespace, staticPublished.Handle))

	registration := servers.OAuthRegistrationAuthority{Revision: "1", Mode: contract.RegistrationStatic, Issuer: "https://issuer.example", ClientID: "public-client", CallbackURL: "http://127.0.0.1:8210/oauth/callback", ResourceURL: "https://resource.example/mcp", TokenEndpointAuthMethod: contract.TokenEndpointAuthNone, CreatedAt: now.Format(time.RFC3339Nano)}
	desired := contract.StaticOAuthRegistration{Mode: contract.RegistrationStatic, Issuer: &registration.Issuer, ClientID: registration.ClientID, TokenEndpointAuthMethod: contract.TokenEndpointAuthNone}
	oauthTransport := contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: registration.ResourceURL, ProtocolMode: contract.ProtocolModern, Authentication: contract.OAuthAuthentication{Mode: contract.AuthenticationOAuth, Registration: desired, TrustedOrigins: []string{}}}
	expires, refresh := now.Add(time.Hour).Format(time.RFC3339Nano), refreshCanary
	oauthMaterial, err := json.Marshal(oauth.TokenGeneration{Version: 1, ServerID: serverID, Issuer: registration.Issuer, RegistrationRevision: registration.Revision, Resource: registration.ResourceURL, AccessToken: accessCanary, RefreshToken: &refresh, Scopes: []string{"read"}, ScopeSpecified: true, IssuedAt: now.Format(time.RFC3339Nano), ExpiresAt: &expires})
	require.NoError(t, err)
	oauthNamespace, err := keyring.NewNamespace(installationID, serverID, keyring.RecordOAuthTokens)
	require.NoError(t, err)
	oauthPublished, err := coordinator.Replace(ctx, oauthNamespace, oauthMaterial)
	require.NoError(t, err)
	oauthRepository := &repository{server: candidateServer(t, oauthTransport), registration: registration, authority: servers.AuthorityMetadata{RegistrationRevision: "1", CredentialRevisions: contract.CredentialRevisions{OAuthTokens: oauthPublished.Revision}, OAuthTokensHandle: pointer(string(oauthPublished.Handle))}}
	oauthCandidate := runtimes.Candidate{Server: oauthRepository.server, Authority: oauthRepository.authority, RuntimeID: "01ARZ3NDEKTSV4RRFFQ69G5FAY", Generation: 1}
	oauthResolver, err := credentialauthority.New(oauthRepository, coordinator, installationID, func() time.Time { return now })
	require.NoError(t, err)
	oauthOutcome := oauthResolver.Resolve(ctx, oauthCandidate)
	require.NotNil(t, oauthOutcome.Lease)
	oauthKey, err := owner.Admit(oauthCandidate, oauthOutcome.Lease, nil)
	require.NoError(t, err)
	oauthSecret, ok := owner.Material(oauthKey, contract.ServerCredentialOAuthTokens)
	require.True(t, ok)
	assert.Equal(t, accessCanary, string(oauthSecret))
	assert.NotContains(t, string(oauthSecret), refreshCanary)
	assert.True(t, owner.Release(oauthKey, true))
	assert.Equal(t, make([]byte, len(oauthSecret)), oauthSecret)
	require.NoError(t, provider.DeleteGeneration(ctx, oauthNamespace, oauthPublished.Handle))
}
