package oauth

import (
	"context"
	"database/sql"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCallbackConsumesStateBeforeCodeErrorAndIssuerValidation(t *testing.T) {
	store := &callbackFlowStore{flowStoreFake: flowStoreFake{created: flowCreateResult(contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic})}}
	service := callbackService(store, &tokenRequester{}, &tokenSecrets{})
	service.byState["state-once"] = callbackBundle("state-once", true, contract.TokenEndpointAuthNone)

	result := service.HandleCallback(context.Background(), "state=state-once&code=code&error=denied&iss=https%3A%2F%2Fissuer.example")
	assert.Equal(t, CallbackInvalid, result.Outcome)
	assert.Empty(t, service.byState)
	assert.Zero(t, store.beginCalls)

	replay := service.HandleCallback(context.Background(), "state=state-once&code=code&iss=https%3A%2F%2Fissuer.example")
	assert.Equal(t, CallbackInvalid, replay.Outcome)

	service.byState["error-state"] = callbackBundle("error-state", true, contract.TokenEndpointAuthNone)
	missingIssuer := service.HandleCallback(context.Background(), "state=error-state&error=access_denied")
	assert.Equal(t, CallbackInvalid, missingIssuer.Outcome)
	assert.Empty(t, service.byState)
	assert.Equal(t, contract.AuthFlowFailed, store.transitioned.State)

	service.byState["valid-error"] = callbackBundle("valid-error", true, contract.TokenEndpointAuthNone)
	errorResult := service.HandleCallback(context.Background(), "state=valid-error&error=access_denied&iss=https%3A%2F%2Fissuer.example&error_description=ignored")
	assert.Equal(t, CallbackInvalid, errorResult.Outcome)
	assert.Equal(t, contract.AuthFlowFailed, store.transitioned.State)
	require.NotNil(t, store.transitioned.Diagnostic)
	assert.Equal(t, contract.OAuthDiagnosticCallbackValidation, store.transitioned.Diagnostic.Stage)
	assert.Equal(t, contract.ReasonAuthenticationRejected, store.transitioned.Diagnostic.Reason)
	assert.Zero(t, store.beginCalls)
}

func TestCallbackRejectsStateMultiplicityBeforeConsumption(t *testing.T) {
	service := callbackService(&callbackFlowStore{}, &tokenRequester{}, &tokenSecrets{})
	service.byState["retryable"] = callbackBundle("retryable", false, contract.TokenEndpointAuthNone)
	for _, raw := range []string{"", "state=", "state=retryable&state=again", "state=%zz"} {
		result := service.HandleCallback(context.Background(), raw)
		assert.Equal(t, CallbackInvalid, result.Outcome, raw)
	}
	assert.Contains(t, service.byState, "retryable")
}

func TestCallbackSendsExactNoneTokenFormAndPublishesBoundGeneration(t *testing.T) {
	store := &callbackFlowStore{flowStoreFake: flowStoreFake{created: flowCreateResult(contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic})}}
	requester := &tokenRequester{status: http.StatusOK, header: http.Header{"Content-Type": []string{"application/json"}}, body: []byte(`{"access_token":"opaque.access.token","token_type":"Bearer","refresh_token":"refresh-token","expires_in":3600,"scope":"alpha"}`)}
	secrets := new(tokenSecrets)
	service := callbackService(store, requester, secrets)
	bundle := callbackBundle("state-value", false, contract.TokenEndpointAuthNone)
	bundle.requestedScopes = []string{"alpha", "beta"}
	service.byState[bundle.state] = bundle

	result := service.HandleCallback(context.Background(), "state=state-value&code=authorization-code")
	require.Equal(t, CallbackSucceeded, result.Outcome)
	require.Len(t, requester.requests, 1)
	wire := requester.requests[0]
	assert.Equal(t, "https://issuer.example/token", wire.rawURL)
	assert.Equal(t, "application/x-www-form-urlencoded", wire.header.Get("Content-Type"))
	assert.Empty(t, wire.header.Get("Authorization"))
	assert.Equal(t, url.Values{
		"client_id": {"client-id"}, "code": {"authorization-code"}, "code_verifier": {"verifier-value"},
		"grant_type": {"authorization_code"}, "redirect_uri": {"http://127.0.0.1:8210/oauth/callback"}, "resource": {"https://resource.example/mcp"},
	}.Encode(), string(wire.body))
	assert.Equal(t, int64(256*1024), wire.maximum)
	require.NotEmpty(t, secrets.secret)
	decoded, err := DecodeTokenGeneration(secrets.secret)
	require.NoError(t, err)
	assert.Equal(t, "opaque.access.token", decoded.AccessToken)
	assert.Equal(t, "refresh-token", *decoded.RefreshToken)
	assert.Equal(t, []string{"alpha"}, decoded.Scopes)
	assert.True(t, decoded.ScopeSpecified)
	assert.Equal(t, bundle.serverID, decoded.ServerID)
	assert.Equal(t, bundle.registration.Revision, decoded.RegistrationRevision)
	assert.Zero(t, service.CallbackStatus().InUse)
}

