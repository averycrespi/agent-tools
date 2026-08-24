package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var registrationTime = time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC)

type recordedMachineRequest struct {
	url     string
	trusted bool
	method  string
	header  http.Header
	body    []byte
	maximum int64
}

type registrationRequester struct {
	status   int
	header   http.Header
	body     string
	err      error
	requests []recordedMachineRequest
}

func (requester *registrationRequester) Request(_ context.Context, rawURL string, trusted bool, method string, header http.Header, body []byte, maximum int64) (int, http.Header, []byte, error) {
	requester.requests = append(requester.requests, recordedMachineRequest{url: rawURL, trusted: trusted, method: method, header: header.Clone(), body: append([]byte(nil), body...), maximum: maximum})
	return requester.status, requester.header, []byte(requester.body), requester.err
}

type registrationStoreFake struct {
	published []servers.OAuthRegistrationAuthority
	callback  keyring.AuthorityCallback
	read      servers.OAuthRegistrationAuthority
	err       error
}

func (store *registrationStoreFake) PublishPublicRegistration(_ context.Context, _ servers.RegistrationFence, registration servers.OAuthRegistrationAuthority) (servers.OAuthRegistrationAuthority, error) {
	if store.err != nil {
		return servers.OAuthRegistrationAuthority{}, store.err
	}
	registration.Revision = "1"
	store.published = append(store.published, registration)
	store.read = registration
	return registration, nil
}
func (store *registrationStoreFake) RegistrationAuthorityCallback(_ servers.RegistrationFence, registration servers.OAuthRegistrationAuthority) (keyring.AuthorityCallback, error) {
	if store.err != nil {
		return nil, store.err
	}
	store.read = registration
	return store.callback, nil
}
func (store *registrationStoreFake) OAuthRegistration(context.Context, string) (servers.OAuthRegistrationAuthority, error) {
	if store.err != nil {
		return servers.OAuthRegistrationAuthority{}, store.err
	}
	store.read.Revision = "1"
	return store.read, nil
}

type secretPublisherFake struct {
	calls  int
	secret []byte
	err    error
}

func (publisher *secretPublisherFake) ReplaceFenced(_ context.Context, _ keyring.Namespace, secret []byte, _ keyring.AuthorityCallback) (keyring.CutoverResult, error) {
	publisher.calls++
	publisher.secret = append([]byte(nil), secret...)
	return keyring.CutoverResult{Revision: "1"}, publisher.err
}

func TestStaticRegistrationPublishesWithoutNetworkOrSecret(t *testing.T) {
	requester := &registrationRequester{}
	store := &registrationStoreFake{}
	secrets := &secretPublisherFake{}
	registrar := newRegistrar(requester, store, secrets, "01ARZ3NDEKTSV4RRFFQ69G5FAV", func() time.Time { return registrationTime }, func() bool { return true })
	graph := registrationGraph([]string{"client_secret_basic"})
	issuer := graph.Issuer
	published, err := registrar.Register(context.Background(), registrationRequest(graph, contract.StaticOAuthRegistration{Mode: contract.RegistrationStatic, Issuer: &issuer, ClientID: "static-client", TokenEndpointAuthMethod: contract.TokenEndpointAuthClientSecretBasic}))
	require.NoError(t, err)
	assert.Equal(t, "1", published.Revision)
	assert.Equal(t, contract.RegistrationStatic, published.Mode)
	assert.Equal(t, "static-client", published.ClientID)
	assert.Equal(t, graph.Resource, published.ResourceURL)
	assert.Empty(t, requester.requests)
	assert.Zero(t, secrets.calls)
}

func TestRegistrationRejectsInvalidCallbackAndUnsupportedMethodBeforeNetwork(t *testing.T) {
	graph := registrationGraph([]string{"none"})
	tests := []RegistrationRequest{
		registrationRequest(graph, contract.StaticOAuthRegistration{Mode: contract.RegistrationStatic, ClientID: "client", TokenEndpointAuthMethod: contract.TokenEndpointAuthClientSecretBasic}),
		registrationRequest(graph, contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic}),
	}
	tests[1].CallbackURL = "http://example.com/oauth/callback"
	for _, request := range tests {
		requester := &registrationRequester{}
		registrar := newRegistrar(requester, &registrationStoreFake{}, &secretPublisherFake{}, "01ARZ3NDEKTSV4RRFFQ69G5FAV", func() time.Time { return registrationTime }, func() bool { return true })
		_, err := registrar.Register(context.Background(), request)
		assert.ErrorIs(t, err, ErrRegistrationRejected)
		assert.Empty(t, requester.requests)
	}
}

