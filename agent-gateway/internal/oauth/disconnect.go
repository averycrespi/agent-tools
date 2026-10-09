package oauth

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/remote"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/runtimes"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
)

type disconnectRepository interface {
	oauthAuditRecorder
	OAuthRegistration(context.Context, string) (servers.OAuthRegistrationAuthority, error)
	CredentialAuthorityCallback(servers.CredentialFence) (keyring.AuthorityCallback, error)
	InvalidateOAuthRegistrationForDelete(context.Context, string, string) (string, error)
}

type disconnectOperation interface {
	ReadActive(context.Context, keyring.Namespace) ([]byte, keyring.CutoverResult, error)
	InvalidateFencedExact(context.Context, keyring.Namespace, keyring.AuthorityCallback) (keyring.CutoverResult, error)
	CleanupCandidates(context.Context, keyring.Namespace) error
}

type disconnectCoordinator interface {
	WithOperation(context.Context, func(disconnectOperation) error) error
}

type coordinatorDisconnect struct{ coordinator *keyring.Coordinator }

func (adapter coordinatorDisconnect) WithOperation(ctx context.Context, use func(disconnectOperation) error) error {
	return adapter.coordinator.WithOperation(ctx, func(operation *keyring.Operation) error { return use(operation) })
}

type DisconnectService struct {
	diagnostics    diagnostics.HTTPProxyObserver
	repository     disconnectRepository
	coordinator    disconnectCoordinator
	resolver       refreshResolver
	requester      refreshRequester
	installationID string
}

func NewDisconnectService(repository disconnectRepository, coordinator *keyring.Coordinator, resolver refreshResolver, factory *remote.Factory, installationID string) (*DisconnectService, error) {
	if factory == nil {
		return nil, ErrFlowRejected
	}
	return newDisconnectService(repository, coordinatorDisconnect{coordinator: coordinator}, resolver, refreshHTTP{factory: factory}, installationID)
}

func newDisconnectService(repository disconnectRepository, coordinator disconnectCoordinator, resolver refreshResolver, requester refreshRequester, installationID string) (*DisconnectService, error) {
	if repository == nil || coordinator == nil || resolver == nil || requester == nil {
		return nil, ErrFlowRejected
	}
	if _, err := keyring.NewNamespace(installationID, installationID, keyring.RecordOAuthTokens); err != nil {
		return nil, ErrFlowRejected
	}
	return &DisconnectService{repository: repository, coordinator: coordinator, resolver: resolver, requester: requester, installationID: installationID}, nil
}

func (service *DisconnectService) ReconcileCredentials(ctx context.Context, operation servers.Operation, server servers.Server, authority servers.AuthorityMetadata, credentialState contract.ServerCredentialState) (runtimes.CredentialLifecycleOutcome, bool) {
	switch operation.Kind {
	case contract.OperationDisconnectCredentials, contract.OperationDelete:
		return service.disconnect(ctx, operation, server, authority), true
	case contract.OperationRetry:
		if credentialState != contract.ServerCredentialCleanupPending {
			return runtimes.CredentialLifecycleOutcome{}, false
		}
		return service.cleanupOnly(ctx, server.ID, authority), true
	default:
		return runtimes.CredentialLifecycleOutcome{}, false
	}
}