func TestTokenRequestClientAuthenticationForms(t *testing.T) {
	secret := "s e:c/ret"
	tests := []struct {
		method     contract.TokenEndpointAuthMethod
		wantAuth   bool
		wantClient bool
		wantSecret bool
	}{
		{contract.TokenEndpointAuthClientSecretBasic, true, true, false},
		{contract.TokenEndpointAuthClientSecretPost, false, true, true},
		{contract.TokenEndpointAuthNone, false, true, false},
	}
	for _, test := range tests {
		header, body, err := tokenRequest("code", callbackBundle("state", false, test.method), []byte(secret))
		require.NoError(t, err)
		values, err := url.ParseQuery(string(body))
		require.NoError(t, err)
		assert.Equal(t, test.wantAuth, header.Get("Authorization") != "")
		assert.Equal(t, test.wantClient, values.Has("client_id"))
		assert.Equal(t, test.wantSecret, values.Has("client_secret"))
		if test.wantAuth {
			encoded := strings.TrimPrefix(header.Get("Authorization"), "Basic ")
			decoded, err := base64.StdEncoding.DecodeString(encoded)
			require.NoError(t, err)
			assert.Equal(t, url.QueryEscape("client-id")+":"+url.QueryEscape(secret), string(decoded))
		}
	}
}

func TestCallbackShutdownCancelsExchangeAndReleasesAdmission(t *testing.T) {
	started := make(chan struct{}, 1)
	requester := &tokenRequester{started: started, respectContext: true}
	service := callbackService(&callbackFlowStore{flowStoreFake: flowStoreFake{created: flowCreateResult(contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic})}}, requester, &tokenSecrets{})
	service.byState["shutdown-state"] = callbackBundle("shutdown-state", false, contract.TokenEndpointAuthNone)
	done := make(chan CallbackResult, 1)
	go func() { done <- service.HandleCallback(context.Background(), "state=shutdown-state&code=code") }()
	<-started
	assert.Equal(t, int64(1), service.CallbackStatus().InUse)
	service.Shutdown()
	result := <-done
	assert.Equal(t, CallbackInvalid, result.Outcome)
	assert.Zero(t, service.CallbackStatus().InUse)
	assert.Empty(t, service.byState)
	assert.Equal(t, CallbackTransient, service.HandleCallback(context.Background(), "state=other&code=code").Outcome)
}

func TestCallbackAdmissionEightPlusOneDoesNotConsumeSaturatedState(t *testing.T) {
	started := make(chan struct{}, 8)
	release := make(chan struct{})
	requester := &tokenRequester{status: http.StatusOK, header: http.Header{"Content-Type": []string{"application/json"}}, body: []byte(`{"access_token":"token","token_type":"Bearer"}`), started: started, release: release}
	store := &callbackFlowStore{flowStoreFake: flowStoreFake{created: flowCreateResult(contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic})}}
	service := callbackService(store, requester, &tokenSecrets{})
	for index := 0; index < 9; index++ {
		state := "state-" + string(rune('a'+index))
		service.byState[state] = callbackBundle(state, false, contract.TokenEndpointAuthNone)
	}
	var wait sync.WaitGroup
	for index := 0; index < 8; index++ {
		state := "state-" + string(rune('a'+index))
		wait.Add(1)
		go func() {
			defer wait.Done()
			service.HandleCallback(context.Background(), "state="+state+"&code=code")
		}()
	}
	for index := 0; index < 8; index++ {
		<-started
	}
	status := service.CallbackStatus()
	assert.Equal(t, int64(8), status.InUse)
	assert.True(t, status.Saturated)
	ninth := service.HandleCallback(context.Background(), "state=state-i&code=code")
	assert.Equal(t, CallbackTransient, ninth.Outcome)
	assert.Contains(t, service.byState, "state-i")
	close(release)
	wait.Wait()
	assert.Zero(t, service.CallbackStatus().InUse)
}

