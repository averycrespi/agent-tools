package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var flowTime = time.Date(2026, 8, 22, 14, 0, 0, 0, time.UTC)

type effectAuditRecorder struct {
	refusePhase string
	events      []contract.AuditEvent
}

func (recorder *effectAuditRecorder) record(event contract.AuditEvent) error {
	if event.Phase == recorder.refusePhase {
		return errors.New("audit refused")
	}
	recorder.events = append(recorder.events, event)
	return nil
}

func (recorder *effectAuditRecorder) assertEvidence(t *testing.T, action string, credential contract.AuditCredential) {
	t.Helper()
	require.Len(t, recorder.events, map[string]int{"attempt": 0, "outcome": 1, "": 2}[recorder.refusePhase])
	for _, event := range recorder.events {
		assert.Equal(t, action, event.Action)
		assert.Equal(t, contract.AuditSystem, event.Actor.Type)
		assert.Equal(t, &credential, event.Initiator)
		assert.Equal(t, credential.ID, event.CorrelationID)
	}
	if recorder.refusePhase == "outcome" {
		assert.Equal(t, "pending", recorder.events[0].Outcome)
	}
	if recorder.refusePhase == "" {
		assert.Equal(t, "succeeded", recorder.events[1].Outcome)
	}
	encoded, err := json.Marshal(recorder.events)
	require.NoError(t, err)
	for _, secret := range []string{"old-access", "old-refresh", "new-access", "new-refresh", "client-secret", "https://"} {
		assert.NotContains(t, string(encoded), secret)
	}
}

func TestFlowPreparationAuditFencesOneTimeAuthority(t *testing.T) {
	for _, phase := range []string{"attempt", "outcome", ""} {
		t.Run(phase, func(t *testing.T) {
			recorder := &effectAuditRecorder{refusePhase: phase}
			store := &flowStoreFake{created: flowCreateResult(contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic}), auditHook: recorder.record}
			bundle := callbackBundle("", false, contract.TokenEndpointAuthNone)
			bundle.graph.AuthorizationEndpoint = "https://issuer.example/authorize"
			registrar := &flowRegistrarFake{registration: bundle.registration}
			service := newFlowService(store, flowResolverFake{graph: bundle.graph}, registrar, zeroReader{}, bundle.registration.CallbackURL, func() time.Time { return flowTime })
			credential := contract.AuditCredential{ID: refreshServerID, Fingerprint: "0123456789abcdef"}
			result, err := service.Create(audit.WithOperator(t.Context(), credential, credential.ID), FlowRequest{ServerID: store.created.Flow.ServerID, ExpectedDesiredRevision: "1"})
			if phase == "" {
				require.NoError(t, err)
				assert.NotEmpty(t, result.AuthorizationURL)
			} else {
				require.Error(t, err)
				assert.Empty(t, result.AuthorizationURL)
				assert.Empty(t, service.byState)
			}
			if phase == "attempt" {
				assert.Zero(t, registrar.calls)
			} else {
				assert.Equal(t, 1, registrar.calls)
			}
			recorder.assertEvidence(t, "prepare", credential)
		})
	}
}

