package credentialauthority

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/oauth"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/runtimes"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servercredentials"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testInstallationID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	testServerID       = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
)

var testNow = time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

func TestResolverValidatesExactStaticSlotsAndBinding(t *testing.T) {
	handle := testHandle(t, 1)
	contents, err := servercredentials.EncodeStaticGeneration(map[string]string{"api": "secret"})
	require.NoError(t, err)
	coordinator := &resolverCoordinator{values: map[keyring.RecordKind]resolverRead{keyring.RecordStaticCredential: {contents: contents, result: keyring.CutoverResult{Handle: handle, Revision: "1"}}}}
	resolver, err := New(&resolverRepository{}, coordinator, testInstallationID, func() time.Time { return testNow })
	require.NoError(t, err)
	transport := marshalTransport(t, contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/bin/server", Arguments: []string{}, WorkingDirectory: "/tmp", Environment: map[string]string{}, SecretEnvironment: map[string]string{"TOKEN": "api"}})
	candidate := runtimes.Candidate{Server: servers.Server{ID: testServerID, Transport: transport}, Authority: servers.AuthorityMetadata{CredentialRevisions: contract.CredentialRevisions{StaticCredential: "1"}, StaticCredentialHandle: pointer(string(handle))}}
	assert.Equal(t, contract.ServerCredentialReady, resolver.Resolve(context.Background(), candidate).CredentialState)
	candidate.Authority.StaticCredentialHandle = pointer(string(testHandle(t, 2)))
	outcome := resolver.Resolve(context.Background(), candidate)
	assert.Equal(t, contract.RuntimeAuthenticationRequired, outcome.State)
	assert.Equal(t, contract.ServerCredentialAbsent, outcome.CredentialState)
}

func TestResolverValidatesOAuthRegistrationClientTokenAndExpiry(t *testing.T) {
	clientHandle, tokenHandle := testHandle(t, 3), testHandle(t, 4)
	expires := testNow.Add(time.Hour).Format(time.RFC3339Nano)
	refresh := "refresh"
	tokenBytes, err := json.Marshal(oauth.TokenGeneration{Version: 1, ServerID: testServerID, Issuer: "https://issuer.example", RegistrationRevision: "1", Resource: "https://resource.example/mcp", AccessToken: "access", RefreshToken: &refresh, IssuedAt: testNow.Add(-time.Minute).Format(time.RFC3339Nano), ExpiresAt: &expires})
	require.NoError(t, err)
	registration := servers.OAuthRegistrationAuthority{Revision: "1", Mode: contract.RegistrationStatic, Issuer: "https://issuer.example", ClientID: "client", CallbackURL: "http://127.0.0.1:8210/oauth/callback", ResourceURL: "https://resource.example/mcp", TokenEndpointAuthMethod: contract.TokenEndpointAuthClientSecretPost, CreatedAt: testNow.Format(time.RFC3339Nano)}
	transport := marshalTransport(t, contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: registration.ResourceURL, ProtocolMode: contract.ProtocolModern, Authentication: contract.OAuthAuthentication{Mode: contract.AuthenticationOAuth, Registration: contract.StaticOAuthRegistration{Mode: contract.RegistrationStatic, Issuer: &registration.Issuer, ClientID: registration.ClientID, TokenEndpointAuthMethod: registration.TokenEndpointAuthMethod}, TrustedOrigins: []string{}}})
	coordinator := &resolverCoordinator{values: map[keyring.RecordKind]resolverRead{
		keyring.RecordOAuthClient: {contents: []byte("secret"), result: keyring.CutoverResult{Handle: clientHandle, Revision: "1"}},
		keyring.RecordOAuthTokens: {contents: tokenBytes, result: keyring.CutoverResult{Handle: tokenHandle, Revision: "1"}},
	}}
	resolver, err := New(&resolverRepository{registration: registration}, coordinator, testInstallationID, func() time.Time { return testNow })
	require.NoError(t, err)
	candidate := runtimes.Candidate{Server: servers.Server{ID: testServerID, Transport: transport}, Authority: servers.AuthorityMetadata{RegistrationRevision: "1", CredentialRevisions: contract.CredentialRevisions{OAuthClient: "1", OAuthTokens: "1"}, OAuthClientHandle: pointer(string(clientHandle)), OAuthTokensHandle: pointer(string(tokenHandle))}}
	assert.Equal(t, contract.ServerCredentialReady, resolver.Resolve(context.Background(), candidate).CredentialState)
	expired := testNow.Format(time.RFC3339Nano)
	registration.ClientSecretExpiresAt = &expired
	resolver.repository = &resolverRepository{registration: registration}
	outcome := resolver.Resolve(context.Background(), candidate)
	assert.Equal(t, contract.ServerCredentialReauthenticationRequired, outcome.CredentialState)
	assert.Equal(t, contract.ReasonRegistrationExpired, *outcome.Reason)
}