func TestDynamicRegistrationSelectsBasicThenPostThenNoneAndSendsExactUnauthenticatedRequest(t *testing.T) {
	tests := []struct {
		name       string
		methods    []string
		selected   contract.TokenEndpointAuthMethod
		secret     string
		expires    int64
		wantSecret bool
	}{
		{name: "basic", methods: []string{"none", "client_secret_post", "client_secret_basic"}, selected: contract.TokenEndpointAuthClientSecretBasic, secret: "basic-secret", expires: registrationTime.Add(time.Hour).Unix(), wantSecret: true},
		{name: "post", methods: []string{"none", "client_secret_post"}, selected: contract.TokenEndpointAuthClientSecretPost, secret: "post-secret", expires: 0, wantSecret: true},
		{name: "none", methods: []string{"none"}, selected: contract.TokenEndpointAuthNone},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			graph := registrationGraph(test.methods)
			response := dynamicResponseJSON("dynamic-client", test.selected, test.secret, test.expires)
			requester := &registrationRequester{status: http.StatusCreated, header: http.Header{"Content-Type": []string{"application/json"}}, body: response}
			store := &registrationStoreFake{}
			secrets := &secretPublisherFake{}
			registrar := newRegistrar(requester, store, secrets, "01ARZ3NDEKTSV4RRFFQ69G5FAV", func() time.Time { return registrationTime }, func() bool { return true })
			published, err := registrar.Register(context.Background(), registrationRequest(graph, contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic}))
			require.NoError(t, err)
			assert.Equal(t, test.selected, published.TokenEndpointAuthMethod)
			require.Len(t, requester.requests, 1)
			wire := requester.requests[0]
			assert.Equal(t, http.MethodPost, wire.method)
			assert.Equal(t, graph.RegistrationEndpoint, wire.url)
			assert.False(t, wire.trusted)
			assert.Empty(t, wire.header.Values("Authorization"))
			assert.Equal(t, contract.MediaTypeJSON, wire.header.Get("Content-Type"))
			assert.Equal(t, `{"application_type":"native","redirect_uris":["http://127.0.0.1:8210/oauth/callback"],"response_types":["code"],"grant_types":["authorization_code","refresh_token"],"token_endpoint_auth_method":"`+string(test.selected)+`"}`, string(wire.body))
			if test.wantSecret {
				assert.Equal(t, 1, secrets.calls)
				assert.Equal(t, test.secret, string(secrets.secret))
				assert.Empty(t, store.published)
			} else {
				assert.Zero(t, secrets.calls)
				require.Len(t, store.published, 1)
			}
		})
	}
}

