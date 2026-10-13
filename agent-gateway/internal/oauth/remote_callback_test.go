package oauth

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRemoteCallbackSelectionBeforeRegistrationAndListenerAcquisition(t *testing.T) {
	const main = "https://gateway.example:8443/oauth/callback"
	for _, selected := range []string{"", main, "https://other.example/oauth/callback", "https://gateway.example/oauth/callback", "http://localhost:3118/callback"} {
		t.Run(selected, func(t *testing.T) {
			created := flowCreateResult(contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic})
			if selected != "" {
				created.Configuration.Authentication.CallbackURI = &selected
			}
			store := &flowStoreFake{created: created}
			registration := flowRegistration()
			registration.CallbackURL = main
			registrar := &flowRegistrarFake{registration: registration}
			service := newFlowService(store, flowResolverFake{graph: Graph{Resource: created.Configuration.Resource, Issuer: registration.Issuer, AuthorizationEndpoint: "https://issuer.example/authorize"}}, registrar, zeroReader{}, main, func() time.Time { return flowTime })
			service.listeners = callbackAcquireFunc(func(context.Context, string, string, time.Time) (func(), error) {
				t.Fatal("remote flow acquired a public listener")
				return nil, nil
			})
			flow, err := service.Create(t.Context(), FlowRequest{ServerID: created.Flow.ServerID, ExpectedDesiredRevision: "1"})
			if selected == "" || selected == main {
				require.NoError(t, err)
				parsed, err := url.Parse(flow.AuthorizationURL)
				require.NoError(t, err)
				assert.Equal(t, main, parsed.Query().Get("redirect_uri"))
				assert.Equal(t, 1, registrar.calls)
			} else {
				require.Error(t, err)
				assert.Empty(t, flow.AuthorizationURL)
				assert.Zero(t, registrar.calls)
				assert.Empty(t, service.byState)
			}
			service.Shutdown()
		})
	}
}

func TestRemoteCallbackBindsAuthorityAndPreservesSingleExchange(t *testing.T) {
	const main = "https://gateway.example:8443/oauth/callback"
	store := &callbackFlowStore{flowStoreFake: flowStoreFake{created: flowCreateResult(contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic})}}
	requester := &tokenRequester{status: 200, header: http.Header{"Content-Type": {contract.MediaTypeJSON}}, body: []byte(`{"access_token":"fixture-token","token_type":"Bearer"}`)}
	service := callbackService(store, requester, new(tokenSecrets))
	service.callbackURL = main
	bundle := callbackBundle("remote-state", false, contract.TokenEndpointAuthNone)
	bundle.registration.CallbackURL = main
	service.byState[bundle.state] = bundle
	query := "state=remote-state&code=fixture-code"
	for _, uri := range []string{"https://other.example:8443/oauth/callback", "https://gateway.example/oauth/callback", "https://gateway.example:8443/other", "http://127.0.0.1:8210/oauth/callback"} {
		assert.Equal(t, CallbackInvalid, service.HandleCallbackAt(t.Context(), query, uri, "").Outcome)
		assert.Contains(t, service.byState, bundle.state)
		assert.Empty(t, requester.requests)
	}
	require.Equal(t, CallbackSucceeded, service.HandleCallback(t.Context(), query).Outcome)
	require.Len(t, requester.requests, 1)
	form, err := url.ParseQuery(string(requester.requests[0].body))
	require.NoError(t, err)
	assert.Equal(t, main, form.Get("redirect_uri"))
	assert.Equal(t, CallbackInvalid, service.HandleCallback(t.Context(), query).Outcome)
	assert.Len(t, requester.requests, 1)
	service.Shutdown()
}
