package keyring_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfidentialRegistrationAndClientSecretPublishAtomically(t *testing.T) {
	repository, store, coordinator, _, _, server, closeHarness := newServerCutoverHarness(t, nil)
	defer closeHarness()
	server = patchDynamicOAuthServer(t, repository, server)
	authority := registrationAuthority(contract.TokenEndpointAuthClientSecretBasic)
	callback, err := repository.RegistrationAuthorityCallback(servers.RegistrationFence{ServerID: server.ID, ExpectedDesiredRevision: server.DesiredRevision, ExpectedRegistrationRevision: "0", ExpectedOAuthClientRevision: "0"}, authority)
	require.NoError(t, err)
	namespace, err := keyring.NewNamespace(serverTestInstallationID, server.ID, keyring.RecordOAuthClient)
	require.NoError(t, err)
	secret := []byte("oauth-client-secret-canary")
	result, err := coordinator.ReplaceFenced(context.Background(), namespace, secret, callback)
	require.NoError(t, err)
	assert.Equal(t, "1", result.Revision)

	registration, err := repository.OAuthRegistration(context.Background(), server.ID)
	require.NoError(t, err)
	assert.Equal(t, "1", registration.Revision)
	metadata, err := repository.Authority(context.Background(), server.ID)
	require.NoError(t, err)
	assert.Equal(t, "1", metadata.RegistrationRevision)
	assert.Equal(t, "1", metadata.CredentialRevisions.OAuthClient)
	require.NotNil(t, metadata.OAuthClientHandle)
	loaded, active, err := coordinator.ReadActive(context.Background(), namespace)
	require.NoError(t, err)
	assert.Equal(t, secret, loaded)
	assert.Equal(t, result.Handle, active.Handle)

	require.NoError(t, store.View(context.Background(), func(transaction *sql.Tx) error {
		var leaked int
		err := transaction.QueryRow(`
			SELECT count(*) FROM server_oauth_registrations
			WHERE issuer LIKE '%oauth-client-secret-canary%' OR client_id LIKE '%oauth-client-secret-canary%'
			   OR callback_url LIKE '%oauth-client-secret-canary%' OR resource_url LIKE '%oauth-client-secret-canary%'`).Scan(&leaked)
		assert.Zero(t, leaked)
		return err
	}))
}

func TestConfidentialRegistrationLostResponseReconstructsCommittedAuthority(t *testing.T) {
	repository, store, baseline, _, _, server, closeHarness := newServerCutoverHarness(t, nil)
	defer closeHarness()
	server = patchDynamicOAuthServer(t, repository, server)
	callback, err := repository.RegistrationAuthorityCallback(servers.RegistrationFence{ServerID: server.ID, ExpectedDesiredRevision: server.DesiredRevision, ExpectedRegistrationRevision: "0", ExpectedOAuthClientRevision: "0"}, registrationAuthority(contract.TokenEndpointAuthClientSecretBasic))
	require.NoError(t, err)
	namespace, err := keyring.NewNamespace(serverTestInstallationID, server.ID, keyring.RecordOAuthClient)
	require.NoError(t, err)
	lostResponse := errors.New("injected response loss")
	coordinator := keyring.NewCoordinatorWithAfterCommitForTest(baseline.ProviderForTest(), store, serverTestClock{}, new(serverTestEntropy), func() error { return lostResponse })
	result, err := coordinator.ReplaceFenced(context.Background(), namespace, []byte("committed-client-secret"), callback)
	assert.ErrorIs(t, err, lostResponse)
	assert.Equal(t, "1", result.Revision)

	registration, err := repository.OAuthRegistration(context.Background(), server.ID)
	require.NoError(t, err)
	assert.Equal(t, "1", registration.Revision)
	loaded, active, err := baseline.ReadActive(context.Background(), namespace)
	require.NoError(t, err)
	assert.Equal(t, []byte("committed-client-secret"), loaded)
	assert.Equal(t, "1", active.Revision)
}