func TestForegroundFlowBuildsExactOneTimePKCEURLAndMemoryBundle(t *testing.T) {
	entropy := make([]byte, 64)
	for index := range entropy {
		entropy[index] = byte(index + 1)
	}
	store := &flowStoreFake{created: flowCreateResult(contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic})}
	resolver := flowResolverFake{graph: Graph{
		Resource: "https://resource.example/mcp", Issuer: "https://issuer.example",
		AuthorizationEndpoint: "https://issuer.example/authorize", TokenEndpoint: "https://issuer.example/token",
		RegistrationEndpoint: "https://issuer.example/register", ProtectedScopesSupported: []string{"zeta", "alpha", "alpha"},
		ScopesSupported: []string{"offline_access", "zeta", "alpha"}, TokenEndpointAuthMethodsSupported: []string{"none"},
	}}
	registrar := &flowRegistrarFake{registration: servers.OAuthRegistrationAuthority{Revision: "1", Mode: contract.RegistrationDynamic, Issuer: "https://issuer.example", ClientID: "client-id", CallbackURL: "http://127.0.0.1:8210/oauth/callback", ResourceURL: "https://resource.example/mcp", TokenEndpointAuthMethod: contract.TokenEndpointAuthNone, CreatedAt: flowTime.Format(time.RFC3339Nano)}}
	reader := bytesReader(entropy)
	service := newFlowService(store, resolver, registrar, &reader, "http://127.0.0.1:8210/oauth/callback", func() time.Time { return flowTime })

	creation, err := service.Create(context.Background(), FlowRequest{ServerID: store.created.Flow.ServerID, ExpectedDesiredRevision: "1"})
	require.NoError(t, err)
	assert.Equal(t, contract.AuthFlowAwaitingCallback, creation.Flow.FlowState)
	assert.Equal(t, "1", creation.Flow.RegistrationRevision)
	parsed, err := url.Parse(creation.AuthorizationURL)
	require.NoError(t, err)
	assert.Equal(t, "https://issuer.example/authorize", parsed.Scheme+"://"+parsed.Host+parsed.Path)
	stateBytes := entropy[:32]
	verifierBytes := entropy[32:]
	state := base64.RawURLEncoding.EncodeToString(stateBytes)
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)
	challenge := sha256.Sum256([]byte(verifier))
	assert.Equal(t, url.Values{
		"client_id": {"client-id"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"},
		"redirect_uri": {"http://127.0.0.1:8210/oauth/callback"}, "resource": {"https://resource.example/mcp"}, "response_type": {"code"},
		"scope": {"alpha zeta"}, "state": {state},
	}, parsed.Query())
	assert.Equal(t, parsed.RawQuery, parsed.Query().Encode())
	require.Len(t, service.byState, 1)
	bundle := service.byState[state]
	assert.Equal(t, verifier, bundle.verifier)
	assert.Equal(t, []string{"alpha", "zeta"}, bundle.requestedScopes)
	assert.Equal(t, "https://issuer.example/token", bundle.graph.TokenEndpoint)
	durable, err := json.Marshal(creation.Flow)
	require.NoError(t, err)
	assert.NotContains(t, string(durable), state)
	assert.NotContains(t, string(durable), verifier)
}

func TestForegroundFlowScopePrecedenceOfflineAndStepUpUnion(t *testing.T) {
	tests := []struct {
		name       string
		request    FlowRequest
		protected  []string
		supported  []string
		offline    bool
		wantScopes string
	}{
		{name: "challenge wins", request: FlowRequest{ChallengeScopes: []string{"challenge", "alpha"}}, protected: []string{"protected"}, wantScopes: "alpha challenge"},
		{name: "protected fallback", protected: []string{"zeta", "alpha"}, wantScopes: "alpha zeta"},
		{name: "omitted", wantScopes: ""},
		{name: "offline advertised", protected: []string{"alpha"}, supported: []string{"offline_access"}, offline: true, wantScopes: "alpha offline_access"},
		{name: "step up union", request: FlowRequest{ChallengeScopes: []string{"new", "shared"}, PriorRequestedScopes: []string{"old", "shared"}}, wantScopes: "new old shared"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registration := contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic}
			created := flowCreateResult(registration)
			created.Configuration.Authentication.RequestOfflineAccess = test.offline
			store := &flowStoreFake{created: created}
			service := newFlowService(store, flowResolverFake{graph: Graph{Resource: created.Configuration.Resource, Issuer: "https://issuer.example", AuthorizationEndpoint: "https://issuer.example/authorize", TokenEndpoint: "https://issuer.example/token", ProtectedScopesSupported: test.protected, ScopesSupported: test.supported, TokenEndpointAuthMethodsSupported: []string{"none"}}}, &flowRegistrarFake{registration: flowRegistration()}, io.LimitReader(zeroReader{}, 64), "http://127.0.0.1:8210/oauth/callback", func() time.Time { return flowTime })
			request := test.request
			request.ServerID, request.ExpectedDesiredRevision = created.Flow.ServerID, "1"
			creation, err := service.Create(context.Background(), request)
			require.NoError(t, err)
			parsed, err := url.Parse(creation.AuthorizationURL)
			require.NoError(t, err)
			assert.Equal(t, test.wantScopes, parsed.Query().Get("scope"))
			if test.wantScopes == "" {
				assert.NotContains(t, parsed.Query(), "scope")
			}
		})
	}
}

