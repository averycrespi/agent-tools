package oauth

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/runtimes"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRefreshServiceSingleFlightUsesExactPublicFormAndDistinctRotation(t *testing.T) {
	old := refreshGeneration(contract.TokenEndpointAuthNone)
	operation := &refreshOperationFake{token: mustTokenBytes(t, old)}
	coordinator := &refreshCoordinatorFake{operation: operation}
	requester := &refreshRequesterFake{started: make(chan struct{}, 1), release: make(chan struct{}), status: 200, header: http.Header{"Content-Type": []string{contract.MediaTypeJSON}}, body: []byte(`{"access_token":"new-access","token_type":"Bearer","refresh_token":"new-refresh"}`)}
	states := []contract.ServerCredentialState{}
	service := newRefreshService(refreshStoreFake{prepared: refreshPrepared(contract.TokenEndpointAuthNone)}, coordinator, refreshResolverFake{graph: refreshGraph()}, requester, refreshServerID, func() time.Time { return refreshNow }, func(_ string, state contract.ServerCredentialState, _ bool) { states = append(states, state) }, nil)
	results := make(chan error, 2)
	go func() {
		_, err := service.Refresh(context.Background(), RefreshRequest{ServerID: refreshServerID})
		results <- err
	}()
	<-requester.started
	service.mu.Lock()
	call := service.calls[refreshServerID]
	service.mu.Unlock()
	require.NotNil(t, call)
	go func() {
		_, err := service.Refresh(context.Background(), RefreshRequest{ServerID: refreshServerID})
		results <- err
	}()
	<-call.joinObserved
	close(requester.release)
	require.NoError(t, <-results)
	require.NoError(t, <-results)
	assert.Equal(t, 1, requester.calls)
	assert.Equal(t, 1, coordinator.calls)
	values, err := url.ParseQuery(string(requester.requestBody))
	require.NoError(t, err)
	assert.Equal(t, "refresh_token", values.Get("grant_type"))
	assert.Equal(t, "old-refresh", values.Get("refresh_token"))
	assert.Equal(t, "https://resource.example/mcp", values.Get("resource"))
	assert.Equal(t, "client", values.Get("client_id"))
	assert.False(t, values.Has("scope"))
	installed, err := DecodeTokenGeneration(operation.installed)
	require.NoError(t, err)
	assert.Equal(t, "new-refresh", *installed.RefreshToken)
	assert.Contains(t, states, contract.ServerCredentialRefreshing)
}

func TestRefreshOAuthChallengeUsesExactFenceWithoutGenericCallbacks(t *testing.T) {
	operation := &refreshOperationFake{token: mustTokenBytes(t, refreshGeneration(contract.TokenEndpointAuthNone))}
	requester := &refreshRequesterFake{status: 200, header: http.Header{"Content-Type": []string{contract.MediaTypeJSON}}, body: []byte(`{"access_token":"new-access","token_type":"Bearer","refresh_token":"new-refresh"}`)}
	states, triggers := 0, 0
	service := newRefreshService(refreshStoreFake{prepared: refreshPrepared(contract.TokenEndpointAuthNone)}, &refreshCoordinatorFake{operation: operation}, refreshResolverFake{graph: refreshGraph()}, requester, refreshServerID, func() time.Time { return refreshNow }, func(string, contract.ServerCredentialState, bool) { states++ }, func(string) { triggers++ })
	prepared := refreshPrepared(contract.TokenEndpointAuthNone)

	result, err := service.RefreshOAuthChallenge(context.Background(), runtimes.OAuthChallengeRefreshRequest{
		ServerID: refreshServerID, ExpectedDesiredRevision: prepared.Fence.ExpectedDesiredRevision,
		ExpectedRegistrationRevision: prepared.Fence.ExpectedRegistrationRevision,
		ExpectedOAuthClientRevision:  prepared.Fence.ExpectedOAuthClientRevision,
		ExpectedOAuthTokensRevision:  prepared.Fence.ExpectedOAuthTokensRevision,
		ChallengeMetadata:            []string{"https://resource.example/metadata"},
	})

	require.NoError(t, err)
	assert.Equal(t, "2", result.OAuthTokensRevision)
	assert.Equal(t, 1, requester.calls)
	assert.Zero(t, states)
	assert.Zero(t, triggers)
}

