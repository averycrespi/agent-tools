package api

import (
	"bytes"
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/backup"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/events"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/httpboundary"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/oauth"
	serverdomain "github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

type CredentialService interface {
	Authenticate(context.Context, string) (contract.AdminCredential, error)
	Authority(context.Context) (contract.AdminAuthority, error)
	Create(context.Context, *time.Time) (contract.CreatedAdminCredential, error)
	CreateConditional(context.Context, *time.Time, string) (contract.CreatedAdminCredential, error)
	CompleteRotation(context.Context, string, string, string) (contract.AdminCredentialRotationResult, error)
	Get(context.Context, string) (contract.AdminCredential, error)
	List(context.Context) ([]contract.AdminCredential, error)
	Revoke(context.Context, string) error
}

type SessionService interface {
	Exchange(context.Context, string) (admin.CreatedSession, error)
	Bootstrap(context.Context, string) (admin.CreatedSession, error)
	Authenticate(context.Context, string, string, string, bool) (contract.AdminCredential, error)
	Logout(string) error
	Subscribe(string) (<-chan struct{}, error)
	Status() contract.LimitStatus
}

type BackupService interface {
	Create(context.Context, string, string) (contract.Backup, bool, error)
	List(context.Context) ([]contract.Backup, error)
	Get(context.Context, string) (contract.Backup, error)
	Delete(context.Context, string) error
}

type EventService interface {
	Subscribe(string, <-chan struct{}) (*events.Subscription, error)
}

type CatalogService interface {
	Status(context.Context, string) (catalog.DurableStatus, error)
	GetDescriptor(context.Context, string, string) (contract.ToolDescriptor, error)
	ListDescriptors(context.Context, string, contract.DescriptorRetiredFilter, *catalog.DescriptorCursor, int) (catalog.DescriptorPage, error)
}

type ActiveCatalogService interface {
	Status(string) catalog.ActiveStatus
	List(*catalog.ActiveCursor, int) (catalog.ActivePage, error)
	Occupancy() contract.LimitStatus
}

type OAuthCallbackService interface {
	HandleCallback(context.Context, string) oauth.CallbackResult
}

type RuntimeStatus struct {
	State           contract.RuntimeState
	Reason          *contract.PublicReason
	RuntimeID       *string
	CredentialState contract.ServerCredentialState
	CatalogState    contract.ActiveCatalogState
	Reconciliation  contract.LimitStatus
}

type Options struct {
	Credentials      CredentialService
	Sessions         SessionService
	Backups          BackupService
	Events           EventService
	Invalidate       func(contract.Invalidation)
	NewKeepalive     func() (<-chan time.Time, func())
	Origin           string
	Status           func() contract.SystemStatus
	OAuthCallback    OAuthCallbackService
	Servers          ServerService
	Principals       PrincipalService
	GrantRequests    GrantRequestService
	Invocations      InvocationReader
	GrantTarget      authorization.CurrentGrantTargetValidator
	AuthFlows        AuthFlowService
	Replacements     CredentialReplacementService
	Catalog          CatalogService
	ActiveCatalog    ActiveCatalogService
	OperationState   OperationStateProvider
	RuntimeStatus    func(string) RuntimeStatus
	TriggerServer    func(string, *string, bool)
	CatalogTraversal func(string) contract.LimitStatus
	DispatchStatus   func(string) contract.LimitStatus
}

type Handler struct {
	credentials      CredentialService
	sessions         SessionService
	backups          BackupService
	events           EventService
	invalidate       func(contract.Invalidation)
	newKeepalive     func() (<-chan time.Time, func())
	origin           string
	status           func() contract.SystemStatus
	callbackService  OAuthCallbackService
	servers          ServerService
	principals       PrincipalService
	grantRequests    GrantRequestService
	invocations      InvocationReader
	grantTarget      authorization.CurrentGrantTargetValidator
	authFlows        AuthFlowService
	replacements     CredentialReplacementService
	catalog          CatalogService
	activeCatalog    ActiveCatalogService
	operationState   OperationStateProvider
	runtimeStatus    func(string) RuntimeStatus
	triggerServer    func(string, *string, bool)
	catalogTraversal func(string) contract.LimitStatus
	dispatchStatus   func(string) contract.LimitStatus
}

//go:embed static/*
var staticFiles embed.FS

const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; font-src 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'; form-action 'self'"

type authContextKey struct{}

type authentication struct {
	credential contract.AdminCredential
	sessionID  string
	bearer     string
	viaSession bool
	bootstrap  *admin.CreatedSession
}

func New(options Options) *Handler {
	if options.Status == nil {
		options.Status = func() contract.SystemStatus { return contract.SystemStatus{} }
	}
	if options.NewKeepalive == nil {
		options.NewKeepalive = func() (<-chan time.Time, func()) {
			ticker := time.NewTicker(contract.SSEKeepaliveInterval)
			return ticker.C, ticker.Stop
		}
	}
	if options.Origin == "" {
		options.Origin = contract.CanonicalOrigin
	}
	if options.OperationState == nil {
		options.OperationState = func(context.Context, string) serverdomain.OperationTriggerState {
			return serverdomain.OperationTriggerState{RuntimeState: contract.RuntimeInactive, CredentialState: contract.ServerCredentialNotRequired, CatalogState: contract.ActiveCatalogAbsent}
		}
	}
	if options.RuntimeStatus == nil {
		options.RuntimeStatus = func(string) RuntimeStatus {
			return RuntimeStatus{State: contract.RuntimeInactive, CatalogState: contract.ActiveCatalogAbsent, Reconciliation: limitStatus("per_server_reconciliation")}
		}
	}
	if options.CatalogTraversal == nil {
		options.CatalogTraversal = func(string) contract.LimitStatus { return contract.LimitStatus{Limit: 1} }
	}
	if options.DispatchStatus == nil {
		options.DispatchStatus = func(string) contract.LimitStatus { return limitStatus("per_server_downstream_dispatch") }
	}
	return &Handler{credentials: options.Credentials, sessions: options.Sessions, backups: options.Backups, events: options.Events, invalidate: options.Invalidate, newKeepalive: options.NewKeepalive, origin: options.Origin, status: options.Status, callbackService: options.OAuthCallback, servers: options.Servers, principals: options.Principals, grantRequests: options.GrantRequests, invocations: options.Invocations, grantTarget: options.GrantTarget, authFlows: options.AuthFlows, replacements: options.Replacements, catalog: options.Catalog, activeCatalog: options.ActiveCatalog, operationState: options.OperationState, runtimeStatus: options.RuntimeStatus, triggerServer: options.TriggerServer, catalogTraversal: options.CatalogTraversal, dispatchStatus: options.DispatchStatus}
}

func (handler *Handler) Authenticate(ctx context.Context, request *http.Request, authority contract.CredentialAuthority) (context.Context, error) {
	if request.URL.Path == "/api/v1/admin-sessions/current" && request.Method == http.MethodPost {
		return handler.authenticateBootstrap(ctx, request)
	}
	bearer, bearerPresent, err := parseBearer(request.Header.Values("Authorization"))
	if err != nil {
		return ctx, boundaryError(err)
	}
	sessionID, sessionPresent, err := parseSessionCookie(request)
	if err != nil {
		return ctx, httpboundary.Error{Code: contract.ProblemAmbiguousCredentials}
	}
	if bearerPresent && sessionPresent {
		return ctx, httpboundary.Error{Code: contract.ProblemAmbiguousCredentials}
	}

	var result authentication
	switch authority {
	case contract.AuthorityAdminBearer:
		if !bearerPresent || sessionPresent {
			return ctx, httpboundary.Error{Code: contract.ProblemAuthenticationRequired}
		}
		credential, authErr := handler.credentials.Authenticate(ctx, bearer)
		if authErr != nil {
			return ctx, boundaryError(authErr)
		}
		result = authentication{credential: credential, bearer: bearer}
	case contract.AuthorityAdminSession:
		if !sessionPresent || bearerPresent {
			return ctx, httpboundary.Error{Code: contract.ProblemAuthenticationRequired}
		}
		if !handler.acceptsSessionOrigin(request) {
			return ctx, httpboundary.Error{Code: contract.ProblemForbiddenOrigin}
		}
		credential, authErr := handler.sessions.Authenticate(ctx, "", sessionID, request.Header.Get("X-CSRF-Token"), handler.sessionRequiresCSRF(request))
		if authErr != nil {
			return ctx, boundaryError(authErr)
		}
		result = authentication{credential: credential, sessionID: sessionID, viaSession: true}
	case contract.AuthorityAdmin:
		switch {
		case bearerPresent:
			credential, authErr := handler.credentials.Authenticate(ctx, bearer)
			if authErr != nil {
				return ctx, boundaryError(authErr)
			}
			result = authentication{credential: credential, bearer: bearer}
		case sessionPresent:
			if !handler.acceptsSessionOrigin(request) {
				return ctx, httpboundary.Error{Code: contract.ProblemForbiddenOrigin}
			}
			credential, authErr := handler.sessions.Authenticate(ctx, "", sessionID, request.Header.Get("X-CSRF-Token"), handler.sessionRequiresCSRF(request))
			if authErr != nil {
				return ctx, boundaryError(authErr)
			}
			result = authentication{credential: credential, sessionID: sessionID, viaSession: true}
		default:
			return ctx, httpboundary.Error{Code: contract.ProblemAuthenticationRequired}
		}
	default:
		return ctx, httpboundary.Error{Code: contract.ProblemAuthenticationRequired}
	}
	return context.WithValue(ctx, authContextKey{}, result), nil
}

func (handler *Handler) acceptsSessionOrigin(request *http.Request) bool {
	if request.Header.Get("Origin") == handler.origin {
		return true
	}
	return !unsafe(request.Method) && request.Header.Get("Origin") == "" && request.Header.Get("X-CSRF-Token") != ""
}

func (handler *Handler) sessionRequiresCSRF(request *http.Request) bool {
	return unsafe(request.Method) || request.Header.Get("Origin") == ""
}

func (handler *Handler) authenticateBootstrap(ctx context.Context, request *http.Request) (context.Context, error) {
	if request.Header.Get("Origin") != handler.origin {
		return ctx, httpboundary.Error{Code: contract.ProblemForbiddenOrigin}
	}
	cookies := sessionCookieValues(request)
	if len(request.Header.Values("Authorization")) > 0 {
		if len(cookies) > 0 {
			return ctx, httpboundary.Error{Code: contract.ProblemAmbiguousCredentials}
		}
		return ctx, httpboundary.Error{Code: contract.ProblemAuthenticationRequired}
	}
	if len(cookies) == 0 {
		return ctx, httpboundary.Error{Code: contract.ProblemAuthenticationRequired}
	}
	if len(cookies) > 1 {
		return ctx, bootstrapAuthenticationError(contract.ProblemAmbiguousCredentials)
	}
	if !validSessionValue(cookies[0]) {
		return ctx, bootstrapAuthenticationError(contract.ProblemAuthenticationRequired)
	}
	session, err := handler.sessions.Bootstrap(ctx, cookies[0])
	if err != nil {
		return ctx, bootstrapAuthenticationError(contract.ProblemAuthenticationRequired)
	}
	result := authentication{sessionID: cookies[0], viaSession: true, bootstrap: &session}
	return context.WithValue(ctx, authContextKey{}, result), nil
}

func bootstrapAuthenticationError(code contract.ProblemCode) httpboundary.Error {
	return httpboundary.Error{Code: code, SetCookie: expiredSessionCookie().String()}
}

func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	path := request.URL.Path
	switch {
	case path == "/" && request.Method == http.MethodGet:
		handler.serveStatic(writer, "static/index.html", "text/html; charset=utf-8", true)
	case path == "/assets/app.css" && request.Method == http.MethodGet:
		handler.serveStatic(writer, "static/app.css", "text/css; charset=utf-8", false)
	case path == "/assets/app.js" && request.Method == http.MethodGet:
		handler.serveStatic(writer, "static/app.js", "application/javascript; charset=utf-8", false)
	case path == "/assets/favicon.svg" && request.Method == http.MethodGet:
		handler.serveStatic(writer, "static/favicon.svg", "image/svg+xml", false)
	case path == "/oauth/callback" && request.Method == http.MethodGet:
		handler.oauthCallback(writer, request)
	case path == "/api/v1/admin-sessions" && request.Method == http.MethodPost:
		handler.exchange(writer, request)
	case path == "/api/v1/admin-sessions/current" && request.Method == http.MethodPost:
		handler.bootstrap(writer, request)
	case path == "/api/v1/admin-sessions/current" && request.Method == http.MethodDelete:
		handler.logout(writer, request)
	case path == "/api/v1/admin-credentials" && request.Method == http.MethodGet:
		handler.listCredentials(writer, request)
	case path == "/api/v1/admin-credentials" && request.Method == http.MethodPost:
		handler.createCredential(writer, request)
	case path == "/api/v1/admin-authority" && request.Method == http.MethodGet:
		handler.getAdminAuthority(writer, request)
	case strings.HasPrefix(path, "/api/v1/admin-credentials/") && strings.HasSuffix(path, "/rotation-completion") && request.Method == http.MethodPost:
		handler.completeCredentialRotation(writer, request)
	case strings.HasPrefix(path, "/api/v1/admin-credentials/") && request.Method == http.MethodGet:
		handler.getCredential(writer, request)
	case strings.HasPrefix(path, "/api/v1/admin-credentials/") && request.Method == http.MethodDelete:
		handler.revokeCredential(writer, request)
	case path == "/api/v1/system-status" && request.Method == http.MethodGet:
		if !bodyless(request) || len(request.URL.Query()) != 0 {
			writeProblem(writer, contract.ProblemMalformedRequest)
			return
		}
		writeJSON(writer, http.StatusOK, handler.status())
	case path == "/api/v1/backups" && request.Method == http.MethodGet:
		handler.listBackups(writer, request)
	case path == "/api/v1/backups" && request.Method == http.MethodPost:
		handler.createBackup(writer, request)
	case strings.HasPrefix(path, "/api/v1/backups/") && request.Method == http.MethodGet:
		handler.getBackup(writer, request)
	case strings.HasPrefix(path, "/api/v1/backups/") && request.Method == http.MethodDelete:
		handler.deleteBackup(writer, request)
	case path == "/api/v1/events" && (request.Method == http.MethodGet || request.Method == http.MethodPost):
		handler.streamEvents(writer, request)
	case path == "/api/v1/catalog" && handler.activeCatalog != nil:
		handler.activeCatalogCollection(writer, request)
	case path == "/api/v1/invocations" && handler.invocations != nil:
		handler.invocationsCollection(writer, request)
	case strings.HasPrefix(path, "/api/v1/invocations/") && handler.invocations != nil:
		segments := strings.Split(strings.TrimPrefix(path, "/api/v1/invocations/"), "/")
		if len(segments) == 1 && segments[0] != "" {
			handler.invocationMember(writer, request, segments[0])
		} else {
			writeProblem(writer, contract.ProblemNotFound)
		}
	case path == "/api/v1/grant-requests" && handler.grantRequests != nil:
		handler.grantRequestsCollection(writer, request)
	case strings.HasPrefix(path, "/api/v1/grant-requests/") && handler.grantRequests != nil:
		segments := strings.Split(strings.TrimPrefix(path, "/api/v1/grant-requests/"), "/")
		switch {
		case len(segments) == 1 && segments[0] != "":
			handler.grantRequestMember(writer, request, segments[0], "")
		case len(segments) == 2 && segments[0] != "" && (segments[1] == "approve" || segments[1] == "reject"):
			handler.grantRequestMember(writer, request, segments[0], segments[1])
		default:
			writeProblem(writer, contract.ProblemNotFound)
		}
	case path == "/api/v1/grants" && handler.principals != nil:
		handler.grantsCollection(writer, request)
	case strings.HasPrefix(path, "/api/v1/grants/") && handler.principals != nil:
		segments := strings.Split(strings.TrimPrefix(path, "/api/v1/grants/"), "/")
		if len(segments) == 1 && segments[0] != "" {
			handler.grantMember(writer, request, segments[0])
		} else {
			writeProblem(writer, contract.ProblemNotFound)
		}
	case path == "/api/v1/principals" && handler.principals != nil:
		handler.principalsCollection(writer, request)
	case strings.HasPrefix(path, "/api/v1/principals/") && handler.principals != nil:
		segments := strings.Split(strings.TrimPrefix(path, "/api/v1/principals/"), "/")
		switch {
		case len(segments) == 1 && segments[0] != "":
			handler.principalMember(writer, request, segments[0])
		case len(segments) == 2 && segments[0] != "" && segments[1] == "credential":
			handler.principalCredential(writer, request, segments[0])
		default:
			writeProblem(writer, contract.ProblemNotFound)
		}
	case path == "/api/v1/servers" && handler.servers != nil:
		handler.serversCollection(writer, request)
	case strings.HasPrefix(path, "/api/v1/servers/") && (handler.servers != nil || handler.authFlows != nil || handler.replacements != nil || handler.catalog != nil):
		segments := strings.Split(strings.TrimPrefix(path, "/api/v1/servers/"), "/")
		switch {
		case len(segments) == 1 && segments[0] != "":
			handler.serverMember(writer, request, segments[0])
		case len(segments) == 2 && segments[0] != "" && segments[1] == "operations":
			handler.operationsCollection(writer, request, segments[0])
		case len(segments) == 3 && segments[0] != "" && segments[1] == "operations" && segments[2] != "":
			handler.operationMember(writer, request, segments[0], segments[2])
		case handler.replacements != nil && len(segments) == 2 && segments[0] != "" && segments[1] == "credential-replacements":
			handler.credentialReplacements(writer, request, segments[0])
		case handler.catalog != nil && len(segments) == 2 && segments[0] != "" && segments[1] == "descriptors":
			handler.descriptorsCollection(writer, request, segments[0])
		case handler.catalog != nil && len(segments) == 3 && segments[0] != "" && segments[1] == "descriptors" && segments[2] != "":
			handler.descriptorMember(writer, request, segments[0], segments[2])
		case handler.authFlows != nil && len(segments) == 2 && segments[0] != "" && segments[1] == "auth-flows":
			handler.authFlowsCollection(writer, request, segments[0])
		case handler.authFlows != nil && len(segments) == 3 && segments[0] != "" && segments[1] == "auth-flows" && segments[2] != "":
			handler.authFlowMember(writer, request, segments[0], segments[2])
		default:
			writeProblem(writer, contract.ProblemNotFound)
		}
		return
	default:
		writeProblem(writer, contract.ProblemNotFound)
	}
}

