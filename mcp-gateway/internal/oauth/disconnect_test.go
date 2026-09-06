package oauth

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDisconnectOAuthInvalidatesBeforeOneShotRefreshThenAccessRevocationAndCleanup(t *testing.T) {
	transport, err := json.Marshal(contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: "https://resource.example/mcp", ProtocolMode: contract.ProtocolModern, Authentication: contract.OAuthAuthentication{Mode: contract.AuthenticationOAuth, Registration: contract.StaticOAuthRegistration{Mode: contract.RegistrationStatic, ClientID: "client", TokenEndpointAuthMethod: contract.TokenEndpointAuthClientSecretPost}}})
	require.NoError(t, err)
	operation := &disconnectOperationFake{token: mustTokenBytes(t, refreshGeneration(contract.TokenEndpointAuthClientSecretPost)), client: []byte("secret")}
	requester := &refreshRequesterFake{status: http.StatusOK}
	repository := &disconnectRepositoryFake{registration: servers.OAuthRegistrationAuthority{Revision: "1", Mode: contract.RegistrationStatic, Issuer: "https://issuer.example", ClientID: "client", ResourceURL: "https://resource.example/mcp", TokenEndpointAuthMethod: contract.TokenEndpointAuthClientSecretPost}}
	service, err := newDisconnectService(repository, &disconnectCoordinatorFake{operation: operation}, refreshResolverFake{graph: Graph{Resource: "https://resource.example/mcp", Issuer: "https://issuer.example", RevocationEndpoint: "https://issuer.example/revoke", RevocationEndpointAuthMethodsSupported: []string{"client_secret_post"}, trustedOrigins: map[string]struct{}{}}}, requester, refreshServerID)
	require.NoError(t, err)
	authority := servers.AuthorityMetadata{RegistrationRevision: "1", CredentialRevisions: contract.CredentialRevisions{OAuthClient: "1", OAuthTokens: "1"}, OAuthClientHandle: stringPointer("client-handle"), OAuthTokensHandle: stringPointer("tokens-handle")}
	outcome, handled := service.ReconcileCredentials(context.Background(), servers.Operation{Kind: contract.OperationDisconnectCredentials, TargetDesiredRevision: "1", TargetCredentialRevisions: authority.CredentialRevisions}, servers.Server{ID: refreshServerID, Transport: transport}, authority, contract.ServerCredentialReady)
	require.True(t, handled)
	assert.False(t, outcome.CleanupPending)
	assert.Equal(t, contract.ServerCredentialAbsent, outcome.CredentialState)
	assert.Equal(t, []string{"oauth_tokens"}, operation.invalidated)
	assert.Equal(t, 3, operation.cleaned)
	assert.Equal(t, 2, requester.calls)
	first, err := url.ParseQuery(string(requester.requestBodies[0]))
	require.NoError(t, err)
	second, err := url.ParseQuery(string(requester.requestBodies[1]))
	require.NoError(t, err)
	assert.Equal(t, "old-refresh", first.Get("token"))
	assert.Equal(t, "refresh_token", first.Get("token_type_hint"))
	assert.Equal(t, "old-access", second.Get("token"))
	assert.Equal(t, "access_token", second.Get("token_type_hint"))
}

func TestRevocationAuditFencesNetworkAndDoesNotClaimSettlement(t *testing.T) {
	for _, phase := range []string{"attempt", "outcome", ""} {
		t.Run(phase, func(t *testing.T) {
			recorder := &effectAuditRecorder{refusePhase: phase}
			requester := &refreshRequesterFake{status: http.StatusOK}
			graph := refreshGraph()
			graph.RevocationEndpoint = "https://issuer.example/revoke"
			graph.RevocationEndpointAuthMethodsSupported = []string{"none"}
			service, err := newDisconnectService(&disconnectRepositoryFake{auditHook: recorder.record}, &disconnectCoordinatorFake{}, refreshResolverFake{graph: graph}, requester, refreshServerID)
			require.NoError(t, err)
			prepared := refreshPrepared(contract.TokenEndpointAuthNone)
			credential := contract.AuditCredential{ID: refreshServerID, Fingerprint: "0123456789abcdef"}
			observation := service.revoke(audit.WithOperator(t.Context(), credential, credential.ID), refreshServerID, prepared.Configuration, prepared.Registration, refreshGeneration(contract.TokenEndpointAuthNone), nil)
			if phase == "" {
				assert.Nil(t, observation)
			} else {
				require.NotNil(t, observation)
				assert.Equal(t, contract.ReasonRevocationFailed, *observation)
			}
			if phase == "attempt" {
				assert.Zero(t, requester.calls)
			} else {
				assert.Equal(t, 2, requester.calls)
			}
			recorder.assertEvidence(t, "revoke", credential)
		})
	}
}