func TestRefreshOAuthChallengePostHandoffUncertaintyInvalidatesWithoutGenericCallbacks(t *testing.T) {
	operation := &refreshOperationFake{token: mustTokenBytes(t, refreshGeneration(contract.TokenEndpointAuthNone))}
	requester := &refreshRequesterFake{err: errors.New("EOF"), handoff: true}
	states, triggers := 0, 0
	service := newRefreshService(refreshStoreFake{prepared: refreshPrepared(contract.TokenEndpointAuthNone)}, &refreshCoordinatorFake{operation: operation}, refreshResolverFake{graph: refreshGraph()}, requester, refreshServerID, func() time.Time { return refreshNow }, func(string, contract.ServerCredentialState, bool) { states++ }, func(string) { triggers++ })
	prepared := refreshPrepared(contract.TokenEndpointAuthNone)

	_, err := service.RefreshOAuthChallenge(context.Background(), runtimes.OAuthChallengeRefreshRequest{ServerID: refreshServerID, ExpectedDesiredRevision: prepared.Fence.ExpectedDesiredRevision, ExpectedRegistrationRevision: prepared.Fence.ExpectedRegistrationRevision, ExpectedOAuthClientRevision: prepared.Fence.ExpectedOAuthClientRevision, ExpectedOAuthTokensRevision: prepared.Fence.ExpectedOAuthTokensRevision})

	assert.Error(t, err)
	assert.Equal(t, 1, operation.invalidations)
	assert.Zero(t, states)
	assert.Zero(t, triggers)
}

func TestRefreshOAuthChallengeRejectsStaleFenceBeforeNetwork(t *testing.T) {
	operation := &refreshOperationFake{token: mustTokenBytes(t, refreshGeneration(contract.TokenEndpointAuthNone))}
	requester := &refreshRequesterFake{}
	service := newRefreshService(refreshStoreFake{prepared: refreshPrepared(contract.TokenEndpointAuthNone)}, &refreshCoordinatorFake{operation: operation}, refreshResolverFake{graph: refreshGraph()}, requester, refreshServerID, func() time.Time { return refreshNow }, nil, nil)

	_, err := service.RefreshOAuthChallenge(context.Background(), runtimes.OAuthChallengeRefreshRequest{ServerID: refreshServerID, ExpectedDesiredRevision: "stale", ExpectedRegistrationRevision: "1", ExpectedOAuthClientRevision: "0", ExpectedOAuthTokensRevision: "1"})

	assert.ErrorIs(t, err, servers.ErrStaleRevision)
	assert.Zero(t, requester.calls)
}

func TestRefreshOAuthChallengeDoesNotJoinGenericRefresh(t *testing.T) {
	operation := &refreshOperationFake{token: mustTokenBytes(t, refreshGeneration(contract.TokenEndpointAuthNone))}
	requester := &refreshRequesterFake{started: make(chan struct{}, 1), release: make(chan struct{}), status: 200, header: http.Header{"Content-Type": []string{contract.MediaTypeJSON}}, body: []byte(`{"access_token":"new-access","token_type":"Bearer","refresh_token":"new-refresh"}`)}
	service := newRefreshService(refreshStoreFake{prepared: refreshPrepared(contract.TokenEndpointAuthNone)}, &refreshCoordinatorFake{operation: operation}, refreshResolverFake{graph: refreshGraph()}, requester, refreshServerID, func() time.Time { return refreshNow }, nil, nil)
	generic := make(chan error, 1)
	go func() {
		_, err := service.Refresh(context.Background(), RefreshRequest{ServerID: refreshServerID})
		generic <- err
	}()
	<-requester.started
	prepared := refreshPrepared(contract.TokenEndpointAuthNone)

	_, err := service.RefreshOAuthChallenge(context.Background(), runtimes.OAuthChallengeRefreshRequest{ServerID: refreshServerID, ExpectedDesiredRevision: prepared.Fence.ExpectedDesiredRevision, ExpectedRegistrationRevision: prepared.Fence.ExpectedRegistrationRevision, ExpectedOAuthClientRevision: prepared.Fence.ExpectedOAuthClientRevision, ExpectedOAuthTokensRevision: prepared.Fence.ExpectedOAuthTokensRevision})

	assert.ErrorIs(t, err, ErrRefreshIneligible)
	close(requester.release)
	require.NoError(t, <-generic)
	assert.Equal(t, 1, requester.calls)
}

