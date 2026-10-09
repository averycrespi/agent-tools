package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
	"github.com/stretchr/testify/require"
)

type diagnosticDisconnectOperation struct {
	*disconnectOperationFake
	readErr, cleanupErr error
}

func (operation *diagnosticDisconnectOperation) ReadActive(ctx context.Context, namespace keyring.Namespace) ([]byte, keyring.CutoverResult, error) {
	if namespace.Kind() == keyring.RecordOAuthTokens && operation.readErr != nil {
		return nil, keyring.CutoverResult{}, operation.readErr
	}
	return operation.disconnectOperationFake.ReadActive(ctx, namespace)
}
func (operation *diagnosticDisconnectOperation) CleanupCandidates(ctx context.Context, namespace keyring.Namespace) error {
	_ = operation.disconnectOperationFake.CleanupCandidates(ctx, namespace)
	if namespace.Kind() == keyring.RecordOAuthTokens {
		return operation.cleanupErr
	}
	return nil
}

func TestDisconnectDefaultSinkSeparatesLocalRemoteAndCleanupEffects(t *testing.T) {
	for _, mode := range []string{"HTTP", "material", "cleanup"} {
		t.Run(mode, func(t *testing.T) {
			const secret = "actual-disconnect-client-canary"
			operation := &diagnosticDisconnectOperation{disconnectOperationFake: &disconnectOperationFake{token: mustTokenBytes(t, refreshGeneration(contract.TokenEndpointAuthClientSecretPost)), client: []byte(secret)}}
			requester := &refreshRequesterFake{status: http.StatusOK}
			if mode == "HTTP" {
				requester.status = 503
			}
			if mode == "material" {
				operation.readErr = errors.New("token custody read refused " + secret)
			}
			if mode == "cleanup" {
				operation.cleanupErr = errors.New("physical generation delete refused " + secret)
			}
			repository := &disconnectRepositoryFake{registration: servers.OAuthRegistrationAuthority{Revision: "1", Mode: contract.RegistrationStatic, Issuer: "https://issuer.example", ClientID: "client", ResourceURL: "https://resource.example/mcp", TokenEndpointAuthMethod: contract.TokenEndpointAuthClientSecretPost}}
			service, err := newDisconnectService(repository, &disconnectCoordinatorFake{operation: operation}, refreshResolverFake{graph: Graph{Resource: "https://resource.example/mcp", Issuer: "https://issuer.example", RevocationEndpoint: "https://issuer.example/revoke", RevocationEndpointAuthMethodsSupported: []string{"client_secret_post"}, trustedOrigins: map[string]struct{}{}}}, requester, refreshServerID)
			require.NoError(t, err)
			var output bytes.Buffer
			adapter := diagnostics.New(&output, diagnostics.Warn)
			t.Cleanup(func() { adapter.Finish(nil); <-adapter.Done() })
			service.SetDiagnostics(adapter)
			transport, err := json.Marshal(contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: "https://resource.example/mcp", ProtocolMode: contract.ProtocolModern, Authentication: contract.OAuthAuthentication{Mode: contract.AuthenticationOAuth, Registration: contract.StaticOAuthRegistration{Mode: contract.RegistrationStatic, ClientID: "client", TokenEndpointAuthMethod: contract.TokenEndpointAuthClientSecretPost}}})
			require.NoError(t, err)
			authority := servers.AuthorityMetadata{RegistrationRevision: "1", CredentialRevisions: contract.CredentialRevisions{OAuthClient: "1", OAuthTokens: "1"}, OAuthClientHandle: stringPointer("client-handle"), OAuthTokensHandle: stringPointer("token-handle")}
			outcome, handled := service.ReconcileCredentials(t.Context(), servers.Operation{Kind: contract.OperationDisconnectCredentials, TargetDesiredRevision: "1", TargetCredentialRevisions: authority.CredentialRevisions}, servers.Server{ID: refreshServerID, Transport: transport}, authority, contract.ServerCredentialReady)
			require.True(t, handled)
			require.Equal(t, mode == "cleanup", outcome.CleanupPending)
			require.Equal(t, []string{"oauth_tokens"}, operation.invalidated)
			require.Equal(t, 3, operation.cleaned)
			require.True(t, adapter.Finish(nil))
			require.Contains(t, output.String(), "local_invalidation_ack=1 intended=1")
			require.NotContains(t, output.String(), secret)
			require.NotContains(t, output.String(), "old-access")
			require.NotContains(t, output.String(), "old-refresh")
			if mode == "material" {
				require.Zero(t, requester.calls)
				require.Contains(t, output.String(), "token custody read refused")
				require.Contains(t, output.String(), "remote_revocation=not_attempted")
			} else {
				require.Equal(t, 2, requester.calls)
			}
			if mode == "HTTP" {
				require.Contains(t, output.String(), "response_status=503")
				require.Contains(t, output.String(), "remote_requests=2")
			}
			if mode == "cleanup" {
				require.Contains(t, output.String(), "physical generation delete refused")
				require.Contains(t, output.String(), "http_acknowledged_not_proof_of_remote_erasure")
			}
		})
	}
}
