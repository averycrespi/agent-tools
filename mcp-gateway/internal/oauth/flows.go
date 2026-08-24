package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
)

var ErrFlowRejected = errors.New("OAuth flow preparation was rejected")

type FlowRequest struct {
	ServerID                string
	ExpectedDesiredRevision string
	ChallengeMetadata       []string
	ChallengeScopes         []string
	PriorRequestedScopes    []string
}

type flowStore interface {
	CreateAuthFlow(context.Context, servers.AuthFlowCreateRequest) (servers.AuthFlowCreateResult, error)
	Authority(context.Context, string) (servers.AuthorityMetadata, error)
	MarkAuthFlowAwaiting(context.Context, string, string, string) (servers.AuthFlow, error)
	TransitionAuthFlow(context.Context, string, contract.AuthFlowState, *contract.PublicReason) (servers.AuthFlow, error)
	CancelAuthFlow(context.Context, string, string) (servers.AuthFlow, error)
	GetAuthFlow(context.Context, string, string) (servers.AuthFlow, error)
	ListAuthFlows(context.Context, string, *servers.SnapshotCursor, int) (servers.AuthFlowPage, error)
	ExpireAuthFlows(context.Context) ([]string, error)
	InterruptAuthFlows(context.Context) error
	AuthFlowStatus(context.Context) (contract.LimitStatus, error)
}

type flowResolver interface {
	Discover(context.Context, Input) (Graph, error)
}

type flowRegistrar interface {
	Register(context.Context, RegistrationRequest) (servers.OAuthRegistrationAuthority, error)
}

type flowBundle struct {
	serverID           string
	flowID             string
	desiredRevision    string
	authority          servers.AuthorityMetadata
	registration       servers.OAuthRegistrationAuthority
	graph              Graph
	state              string
	verifier           string
	requestedScopes    []string
	issuerResponseUsed bool
}

type FlowService struct {
	store       flowStore
	resolver    flowResolver
	registrar   flowRegistrar
	entropy     io.Reader
	callbackURL string
	now         func() time.Time

	entropyMu sync.Mutex
	mu        sync.Mutex
	byState   map[string]flowBundle
}

func NewFlowService(store *servers.Repository, resolver *Resolver, registrar *Registrar, entropy io.Reader, callbackURL string, now func() time.Time) (*FlowService, error) {
	if store == nil || resolver == nil || registrar == nil || entropy == nil || now == nil || !validCallbackURL(callbackURL) {
		return nil, ErrFlowRejected
	}
	return newFlowService(store, resolver, registrar, entropy, callbackURL, now), nil
}

func newFlowService(store flowStore, resolver flowResolver, registrar flowRegistrar, entropy io.Reader, callbackURL string, now func() time.Time) *FlowService {
	return &FlowService{store: store, resolver: resolver, registrar: registrar, entropy: entropy, callbackURL: callbackURL, now: now, byState: make(map[string]flowBundle)}
}

func (service *FlowService) Start(ctx context.Context) error {
	service.clearAll()
	return service.store.InterruptAuthFlows(ctx)
}

func (service *FlowService) Shutdown() {
	service.clearAll()
}

func (service *FlowService) CreateInitial(ctx context.Context, serverID, expectedDesiredRevision string) (contract.AuthFlowCreation, error) {
	return service.Create(ctx, FlowRequest{ServerID: serverID, ExpectedDesiredRevision: expectedDesiredRevision})
}