func TestRefreshServicePostHandoffFailureInvalidatesAndRequiresReauthorization(t *testing.T) {
	operation := &refreshOperationFake{token: mustTokenBytes(t, refreshGeneration(contract.TokenEndpointAuthNone))}
	requester := &refreshRequesterFake{err: errors.New("EOF"), handoff: true}
	var final contract.ServerCredentialState
	service := newRefreshService(refreshStoreFake{prepared: refreshPrepared(contract.TokenEndpointAuthNone)}, &refreshCoordinatorFake{operation: operation}, refreshResolverFake{graph: refreshGraph()}, requester, refreshServerID, func() time.Time { return refreshNow }, func(_ string, state contract.ServerCredentialState, _ bool) { final = state }, nil)
	_, err := service.Refresh(context.Background(), RefreshRequest{ServerID: refreshServerID})
	assert.Error(t, err)
	assert.Equal(t, 1, operation.invalidations)
	assert.Equal(t, contract.ServerCredentialReauthenticationRequired, final)
	assert.Equal(t, 1, requester.calls)
}

func TestRefreshRequestBasicUsesOnlyPercentEncodedAuthorization(t *testing.T) {
	header, body, err := refreshTokenRequest("refresh", servers.OAuthRegistrationAuthority{ClientID: "client:id", TokenEndpointAuthMethod: contract.TokenEndpointAuthClientSecretBasic}, "https://resource.example/mcp", []byte("s/ecret"))
	require.NoError(t, err)
	assert.Equal(t, "Basic "+base64.StdEncoding.EncodeToString([]byte(url.QueryEscape("client:id")+":"+url.QueryEscape("s/ecret"))), header.Get("Authorization"))
	values, err := url.ParseQuery(string(body))
	require.NoError(t, err)
	assert.False(t, values.Has("client_id"))
	assert.False(t, values.Has("client_secret"))
	assert.False(t, values.Has("scope"))
}

func TestRefreshRequestConfidentialOmissionRetainsRefreshToken(t *testing.T) {
	operation := &refreshOperationFake{token: mustTokenBytes(t, refreshGeneration(contract.TokenEndpointAuthClientSecretPost)), client: []byte("client-secret")}
	requester := &refreshRequesterFake{status: 200, header: http.Header{"Content-Type": []string{contract.MediaTypeJSON}}, body: []byte(`{"access_token":"new-access","token_type":"Bearer"}`)}
	service := newRefreshService(refreshStoreFake{prepared: refreshPrepared(contract.TokenEndpointAuthClientSecretPost)}, &refreshCoordinatorFake{operation: operation}, refreshResolverFake{graph: refreshGraph()}, requester, refreshServerID, func() time.Time { return refreshNow }, nil, nil)
	_, err := service.Refresh(context.Background(), RefreshRequest{ServerID: refreshServerID})
	require.NoError(t, err)
	values, err := url.ParseQuery(string(requester.requestBody))
	require.NoError(t, err)
	assert.Equal(t, "client-secret", values.Get("client_secret"))
	installed, err := DecodeTokenGeneration(operation.installed)
	require.NoError(t, err)
	assert.Equal(t, "old-refresh", *installed.RefreshToken)
}

const refreshServerID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

var refreshNow = time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

func refreshGeneration(method contract.TokenEndpointAuthMethod) TokenGeneration {
	expires := refreshNow.Add(30 * time.Second).Format(time.RFC3339Nano)
	refresh := "old-refresh"
	return TokenGeneration{Version: 1, ServerID: refreshServerID, Issuer: "https://issuer.example", RegistrationRevision: "1", Resource: "https://resource.example/mcp", AccessToken: "old-access", RefreshToken: &refresh, Scopes: []string{"read"}, ScopeSpecified: true, IssuedAt: refreshNow.Add(-9*time.Minute - 30*time.Second).Format(time.RFC3339Nano), ExpiresAt: &expires}
}

func mustTokenBytes(t *testing.T, generation TokenGeneration) []byte {
	t.Helper()
	contents, err := json.Marshal(generation)
	require.NoError(t, err)
	return contents
}