func (service *DisconnectService) disconnect(ctx context.Context, operation servers.Operation, server servers.Server, authority servers.AuthorityMetadata) runtimes.CredentialLifecycleOutcome {
	registration, registrationErr := service.repository.OAuthRegistration(ctx, server.ID)
	configuration, oauthMode := cleanupOAuthConfiguration(server, registration)
	kinds := cleanupKinds(operation.Kind, server, authority, oauthMode)
	if len(kinds) == 0 && operation.Kind != contract.OperationDelete {
		return runtimes.CredentialLifecycleOutcome{CredentialState: contract.ServerCredentialAbsent}
	}

	var tokens TokenGeneration
	var tokenLoaded bool
	var clientSecret []byte
	causes := registrationErr
	invalidated := 0
	phase, revocation := "read material and invalidate", "not_attempted"
	defer func() {
		if causes != nil {
			sensitive := []string{string(clientSecret), tokens.AccessToken}
			if tokens.RefreshToken != nil {
				sensitive = append(sensitive, *tokens.RefreshToken)
			}
			service.observeDisconnect(server.ID, fmt.Sprintf("operation_id=%s phase=%s local_invalidation_ack=%d intended=%d remote_revocation=%s; ", operation.ID, phase, invalidated, len(kinds), revocation), causes, sensitive...)
		}
		clear(clientSecret)
	}()
	invalidationFailed := false
	err := service.coordinator.WithOperation(ctx, func(admitted disconnectOperation) error {
		if containsCleanupKind(kinds, contract.ServerCredentialOAuthTokens) && authority.OAuthTokensHandle != nil {
			encoded, _, err := admitted.ReadActive(ctx, service.namespace(server.ID, contract.ServerCredentialOAuthTokens))
			if err == nil {
				tokens, err = DecodeTokenGeneration(encoded)
				clear(encoded)
				tokenLoaded = err == nil
			}
			if err != nil {
				causes = errors.Join(causes, withDisconnectCause("read revocation token generation", err))
			}
		}
		if registration.TokenEndpointAuthMethod != contract.TokenEndpointAuthNone && authority.OAuthClientHandle != nil {
			var readErr error
			clientSecret, _, readErr = admitted.ReadActive(ctx, service.namespace(server.ID, contract.ServerCredentialOAuthClient))
			if readErr != nil {
				causes = errors.Join(causes, withDisconnectCause("read revocation client credential", readErr))
			}
		}
		for _, kind := range kinds {
			revision := credentialRevision(authority.CredentialRevisions, kind)
			callback, callbackErr := service.repository.CredentialAuthorityCallback(servers.CredentialFence{ServerID: server.ID, Kind: kind, ExpectedDesiredRevision: operation.TargetDesiredRevision, ExpectedCredentialRevision: revision, ExpectedRegistrationRevision: authority.RegistrationRevision})
			if callbackErr != nil {
				causes = errors.Join(causes, withDisconnectCause("prepare "+string(kind)+" invalidation fence", callbackErr))
				invalidationFailed = true
				continue
			}
			if _, invalidateErr := admitted.InvalidateFencedExact(ctx, service.namespace(server.ID, kind), callback); invalidateErr != nil {
				invalidationFailed = true
				causes = errors.Join(causes, withDisconnectCause("invalidate "+string(kind)+" authority", invalidateErr))
			} else {
				invalidated++
			}
		}
		return nil
	})
	causes = errors.Join(causes, err)
	if err != nil || invalidationFailed {
		return runtimes.CredentialLifecycleOutcome{CredentialState: contract.ServerCredentialCleanupPending, CleanupPending: true}
	}

	var observation *contract.PublicReason
	if tokenLoaded {
		phase, revocation = "remote revocation", "attempted_or_skipped_per_detail"
		var revokeErr error
		reason := service.revoke(ctx, server.ID, configuration, registration, tokens, clientSecret, &revokeErr)
		causes = errors.Join(causes, revokeErr)
		if reason == nil {
			revocation = "http_acknowledged_not_proof_of_remote_erasure"
		}
		observation = reason
	}
	if operation.Kind == contract.OperationDelete && authority.RegistrationRevision != "0" {
		if _, err := service.repository.InvalidateOAuthRegistrationForDelete(ctx, server.ID, authority.RegistrationRevision); err != nil {
			phase = "invalidate registration"
			causes = errors.Join(causes, err)
			return runtimes.CredentialLifecycleOutcome{CredentialState: contract.ServerCredentialCleanupPending, CleanupPending: true}
		}
	}
	phase = "physical retained generation cleanup"
	var cleanupErr error
	cleanup := service.cleanupOnly(ctx, server.ID, authority, &cleanupErr)
	causes = errors.Join(causes, cleanupErr)
	if cleanup.CleanupPending {
		return cleanup
	}
	cleanup.Reason = observation
	return cleanup
}

func (service *DisconnectService) cleanupOnly(ctx context.Context, serverID string, authority servers.AuthorityMetadata, details ...*error) runtimes.CredentialLifecycleOutcome {
	err := service.coordinator.WithOperation(ctx, func(admitted disconnectOperation) error {
		var failures []error
		for _, kind := range []contract.ServerCredentialKind{contract.ServerCredentialStatic, contract.ServerCredentialOAuthClient, contract.ServerCredentialOAuthTokens} {
			if cleanupErr := admitted.CleanupCandidates(ctx, service.namespace(serverID, kind)); cleanupErr != nil {
				failures = append(failures, withDisconnectCause("cleanup "+string(kind)+" retained generations", cleanupErr))
			}
		}
		return errors.Join(failures...)
	})
	if len(details) != 0 {
		*details[0] = err
	} else if err != nil {
		service.observeDisconnect(serverID, "cleanup_only=true remote_revocation=not_attempted; ", err)
	}
	if err != nil {
		return runtimes.CredentialLifecycleOutcome{CredentialState: contract.ServerCredentialCleanupPending, CleanupPending: true}
	}
	return runtimes.CredentialLifecycleOutcome{CredentialState: contract.ServerCredentialAbsent}
}