func (handler *Handler) serveStatic(writer http.ResponseWriter, name, contentType string, shell bool) {
	contents, err := staticFiles.ReadFile(name)
	if err != nil {
		writeProblem(writer, contract.ProblemNotFound)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", contentType)
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	if shell {
		writer.Header().Set("Content-Security-Policy", contentSecurityPolicy)
	}
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(contents)
}

func (handler *Handler) oauthCallback(writer http.ResponseWriter, request *http.Request) {
	result := oauth.CallbackResult{Outcome: oauth.CallbackInvalid}
	if handler.callbackService != nil {
		result = handler.callbackService.HandleCallback(request.Context(), request.URL.RawQuery)
	}
	status, body := http.StatusBadRequest, callbackFailedHTML
	switch result.Outcome {
	case oauth.CallbackSucceeded:
		status, body = http.StatusOK, callbackSucceededHTML
		if result.FlowID != "" {
			handler.emit(contract.Invalidation{Kind: contract.InvalidationServerAuthFlows, ResourceID: &result.FlowID})
		}
		if result.ServerID != "" {
			handler.emit(contract.Invalidation{Kind: contract.InvalidationServers, ResourceID: &result.ServerID})
			handler.trigger(result.ServerID, nil, true)
		}
	case oauth.CallbackTransient:
		status, body = http.StatusServiceUnavailable, callbackTransientHTML
	case oauth.CallbackInvalid:
		if result.FlowID != "" {
			handler.emit(contract.Invalidation{Kind: contract.InvalidationServerAuthFlows, ResourceID: &result.FlowID})
		}
		if result.ServerID != "" {
			handler.emit(contract.Invalidation{Kind: contract.InvalidationServers, ResourceID: &result.ServerID})
			handler.trigger(result.ServerID, nil, true)
		}
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(status)
	_, _ = writer.Write([]byte(body))
}

const (
	callbackSucceededHTML = "<!doctype html><html><head><meta charset=\"utf-8\"><title>Authorization complete</title></head><body>Authorization complete. You may close this window.</body></html>\n"
	callbackFailedHTML    = "<!doctype html><html><head><meta charset=\"utf-8\"><title>Authorization failed</title></head><body>Authorization failed. Return to Gateway and start a new authorization flow.</body></html>\n"
	callbackTransientHTML = "<!doctype html><html><head><meta charset=\"utf-8\"><title>Gateway unavailable</title></head><body>Gateway is temporarily unavailable. Retry the authorization callback.</body></html>\n"
)

func (handler *Handler) exchange(writer http.ResponseWriter, request *http.Request) {
	if !decodeEmptyObject(writer, request) {
		return
	}
	authenticated, ok := request.Context().Value(authContextKey{}).(authentication)
	if !ok {
		writeProblem(writer, contract.ProblemAuthenticationRequired)
		return
	}
	session, err := handler.sessions.Exchange(request.Context(), authenticated.bearer)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	http.SetCookie(writer, &http.Cookie{ //nolint:gosec // The exact plain-loopback HTTP contract intentionally omits Secure.
		Name: contract.SessionCookieName, Value: session.ID, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	writeJSON(writer, http.StatusCreated, contract.AdminSessionCreated{
		CSRFToken: session.CSRFToken, IdleExpiresAt: session.IdleExpiresAt.UTC().Format(time.RFC3339Nano), AbsoluteExpiresAt: session.AbsoluteExpiresAt.UTC().Format(time.RFC3339Nano),
	})
}

func (handler *Handler) bootstrap(writer http.ResponseWriter, request *http.Request) {
	if !decodeEmptyObject(writer, request) {
		return
	}
	authenticated, ok := request.Context().Value(authContextKey{}).(authentication)
	if !ok || authenticated.bootstrap == nil {
		writeProblem(writer, contract.ProblemAuthenticationRequired)
		return
	}
	session := *authenticated.bootstrap
	writeJSON(writer, http.StatusOK, contract.AdminSessionBootstrap{
		CSRFToken: session.CSRFToken, IdleExpiresAt: session.IdleExpiresAt.UTC().Format(time.RFC3339Nano), AbsoluteExpiresAt: session.AbsoluteExpiresAt.UTC().Format(time.RFC3339Nano),
	})
}

func (handler *Handler) logout(writer http.ResponseWriter, request *http.Request) {
	if !decodeEmptyObject(writer, request) {
		return
	}
	authenticated, ok := request.Context().Value(authContextKey{}).(authentication)
	if !ok || !authenticated.viaSession {
		writeProblem(writer, contract.ProblemAuthenticationRequired)
		return
	}
	if err := handler.sessions.Logout(authenticated.sessionID); err != nil {
		writeServiceError(writer, err)
		return
	}
	http.SetCookie(writer, &http.Cookie{ //nolint:gosec // The exact plain-loopback HTTP contract intentionally omits Secure.
		Name: contract.SessionCookieName, Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) listCredentials(writer http.ResponseWriter, request *http.Request) {
	if !bodyless(request) {
		writeProblem(writer, contract.ProblemMalformedRequest)
		return
	}
	limit, after, problem := parseCollectionQuery(request.URL.Query(), "admin_credentials")
	if problem != "" {
		writeProblem(writer, problem)
		return
	}
	items, err := handler.credentials.List(request.Context())
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	sort.Slice(items, func(left, right int) bool { return items[left].ID < items[right].ID })
	start := sort.Search(len(items), func(index int) bool { return items[index].ID > after })
	end := start + limit
	var next *string
	if end < len(items) {
		value := encodeCursor("admin_credentials", items[end-1].ID)
		next = &value
	} else {
		end = len(items)
	}
	writeJSON(writer, http.StatusOK, contract.Collection[contract.AdminCredential]{Items: items[start:end], NextCursor: next})
}

func (handler *Handler) getAdminAuthority(writer http.ResponseWriter, request *http.Request) {
	if !bodyless(request) || len(request.URL.Query()) != 0 {
		writeProblem(writer, contract.ProblemMalformedRequest)
		return
	}
	authority, err := handler.credentials.Authority(request.Context())
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writer.Header().Set("ETag", contract.AdminAuthorityETag(authority.Revision))
	writeJSON(writer, http.StatusOK, authority)
}

func (handler *Handler) createCredential(writer http.ResponseWriter, request *http.Request) {
	var raw map[string]json.RawMessage
	if !decodeStrictBody(writer, request, &raw) {
		return
	}
	value, ok := raw["expires_at"]
	if !ok || len(raw) != 1 {
		writeProblem(writer, contract.ProblemInvalidJSON)
		return
	}
	var expires *time.Time
	if !bytes.Equal(value, []byte("null")) {
		var text string
		if json.Unmarshal(value, &text) != nil {
			writeProblem(writer, contract.ProblemInvalidJSON)
			return
		}
		parsed, err := time.Parse(time.RFC3339, text)
		if err != nil {
			writeProblem(writer, contract.ProblemInvalidJSON)
			return
		}
		expires = &parsed
	}
	expected, conditional, ok := adminAuthorityPrecondition(writer, request, false)
	if !ok {
		return
	}
	var created contract.CreatedAdminCredential
	var err error
	if conditional {
		authenticated, present := request.Context().Value(authContextKey{}).(authentication)
		if !present || authenticated.viaSession {
			writeProblem(writer, contract.ProblemAuthenticationRequired)
			return
		}
		created, err = handler.credentials.CreateConditional(request.Context(), expires, expected)
	} else {
		created, err = handler.credentials.Create(request.Context(), expires)
	}
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	handler.emit(contract.Invalidation{Kind: contract.InvalidationAdminCredentials, ResourceID: &created.ID})
	if conditional {
		writer.Header().Set("ETag", contract.AdminAuthorityETag(created.Revision))
	}
	writeJSON(writer, http.StatusCreated, created)
}

func (handler *Handler) completeCredentialRotation(writer http.ResponseWriter, request *http.Request) {
	segments := strings.Split(strings.TrimPrefix(request.URL.Path, "/api/v1/admin-credentials/"), "/")
	if len(segments) != 2 || segments[0] == "" || segments[1] != "rotation-completion" || len(request.URL.Query()) != 0 {
		writeProblem(writer, contract.ProblemNotFound)
		return
	}
	var input contract.AdminCredentialRotationCompletion
	if !decodeStrictBody(writer, request, &input) {
		return
	}
	if input.ReplacementID == "" {
		writeProblem(writer, contract.ProblemInvalidJSON)
		return
	}
	expected, _, ok := adminAuthorityPrecondition(writer, request, true)
	if !ok {
		return
	}
	result, err := handler.credentials.CompleteRotation(request.Context(), segments[0], input.ReplacementID, expected)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writer.Header().Set("ETag", contract.AdminAuthorityETag(result.OldCredential.Revision))
	writeJSON(writer, http.StatusOK, result)
}

func (handler *Handler) getCredential(writer http.ResponseWriter, request *http.Request) {
	if !bodyless(request) || len(request.URL.Query()) != 0 {
		writeProblem(writer, contract.ProblemMalformedRequest)
		return
	}
	item, err := handler.credentials.Get(request.Context(), strings.TrimPrefix(request.URL.Path, "/api/v1/admin-credentials/"))
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, item)
}

func (handler *Handler) revokeCredential(writer http.ResponseWriter, request *http.Request) {
	if !decodeEmptyObject(writer, request) {
		return
	}
	if err := handler.credentials.Revoke(request.Context(), strings.TrimPrefix(request.URL.Path, "/api/v1/admin-credentials/")); err != nil {
		writeServiceError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) listBackups(writer http.ResponseWriter, request *http.Request) {
	if handler.backups == nil || !bodyless(request) {
		writeProblem(writer, contract.ProblemMalformedRequest)
		return
	}
	limit, after, problem := parseCollectionQuery(request.URL.Query(), "backups")
	if problem != "" {
		writeProblem(writer, problem)
		return
	}
	items, err := handler.backups.List(request.Context())
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	sort.Slice(items, func(left, right int) bool { return items[left].ID < items[right].ID })
	start := sort.Search(len(items), func(index int) bool { return items[index].ID > after })
	end := start + limit
	var next *string
	if end < len(items) {
		value := encodeCursor("backups", items[end-1].ID)
		next = &value
	} else {
		end = len(items)
	}
	writeJSON(writer, http.StatusOK, contract.Collection[contract.Backup]{Items: items[start:end], NextCursor: next})
}

func (handler *Handler) createBackup(writer http.ResponseWriter, request *http.Request) {
	if handler.backups == nil || !decodeEmptyObject(writer, request) {
		return
	}
	key := request.Header.Get("Idempotency-Key")
	if key == "" {
		writeProblem(writer, contract.ProblemInvalidIdempotencyKey)
		return
	}
	authenticated, ok := request.Context().Value(authContextKey{}).(authentication)
	if !ok {
		writeProblem(writer, contract.ProblemAuthenticationRequired)
		return
	}
	item, replay, err := handler.backups.Create(request.Context(), authenticated.credential.ID, key)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	status := http.StatusCreated
	if replay {
		status = http.StatusOK
	} else {
		id := item.ID
		handler.emit(contract.Invalidation{Kind: contract.InvalidationBackups, ResourceID: &id})
	}
	writeJSON(writer, status, item)
}

func (handler *Handler) getBackup(writer http.ResponseWriter, request *http.Request) {
	if handler.backups == nil || !bodyless(request) || len(request.URL.Query()) != 0 {
		writeProblem(writer, contract.ProblemMalformedRequest)
		return
	}
	item, err := handler.backups.Get(request.Context(), strings.TrimPrefix(request.URL.Path, "/api/v1/backups/"))
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, item)
}

func (handler *Handler) deleteBackup(writer http.ResponseWriter, request *http.Request) {
	if handler.backups == nil || !decodeEmptyObject(writer, request) {
		return
	}
	id := strings.TrimPrefix(request.URL.Path, "/api/v1/backups/")
	if err := handler.backups.Delete(request.Context(), id); err != nil {
		writeServiceError(writer, err)
		return
	}
	handler.emit(contract.Invalidation{Kind: contract.InvalidationBackups, ResourceID: &id})
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) streamEvents(writer http.ResponseWriter, request *http.Request) {
	if handler.events == nil || len(request.URL.Query()) != 0 || len(request.Header.Values("Last-Event-ID")) != 0 {
		writeProblem(writer, contract.ProblemMalformedRequest)
		return
	}
	switch request.Method {
	case http.MethodGet:
		if !bodyless(request) {
			writeProblem(writer, contract.ProblemMalformedRequest)
			return
		}
	case http.MethodPost:
		if !decodeEmptyObject(writer, request) {
			return
		}
	default:
		writeProblem(writer, contract.ProblemMethodNotAllowed)
		return
	}
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeProblem(writer, contract.ProblemStorageUnavailable)
		return
	}
	authenticated, ok := request.Context().Value(authContextKey{}).(authentication)
	if !ok {
		writeProblem(writer, contract.ProblemAuthenticationRequired)
		return
	}
	var terminal <-chan struct{}
	if authenticated.viaSession {
		var err error
		terminal, err = handler.sessions.Subscribe(authenticated.sessionID)
		if err != nil {
			writeServiceError(writer, err)
			return
		}
	}
	subscription, err := handler.events.Subscribe(authenticated.credential.ID, terminal)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	defer subscription.Close()

	writer.Header().Set("Content-Type", contract.MediaTypeEventStream)
	writer.Header().Set("Connection", "keep-alive")
	writer.WriteHeader(http.StatusOK)
	if !writeSSE(writer, flusher, []byte(": keepalive\n\n")) {
		return
	}
	keepalive, stop := handler.newKeepalive()
	defer stop()
	for {
		select {
		case event, open := <-subscription.Events():
			if !open {
				return
			}
			payload, marshalErr := json.Marshal(event)
			if marshalErr != nil {
				return
			}
			frame := append([]byte("event: invalidate\ndata: "), payload...)
			frame = append(frame, '\n', '\n')
			if len(frame) > limitValue("sse_frame_bytes") || !writeSSE(writer, flusher, frame) {
				return
			}
		case <-keepalive:
			if !writeSSE(writer, flusher, []byte(": keepalive\n\n")) {
				return
			}
		case <-subscription.Done():
			return
		case <-request.Context().Done():
			return
		}
	}
}

func writeSSE(writer http.ResponseWriter, flusher http.Flusher, frame []byte) bool {
	_ = http.NewResponseController(writer).SetWriteDeadline(time.Now().Add(contract.SSEBlockedWriteDeadline))
	if _, err := writer.Write(frame); err != nil {
		return false
	}
	flusher.Flush()
	return true
}

func (handler *Handler) emit(event contract.Invalidation) {
	if handler.invalidate == nil {
		return
	}
	handler.invalidate(event)
	if event.Kind != contract.InvalidationSystemStatus {
		handler.invalidate(contract.Invalidation{Kind: contract.InvalidationSystemStatus})
	}
}

func parseBearer(values []string) (string, bool, error) {
	if len(values) == 0 {
		return "", false, nil
	}
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") || strings.TrimPrefix(values[0], "Bearer ") == "" || strings.Contains(strings.TrimPrefix(values[0], "Bearer "), " ") {
		return "", true, admin.ErrAuthenticationRequired
	}
	return strings.TrimPrefix(values[0], "Bearer "), true, nil
}

func parseSessionCookie(request *http.Request) (string, bool, error) {
	values := sessionCookieValues(request)
	if len(values) > 1 {
		return "", true, admin.ErrAmbiguousCredentials
	}
	if len(values) == 0 {
		return "", false, nil
	}
	return values[0], true, nil
}

func sessionCookieValues(request *http.Request) []string {
	values := make([]string, 0, 1)
	for _, cookie := range request.Cookies() {
		if cookie.Name == contract.SessionCookieName {
			values = append(values, cookie.Value)
		}
	}
	return values
}

func validSessionValue(value string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == contract.SessionValueBytes && base64.RawURLEncoding.EncodeToString(decoded) == value
}

func expiredSessionCookie() *http.Cookie {
	return &http.Cookie{ //nolint:gosec // The exact plain-loopback HTTP contract intentionally omits Secure.
		Name: contract.SessionCookieName, Path: "/", MaxAge: -1, Expires: time.Unix(1, 0).UTC(), HttpOnly: true, SameSite: http.SameSiteStrictMode,
	}
}

func parseCollectionQuery(query url.Values, collection string) (int, string, contract.ProblemCode) {
	for key, values := range query {
		if (key != "cursor" && key != "limit") || len(values) != 1 {
			return 0, "", contract.ProblemMalformedRequest
		}
	}
	limit := contract.AdminListPageDefault
	limitName := "admin_list_page"
	if collection == "backups" {
		limit = contract.BackupListPageDefault
		limitName = "backup_list_page"
	}
	if text := query.Get("limit"); text != "" {
		value, err := strconv.Atoi(text)
		maximum, _ := contract.FixedLimitByName(limitName)
		if err != nil || value < 1 || int64(value) > maximum.Maximum {
			return 0, "", contract.ProblemMalformedRequest
		}
		limit = value
	}
	after := ""
	if cursor := query.Get("cursor"); cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(cursor)
		parts := strings.Split(string(decoded), "\x00")
		if err != nil || len(cursor) > limitValue("cursor_bytes") || len(parts) != 3 || parts[0] != "v1" || parts[1] != collection || len(parts[2]) != 26 {
			return 0, "", contract.ProblemInvalidCursor
		}
		after = parts[2]
	}
	return limit, after, ""
}

func encodeCursor(collection, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte("v1\x00" + collection + "\x00" + id))
}

func decodeEmptyObject(writer http.ResponseWriter, request *http.Request) bool {
	var value map[string]json.RawMessage
	if !decodeStrictBody(writer, request, &value) {
		return false
	}
	if len(value) != 0 {
		writeProblem(writer, contract.ProblemInvalidJSON)
		return false
	}
	return true
}

func decodeStrictBody(writer http.ResponseWriter, request *http.Request, destination any) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != contract.MediaTypeJSON {
		writeProblem(writer, contract.ProblemUnsupportedMediaType)
		return false
	}
	err = strictjson.DecodeReader(request.Body, destination, strictjson.Options{
		MaxBytes:             int64(limitValue("api_json_body_bytes")),
		MaxDepth:             limitValue("json_depth"),
		RejectUnknownMembers: true,
	})
	if errors.Is(err, strictjson.ErrTooLarge) {
		writeProblem(writer, contract.ProblemBodyTooLarge)
		return false
	}
	if err != nil {
		writeProblem(writer, contract.ProblemInvalidJSON)
		return false
	}
	return true
}