func refreshPrepared(method contract.TokenEndpointAuthMethod) servers.OAuthRefreshContext {
	return servers.OAuthRefreshContext{Configuration: servers.AuthFlowOAuthConfiguration{Resource: "https://resource.example/mcp", Authentication: contract.OAuthAuthentication{Mode: contract.AuthenticationOAuth, TrustedOrigins: []string{}}}, Registration: servers.OAuthRegistrationAuthority{Revision: "1", Mode: contract.RegistrationStatic, Issuer: "https://issuer.example", ClientID: "client", ResourceURL: "https://resource.example/mcp", TokenEndpointAuthMethod: method}, Fence: servers.OAuthRefreshFence{ServerID: refreshServerID, ExpectedDesiredRevision: "1", ExpectedRegistrationRevision: "1", ExpectedOAuthClientRevision: map[bool]string{true: "0", false: "1"}[method == contract.TokenEndpointAuthNone], ExpectedOAuthTokensRevision: "1"}}
}

func refreshGraph() Graph {
	return Graph{Resource: "https://resource.example/mcp", Issuer: "https://issuer.example", TokenEndpoint: "https://issuer.example/token", TokenEndpointAuthMethodsSupported: []string{"none", "client_secret_basic", "client_secret_post"}, trustedOrigins: map[string]struct{}{}}
}

type refreshStoreFake struct{ prepared servers.OAuthRefreshContext }

func (store refreshStoreFake) PrepareOAuthRefresh(context.Context, string) (servers.OAuthRefreshContext, error) {
	return store.prepared, nil
}
func (store refreshStoreFake) OAuthRefreshAuthorityCallback(servers.OAuthRefreshFence) (keyring.AuthorityCallback, error) {
	return func(context.Context, *sql.Tx, keyring.AuthorityUpdate) (string, error) { return "2", nil }, nil
}

type refreshResolverFake struct{ graph Graph }

func (resolver refreshResolverFake) Discover(context.Context, Input) (Graph, error) {
	return resolver.graph, nil
}

type refreshCoordinatorFake struct {
	operation refreshOperation
	calls     int
}

func (coordinator *refreshCoordinatorFake) WithOperation(_ context.Context, use func(refreshOperation) error) error {
	coordinator.calls++
	return use(coordinator.operation)
}

type refreshOperationFake struct {
	token         []byte
	client        []byte
	installed     []byte
	invalidations int
}

func (operation *refreshOperationFake) ReadActive(_ context.Context, namespace keyring.Namespace) ([]byte, keyring.CutoverResult, error) {
	if namespace.Kind() == keyring.RecordOAuthClient {
		return append([]byte(nil), operation.client...), keyring.CutoverResult{Revision: "1"}, nil
	}
	return append([]byte(nil), operation.token...), keyring.CutoverResult{Revision: "1"}, nil
}
func (operation *refreshOperationFake) ReplaceFencedAfterAuthorizationSuccess(_ context.Context, _ keyring.Namespace, secret []byte, _ keyring.AuthorityCallback) (keyring.CutoverResult, error) {
	operation.installed = append([]byte(nil), secret...)
	return keyring.CutoverResult{Revision: "2"}, nil
}
func (operation *refreshOperationFake) InvalidateFencedExact(context.Context, keyring.Namespace, keyring.AuthorityCallback) (keyring.CutoverResult, error) {
	operation.invalidations++
	return keyring.CutoverResult{Revision: "2"}, nil
}

type refreshRequesterFake struct {
	mu            sync.Mutex
	calls         int
	status        int
	header        http.Header
	body          []byte
	err           error
	handoff       bool
	started       chan struct{}
	release       chan struct{}
	requestBody   []byte
	requestBodies [][]byte
}

func (requester *refreshRequesterFake) Request(_ context.Context, _ string, _ bool, _ http.Header, body []byte, _ int64, before func()) (int, http.Header, []byte, error) {
	requester.mu.Lock()
	requester.calls++
	requester.requestBody = append([]byte(nil), body...)
	requester.requestBodies = append(requester.requestBodies, append([]byte(nil), body...))
	requester.mu.Unlock()
	if requester.handoff && before != nil {
		before()
	}
	if requester.started != nil {
		requester.started <- struct{}{}
		<-requester.release
	}
	if !requester.handoff && requester.err == nil && before != nil {
		before()
	}
	return requester.status, requester.header.Clone(), append([]byte(nil), requester.body...), requester.err
}
