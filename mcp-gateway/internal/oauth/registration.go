package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/remote"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

var (
	ErrRegistrationRejected = errors.New("OAuth registration is invalid")
	ErrRegistrationStale    = errors.New("OAuth registration is stale")
)

type RegistrationRequest struct {
	ServerID                     string
	ExpectedDesiredRevision      string
	ExpectedRegistrationRevision string
	ExpectedOAuthClientRevision  string
	ExpectedAuthFlowID           string
	Registration                 contract.OAuthRegistration
	Graph                        Graph
	CallbackURL                  string
}

type registrationStore interface {
	PublishPublicRegistration(context.Context, servers.RegistrationFence, servers.OAuthRegistrationAuthority) (servers.OAuthRegistrationAuthority, error)
	RegistrationAuthorityCallback(servers.RegistrationFence, servers.OAuthRegistrationAuthority) (keyring.AuthorityCallback, error)
	OAuthRegistration(context.Context, string) (servers.OAuthRegistrationAuthority, error)
}

type secretPublisher interface {
	ReplaceFenced(context.Context, keyring.Namespace, []byte, keyring.AuthorityCallback) (keyring.CutoverResult, error)
}

type machineRequester interface {
	Request(context.Context, string, bool, string, http.Header, []byte, int64) (int, http.Header, []byte, error)
}

type Registrar struct {
	requester      machineRequester
	store          registrationStore
	secrets        secretPublisher
	installationID string
	now            func() time.Time
	current        func() bool
}

type hardenedRequester struct {
	factory *remote.Factory
}

type dynamicRequest struct {
	ApplicationType         string                           `json:"application_type"`
	RedirectURIs            []string                         `json:"redirect_uris"`
	ResponseTypes           []string                         `json:"response_types"`
	GrantTypes              []string                         `json:"grant_types"`
	TokenEndpointAuthMethod contract.TokenEndpointAuthMethod `json:"token_endpoint_auth_method"`
}

type dynamicResponse struct {
	ClientID                string
	RedirectURIs            []string
	ResponseTypes           []string
	GrantTypes              []string
	TokenEndpointAuthMethod contract.TokenEndpointAuthMethod
	ClientSecret            string
	ClientSecretExpiresAt   int64
	SecretPresent           bool
}

func NewRegistrar(factory *remote.Factory, store *servers.Repository, secrets *keyring.Coordinator, installationID string, now func() time.Time, current func() bool) (*Registrar, error) {
	if factory == nil || store == nil || secrets == nil || installationID == "" {
		return nil, ErrRegistrationRejected
	}
	if now == nil {
		now = time.Now
	}
	if current == nil {
		current = func() bool { return true }
	}
	return newRegistrar(hardenedRequester{factory: factory}, store, secrets, installationID, now, current), nil
}

func newRegistrar(requester machineRequester, store registrationStore, secrets secretPublisher, installationID string, now func() time.Time, current func() bool) *Registrar {
	return &Registrar{requester: requester, store: store, secrets: secrets, installationID: installationID, now: now, current: current}
}

func (requester hardenedRequester) Request(ctx context.Context, rawURL string, trusted bool, method string, header http.Header, body []byte, maximum int64) (int, http.Header, []byte, error) {
	endpoint, err := remote.Parse(rawURL, remote.Policy{AllowRestricted: trusted, AllowQuery: true})
	if err != nil {
		return 0, nil, nil, newDiagnosticFailure(ErrRegistrationRejected, contract.ReasonConfigurationInvalid, 0)
	}
	requestCtx, cancel := context.WithTimeout(ctx, contract.OAuthRequestDeadline)
	defer cancel()
	response, err := requester.factory.Exchange(requestCtx, remote.Request{Endpoint: endpoint, Method: method, Header: header, Body: body, MaxBody: maximum})
	if err != nil {
		return 0, nil, nil, newDiagnosticFailure(ErrRegistrationRejected, contract.ReasonConnectivity, 0)
	}
	return response.StatusCode, response.Header, response.Body, nil
}