func TestInitialOAuthTokenPublicationActivatesAuthorityAndFlowAtomically(t *testing.T) {
	repository, store, coordinator, _, _, server, closeHarness := newServerCutoverHarness(t, nil)
	defer closeHarness()
	server = patchDynamicOAuthServer(t, repository, server)
	flow, err := repository.CreateAuthFlow(context.Background(), servers.AuthFlowCreateRequest{ServerID: server.ID, ExpectedDesiredRevision: server.DesiredRevision})
	require.NoError(t, err)
	registration := registrationAuthority(contract.TokenEndpointAuthNone)
	registration.ClientSecretExpiresAt = nil
	_, err = repository.PublishPublicRegistration(context.Background(), servers.RegistrationFence{ServerID: server.ID, ExpectedDesiredRevision: server.DesiredRevision, ExpectedRegistrationRevision: "0", ExpectedOAuthClientRevision: "0", ExpectedAuthFlowID: flow.Flow.ID}, registration)
	require.NoError(t, err)
	_, err = repository.MarkAuthFlowAwaiting(context.Background(), flow.Flow.ID, server.DesiredRevision, "1")
	require.NoError(t, err)
	fence := servers.OAuthTokenFence{ServerID: server.ID, FlowID: flow.Flow.ID, ExpectedDesiredRevision: server.DesiredRevision, ExpectedRegistrationRevision: "1", ExpectedOAuthClientRevision: "0", ExpectedOAuthTokensRevision: "0"}
	_, err = repository.BeginAuthFlowExchange(context.Background(), fence)
	require.NoError(t, err)
	callback, err := repository.OAuthTokenAuthorityCallback(fence)
	require.NoError(t, err)
	namespace, err := keyring.NewNamespace(serverTestInstallationID, server.ID, keyring.RecordOAuthTokens)
	require.NoError(t, err)
	secret := []byte(`{"version":1,"access_token":"token-canary"}`)
	result, err := coordinator.ReplaceFencedAfterAuthorizationSuccess(context.Background(), namespace, secret, callback)
	require.NoError(t, err)
	assert.Equal(t, "1", result.Revision)
	stored, err := repository.GetAuthFlow(context.Background(), server.ID, flow.Flow.ID)
	require.NoError(t, err)
	assert.Equal(t, contract.AuthFlowSucceeded, stored.State)
	authority, err := repository.Authority(context.Background(), server.ID)
	require.NoError(t, err)
	assert.Equal(t, "1", authority.CredentialRevisions.OAuthTokens)
	loaded, active, err := coordinator.ReadActive(context.Background(), namespace)
	require.NoError(t, err)
	assert.Equal(t, secret, loaded)
	assert.Equal(t, "1", active.Revision)
	backup := filepath.Join(t.TempDir(), "token-safe.db")
	require.NoError(t, store.BackupTo(context.Background(), backup))
	contents, err := os.ReadFile(backup)
	require.NoError(t, err)
	assert.False(t, bytes.Contains(contents, []byte("token-canary")))
}

func TestInitialOAuthTokenPostCommitFailureLeavesNoAuthorityAndFailsFlow(t *testing.T) {
	repository, store, baseline, _, _, server, closeHarness := newServerCutoverHarness(t, nil)
	defer closeHarness()
	server = patchDynamicOAuthServer(t, repository, server)
	flow, err := repository.CreateAuthFlow(context.Background(), servers.AuthFlowCreateRequest{ServerID: server.ID, ExpectedDesiredRevision: server.DesiredRevision})
	require.NoError(t, err)
	registration := registrationAuthority(contract.TokenEndpointAuthNone)
	registration.ClientSecretExpiresAt = nil
	_, err = repository.PublishPublicRegistration(context.Background(), servers.RegistrationFence{ServerID: server.ID, ExpectedDesiredRevision: server.DesiredRevision, ExpectedRegistrationRevision: "0", ExpectedOAuthClientRevision: "0", ExpectedAuthFlowID: flow.Flow.ID}, registration)
	require.NoError(t, err)
	_, err = repository.MarkAuthFlowAwaiting(context.Background(), flow.Flow.ID, server.DesiredRevision, "1")
	require.NoError(t, err)
	fence := servers.OAuthTokenFence{ServerID: server.ID, FlowID: flow.Flow.ID, ExpectedDesiredRevision: server.DesiredRevision, ExpectedRegistrationRevision: "1", ExpectedOAuthClientRevision: "0", ExpectedOAuthTokensRevision: "0"}
	_, err = repository.BeginAuthFlowExchange(context.Background(), fence)
	require.NoError(t, err)
	callback, err := repository.OAuthTokenAuthorityCallback(fence)
	require.NoError(t, err)
	lost := errors.New("injected post-commit failure")
	coordinator := keyring.NewCoordinatorWithAfterCommitForTest(baseline.ProviderForTest(), store, serverTestClock{}, new(serverTestEntropy), func() error { return lost })
	namespace, err := keyring.NewNamespace(serverTestInstallationID, server.ID, keyring.RecordOAuthTokens)
	require.NoError(t, err)
	_, err = coordinator.ReplaceFencedAfterAuthorizationSuccess(context.Background(), namespace, []byte(`{"version":1,"access_token":"unusable-token"}`), callback)
	assert.ErrorIs(t, err, lost)
	stored, err := repository.GetAuthFlow(context.Background(), server.ID, flow.Flow.ID)
	require.NoError(t, err)
	assert.Equal(t, contract.AuthFlowFailed, stored.State)
	authority, err := repository.Authority(context.Background(), server.ID)
	require.NoError(t, err)
	assert.Equal(t, "2", authority.CredentialRevisions.OAuthTokens)
	assert.Nil(t, authority.OAuthTokensHandle)
	_, _, err = baseline.ReadActive(context.Background(), namespace)
	assert.ErrorIs(t, err, keyring.ErrNoAuthority)
}

