package oauth

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/remote"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
)

var (
	ErrRefreshIneligible      = errors.New("OAuth token refresh is not eligible")
	ErrRefreshReauthorization = errors.New("OAuth token refresh requires reauthorization")
)

type RefreshRequest struct {
	ServerID          string
	ForceInvalidToken bool
	ChallengeMetadata []string
}

type RefreshResult struct {
	Revision  string
	Refreshed bool
}

type refreshStore interface {
	PrepareOAuthRefresh(context.Context, string) (servers.OAuthRefreshContext, error)
	OAuthRefreshAuthorityCallback(servers.OAuthRefreshFence) (keyring.AuthorityCallback, error)
}

type refreshOperation interface {
	ReadActive(context.Context, keyring.Namespace) ([]byte, keyring.CutoverResult, error)
	ReplaceFencedAfterAuthorizationSuccess(context.Context, keyring.Namespace, []byte, keyring.AuthorityCallback) (keyring.CutoverResult, error)
	InvalidateFencedExact(context.Context, keyring.Namespace, keyring.AuthorityCallback) (keyring.CutoverResult, error)
}

type refreshCoordinator interface {
	WithOperation(context.Context, func(refreshOperation) error) error
}

type coordinatorRefresh struct{ coordinator *keyring.Coordinator }

func (adapter coordinatorRefresh) WithOperation(ctx context.Context, use func(refreshOperation) error) error {
	return adapter.coordinator.WithOperation(ctx, func(operation *keyring.Operation) error { return use(operation) })
}

type refreshResolver interface {
	Discover(context.Context, Input) (Graph, error)
}

type refreshRequester interface {
	Request(context.Context, string, bool, http.Header, []byte, int64, func()) (int, http.Header, []byte, error)
}

type refreshHTTP struct{ factory *remote.Factory }

func (client refreshHTTP) Request(ctx context.Context, rawURL string, trusted bool, header http.Header, body []byte, maximum int64, before func()) (int, http.Header, []byte, error) {
	endpoint, err := remote.Parse(rawURL, remote.Policy{AllowRestricted: trusted, AllowQuery: true})
	if err != nil {
		return 0, nil, nil, ErrTokenRejected
	}
	requestCtx, cancel := context.WithTimeout(ctx, contract.OAuthRequestDeadline)
	defer cancel()
	response, err := client.factory.Exchange(requestCtx, remote.Request{Endpoint: endpoint, Method: http.MethodPost, Header: header, Body: body, MaxBody: maximum, BeforeRoundTrip: before})
	if err != nil {
		return 0, nil, nil, err
	}
	return response.StatusCode, response.Header, response.Body, nil
}

type refreshCall struct {
	done         chan struct{}
	joinObserved chan struct{}
	joinOnce     sync.Once
	result       RefreshResult
	err          error
}

type RefreshService struct {
	store          refreshStore
	coordinator    refreshCoordinator
	resolver       refreshResolver
	requester      refreshRequester
	installationID string
	now            func() time.Time
	state          func(string, contract.ServerCredentialState, bool)
	trigger        func(string)

	mu       sync.Mutex
	calls    map[string]*refreshCall
	ctx      context.Context
	cancel   context.CancelFunc
	draining bool
}

func NewRefreshService(store *servers.Repository, coordinator *keyring.Coordinator, resolver *Resolver, factory *remote.Factory, installationID string, now func() time.Time, state func(string, contract.ServerCredentialState, bool), trigger func(string)) (*RefreshService, error) {
	if store == nil || coordinator == nil || resolver == nil || factory == nil || installationID == "" || now == nil {
		return nil, ErrTokenRejected
	}
	return newRefreshService(store, coordinatorRefresh{coordinator: coordinator}, resolver, refreshHTTP{factory: factory}, installationID, now, state, trigger), nil
}