func TestDynamicRegistrationRejectsEveryInvalidResponseWithoutRetryOrPublication(t *testing.T) {
	future := registrationTime.Add(time.Hour).Unix()
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "wrong status", status: 200, body: dynamicResponseJSON("client", contract.TokenEndpointAuthClientSecretBasic, "secret", future)},
		{name: "missing client", status: 201, body: `{"redirect_uris":["http://127.0.0.1:8210/oauth/callback"],"response_types":["code"],"grant_types":["authorization_code","refresh_token"],"token_endpoint_auth_method":"client_secret_basic","client_secret":"secret","client_secret_expires_at":1}`},
		{name: "wrong callback", status: 201, body: `{"client_id":"client","redirect_uris":["http://127.0.0.1:8210/wrong"],"response_types":["code"],"grant_types":["authorization_code","refresh_token"],"token_endpoint_auth_method":"client_secret_basic","client_secret":"secret","client_secret_expires_at":` + jsonNumber(future) + `}`},
		{name: "missing grant", status: 201, body: `{"client_id":"client","redirect_uris":["http://127.0.0.1:8210/oauth/callback"],"response_types":["code"],"grant_types":["authorization_code"],"token_endpoint_auth_method":"client_secret_basic","client_secret":"secret","client_secret_expires_at":` + jsonNumber(future) + `}`},
		{name: "wrong method", status: 201, body: dynamicResponseJSON("client", contract.TokenEndpointAuthClientSecretPost, "secret", future)},
		{name: "confidential missing secret", status: 201, body: dynamicResponseJSON("client", contract.TokenEndpointAuthClientSecretBasic, "", future)},
		{name: "secret missing expiry", status: 201, body: `{"client_id":"client","redirect_uris":["http://127.0.0.1:8210/oauth/callback"],"response_types":["code"],"grant_types":["authorization_code","refresh_token"],"token_endpoint_auth_method":"client_secret_basic","client_secret":"secret"}`},
		{name: "expired secret", status: 201, body: dynamicResponseJSON("client", contract.TokenEndpointAuthClientSecretBasic, "secret", registrationTime.Unix())},
		{name: "public returned secret", status: 201, body: dynamicResponseJSON("client", contract.TokenEndpointAuthNone, "secret", future)},
		{name: "duplicate member", status: 201, body: `{"client_id":"one","client_id":"two"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			graph := registrationGraph([]string{"client_secret_basic"})
			if test.name == "public returned secret" {
				graph.TokenEndpointAuthMethodsSupported = []string{"none"}
			}
			requester := &registrationRequester{status: test.status, header: http.Header{"Content-Type": []string{"application/json"}}, body: test.body}
			store := &registrationStoreFake{}
			secrets := &secretPublisherFake{}
			registrar := newRegistrar(requester, store, secrets, "01ARZ3NDEKTSV4RRFFQ69G5FAV", func() time.Time { return registrationTime }, func() bool { return true })
			_, err := registrar.Register(context.Background(), registrationRequest(graph, contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic}))
			assert.Error(t, err)
			assert.Len(t, requester.requests, 1)
			assert.Zero(t, secrets.calls)
			assert.Empty(t, store.published)
		})
	}
}

func TestDynamicRegistrationReusesOnlyUnexpiredExactAuthority(t *testing.T) {
	graph := registrationGraph([]string{"client_secret_basic"})
	request := registrationRequest(graph, contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic})
	request.ExpectedRegistrationRevision = "1"
	future := registrationTime.Add(time.Hour).Format(time.RFC3339Nano)
	store := &registrationStoreFake{read: servers.OAuthRegistrationAuthority{Revision: "1", Mode: contract.RegistrationDynamic, Issuer: graph.Issuer, ClientID: "existing-client", CallbackURL: request.CallbackURL, ResourceURL: graph.Resource, TokenEndpointAuthMethod: contract.TokenEndpointAuthClientSecretBasic, CreatedAt: registrationTime.Format(time.RFC3339Nano), ClientSecretExpiresAt: &future}}
	requester := &registrationRequester{}
	registrar := newRegistrar(requester, store, &secretPublisherFake{}, "01ARZ3NDEKTSV4RRFFQ69G5FAV", func() time.Time { return registrationTime }, func() bool { return true })
	registration, err := registrar.Register(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, "existing-client", registration.ClientID)
	assert.Empty(t, requester.requests)

	expired := registrationTime.Format(time.RFC3339Nano)
	store.read.ClientSecretExpiresAt = &expired
	requester.status = http.StatusCreated
	requester.header = http.Header{"Content-Type": []string{"application/json"}}
	requester.body = dynamicResponseJSON("replacement-client", contract.TokenEndpointAuthClientSecretBasic, "replacement-secret", registrationTime.Add(2*time.Hour).Unix())
	_, err = registrar.Register(context.Background(), request)
	require.NoError(t, err)
	assert.Len(t, requester.requests, 1)
}

func TestDynamicRegistrationStaleAfterResponsePublishesNothing(t *testing.T) {
	currentCalls := 0
	current := func() bool { currentCalls++; return currentCalls == 1 }
	graph := registrationGraph([]string{"none"})
	requester := &registrationRequester{status: 201, header: http.Header{"Content-Type": []string{"application/json"}}, body: dynamicResponseJSON("client", contract.TokenEndpointAuthNone, "", 0)}
	store := &registrationStoreFake{}
	registrar := newRegistrar(requester, store, &secretPublisherFake{}, "01ARZ3NDEKTSV4RRFFQ69G5FAV", func() time.Time { return registrationTime }, current)
	_, err := registrar.Register(context.Background(), registrationRequest(graph, contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic}))
	assert.ErrorIs(t, err, ErrRegistrationStale)
	assert.Empty(t, store.published)
}

func registrationGraph(methods []string) Graph {
	return Graph{Resource: "https://resource.example/mcp", Issuer: "https://issuer.example", RegistrationEndpoint: "https://register.example/clients", TokenEndpointAuthMethodsSupported: methods, trustedOrigins: map[string]struct{}{"https://resource.example": {}}}
}

func registrationRequest(graph Graph, registration contract.OAuthRegistration) RegistrationRequest {
	return RegistrationRequest{ServerID: "01ARZ3NDEKTSV4RRFFQ69G5FAW", ExpectedDesiredRevision: "1", ExpectedRegistrationRevision: "0", ExpectedOAuthClientRevision: "0", Registration: registration, Graph: graph, CallbackURL: "http://127.0.0.1:8210/oauth/callback"}
}

func dynamicResponseJSON(clientID string, method contract.TokenEndpointAuthMethod, secret string, expires int64) string {
	value := map[string]any{"client_id": clientID, "redirect_uris": []string{"http://127.0.0.1:8210/oauth/callback"}, "response_types": []string{"code"}, "grant_types": []string{"authorization_code", "refresh_token"}, "token_endpoint_auth_method": method, "registration_access_token": "discard-me", "registration_client_uri": "https://register.example/manage"}
	if secret != "" {
		value["client_secret"] = secret
		value["client_secret_expires_at"] = expires
	}
	contents, _ := json.Marshal(value)
	return string(contents)
}

func jsonNumber(value int64) string { return strconv.FormatInt(value, 10) }