func TestResolverPreservesKeyringCapabilityClassesAndAdmission(t *testing.T) {
	states := []struct {
		err        error
		credential contract.ServerCredentialState
		reason     contract.PublicReason
		retry      bool
	}{
		{err: &keyring.CapabilityError{Capability: keyring.Capability{State: contract.KeyringAbsent}}, credential: contract.ServerCredentialAbsent, reason: contract.ReasonKeyringAbsent},
		{err: &keyring.CapabilityError{Capability: keyring.Capability{State: contract.KeyringLocked}}, credential: contract.ServerCredentialLocked, reason: contract.ReasonKeyringLocked},
		{err: &keyring.CapabilityError{Capability: keyring.Capability{State: contract.KeyringInteractionRequired}}, credential: contract.ServerCredentialInteractionRequired, reason: contract.ReasonKeyringInteractionRequired},
		{err: &keyring.CapabilityError{Capability: keyring.Capability{State: contract.KeyringUnavailable}}, credential: contract.ServerCredentialUnavailable, reason: contract.ReasonKeyringUnavailable, retry: true},
		{err: &keyring.CapabilityError{Capability: keyring.Capability{State: contract.KeyringUnsupported}}, credential: contract.ServerCredentialUnsupported, reason: contract.ReasonKeyringUnsupported},
		{err: keyring.ErrWorkLimit, credential: contract.ServerCredentialUnavailable, reason: contract.ReasonResourceLimit, retry: true},
	}
	transport := marshalTransport(t, contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: "http://127.0.0.1:9000/mcp", ProtocolMode: contract.ProtocolModern, Authentication: contract.BearerAuthentication{Mode: contract.AuthenticationBearer}})
	for _, test := range states {
		resolver, err := New(&resolverRepository{}, &resolverCoordinator{err: test.err}, testInstallationID, func() time.Time { return testNow })
		require.NoError(t, err)
		outcome := resolver.Resolve(context.Background(), runtimes.Candidate{Server: servers.Server{ID: testServerID, Transport: transport}, Authority: servers.AuthorityMetadata{CredentialRevisions: contract.CredentialRevisions{StaticCredential: "1"}, StaticCredentialHandle: pointer(string(testHandle(t, 5)))}})
		assert.Equal(t, test.credential, outcome.CredentialState)
		assert.Equal(t, test.reason, *outcome.Reason)
		assert.Equal(t, test.retry, outcome.Retryable)
	}
}

func TestResolverSkipsKeyringForCredentialFreeTransport(t *testing.T) {
	coordinator := &resolverCoordinator{err: errors.New("must not read")}
	resolver, err := New(&resolverRepository{}, coordinator, testInstallationID, func() time.Time { return testNow })
	require.NoError(t, err)
	transport := marshalTransport(t, contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: "http://127.0.0.1:9000/mcp", ProtocolMode: contract.ProtocolModern, Authentication: contract.NoAuthentication{Mode: contract.AuthenticationNone}})
	outcome := resolver.Resolve(context.Background(), runtimes.Candidate{Server: servers.Server{ID: testServerID, Transport: transport}})
	assert.Equal(t, contract.ServerCredentialNotRequired, outcome.CredentialState)
	assert.Equal(t, 0, coordinator.calls)
}

type resolverRepository struct {
	registration servers.OAuthRegistrationAuthority
}

func (repository *resolverRepository) OAuthRegistration(context.Context, string) (servers.OAuthRegistrationAuthority, error) {
	return repository.registration, nil
}

type resolverRead struct {
	contents []byte
	result   keyring.CutoverResult
}

type resolverCoordinator struct {
	values map[keyring.RecordKind]resolverRead
	err    error
	calls  int
}

func (coordinator *resolverCoordinator) ReadActive(_ context.Context, namespace keyring.Namespace) ([]byte, keyring.CutoverResult, error) {
	coordinator.calls++
	if coordinator.err != nil {
		return nil, keyring.CutoverResult{}, coordinator.err
	}
	value := coordinator.values[namespace.Kind()]
	return append([]byte(nil), value.contents...), value.result, nil
}

func marshalTransport(t *testing.T, value any) json.RawMessage {
	t.Helper()
	contents, err := json.Marshal(value)
	require.NoError(t, err)
	return contents
}

func testHandle(t *testing.T, value byte) keyring.Handle {
	t.Helper()
	handle, err := keyring.NewHandle(&repeatReader{value: value})
	require.NoError(t, err)
	return handle
}

type repeatReader struct{ value byte }

func (reader *repeatReader) Read(target []byte) (int, error) {
	for index := range target {
		target[index] = reader.value
	}
	return len(target), nil
}

func pointer(value string) *string { return &value }