func (registrar *Registrar) Register(ctx context.Context, request RegistrationRequest) (servers.OAuthRegistrationAuthority, error) {
	if registrar == nil || registrar.requester == nil || registrar.store == nil || registrar.secrets == nil || !registrar.current() {
		return servers.OAuthRegistrationAuthority{}, ErrRegistrationStale
	}
	if request.ServerID == "" || request.Graph.Resource == "" || request.Graph.Issuer == "" || !validCallbackURL(request.CallbackURL) {
		return servers.OAuthRegistrationAuthority{}, ErrRegistrationRejected
	}
	fence := servers.RegistrationFence{ServerID: request.ServerID, ExpectedDesiredRevision: request.ExpectedDesiredRevision, ExpectedRegistrationRevision: request.ExpectedRegistrationRevision, ExpectedOAuthClientRevision: request.ExpectedOAuthClientRevision, ExpectedAuthFlowID: request.ExpectedAuthFlowID}
	created := registrar.now().UTC()
	if request.ExpectedRegistrationRevision != "0" {
		existing, err := registrar.store.OAuthRegistration(ctx, request.ServerID)
		if err != nil {
			return servers.OAuthRegistrationAuthority{}, classifyRegistrationError(err)
		}
		if registrationUsable(existing, request, created) {
			return existing, nil
		}
	}
	switch registration := request.Registration.(type) {
	case contract.StaticOAuthRegistration:
		if registration.Mode != contract.RegistrationStatic || registration.ClientID == "" || !utf8.ValidString(registration.ClientID) || int64(len(registration.ClientID)) > limit("oauth_client_id_bytes") || registration.Issuer != nil && *registration.Issuer != request.Graph.Issuer || !contains(request.Graph.TokenEndpointAuthMethodsSupported, string(registration.TokenEndpointAuthMethod)) {
			return servers.OAuthRegistrationAuthority{}, ErrRegistrationRejected
		}
		authority := registrationAuthority(contract.RegistrationStatic, request.Graph, registration.ClientID, request.CallbackURL, registration.TokenEndpointAuthMethod, created, nil)
		if !registrar.current() {
			return servers.OAuthRegistrationAuthority{}, ErrRegistrationStale
		}
		published, err := registrar.store.PublishPublicRegistration(ctx, fence, authority)
		if err != nil {
			return servers.OAuthRegistrationAuthority{}, classifyRegistrationError(err)
		}
		return published, nil
	case contract.DynamicOAuthRegistration:
		if registration.Mode != contract.RegistrationDynamic || registration.Issuer != nil && *registration.Issuer != request.Graph.Issuer || request.Graph.RegistrationEndpoint == "" {
			return servers.OAuthRegistrationAuthority{}, ErrRegistrationRejected
		}
		return registrar.registerDynamic(ctx, request, fence, created)
	default:
		return servers.OAuthRegistrationAuthority{}, ErrRegistrationRejected
	}
}