func bodyless(request *http.Request) bool {
	contents, err := io.ReadAll(io.LimitReader(request.Body, 1))
	return err == nil && len(contents) == 0
}

func unsafe(method string) bool { return method != http.MethodGet && method != http.MethodHead }

func boundaryError(err error) error {
	switch {
	case errors.Is(err, admin.ErrCredentialDomainMismatch):
		return httpboundary.Error{Code: contract.ProblemCredentialDomainMismatch}
	case errors.Is(err, admin.ErrAmbiguousCredentials):
		return httpboundary.Error{Code: contract.ProblemAmbiguousCredentials}
	case errors.Is(err, admin.ErrCSRF):
		return httpboundary.Error{Code: contract.ProblemCSRFFailed}
	default:
		return httpboundary.Error{Code: contract.ProblemAuthenticationRequired}
	}
}

func adminAuthorityPrecondition(writer http.ResponseWriter, request *http.Request, required bool) (string, bool, bool) {
	values := request.Header.Values("If-Match")
	if len(values) == 0 {
		if required {
			writeProblem(writer, contract.ProblemAdminAuthorityPreconditionRequired)
			return "", false, false
		}
		return "", false, true
	}
	if len(values) != 1 {
		writeProblem(writer, contract.ProblemStaleAdminAuthority)
		return "", true, false
	}
	const prefix = `"admin-authority-`
	value := values[0]
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, `"`) {
		writeProblem(writer, contract.ProblemStaleAdminAuthority)
		return "", true, false
	}
	revision := strings.TrimSuffix(strings.TrimPrefix(value, prefix), `"`)
	if revision == "" || (len(revision) > 1 && revision[0] == '0') {
		writeProblem(writer, contract.ProblemStaleAdminAuthority)
		return "", true, false
	}
	if _, err := strconv.ParseUint(revision, 10, 64); err != nil {
		writeProblem(writer, contract.ProblemStaleAdminAuthority)
		return "", true, false
	}
	return revision, true, true
}