func (service *DisconnectService) revoke(ctx context.Context, serverID string, configuration servers.AuthFlowOAuthConfiguration, registration servers.OAuthRegistrationAuthority, tokens TokenGeneration, clientSecret []byte, details ...*error) (observation *contract.PublicReason) {
	var causes error
	attempted, acknowledged := 0, 0
	defer func() {
		if len(details) == 0 {
			return
		}
		if causes != nil {
			secrets := []string{string(clientSecret), tokens.AccessToken}
			if tokens.RefreshToken != nil {
				secrets = append(secrets, *tokens.RefreshToken)
			}
			detail := diagnostics.Snapshot("oauth", "remote revocation", serverID, causes, secrets...)
			detail.Explanation = fmt.Sprintf("remote_requests=%d HTTP_200_ack=%d remote_effect=unknown; %s", attempted, acknowledged, detail.Explanation)
			*details[0] = diagnostics.WithDetail(causes, detail)
		}
	}()
	attempt, err := beginOAuthEffect(ctx, service.repository, time.Now(), "revoke", contract.AuditTarget{Type: "server", ID: serverID})
	if err != nil {
		causes = fmt.Errorf("revocation attempt audit: %w", err)
		reason := contract.ReasonRevocationFailed
		return &reason
	}
	defer func() {
		outcome := "succeeded"
		if observation != nil {
			outcome = "unknown"
		}
		if err := finishOAuthEffect(ctx, service.repository, time.Now(), attempt, outcome); err != nil {
			causes = errors.Join(causes, fmt.Errorf("revocation outcome audit: %w", err))
			reason := contract.ReasonRevocationFailed
			observation = &reason
		}
	}()
	graph, err := service.resolver.Discover(ctx, Input{Resource: registration.ResourceURL, DesiredIssuer: &registration.Issuer, TrustedOrigins: configuration.Authentication.TrustedOrigins})
	if err != nil {
		causes = fmt.Errorf("discover revocation endpoint: %w", err)
		reason := contract.ReasonRevocationFailed
		return &reason
	}
	if graph.RevocationEndpoint == "" {
		causes = errors.New("revocation skipped: metadata has no revocation endpoint")
		reason := contract.ReasonRevocationUnsupported
		return &reason
	}
	method, ok := selectRevocationMethod(graph.RevocationEndpointAuthMethodsSupported, len(clientSecret) != 0)
	if !ok {
		causes = errors.New("revocation skipped: no supported client authentication method")
		reason := contract.ReasonRevocationFailed
		return &reason
	}
	revoke := func(token, hint string) {
		attempted++
		if err := service.revokeToken(ctx, graph, registration.ClientID, method, clientSecret, token, hint); err != nil {
			causes = errors.Join(causes, err)
		} else {
			acknowledged++
		}
	}
	if tokens.RefreshToken != nil && *tokens.RefreshToken != "" {
		revoke(*tokens.RefreshToken, "refresh_token")
	}
	if tokens.AccessToken != "" && (tokens.RefreshToken == nil || tokens.AccessToken != *tokens.RefreshToken) {
		revoke(tokens.AccessToken, "access_token")
	}
	if causes != nil {
		reason := contract.ReasonRevocationFailed
		return &reason
	}
	return nil
}

func (service *DisconnectService) revokeToken(ctx context.Context, graph Graph, clientID string, method contract.TokenEndpointAuthMethod, secret []byte, token, hint string) error {
	header, body, err := revocationRequest(clientID, method, secret, token, hint)
	if err != nil {
		return err
	}
	defer clear(body)
	status, _, _, err := service.requester.Request(ctx, graph.RevocationEndpoint, graph.AllowsRestrictedEndpoint(graph.RevocationEndpoint), header, body, limit("oauth_response_body_bytes"), nil)
	if err != nil || status != http.StatusOK {
		detail := diagnostics.Snapshot("oauth", "revoke "+hint, graph.RevocationEndpoint, err, string(secret), token, header.Get("Authorization"))
		detail.Explanation = fmt.Sprintf("token_kind=%s response_status=%d expected_status=200 dispatch=handed_to_requester remote_effect=unknown; %s", hint, status, detail.Explanation)
		return diagnostics.WithDetail(ErrTokenRejected, detail)
	}
	return nil
}

