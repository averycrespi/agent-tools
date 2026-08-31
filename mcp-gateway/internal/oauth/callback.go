package oauth

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
)

type CallbackOutcome string

const (
	CallbackSucceeded CallbackOutcome = "succeeded"
	CallbackInvalid   CallbackOutcome = "invalid"
	CallbackTransient CallbackOutcome = "transient"
)

type CallbackResult struct {
	Outcome  CallbackOutcome
	ServerID string
	FlowID   string
}

func (service *FlowService) HandleCallback(ctx context.Context, rawQuery string) CallbackResult {
	stateQuery, ok := callbackValues(rawQuery, "state")
	stateValues := stateQuery["state"]
	if !ok || len(stateValues) != 1 || stateValues[0] == "" {
		return CallbackResult{Outcome: CallbackInvalid}
	}
	workCtx, release, ok := service.acquireCallback(ctx)
	if !ok {
		return CallbackResult{Outcome: CallbackTransient}
	}
	defer release()
	state := stateValues[0]
	service.mu.Lock()
	bundle, found := service.byState[state]
	if found {
		delete(service.byState, state)
	}
	service.mu.Unlock()
	if !found {
		return CallbackResult{Outcome: CallbackInvalid}
	}
	result := CallbackResult{Outcome: CallbackInvalid, ServerID: bundle.serverID, FlowID: bundle.flowID}
	values, valid := callbackValues(rawQuery, "code", "error", "iss")
	if !valid || !validIssuer(values["iss"], bundle) {
		service.failConsumed(workCtx, bundle, contract.OAuthDiagnosticCallbackValidation, newDiagnosticFailure(ErrFlowRejected, contract.ReasonProtocolInvalid, 0))
		return result
	}
	codes, errorsFound := values["code"], values["error"]
	if len(codes) != 1 || codes[0] == "" || len(errorsFound) != 0 {
		if len(errorsFound) == 1 && errorsFound[0] != "" && len(codes) == 0 {
			service.failConsumed(workCtx, bundle, contract.OAuthDiagnosticCallbackValidation, newDiagnosticFailure(ErrFlowRejected, contract.ReasonAuthenticationRejected, 0))
			return result
		}
		service.failConsumed(workCtx, bundle, contract.OAuthDiagnosticCallbackValidation, newDiagnosticFailure(ErrFlowRejected, contract.ReasonProtocolInvalid, 0))
		return result
	}
	fence := servers.OAuthTokenFence{
		ServerID: bundle.serverID, FlowID: bundle.flowID, ExpectedDesiredRevision: bundle.desiredRevision,
		ExpectedRegistrationRevision: bundle.registration.Revision,
		ExpectedOAuthClientRevision:  bundle.authority.CredentialRevisions.OAuthClient,
		ExpectedOAuthTokensRevision:  bundle.authority.CredentialRevisions.OAuthTokens,
	}
	if _, err := service.store.BeginAuthFlowExchange(workCtx, fence); err != nil {
		service.failConsumed(workCtx, bundle, contract.OAuthDiagnosticCallbackValidation, err)
		return result
	}
	callback, err := service.store.OAuthTokenAuthorityCallback(fence)
	if err != nil {
		service.failConsumed(workCtx, bundle, contract.OAuthDiagnosticCredentialInstallation, err)
		return result
	}
	tokenNamespace, err := keyring.NewNamespace(service.installationID, bundle.serverID, keyring.RecordOAuthTokens)
	if err != nil {
		service.failConsumed(workCtx, bundle, contract.OAuthDiagnosticCredentialInstallation, err)
		return result
	}
	var clientSecret []byte
	if bundle.registration.TokenEndpointAuthMethod != contract.TokenEndpointAuthNone {
		namespace, err := keyring.NewNamespace(service.installationID, bundle.serverID, keyring.RecordOAuthClient)
		if err != nil {
			service.failConsumed(workCtx, bundle, contract.OAuthDiagnosticCredentialInstallation, err)
			return result
		}
		loaded, active, err := service.secrets.ReadActive(workCtx, namespace)
		if err != nil || active.Revision != bundle.authority.CredentialRevisions.OAuthClient {
			clear(loaded)
			service.failConsumed(workCtx, bundle, contract.OAuthDiagnosticCredentialInstallation, err)
			return result
		}
		clientSecret = loaded
		defer clear(clientSecret)
	}
	if err := service.store.ValidateAuthFlowExchange(workCtx, fence); err != nil {
		service.failConsumed(workCtx, bundle, contract.OAuthDiagnosticCallbackValidation, err)
		return result
	}
	header, body, err := tokenRequest(codes[0], bundle, clientSecret)
	if err != nil {
		service.failConsumed(workCtx, bundle, contract.OAuthDiagnosticTokenExchange, err)
		return result
	}
	status, responseHeader, responseBody, err := service.requester.Request(
		workCtx, bundle.graph.TokenEndpoint, bundle.graph.AllowsRestrictedEndpoint(bundle.graph.TokenEndpoint),
		http.MethodPost, header, body, limit("oauth_response_body_bytes"),
	)
	clear(body)
	if err != nil {
		service.failConsumed(workCtx, bundle, contract.OAuthDiagnosticTokenExchange, err)
		return result
	}
	issuedAt := service.now().UTC()
	token, err := parseTokenResponse(status, responseHeader, responseBody, bundle.requestedScopes, issuedAt)
	clear(responseBody)
	if err != nil {
		service.failConsumed(workCtx, bundle, contract.OAuthDiagnosticTokenExchange, newDiagnosticFailure(err, reasonForHTTPStatus(status), status))
		return result
	}
	generation, err := encodeTokenGeneration(bundle, token, issuedAt)
	if err != nil {
		service.failConsumed(workCtx, bundle, contract.OAuthDiagnosticCredentialInstallation, err)
		return result
	}
	defer clear(generation)
	if _, err := service.secrets.ReplaceFencedAfterAuthorizationSuccess(workCtx, tokenNamespace, generation, callback); err != nil {
		service.failConsumed(workCtx, bundle, contract.OAuthDiagnosticCredentialInstallation, err)
		return result
	}
	result.Outcome = CallbackSucceeded
	return result
}