func callbackService(store *callbackFlowStore, requester *tokenRequester, secrets *tokenSecrets) *FlowService {
	service := newFlowService(store, flowResolverFake{}, &flowRegistrarFake{}, zeroReader{}, "http://127.0.0.1:8210/oauth/callback", func() time.Time { return flowTime })
	service.configureCallback(requester, secrets, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	return service
}

func callbackBundle(state string, issuerRequired bool, method contract.TokenEndpointAuthMethod) flowBundle {
	created := flowCreateResult(contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic})
	return flowBundle{
		serverID: created.Flow.ServerID, flowID: created.Flow.ID, desiredRevision: "1", authority: created.Authority,
		registration: servers.OAuthRegistrationAuthority{Revision: "1", Mode: contract.RegistrationDynamic, Issuer: "https://issuer.example", ClientID: "client-id", CallbackURL: "http://127.0.0.1:8210/oauth/callback", ResourceURL: "https://resource.example/mcp", TokenEndpointAuthMethod: method, CreatedAt: flowTime.Format(time.RFC3339Nano)},
		graph:        Graph{Resource: "https://resource.example/mcp", Issuer: "https://issuer.example", TokenEndpoint: "https://issuer.example/token"},
		state:        state, verifier: "verifier-value", issuerResponseUsed: issuerRequired,
	}
}

type callbackFlowStore struct {
	flowStoreFake
	mu         sync.Mutex
	beginCalls int
}

func (store *callbackFlowStore) BeginAuthFlowExchange(_ context.Context, fence servers.OAuthTokenFence) (servers.AuthFlow, error) {
	store.mu.Lock()
	store.beginCalls++
	store.mu.Unlock()
	flow := store.created.Flow
	flow.ID, flow.ServerID, flow.State = fence.FlowID, fence.ServerID, contract.AuthFlowExchanging
	return flow, nil
}
func (store *callbackFlowStore) OAuthTokenAuthorityCallback(servers.OAuthTokenFence) (keyring.AuthorityCallback, error) {
	return func(context.Context, *sql.Tx, keyring.AuthorityUpdate) (string, error) { return "1", nil }, nil
}

type tokenWire struct {
	rawURL  string
	header  http.Header
	body    []byte
	maximum int64
}

type tokenRequester struct {
	mu             sync.Mutex
	status         int
	header         http.Header
	body           []byte
	requests       []tokenWire
	started        chan struct{}
	release        chan struct{}
	respectContext bool
}

func (requester *tokenRequester) Request(ctx context.Context, rawURL string, _ bool, _ string, header http.Header, body []byte, maximum int64) (int, http.Header, []byte, error) {
	requester.mu.Lock()
	requester.requests = append(requester.requests, tokenWire{rawURL: rawURL, header: header.Clone(), body: append([]byte(nil), body...), maximum: maximum})
	requester.mu.Unlock()
	if requester.started != nil {
		requester.started <- struct{}{}
		if requester.respectContext {
			<-ctx.Done()
			return 0, nil, nil, ctx.Err()
		}
		<-requester.release
	}
	return requester.status, requester.header.Clone(), append([]byte(nil), requester.body...), nil
}

type tokenSecrets struct {
	mu     sync.Mutex
	secret []byte
}

func (secrets *tokenSecrets) ReadActive(context.Context, keyring.Namespace) ([]byte, keyring.CutoverResult, error) {
	return []byte("client-secret"), keyring.CutoverResult{}, nil
}
func (secrets *tokenSecrets) ReplaceFencedAfterAuthorizationSuccess(_ context.Context, _ keyring.Namespace, secret []byte, _ keyring.AuthorityCallback) (keyring.CutoverResult, error) {
	secrets.mu.Lock()
	secrets.secret = append([]byte(nil), secret...)
	secrets.mu.Unlock()
	return keyring.CutoverResult{Revision: "1"}, nil
}