func TestRegistrationCandidateRejectsDesiredDriftAndDrainWithoutAuthority(t *testing.T) {
	tests := []struct {
		name  string
		drift bool
		drain bool
	}{
		{name: "desired drift", drift: true},
		{name: "drain", drain: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository, _, coordinator, _, _, server, closeHarness := newServerCutoverHarness(t, nil)
			defer closeHarness()
			server = patchDynamicOAuthServer(t, repository, server)
			callback, err := repository.RegistrationAuthorityCallback(servers.RegistrationFence{ServerID: server.ID, ExpectedDesiredRevision: server.DesiredRevision, ExpectedRegistrationRevision: "0", ExpectedOAuthClientRevision: "0"}, registrationAuthority(contract.TokenEndpointAuthClientSecretPost))
			require.NoError(t, err)
			namespace, err := keyring.NewNamespace(serverTestInstallationID, server.ID, keyring.RecordOAuthClient)
			require.NoError(t, err)
			if test.drift {
				name := "Changed"
				_, err = repository.Patch(context.Background(), server.ID, server.DesiredRevision, servers.Patch{DisplayName: &name})
				require.NoError(t, err)
			}
			if test.drain {
				coordinator.Drain()
			}
			_, err = coordinator.ReplaceFenced(context.Background(), namespace, []byte("orphan-secret"), callback)
			assert.Error(t, err)
			registration, readErr := repository.OAuthRegistration(context.Background(), server.ID)
			require.NoError(t, readErr)
			assert.Equal(t, "0", registration.Revision)
			metadata, readErr := repository.Authority(context.Background(), server.ID)
			require.NoError(t, readErr)
			assert.Equal(t, "0", metadata.CredentialRevisions.OAuthClient)
			status, statusErr := coordinator.CandidateStatus(context.Background())
			require.NoError(t, statusErr)
			assert.Zero(t, status.InUse)
		})
	}
}

func patchDynamicOAuthServer(t *testing.T, repository *servers.Repository, server servers.Server) servers.Server {
	t.Helper()
	patched, err := repository.Patch(context.Background(), server.ID, server.DesiredRevision, servers.Patch{Transport: contract.StreamableHTTPTransport{
		Kind: contract.TransportStreamableHTTP, URL: "https://resource.example/mcp", ProtocolMode: contract.ProtocolModern,
		Authentication: contract.OAuthAuthentication{Mode: contract.AuthenticationOAuth, Registration: contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic}, TrustedOrigins: []string{}},
	}})
	require.NoError(t, err)
	return patched.Server
}

func registrationAuthority(method contract.TokenEndpointAuthMethod) servers.OAuthRegistrationAuthority {
	expires := serverTestClock{}.Now().Add(time.Hour).Format(time.RFC3339Nano)
	return servers.OAuthRegistrationAuthority{Revision: "0", Mode: contract.RegistrationDynamic, Issuer: "https://issuer.example", ClientID: "client-id", CallbackURL: "http://127.0.0.1:8210/oauth/callback", ResourceURL: "https://resource.example/mcp", TokenEndpointAuthMethod: method, CreatedAt: serverTestClock{}.Now().Format(time.RFC3339Nano), ClientSecretExpiresAt: &expires}
}
