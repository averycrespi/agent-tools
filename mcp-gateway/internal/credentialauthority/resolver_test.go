package credentialauthority

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestResolverAcquiresStaticAndBearerMaterialOnce(t *testing.T) {
	tests := []struct {
		name      string
		transport contract.Transport
		values    map[string]string
	}{
		{name: "stdio", transport: contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/bin/server", Arguments: []string{}, WorkingDirectory: "/tmp", Environment: map[string]string{}, SecretEnvironment: map[string]string{"TOKEN": "api"}}, values: map[string]string{"api": "stdio-canary"}},
		{name: "bearer", transport: contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: "https://resource.example/mcp", ProtocolMode: contract.ProtocolModern, Authentication: contract.BearerAuthentication{Mode: contract.AuthenticationBearer}}, values: map[string]string{"bearer": "bearer-canary"}},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handle := testHandle(t, byte(index+1))
			contents, err := servercredentials.EncodeStaticGeneration(test.values)
			require.NoError(t, err)
			candidate := activationCandidate(t, test.transport, servers.AuthorityMetadata{CredentialRevisions: contract.CredentialRevisions{StaticCredential: "1"}, StaticCredentialHandle: pointer(string(handle))})
			repository := &resolverRepository{server: candidate.Server, authority: candidate.Authority}
			coordinator := &resolverCoordinator{values: map[keyring.RecordKind]resolverRead{keyring.RecordStaticCredential: {contents: contents, result: keyring.CutoverResult{Handle: handle, Revision: "1"}}}}
			resolver, err := New(repository, coordinator, testInstallationID, func() time.Time { return testNow })
			require.NoError(t, err)

			outcome := resolver.Resolve(context.Background(), candidate)

			assert.Equal(t, contract.ServerCredentialReady, outcome.CredentialState)
			require.NotNil(t, outcome.Lease)
			assert.NotContains(t, fmt.Sprintf("%+v", outcome), "canary")
			assert.Equal(t, 1, coordinator.calls)
			owner := runtimes.NewRuntimeOwner()
			key, err := owner.Admit(candidate, outcome.Lease, nil)
			require.NoError(t, err)
			material, ok := owner.Material(key, contract.ServerCredentialStatic)
			require.True(t, ok)
			decoded, err := servercredentials.DecodeStaticGeneration(material)
			require.NoError(t, err)
			assert.Equal(t, test.values, decoded.Values)
			assert.True(t, owner.Release(key, true))
			assert.Equal(t, make([]byte, len(material)), material)
		})
	}
}

func TestResolverRejectsInvalidStaticGenerationWithoutLeasingMaterial(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]string
		handle byte
	}{
		{name: "missing slot", values: map[string]string{"other": "canary"}, handle: 3},
		{name: "extra slot", values: map[string]string{"api": "canary", "other": "canary"}, handle: 4},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handle := testHandle(t, test.handle)
			contents, err := servercredentials.EncodeStaticGeneration(test.values)
			require.NoError(t, err)
			transport := contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/bin/server", Arguments: []string{}, WorkingDirectory: "/tmp", Environment: map[string]string{}, SecretEnvironment: map[string]string{"TOKEN": "api"}}
			candidate := activationCandidate(t, transport, servers.AuthorityMetadata{CredentialRevisions: contract.CredentialRevisions{StaticCredential: "1"}, StaticCredentialHandle: pointer(string(handle))})
			repository := &resolverRepository{server: candidate.Server, authority: candidate.Authority}
			coordinator := &resolverCoordinator{values: map[keyring.RecordKind]resolverRead{keyring.RecordStaticCredential: {contents: contents, result: keyring.CutoverResult{Handle: handle, Revision: "1"}}}}
			resolver, err := New(repository, coordinator, testInstallationID, func() time.Time { return testNow })
			require.NoError(t, err)

			outcome := resolver.Resolve(context.Background(), candidate)

			assert.Equal(t, contract.ServerCredentialUnavailable, outcome.CredentialState)
			assert.Nil(t, outcome.Lease)
			assert.Equal(t, 1, coordinator.calls)
		})
	}
}

func TestResolverRejectsForeignAndMissingStaticAuthority(t *testing.T) {
	transport := contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: "https://resource.example/mcp", ProtocolMode: contract.ProtocolModern, Authentication: contract.BearerAuthentication{Mode: contract.AuthenticationBearer}}
	missing := activationCandidate(t, transport, servers.AuthorityMetadata{})
	missingCoordinator := &resolverCoordinator{err: errors.New("must not read")}
	missingResolver, err := New(&resolverRepository{server: missing.Server, authority: missing.Authority}, missingCoordinator, testInstallationID, func() time.Time { return testNow })
	require.NoError(t, err)
	outcome := missingResolver.Resolve(context.Background(), missing)
	assert.Equal(t, contract.ServerCredentialAbsent, outcome.CredentialState)
	assert.Equal(t, 0, missingCoordinator.calls)

	expectedHandle := testHandle(t, 5)
	foreignHandle := testHandle(t, 6)
	contents, err := servercredentials.EncodeStaticGeneration(map[string]string{"bearer": "foreign-canary"})
	require.NoError(t, err)
	candidate := activationCandidate(t, transport, servers.AuthorityMetadata{CredentialRevisions: contract.CredentialRevisions{StaticCredential: "1"}, StaticCredentialHandle: pointer(string(expectedHandle))})
	coordinator := &resolverCoordinator{values: map[keyring.RecordKind]resolverRead{keyring.RecordStaticCredential: {contents: contents, result: keyring.CutoverResult{Handle: foreignHandle, Revision: "1"}}}}
	resolver, err := New(&resolverRepository{server: candidate.Server, authority: candidate.Authority}, coordinator, testInstallationID, func() time.Time { return testNow })
	require.NoError(t, err)
	outcome = resolver.Resolve(context.Background(), candidate)
	assert.Equal(t, contract.ServerCredentialAbsent, outcome.CredentialState)
	assert.Nil(t, outcome.Lease)
	assert.Equal(t, 1, coordinator.calls)
}