func (service *FlowService) Create(ctx context.Context, request FlowRequest) (contract.AuthFlowCreation, error) {
	if service == nil || service.store == nil || service.resolver == nil || service.registrar == nil || service.entropy == nil || service.now == nil {
		return contract.AuthFlowCreation{}, ErrFlowRejected
	}
	if err := service.expire(ctx); err != nil {
		return contract.AuthFlowCreation{}, err
	}
	prepared, err := service.store.CreateAuthFlow(ctx, servers.AuthFlowCreateRequest{ServerID: request.ServerID, ExpectedDesiredRevision: request.ExpectedDesiredRevision})
	if err != nil {
		return contract.AuthFlowCreation{}, err
	}
	service.removeFlowIDs(prepared.SupersededIDs)
	configuration := prepared.Configuration
	registrationConfig := configuration.Authentication.Registration
	var desiredIssuer *string
	switch registration := registrationConfig.(type) {
	case contract.StaticOAuthRegistration:
		desiredIssuer = registration.Issuer
	case contract.DynamicOAuthRegistration:
		desiredIssuer = registration.Issuer
	default:
		return service.fail(ctx, prepared.Flow.ID, ErrFlowRejected)
	}
	graph, err := service.resolver.Discover(ctx, Input{Resource: configuration.Resource, ChallengeMetadata: request.ChallengeMetadata, DesiredIssuer: desiredIssuer, TrustedOrigins: configuration.Authentication.TrustedOrigins})
	if err != nil {
		return service.fail(ctx, prepared.Flow.ID, ErrFlowRejected)
	}
	registration, err := service.registrar.Register(ctx, RegistrationRequest{
		ServerID: prepared.Flow.ServerID, ExpectedDesiredRevision: prepared.Flow.TargetDesiredRevision,
		ExpectedRegistrationRevision: prepared.Authority.RegistrationRevision, ExpectedOAuthClientRevision: prepared.Authority.CredentialRevisions.OAuthClient,
		ExpectedAuthFlowID: prepared.Flow.ID, Graph: graph, Registration: registrationConfig, CallbackURL: service.callbackURL,
	})
	if err != nil {
		return service.fail(ctx, prepared.Flow.ID, ErrFlowRejected)
	}
	boundAuthority, err := service.store.Authority(ctx, prepared.Flow.ServerID)
	if err != nil || boundAuthority.RegistrationRevision != registration.Revision {
		return service.fail(ctx, prepared.Flow.ID, ErrFlowRejected)
	}
	stateBytes := make([]byte, 32)
	verifierBytes := make([]byte, 32)
	service.entropyMu.Lock()
	_, stateErr := io.ReadFull(service.entropy, stateBytes)
	_, verifierErr := io.ReadFull(service.entropy, verifierBytes)
	service.entropyMu.Unlock()
	if stateErr != nil || verifierErr != nil {
		return service.fail(ctx, prepared.Flow.ID, ErrFlowRejected)
	}
	state := base64.RawURLEncoding.EncodeToString(stateBytes)
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)
	scopes, err := requestedScopes(request, configuration.Authentication.RequestOfflineAccess, graph)
	if err != nil {
		return service.fail(ctx, prepared.Flow.ID, err)
	}
	authorizationURL, err := buildAuthorizationURL(graph.AuthorizationEndpoint, registration.ClientID, service.callbackURL, graph.Resource, state, verifier, scopes)
	if err != nil {
		return service.fail(ctx, prepared.Flow.ID, err)
	}
	bundle := flowBundle{
		serverID: prepared.Flow.ServerID, flowID: prepared.Flow.ID, desiredRevision: prepared.Flow.TargetDesiredRevision,
		authority: boundAuthority, registration: registration, graph: graph, state: state, verifier: verifier,
		requestedScopes: append([]string(nil), scopes...), issuerResponseUsed: graph.AuthorizationResponseIssuerParameterUsed,
	}
	service.mu.Lock()
	_, stateExists := service.byState[state]
	if !stateExists {
		service.byState[state] = bundle
	}
	service.mu.Unlock()
	if stateExists {
		return service.fail(ctx, prepared.Flow.ID, ErrFlowRejected)
	}
	awaiting, err := service.store.MarkAuthFlowAwaiting(ctx, prepared.Flow.ID, prepared.Flow.TargetDesiredRevision, registration.Revision)
	if err != nil {
		service.removeFlowIDs([]string{prepared.Flow.ID})
		return contract.AuthFlowCreation{}, err
	}
	return contract.AuthFlowCreation{Flow: authFlowResource(awaiting), AuthorizationURL: authorizationURL}, nil
}

func (service *FlowService) Get(ctx context.Context, serverID, flowID string) (contract.ServerAuthFlow, error) {
	if err := service.expire(ctx); err != nil {
		return contract.ServerAuthFlow{}, err
	}
	flow, err := service.store.GetAuthFlow(ctx, serverID, flowID)
	return authFlowResource(flow), err
}

func (service *FlowService) List(ctx context.Context, serverID string, cursor *servers.SnapshotCursor, limit int) ([]contract.ServerAuthFlow, *servers.SnapshotCursor, error) {
	if err := service.expire(ctx); err != nil {
		return nil, nil, err
	}
	page, err := service.store.ListAuthFlows(ctx, serverID, cursor, limit)
	if err != nil {
		return nil, nil, err
	}
	items := make([]contract.ServerAuthFlow, len(page.Items))
	for index, flow := range page.Items {
		items[index] = authFlowResource(flow)
	}
	return items, page.Next, nil
}

func (service *FlowService) Cancel(ctx context.Context, serverID, flowID string) (contract.ServerAuthFlow, error) {
	if err := service.expire(ctx); err != nil {
		return contract.ServerAuthFlow{}, err
	}
	flow, err := service.store.CancelAuthFlow(ctx, serverID, flowID)
	if err != nil {
		return contract.ServerAuthFlow{}, err
	}
	if flow.State == contract.AuthFlowCancelled || authFlowIsTerminal(flow.State) {
		service.removeFlowIDs([]string{flowID})
	}
	return authFlowResource(flow), nil
}

func (service *FlowService) Status(ctx context.Context) contract.LimitStatus {
	if err := service.expire(ctx); err != nil {
		limit, _ := contract.FixedLimitByName("oauth_flows")
		return contract.LimitStatus{Limit: limit.Maximum}
	}
	status, err := service.store.AuthFlowStatus(ctx)
	if err != nil {
		limit, _ := contract.FixedLimitByName("oauth_flows")
		return contract.LimitStatus{Limit: limit.Maximum}
	}
	return status
}