func TestRevocationRequestUsesExactClientAuthentication(t *testing.T) {
	tests := []struct {
		name       string
		method     contract.TokenEndpointAuthMethod
		secret     []byte
		authority  string
		clientForm string
		secretForm string
	}{
		{name: "basic", method: contract.TokenEndpointAuthClientSecretBasic, secret: []byte("s/ecret"), authority: "Basic " + base64.StdEncoding.EncodeToString([]byte(url.QueryEscape("client:id")+":"+url.QueryEscape("s/ecret")))},
		{name: "post", method: contract.TokenEndpointAuthClientSecretPost, secret: []byte("s/ecret"), clientForm: "client:id", secretForm: "s/ecret"},
		{name: "none", method: contract.TokenEndpointAuthNone, clientForm: "client:id"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			header, body, err := revocationRequest("client:id", test.method, test.secret, "opaque", "refresh_token")
			require.NoError(t, err)
			assert.Equal(t, test.authority, header.Get("Authorization"))
			assert.Equal(t, "application/x-www-form-urlencoded", header.Get("Content-Type"))
			values, err := url.ParseQuery(string(body))
			require.NoError(t, err)
			assert.Equal(t, "opaque", values.Get("token"))
			assert.Equal(t, "refresh_token", values.Get("token_type_hint"))
			assert.Equal(t, test.clientForm, values.Get("client_id"))
			assert.Equal(t, test.secretForm, values.Get("client_secret"))
		})
	}
}

func TestSelectRevocationMethodDoesNotProbe(t *testing.T) {
	method, ok := selectRevocationMethod([]string{"client_secret_post", "client_secret_basic", "none"}, true)
	require.True(t, ok)
	assert.Equal(t, contract.TokenEndpointAuthClientSecretBasic, method)
	method, ok = selectRevocationMethod([]string{"client_secret_basic", "none"}, false)
	require.True(t, ok)
	assert.Equal(t, contract.TokenEndpointAuthNone, method)
	_, ok = selectRevocationMethod([]string{"none"}, true)
	assert.False(t, ok)
}

type disconnectRepositoryFake struct {
	auditHook    func(contract.AuditEvent) error
	registration servers.OAuthRegistrationAuthority
}

func (repository *disconnectRepositoryFake) RecordOAuthEvent(_ context.Context, event contract.AuditEvent) error {
	if repository.auditHook != nil {
		return repository.auditHook(event)
	}
	return nil
}

func (repository *disconnectRepositoryFake) OAuthRegistration(context.Context, string) (servers.OAuthRegistrationAuthority, error) {
	return repository.registration, nil
}
func (*disconnectRepositoryFake) CredentialAuthorityCallback(servers.CredentialFence) (keyring.AuthorityCallback, error) {
	return func(context.Context, *sql.Tx, keyring.AuthorityUpdate) (string, error) { return "2", nil }, nil
}
func (*disconnectRepositoryFake) InvalidateOAuthRegistrationForDelete(context.Context, string, string) (string, error) {
	return "2", nil
}

type disconnectCoordinatorFake struct{ operation disconnectOperation }

func (coordinator *disconnectCoordinatorFake) WithOperation(ctx context.Context, use func(disconnectOperation) error) error {
	return use(coordinator.operation)
}

type disconnectOperationFake struct {
	token       []byte
	client      []byte
	invalidated []string
	cleaned     int
}

func (operation *disconnectOperationFake) ReadActive(_ context.Context, namespace keyring.Namespace) ([]byte, keyring.CutoverResult, error) {
	if namespace.Kind() == keyring.RecordOAuthClient {
		return append([]byte(nil), operation.client...), keyring.CutoverResult{Revision: "1"}, nil
	}
	return append([]byte(nil), operation.token...), keyring.CutoverResult{Revision: "1"}, nil
}
func (operation *disconnectOperationFake) InvalidateFencedExact(_ context.Context, namespace keyring.Namespace, _ keyring.AuthorityCallback) (keyring.CutoverResult, error) {
	operation.invalidated = append(operation.invalidated, string(namespace.Kind()))
	return keyring.CutoverResult{Revision: "2"}, nil
}
func (operation *disconnectOperationFake) CleanupCandidates(context.Context, keyring.Namespace) error {
	operation.cleaned++
	return nil
}