func revocationRequest(clientID string, method contract.TokenEndpointAuthMethod, secret []byte, token, hint string) (http.Header, []byte, error) {
	if clientID == "" || token == "" || (hint != "refresh_token" && hint != "access_token") {
		return nil, nil, ErrTokenRejected
	}
	values := url.Values{"token": []string{token}, "token_type_hint": []string{hint}}
	header := http.Header{"Accept": []string{contract.MediaTypeJSON}, "Content-Type": []string{"application/x-www-form-urlencoded"}, "User-Agent": []string{""}}
	switch method {
	case contract.TokenEndpointAuthClientSecretBasic:
		if len(secret) == 0 {
			return nil, nil, ErrTokenRejected
		}
		authority := url.QueryEscape(clientID) + ":" + url.QueryEscape(string(secret))
		header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(authority)))
	case contract.TokenEndpointAuthClientSecretPost:
		if len(secret) == 0 {
			return nil, nil, ErrTokenRejected
		}
		values.Set("client_id", clientID)
		values.Set("client_secret", string(secret))
	case contract.TokenEndpointAuthNone:
		values.Set("client_id", clientID)
	default:
		return nil, nil, ErrTokenRejected
	}
	return header, []byte(values.Encode()), nil
}

func selectRevocationMethod(supported []string, confidential bool) (contract.TokenEndpointAuthMethod, bool) {
	choices := []contract.TokenEndpointAuthMethod{contract.TokenEndpointAuthClientSecretBasic, contract.TokenEndpointAuthClientSecretPost}
	if !confidential {
		choices = []contract.TokenEndpointAuthMethod{contract.TokenEndpointAuthNone}
	}
	for _, choice := range choices {
		for _, value := range supported {
			if value == string(choice) {
				return choice, true
			}
		}
	}
	return "", false
}

func cleanupKinds(kind contract.ServerOperationKind, server servers.Server, authority servers.AuthorityMetadata, oauthMode bool) []contract.ServerCredentialKind {
	if kind == contract.OperationDelete {
		result := make([]contract.ServerCredentialKind, 0, 3)
		if authority.StaticCredentialHandle != nil {
			result = append(result, contract.ServerCredentialStatic)
		}
		if authority.OAuthClientHandle != nil {
			result = append(result, contract.ServerCredentialOAuthClient)
		}
		if authority.OAuthTokensHandle != nil {
			result = append(result, contract.ServerCredentialOAuthTokens)
		}
		return result
	}
	if oauthMode {
		if authority.OAuthTokensHandle != nil {
			return []contract.ServerCredentialKind{contract.ServerCredentialOAuthTokens}
		}
		return nil
	}
	if authority.StaticCredentialHandle != nil {
		return []contract.ServerCredentialKind{contract.ServerCredentialStatic}
	}
	return nil
}

func cleanupOAuthConfiguration(server servers.Server, registration servers.OAuthRegistrationAuthority) (servers.AuthFlowOAuthConfiguration, bool) {
	if decoded, err := servers.DecodeTransport(server.Transport); err == nil {
		if transport, ok := decoded.(contract.StreamableHTTPTransport); ok {
			if authentication, ok := transport.Authentication.(contract.OAuthAuthentication); ok {
				projected := contract.OAuthAuthentication{Mode: contract.AuthenticationOAuth, TrustedOrigins: authentication.TrustedOrigins}
				return servers.AuthFlowOAuthConfiguration{Resource: transport.URL, Authentication: projected}, true
			}
		}
	}
	return servers.AuthFlowOAuthConfiguration{Resource: registration.ResourceURL, Authentication: contract.OAuthAuthentication{Mode: contract.AuthenticationOAuth}}, registration.Revision != "" && registration.Revision != "0"
}

func (service *DisconnectService) namespace(serverID string, kind contract.ServerCredentialKind) keyring.Namespace {
	var recordKind keyring.RecordKind
	switch kind {
	case contract.ServerCredentialStatic:
		recordKind = keyring.RecordStaticCredential
	case contract.ServerCredentialOAuthClient:
		recordKind = keyring.RecordOAuthClient
	default:
		recordKind = keyring.RecordOAuthTokens
	}
	namespace, _ := keyring.NewNamespace(service.installationID, serverID, recordKind)
	return namespace
}

func credentialRevision(revisions contract.CredentialRevisions, kind contract.ServerCredentialKind) string {
	switch kind {
	case contract.ServerCredentialStatic:
		return revisions.StaticCredential
	case contract.ServerCredentialOAuthClient:
		return revisions.OAuthClient
	default:
		return revisions.OAuthTokens
	}
}

func containsCleanupKind(kinds []contract.ServerCredentialKind, expected contract.ServerCredentialKind) bool {
	for _, kind := range kinds {
		if kind == expected {
			return true
		}
	}
	return false
}