func (registrar *Registrar) registerDynamic(ctx context.Context, request RegistrationRequest, fence servers.RegistrationFence, created time.Time) (servers.OAuthRegistrationAuthority, error) {
	method, ok := selectDynamicMethod(request.Graph.TokenEndpointAuthMethodsSupported)
	if !ok {
		return servers.OAuthRegistrationAuthority{}, ErrRegistrationRejected
	}
	payload, err := json.Marshal(dynamicRequest{
		ApplicationType: "native", RedirectURIs: []string{request.CallbackURL}, ResponseTypes: []string{"code"},
		GrantTypes: []string{"authorization_code", "refresh_token"}, TokenEndpointAuthMethod: method,
	})
	if err != nil {
		return servers.OAuthRegistrationAuthority{}, ErrRegistrationRejected
	}
	header := http.Header{"Accept": []string{contract.MediaTypeJSON}, "Content-Type": []string{contract.MediaTypeJSON}, "User-Agent": []string{""}}
	status, responseHeader, body, err := registrar.requester.Request(ctx, request.Graph.RegistrationEndpoint, request.Graph.AllowsRestrictedEndpoint(request.Graph.RegistrationEndpoint), http.MethodPost, header, payload, limit("oauth_response_body_bytes"))
	if err != nil {
		return servers.OAuthRegistrationAuthority{}, err
	}
	if status != http.StatusCreated {
		return servers.OAuthRegistrationAuthority{}, newDiagnosticFailure(ErrRegistrationRejected, reasonForHTTPStatus(status), status)
	}
	if !jsonContentType(responseHeader) {
		return servers.OAuthRegistrationAuthority{}, newDiagnosticFailure(ErrRegistrationRejected, contract.ReasonProtocolInvalid, status)
	}
	response, err := parseDynamicResponse(body, request.CallbackURL, method, created)
	if err != nil {
		return servers.OAuthRegistrationAuthority{}, newDiagnosticFailure(ErrRegistrationRejected, contract.ReasonProtocolInvalid, status)
	}
	if !registrar.current() {
		return servers.OAuthRegistrationAuthority{}, ErrRegistrationStale
	}
	var expires *time.Time
	if response.SecretPresent && response.ClientSecretExpiresAt > 0 {
		value := time.Unix(response.ClientSecretExpiresAt, 0).UTC()
		expires = &value
	}
	authority := registrationAuthority(contract.RegistrationDynamic, request.Graph, response.ClientID, request.CallbackURL, method, created, expires)
	if method == contract.TokenEndpointAuthNone {
		published, publishErr := registrar.store.PublishPublicRegistration(ctx, fence, authority)
		if publishErr != nil {
			return servers.OAuthRegistrationAuthority{}, classifyRegistrationError(publishErr)
		}
		return published, nil
	}
	namespace, err := keyring.NewNamespace(registrar.installationID, request.ServerID, keyring.RecordOAuthClient)
	if err != nil {
		return servers.OAuthRegistrationAuthority{}, ErrRegistrationRejected
	}
	callback, err := registrar.store.RegistrationAuthorityCallback(fence, authority)
	if err != nil {
		return servers.OAuthRegistrationAuthority{}, classifyRegistrationError(err)
	}
	if _, err = registrar.secrets.ReplaceFenced(ctx, namespace, []byte(response.ClientSecret), callback); err != nil {
		return servers.OAuthRegistrationAuthority{}, classifyRegistrationError(err)
	}
	published, err := registrar.store.OAuthRegistration(ctx, request.ServerID)
	if err != nil {
		return servers.OAuthRegistrationAuthority{}, classifyRegistrationError(err)
	}
	return published, nil
}

func registrationAuthority(mode contract.RegistrationMode, graph Graph, clientID, callback string, method contract.TokenEndpointAuthMethod, created time.Time, expires *time.Time) servers.OAuthRegistrationAuthority {
	authority := servers.OAuthRegistrationAuthority{Revision: "0", Mode: mode, Issuer: graph.Issuer, ClientID: clientID, CallbackURL: callback, ResourceURL: graph.Resource, TokenEndpointAuthMethod: method, CreatedAt: created.Format(time.RFC3339Nano)}
	if expires != nil {
		value := expires.Format(time.RFC3339Nano)
		authority.ClientSecretExpiresAt = &value
	}
	return authority
}