func writeServiceError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, admin.ErrNotFound), errors.Is(err, backup.ErrNotFound):
		writeProblem(writer, contract.ProblemNotFound)
	case errors.Is(err, backup.ErrInvalidIdempotency):
		writeProblem(writer, contract.ProblemInvalidIdempotencyKey)
	case errors.Is(err, admin.ErrInvalidExpiry):
		writeProblem(writer, contract.ProblemMalformedRequest)
	case errors.Is(err, admin.ErrResourceLimit), errors.Is(err, admin.ErrSessionLimit), errors.Is(err, backup.ErrResourceLimit), errors.Is(err, events.ErrStreamLimit):
		writeProblem(writer, contract.ProblemResourceLimit)
	case errors.Is(err, admin.ErrLastNonExpiring):
		writeProblem(writer, contract.ProblemConflict)
	case errors.Is(err, admin.ErrStaleAuthority):
		writeProblem(writer, contract.ProblemStaleAdminAuthority)
	case errors.Is(err, admin.ErrRotationConflict):
		writeProblem(writer, contract.ProblemAdminRotationConflict)
	case errors.Is(err, events.ErrShuttingDown), errors.Is(err, admin.ErrShuttingDown):
		writeProblem(writer, contract.ProblemShuttingDown)
	default:
		writeProblem(writer, contract.ProblemStorageUnavailable)
	}
}

func writeProblem(writer http.ResponseWriter, code contract.ProblemCode) {
	problem, _ := contract.ProblemForCode(code)
	writer.Header().Set("Content-Type", contract.MediaTypeProblemJSON)
	writer.WriteHeader(problem.Status)
	_ = json.NewEncoder(writer).Encode(contract.ProblemEnvelope(problem))
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", contract.MediaTypeJSON)
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func limitValue(name string) int {
	value, ok := contract.FixedLimitByName(name)
	if !ok {
		panic(fmt.Sprintf("missing contract limit %q", name))
	}
	return int(value.Maximum)
}