func TestResolverRejectsStaticMaterialWhenSafeFenceChangesAfterRead(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*resolverRepository)
	}{
		{name: "desired", mutate: func(repository *resolverRepository) { repository.server.DesiredRevision = "2" }},
		{name: "credential", mutate: func(repository *resolverRepository) { repository.authority.CredentialRevisions.StaticCredential = "2" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handle := testHandle(t, 7)
			contents, err := servercredentials.EncodeStaticGeneration(map[string]string{"bearer": "stale-canary"})
			require.NoError(t, err)
			transport := contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: "https://resource.example/mcp", ProtocolMode: contract.ProtocolModern, Authentication: contract.BearerAuthentication{Mode: contract.AuthenticationBearer}}
			candidate := activationCandidate(t, transport, servers.AuthorityMetadata{CredentialRevisions: contract.CredentialRevisions{StaticCredential: "1"}, StaticCredentialHandle: pointer(string(handle))})
			repository := &resolverRepository{server: candidate.Server, authority: candidate.Authority}
			coordinator := &resolverCoordinator{values: map[keyring.RecordKind]resolverRead{keyring.RecordStaticCredential: {contents: contents, result: keyring.CutoverResult{Handle: handle, Revision: "1"}}}, afterRead: func() { test.mutate(repository) }}
			resolver, err := New(repository, coordinator, testInstallationID, func() time.Time { return testNow })
			require.NoError(t, err)

			outcome := resolver.Resolve(context.Background(), candidate)

			assert.Equal(t, contract.ServerCredentialUnavailable, outcome.CredentialState)
			require.NotNil(t, outcome.Reason)
			assert.Equal(t, contract.ReasonSuperseded, *outcome.Reason)
			assert.Nil(t, outcome.Lease)
			assert.Equal(t, 1, coordinator.calls)
		})
	}
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
		{err: context.Canceled, credential: contract.ServerCredentialUnavailable, reason: contract.ReasonKeyringUnavailable},
	}
	transport := marshalTransport(t, contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: "https://resource.example/mcp", ProtocolMode: contract.ProtocolModern, Authentication: contract.BearerAuthentication{Mode: contract.AuthenticationBearer}})
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
	transports := []contract.Transport{
		contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/bin/server", Arguments: []string{}, WorkingDirectory: "/tmp", Environment: map[string]string{}, SecretEnvironment: map[string]string{}},
		contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: "http://127.0.0.1:9000/mcp", ProtocolMode: contract.ProtocolModern, Authentication: contract.NoAuthentication{Mode: contract.AuthenticationNone}},
	}
	for _, transport := range transports {
		coordinator := &resolverCoordinator{err: errors.New("must not read")}
		candidate := activationCandidate(t, transport, servers.AuthorityMetadata{})
		repository := &resolverRepository{server: candidate.Server, authority: candidate.Authority}
		resolver, err := New(repository, coordinator, testInstallationID, func() time.Time { return testNow })
		require.NoError(t, err)
		outcome := resolver.Resolve(context.Background(), candidate)
		assert.Equal(t, contract.ServerCredentialNotRequired, outcome.CredentialState)
		assert.Nil(t, outcome.Lease)
		assert.Equal(t, 0, coordinator.calls)
	}
}

type resolverRepository struct {
	server       servers.Server
	authority    servers.AuthorityMetadata
	registration servers.OAuthRegistrationAuthority
}

func (repository *resolverRepository) Get(context.Context, string) (servers.Server, error) {
	return repository.server, nil
}

func (repository *resolverRepository) Authority(context.Context, string) (servers.AuthorityMetadata, error) {
	return repository.authority, nil
}

func (repository *resolverRepository) OAuthRegistration(context.Context, string) (servers.OAuthRegistrationAuthority, error) {
	return repository.registration, nil
}

type resolverRead struct {
	contents []byte
	result   keyring.CutoverResult
}

type resolverCoordinator struct {
	values    map[keyring.RecordKind]resolverRead
	err       error
	calls     int
	afterRead func()
}

func (coordinator *resolverCoordinator) ReadActive(_ context.Context, namespace keyring.Namespace) ([]byte, keyring.CutoverResult, error) {
	coordinator.calls++
	if coordinator.err != nil {
		return nil, keyring.CutoverResult{}, coordinator.err
	}
	value := coordinator.values[namespace.Kind()]
	contents := append([]byte(nil), value.contents...)
	if coordinator.afterRead != nil {
		coordinator.afterRead()
	}
	return contents, value.result, nil
}

func activationCandidate(t *testing.T, transport contract.Transport, authority servers.AuthorityMetadata) runtimes.Candidate {
	t.Helper()
	return runtimes.Candidate{
		Server:     servers.Server{ID: testServerID, DesiredState: contract.DesiredServerEnabled, DesiredRevision: "1", Transport: marshalTransport(t, transport)},
		Authority:  authority,
		RuntimeID:  "01ARZ3NDEKTSV4RRFFQ69G5FAX",
		Generation: 1,
	}
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