func newRefreshService(store refreshStore, coordinator refreshCoordinator, resolver refreshResolver, requester refreshRequester, installationID string, now func() time.Time, state func(string, contract.ServerCredentialState, bool), trigger func(string)) *RefreshService {
	if state == nil {
		state = func(string, contract.ServerCredentialState, bool) {}
	}
	if trigger == nil {
		trigger = func(string) {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &RefreshService{store: store, coordinator: coordinator, resolver: resolver, requester: requester, installationID: installationID, now: now, state: state, trigger: trigger, calls: make(map[string]*refreshCall), ctx: ctx, cancel: cancel}
}

func (service *RefreshService) Shutdown() {
	service.mu.Lock()
	service.draining = true
	service.cancel()
	service.mu.Unlock()
}

func (service *RefreshService) Refresh(ctx context.Context, request RefreshRequest) (RefreshResult, error) {
	if request.ServerID == "" {
		return RefreshResult{}, ErrRefreshIneligible
	}
	service.mu.Lock()
	if service.draining {
		service.mu.Unlock()
		return RefreshResult{}, keyring.ErrDraining
	}
	if current := service.calls[request.ServerID]; current != nil {
		current.joinOnce.Do(func() { close(current.joinObserved) })
		service.mu.Unlock()
		select {
		case <-current.done:
			return current.result, current.err
		case <-ctx.Done():
			return RefreshResult{}, ctx.Err()
		}
	}
	call := &refreshCall{done: make(chan struct{}), joinObserved: make(chan struct{})}
	service.calls[request.ServerID] = call
	serviceCtx := service.ctx
	service.mu.Unlock()
	workCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(serviceCtx, cancel)
	call.result, call.err = service.refresh(workCtx, request)
	stop()
	cancel()
	service.mu.Lock()
	delete(service.calls, request.ServerID)
	close(call.done)
	service.mu.Unlock()
	return call.result, call.err
}

func (service *RefreshService) refresh(ctx context.Context, request RefreshRequest) (RefreshResult, error) {
	prepared, err := service.store.PrepareOAuthRefresh(ctx, request.ServerID)
	if err != nil {
		return RefreshResult{}, err
	}
	callback, err := service.store.OAuthRefreshAuthorityCallback(prepared.Fence)
	if err != nil {
		return RefreshResult{}, err
	}
	tokenNamespace, err := keyring.NewNamespace(service.installationID, request.ServerID, keyring.RecordOAuthTokens)
	if err != nil {
		return RefreshResult{}, ErrTokenRejected
	}
	var result RefreshResult
	err = service.coordinator.WithOperation(ctx, func(operation refreshOperation) error {
		oldBytes, active, err := operation.ReadActive(ctx, tokenNamespace)
		if err != nil {
			return err
		}
		defer clear(oldBytes)
		if active.Revision != prepared.Fence.ExpectedOAuthTokensRevision {
			return servers.ErrStaleRevision
		}
		old, err := DecodeTokenGeneration(oldBytes)
		if err != nil || !refreshBindingMatches(old, prepared) || old.RefreshToken == nil || (!request.ForceInvalidToken && !TokenRefreshEligible(old, service.now())) {
			return ErrRefreshIneligible
		}
		var clientSecret []byte
		if prepared.Registration.TokenEndpointAuthMethod != contract.TokenEndpointAuthNone {
			clientNamespace, namespaceErr := keyring.NewNamespace(service.installationID, request.ServerID, keyring.RecordOAuthClient)
			if namespaceErr != nil {
				return ErrTokenRejected
			}
			clientSecret, active, err = operation.ReadActive(ctx, clientNamespace)
			if err != nil || active.Revision != prepared.Fence.ExpectedOAuthClientRevision {
				clear(clientSecret)
				return servers.ErrStaleRevision
			}
			defer clear(clientSecret)
		}
		service.state(request.ServerID, contract.ServerCredentialRefreshing, false)
		graph, err := service.resolver.Discover(ctx, Input{Resource: prepared.Configuration.Resource, ChallengeMetadata: request.ChallengeMetadata, DesiredIssuer: &prepared.Registration.Issuer, TrustedOrigins: prepared.Configuration.Authentication.TrustedOrigins})
		if err != nil || graph.Issuer != old.Issuer || graph.Resource != old.Resource || !slices.Contains(graph.TokenEndpointAuthMethodsSupported, string(prepared.Registration.TokenEndpointAuthMethod)) {
			service.state(request.ServerID, contract.ServerCredentialReady, false)
			return ErrTokenRejected
		}
		header, body, err := refreshTokenRequest(*old.RefreshToken, prepared.Registration, old.Resource, clientSecret)
		if err != nil {
			service.state(request.ServerID, contract.ServerCredentialReady, false)
			return err
		}
		handed := false
		status, responseHeader, responseBody, requestErr := service.requester.Request(ctx, graph.TokenEndpoint, graph.AllowsRestrictedEndpoint(graph.TokenEndpoint), header, body, limit("oauth_response_body_bytes"), func() { handed = true })
		clear(body)
		if requestErr != nil {
			if handed {
				return service.invalidateRefresh(ctx, operation, tokenNamespace, callback, request.ServerID, requestErr)
			}
			service.state(request.ServerID, contract.ServerCredentialReady, false)
			return requestErr
		}
		issuedAt := service.now().UTC()
		requested := []string(nil)
		if old.ScopeSpecified {
			requested = old.Scopes
		}
		token, parseErr := parseTokenResponse(status, responseHeader, responseBody, requested, issuedAt)
		if parseErr != nil {
			oauthError := tokenErrorCode(responseBody)
			clear(responseBody)
			if oauthError == "invalid_grant" || oauthError == "invalid_client" {
				return service.invalidateRefresh(ctx, operation, tokenNamespace, callback, request.ServerID, ErrRefreshReauthorization)
			}
			service.state(request.ServerID, contract.ServerCredentialReady, false)
			return parseErr
		}
		clear(responseBody)
		if prepared.Registration.TokenEndpointAuthMethod == contract.TokenEndpointAuthNone {
			if token.refreshToken == nil || *token.refreshToken == *old.RefreshToken {
				return service.invalidateRefresh(ctx, operation, tokenNamespace, callback, request.ServerID, ErrRefreshReauthorization)
			}
		} else if token.refreshToken == nil {
			value := *old.RefreshToken
			token.refreshToken = &value
		}
		generation, err := encodeBoundTokenGeneration(old, token, issuedAt)
		if err != nil {
			return service.invalidateRefresh(ctx, operation, tokenNamespace, callback, request.ServerID, err)
		}
		defer clear(generation)
		service.state(request.ServerID, contract.ServerCredentialRefreshing, true)
		cutover, err := operation.ReplaceFencedAfterAuthorizationSuccess(ctx, tokenNamespace, generation, callback)
		if err != nil {
			service.state(request.ServerID, contract.ServerCredentialReauthenticationRequired, true)
			return err
		}
		result = RefreshResult{Revision: cutover.Revision, Refreshed: true}
		service.trigger(request.ServerID)
		return nil
	})
	return result, err
}

func (service *RefreshService) invalidateRefresh(ctx context.Context, operation refreshOperation, namespace keyring.Namespace, callback keyring.AuthorityCallback, serverID string, cause error) error {
	service.state(serverID, contract.ServerCredentialReauthenticationRequired, true)
	_, invalidationErr := operation.InvalidateFencedExact(ctx, namespace, callback)
	return errors.Join(cause, invalidationErr)
}

func refreshBindingMatches(token TokenGeneration, prepared servers.OAuthRefreshContext) bool {
	return token.ServerID == prepared.Fence.ServerID && token.Issuer == prepared.Registration.Issuer && token.RegistrationRevision == prepared.Registration.Revision && token.Resource == prepared.Registration.ResourceURL
}

func tokenErrorCode(body []byte) string {
	object, err := decodeObject(body)
	if err != nil {
		return ""
	}
	value, _, err := optionalString(object, "error")
	if err != nil {
		return ""
	}
	return value
}