func parseDynamicResponse(body []byte, callback string, method contract.TokenEndpointAuthMethod, now time.Time) (dynamicResponse, error) {
	var object map[string]json.RawMessage
	if err := strictjson.Decode(body, &object, strictjson.Options{MaxBytes: limit("oauth_response_body_bytes"), MaxDepth: int(limit("oauth_json_depth"))}); err != nil || object == nil {
		return dynamicResponse{}, ErrRegistrationRejected
	}
	clientID, err := requiredString(object, "client_id")
	if err != nil || int64(len(clientID)) > limit("oauth_client_id_bytes") {
		return dynamicResponse{}, ErrRegistrationRejected
	}
	redirects, err := requiredStrings(object, "redirect_uris")
	if err != nil || len(redirects) != 1 || redirects[0] != callback {
		return dynamicResponse{}, ErrRegistrationRejected
	}
	responseTypes, err := requiredStrings(object, "response_types")
	if err != nil || !contains(responseTypes, "code") {
		return dynamicResponse{}, ErrRegistrationRejected
	}
	grantTypes, err := requiredStrings(object, "grant_types")
	if err != nil || !contains(grantTypes, "authorization_code") || !contains(grantTypes, "refresh_token") {
		return dynamicResponse{}, ErrRegistrationRejected
	}
	returnedMethod, err := requiredString(object, "token_endpoint_auth_method")
	if err != nil || returnedMethod != string(method) {
		return dynamicResponse{}, ErrRegistrationRejected
	}
	secret, secretPresent, err := optionalString(object, "client_secret")
	if err != nil || secretPresent && (secret == "" || int64(len(secret)) > limit("oauth_client_secret_bytes")) {
		return dynamicResponse{}, ErrRegistrationRejected
	}
	expires, expiresPresent, err := optionalInteger(object, "client_secret_expires_at")
	if err != nil {
		return dynamicResponse{}, ErrRegistrationRejected
	}
	confidential := method != contract.TokenEndpointAuthNone
	if confidential != secretPresent || !confidential && expiresPresent || expiresPresent && expires != 0 && expires <= now.Unix() {
		return dynamicResponse{}, ErrRegistrationRejected
	}
	return dynamicResponse{ClientID: clientID, RedirectURIs: redirects, ResponseTypes: responseTypes, GrantTypes: grantTypes, TokenEndpointAuthMethod: method, ClientSecret: secret, ClientSecretExpiresAt: expires, SecretPresent: secretPresent}, nil
}

func optionalInteger(object map[string]json.RawMessage, name string) (int64, bool, error) {
	raw, present := object[name]
	if !present {
		return 0, false, nil
	}
	if string(raw) == "null" {
		return 0, true, ErrRegistrationRejected
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return 0, true, ErrRegistrationRejected
	}
	value, err := strconv.ParseInt(string(number), 10, 64)
	if err != nil || value < 0 {
		return 0, true, ErrRegistrationRejected
	}
	return value, true, nil
}

func registrationUsable(existing servers.OAuthRegistrationAuthority, request RegistrationRequest, now time.Time) bool {
	if existing.Revision != request.ExpectedRegistrationRevision || existing.Issuer != request.Graph.Issuer || existing.ResourceURL != request.Graph.Resource || existing.CallbackURL != request.CallbackURL {
		return false
	}
	if existing.ClientSecretExpiresAt != nil {
		expires, err := time.Parse(time.RFC3339Nano, *existing.ClientSecretExpiresAt)
		if err != nil || !now.Before(expires) {
			return false
		}
	}
	switch desired := request.Registration.(type) {
	case contract.StaticOAuthRegistration:
		return existing.Mode == contract.RegistrationStatic && existing.ClientID == desired.ClientID && existing.TokenEndpointAuthMethod == desired.TokenEndpointAuthMethod
	case contract.DynamicOAuthRegistration:
		return existing.Mode == contract.RegistrationDynamic
	default:
		return false
	}
}

func validCallbackURL(raw string) bool {
	if raw == "" || int64(len(raw)) > limit("oauth_url_bytes") {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "/oauth/callback" || parsed.String() != raw || parsed.Port() == "" {
		return false
	}
	address, err := netip.ParseAddr(parsed.Hostname())
	return err == nil && address.Is4() && address.IsLoopback()
}

func selectDynamicMethod(values []string) (contract.TokenEndpointAuthMethod, bool) {
	for _, method := range []contract.TokenEndpointAuthMethod{contract.TokenEndpointAuthClientSecretBasic, contract.TokenEndpointAuthClientSecretPost, contract.TokenEndpointAuthNone} {
		if contains(values, string(method)) {
			return method, true
		}
	}
	return "", false
}

func classifyRegistrationError(err error) error {
	if errors.Is(err, servers.ErrStaleRevision) || errors.Is(err, keyring.ErrDraining) {
		return ErrRegistrationStale
	}
	return ErrRegistrationRejected
}