func (service *FlowService) FenceServer(serverID string) {
	service.mu.Lock()
	defer service.mu.Unlock()
	for state, bundle := range service.byState {
		if bundle.serverID == serverID {
			delete(service.byState, state)
		}
	}
}

func (service *FlowService) expire(ctx context.Context) error {
	expired, err := service.store.ExpireAuthFlows(ctx)
	if err != nil {
		return err
	}
	service.removeFlowIDs(expired)
	return nil
}

func (service *FlowService) fail(ctx context.Context, flowID string, cause error) (contract.AuthFlowCreation, error) {
	reason := contract.ReasonOAuthRejected
	_, _ = service.store.TransitionAuthFlow(ctx, flowID, contract.AuthFlowFailed, &reason)
	service.removeFlowIDs([]string{flowID})
	return contract.AuthFlowCreation{}, cause
}

func (service *FlowService) removeFlowIDs(flowIDs []string) {
	if len(flowIDs) == 0 {
		return
	}
	wanted := make(map[string]struct{}, len(flowIDs))
	for _, flowID := range flowIDs {
		wanted[flowID] = struct{}{}
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	for state, bundle := range service.byState {
		if _, ok := wanted[bundle.flowID]; ok {
			delete(service.byState, state)
		}
	}
}

func (service *FlowService) clearAll() {
	service.mu.Lock()
	defer service.mu.Unlock()
	clear(service.byState)
}

func requestedScopes(request FlowRequest, requestOffline bool, graph Graph) ([]string, error) {
	values := make([]string, 0)
	switch {
	case len(request.PriorRequestedScopes) != 0:
		values = append(values, request.PriorRequestedScopes...)
		values = append(values, request.ChallengeScopes...)
	case len(request.ChallengeScopes) != 0:
		values = append(values, request.ChallengeScopes...)
	default:
		values = append(values, graph.ProtectedScopesSupported...)
	}
	if requestOffline && contains(graph.ScopesSupported, "offline_access") {
		values = append(values, "offline_access")
	}
	if len(values) == 0 {
		return nil, nil
	}
	sort.Strings(values)
	unique := values[:0]
	for _, value := range values {
		if value == "" || !utf8.ValidString(value) || int64(len(value)) > limit("oauth_scope_token_bytes") || strings.ContainsAny(value, " \t\r\n") {
			return nil, ErrFlowRejected
		}
		if len(unique) == 0 || unique[len(unique)-1] != value {
			unique = append(unique, value)
		}
	}
	if int64(len(unique)) > limit("oauth_scope_count") || int64(len(strings.Join(unique, " "))) > limit("oauth_scope_bytes") {
		return nil, ErrFlowRejected
	}
	return append([]string(nil), unique...), nil
}

func buildAuthorizationURL(endpoint, clientID, callback, resource, state, verifier string, scopes []string) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || clientID == "" || resource == "" || state == "" || verifier == "" {
		return "", ErrFlowRejected
	}
	query := parsed.Query()
	for _, name := range []string{"response_type", "client_id", "redirect_uri", "state", "code_challenge", "code_challenge_method", "resource", "scope"} {
		if _, exists := query[name]; exists {
			return "", ErrFlowRejected
		}
	}
	challenge := sha256.Sum256([]byte(verifier))
	query.Set("response_type", "code")
	query.Set("client_id", clientID)
	query.Set("redirect_uri", callback)
	query.Set("state", state)
	query.Set("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:]))
	query.Set("code_challenge_method", "S256")
	query.Set("resource", resource)
	if len(scopes) != 0 {
		query.Set("scope", strings.Join(scopes, " "))
	}
	parsed.RawQuery = query.Encode()
	result := parsed.String()
	if int64(len(parsed.RawQuery)) > limit("oauth_query_bytes") || int64(len(result)) > limit("oauth_url_bytes") {
		return "", ErrFlowRejected
	}
	return result, nil
}

func authFlowResource(flow servers.AuthFlow) contract.ServerAuthFlow {
	return contract.ServerAuthFlow{ID: flow.ID, ServerID: flow.ServerID, FlowState: flow.State, TargetDesiredRevision: flow.TargetDesiredRevision, RegistrationRevision: flow.RegistrationRevision, CreatedAt: flow.CreatedAt, ExpiresAt: flow.ExpiresAt, FinishedAt: flow.FinishedAt, Reason: flow.Reason}
}

func authFlowIsTerminal(state contract.AuthFlowState) bool {
	return state == contract.AuthFlowSucceeded || state == contract.AuthFlowFailed || state == contract.AuthFlowExpired || state == contract.AuthFlowCancelled || state == contract.AuthFlowSuperseded || state == contract.AuthFlowInterrupted
}