func (service *FlowService) CallbackStatus() contract.LimitStatus {
	limit, _ := contract.FixedLimitByName("oauth_callback_work")
	if service == nil || service.callbackSlots == nil {
		return contract.LimitStatus{Limit: limit.Maximum}
	}
	inUse := int64(len(service.callbackSlots))
	return contract.LimitStatus{InUse: inUse, Limit: limit.Maximum, Saturated: inUse >= limit.Maximum}
}

func (service *FlowService) acquireCallback(ctx context.Context) (context.Context, func(), bool) {
	service.callbackMu.Lock()
	if service.callbackDrain || service.callbackSlots == nil || service.requester == nil || service.secrets == nil || service.callbackCtx == nil {
		service.callbackMu.Unlock()
		return nil, nil, false
	}
	select {
	case service.callbackSlots <- struct{}{}:
		callbackCtx := service.callbackCtx
		service.callbackMu.Unlock()
		workCtx, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(callbackCtx, cancel)
		return workCtx, func() {
			stop()
			cancel()
			<-service.callbackSlots
		}, true
	default:
		service.callbackMu.Unlock()
		return nil, nil, false
	}
}

func (service *FlowService) failConsumed(ctx context.Context, bundle flowBundle, stage contract.OAuthDiagnosticStage, cause error) {
	_, _ = service.store.FailAuthFlow(ctx, bundle.flowID, oauthDiagnostic(bundle.flowID, stage, cause))
}

func validIssuer(values []string, bundle flowBundle) bool {
	if bundle.issuerResponseUsed {
		return len(values) == 1 && values[0] == bundle.registration.Issuer
	}
	return len(values) == 0 || len(values) == 1 && values[0] == bundle.registration.Issuer
}

func callbackValues(rawQuery string, names ...string) (map[string][]string, bool) {
	wanted := make(map[string]struct{}, len(names))
	result := make(map[string][]string, len(names))
	for _, name := range names {
		wanted[name] = struct{}{}
	}
	if rawQuery == "" {
		return result, true
	}
	for _, pair := range strings.Split(rawQuery, "&") {
		parts := strings.SplitN(pair, "=", 2)
		name, err := url.QueryUnescape(parts[0])
		if err != nil {
			continue
		}
		if _, consumed := wanted[name]; !consumed {
			continue
		}
		value := ""
		if len(parts) == 2 {
			value, err = url.QueryUnescape(parts[1])
			if err != nil {
				return nil, false
			}
		}
		result[name] = append(result[name], value)
	}
	return result, true
}