func TestForegroundFlowFailureAndCancellationDiscardMemory(t *testing.T) {
	created := flowCreateResult(contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic})
	store := &flowStoreFake{created: created}
	service := newFlowService(store, flowResolverFake{err: ErrTrustRejected}, &flowRegistrarFake{}, io.LimitReader(zeroReader{}, 64), "http://127.0.0.1:8210/oauth/callback", func() time.Time { return flowTime })
	_, err := service.Create(context.Background(), FlowRequest{ServerID: created.Flow.ServerID, ExpectedDesiredRevision: "1"})
	assert.ErrorIs(t, err, ErrFlowRejected)
	assert.Equal(t, contract.AuthFlowFailed, store.transitioned.State)
	require.NotNil(t, store.transitioned.Diagnostic)
	assert.Equal(t, contract.OAuthDiagnosticMetadataDiscovery, store.transitioned.Diagnostic.Stage)
	assert.Equal(t, created.Flow.ID, store.transitioned.Diagnostic.CorrelationID)
	assert.Empty(t, service.byState)

	store.created = created
	resolver := flowResolverFake{graph: Graph{Resource: created.Configuration.Resource, Issuer: "https://issuer.example", AuthorizationEndpoint: "https://issuer.example/authorize", TokenEndpoint: "https://issuer.example/token", TokenEndpointAuthMethodsSupported: []string{"none"}}}
	service = newFlowService(store, resolver, &flowRegistrarFake{registration: flowRegistration()}, io.LimitReader(zeroReader{}, 64), "http://127.0.0.1:8210/oauth/callback", func() time.Time { return flowTime })
	creation, err := service.Create(context.Background(), FlowRequest{ServerID: created.Flow.ServerID, ExpectedDesiredRevision: "1"})
	require.NoError(t, err)
	require.Len(t, service.byState, 1)
	_, err = service.Cancel(context.Background(), creation.Flow.ServerID, creation.Flow.ID)
	require.NoError(t, err)
	assert.Empty(t, service.byState)
}

func flowCreateResult(registration contract.OAuthRegistration) servers.AuthFlowCreateResult {
	return servers.AuthFlowCreateResult{
		Flow:          servers.AuthFlow{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAA", ServerID: "01ARZ3NDEKTSV4RRFFQ69G5FAB", State: contract.AuthFlowPreparing, TargetDesiredRevision: "1", RegistrationRevision: "0", CreatedAt: flowTime.Format(time.RFC3339Nano), ExpiresAt: flowTime.Add(contract.OAuthFlowLifetime).Format(time.RFC3339Nano)},
		Authority:     servers.AuthorityMetadata{RegistrationRevision: "0", CredentialRevisions: contract.CredentialRevisions{StaticCredential: "0", OAuthClient: "0", OAuthTokens: "0"}},
		Configuration: servers.AuthFlowOAuthConfiguration{Resource: "https://resource.example/mcp", Authentication: contract.OAuthAuthentication{Mode: contract.AuthenticationOAuth, Registration: registration, TrustedOrigins: []string{}}},
	}
}

func flowRegistration() servers.OAuthRegistrationAuthority {
	return servers.OAuthRegistrationAuthority{Revision: "1", Mode: contract.RegistrationDynamic, Issuer: "https://issuer.example", ClientID: "client-id", CallbackURL: "http://127.0.0.1:8210/oauth/callback", ResourceURL: "https://resource.example/mcp", TokenEndpointAuthMethod: contract.TokenEndpointAuthNone, CreatedAt: flowTime.Format(time.RFC3339Nano)}
}

type flowStoreFake struct {
	auditHook    func(contract.AuditEvent) error
	created      servers.AuthFlowCreateResult
	transitioned servers.AuthFlow
}

func (store *flowStoreFake) RecordOAuthEvent(_ context.Context, event contract.AuditEvent) error {
	if store.auditHook != nil {
		return store.auditHook(event)
	}
	return nil
}

func (store *flowStoreFake) CreateAuthFlow(context.Context, servers.AuthFlowCreateRequest) (servers.AuthFlowCreateResult, error) {
	return store.created, nil
}
func (store *flowStoreFake) BeginAuthFlowExchange(context.Context, servers.OAuthTokenFence) (servers.AuthFlow, error) {
	return store.created.Flow, nil
}
func (store *flowStoreFake) ValidateAuthFlowExchange(context.Context, servers.OAuthTokenFence) error {
	return nil
}
func (store *flowStoreFake) OAuthTokenAuthorityCallback(servers.OAuthTokenFence) (keyring.AuthorityCallback, error) {
	return nil, nil
}
func (store *flowStoreFake) Authority(context.Context, string) (servers.AuthorityMetadata, error) {
	authority := store.created.Authority
	authority.RegistrationRevision = "1"
	return authority, nil
}
func (store *flowStoreFake) MarkAuthFlowAwaiting(_ context.Context, flowID, _, registrationRevision string) (servers.AuthFlow, error) {
	flow := store.created.Flow
	flow.ID, flow.State, flow.RegistrationRevision = flowID, contract.AuthFlowAwaitingCallback, registrationRevision
	return flow, nil
}
func (store *flowStoreFake) FailAuthFlow(_ context.Context, flowID string, diagnostic contract.OAuthDiagnostic) (servers.AuthFlow, error) {
	store.transitioned = store.created.Flow
	store.transitioned.ID, store.transitioned.State, store.transitioned.Reason = flowID, contract.AuthFlowFailed, &diagnostic.Reason
	store.transitioned.Diagnostic = &diagnostic
	return store.transitioned, nil
}
func (store *flowStoreFake) TransitionAuthFlow(_ context.Context, flowID string, state contract.AuthFlowState, reason *contract.PublicReason) (servers.AuthFlow, error) {
	store.transitioned = store.created.Flow
	store.transitioned.ID, store.transitioned.State, store.transitioned.Reason = flowID, state, reason
	return store.transitioned, nil
}
func (store *flowStoreFake) CancelAuthFlow(_ context.Context, _, _ string) (servers.AuthFlow, error) {
	flow := store.created.Flow
	flow.State = contract.AuthFlowCancelled
	return flow, nil
}
func (store *flowStoreFake) GetAuthFlow(context.Context, string, string) (servers.AuthFlow, error) {
	return store.created.Flow, nil
}
func (store *flowStoreFake) ListAuthFlows(context.Context, string, *servers.SnapshotCursor, int) (servers.AuthFlowPage, error) {
	return servers.AuthFlowPage{Items: []servers.AuthFlow{store.created.Flow}}, nil
}
func (store *flowStoreFake) ExpireAuthFlows(context.Context) ([]string, error) { return nil, nil }
func (store *flowStoreFake) InterruptAuthFlows(context.Context) error          { return nil }
func (store *flowStoreFake) AuthFlowStatus(context.Context) (contract.LimitStatus, error) {
	return contract.LimitStatus{Limit: 16}, nil
}

type flowResolverFake struct {
	graph Graph
	err   error
}

func (resolver flowResolverFake) Discover(context.Context, Input) (Graph, error) {
	return resolver.graph, resolver.err
}

type flowRegistrarFake struct {
	calls        int
	registration servers.OAuthRegistrationAuthority
	err          error
}

func (registrar *flowRegistrarFake) Register(context.Context, RegistrationRequest) (servers.OAuthRegistrationAuthority, error) {
	registrar.calls++
	return registrar.registration, registrar.err
}

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = 1
	}
	return len(buffer), nil
}

type bytesReader []byte

func (reader *bytesReader) Read(buffer []byte) (int, error) {
	if len(*reader) == 0 {
		return 0, io.EOF
	}
	count := copy(buffer, *reader)
	*reader = (*reader)[count:]
	return count, nil
}
