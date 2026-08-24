// Package oauth owns the bounded server-scoped OAuth trust graph.
package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/remote"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

var ErrTrustRejected = errors.New("OAuth trust graph is invalid")

type Input struct {
	Resource          string
	ChallengeMetadata []string
	DesiredIssuer     *string
	TrustedOrigins    []string
}

type Graph struct {
	Resource                                 string
	Issuer                                   string
	AuthorizationEndpoint                    string
	TokenEndpoint                            string
	RegistrationEndpoint                     string
	RevocationEndpoint                       string
	ProtectedScopesSupported                 []string
	ScopesSupported                          []string
	TokenEndpointAuthMethodsSupported        []string
	RevocationEndpointAuthMethodsSupported   []string
	AuthorizationResponseIssuerParameterUsed bool
	trustedOrigins                           map[string]struct{}
}

type fetcher interface {
	Fetch(context.Context, string, bool) (int, http.Header, []byte, error)
}

type Resolver struct {
	fetch fetcher
}

type remoteFetcher struct {
	factory *remote.Factory
}

func (graph Graph) AllowsRestrictedEndpoint(rawURL string) bool {
	parsed, err := parseIdentifier(rawURL, true)
	return err == nil && isTrusted(parsed, graph.trustedOrigins)
}

type protectedMetadata struct {
	resource             string
	authorizationServers []string
	scopesSupported      []string
}

type authorizationMetadata struct {
	issuer                                   string
	authorizationEndpoint                    string
	tokenEndpoint                            string
	registrationEndpoint                     string
	revocationEndpoint                       string
	responseTypes                            []string
	grantTypes                               []string
	codeChallengeMethods                     []string
	scopesSupported                          []string
	tokenEndpointAuthMethods                 []string
	revocationEndpointAuthMethods            []string
	authorizationResponseIssuerParameterUsed bool
}

func NewResolver(factory *remote.Factory) (*Resolver, error) {
	if factory == nil {
		return nil, ErrTrustRejected
	}
	return newResolver(remoteFetcher{factory: factory}), nil
}

func newResolver(fetch fetcher) *Resolver {
	return &Resolver{fetch: fetch}
}

func (fetch remoteFetcher) Fetch(ctx context.Context, rawURL string, trusted bool) (int, http.Header, []byte, error) {
	endpoint, err := remote.Parse(rawURL, remote.Policy{AllowRestricted: trusted, AllowQuery: true})
	if err != nil {
		return 0, nil, nil, ErrTrustRejected
	}
	requestCtx, cancel := context.WithTimeout(ctx, contract.OAuthRequestDeadline)
	defer cancel()
	response, err := fetch.factory.Exchange(requestCtx, remote.Request{
		Endpoint: endpoint,
		Method:   http.MethodGet,
		Header:   http.Header{"Accept": []string{contract.MediaTypeJSON}, "User-Agent": []string{""}},
		MaxBody:  limit("oauth_metadata_body_bytes"),
	})
	if err != nil {
		return 0, nil, nil, ErrTrustRejected
	}
	return response.StatusCode, response.Header, response.Body, nil
}

func (resolver *Resolver) Discover(ctx context.Context, input Input) (Graph, error) {
	resourceURL, err := parseIdentifier(input.Resource, false)
	if err != nil {
		return Graph{}, ErrTrustRejected
	}
	trusted, err := trustedOriginSet(input.TrustedOrigins)
	if err != nil {
		return Graph{}, err
	}
	resourceOrigin := origin(resourceURL)
	trusted[resourceOrigin] = struct{}{}

	metadata, err := resolver.discoverProtected(ctx, input, resourceURL, trusted)
	if err != nil {
		return Graph{}, err
	}
	issuer, err := selectIssuer(metadata.authorizationServers, input.DesiredIssuer, resourceOrigin)
	if err != nil {
		return Graph{}, err
	}
	issuerURL, err := parseIdentifier(issuer, false)
	if err != nil {
		return Graph{}, ErrTrustRejected
	}
	authorization, err := resolver.discoverAuthorization(ctx, issuerURL, trusted)
	if err != nil {
		return Graph{}, err
	}
	if authorization.issuer != issuer || !contains(authorization.responseTypes, "code") || !contains(authorization.grantTypes, "authorization_code") || !contains(authorization.codeChallengeMethods, "S256") {
		return Graph{}, ErrTrustRejected
	}
	for _, endpoint := range []string{authorization.authorizationEndpoint, authorization.tokenEndpoint} {
		if _, endpointErr := parseIdentifier(endpoint, true); endpointErr != nil {
			return Graph{}, ErrTrustRejected
		}
	}
	for _, endpoint := range []string{authorization.registrationEndpoint, authorization.revocationEndpoint} {
		if endpoint != "" {
			if _, endpointErr := parseIdentifier(endpoint, true); endpointErr != nil {
				return Graph{}, ErrTrustRejected
			}
		}
	}
	return Graph{
		Resource: input.Resource, Issuer: issuer,
		AuthorizationEndpoint: authorization.authorizationEndpoint, TokenEndpoint: authorization.tokenEndpoint,
		RegistrationEndpoint: authorization.registrationEndpoint, RevocationEndpoint: authorization.revocationEndpoint,
		ProtectedScopesSupported:                 append([]string(nil), metadata.scopesSupported...),
		ScopesSupported:                          append([]string(nil), authorization.scopesSupported...),
		TokenEndpointAuthMethodsSupported:        append([]string(nil), authorization.tokenEndpointAuthMethods...),
		RevocationEndpointAuthMethodsSupported:   append([]string(nil), authorization.revocationEndpointAuthMethods...),
		AuthorizationResponseIssuerParameterUsed: authorization.authorizationResponseIssuerParameterUsed,
		trustedOrigins:                           trusted,
	}, nil
}

func (resolver *Resolver) discoverProtected(ctx context.Context, input Input, resourceURL *url.URL, trusted map[string]struct{}) (protectedMetadata, error) {
	if len(input.ChallengeMetadata) != 0 {
		if len(input.ChallengeMetadata) != 1 {
			return protectedMetadata{}, ErrTrustRejected
		}
		metadataURL, err := parseIdentifier(input.ChallengeMetadata[0], true)
		if err != nil {
			return protectedMetadata{}, ErrTrustRejected
		}
		return resolver.fetchProtected(ctx, metadataURL.String(), input.Resource, isTrusted(metadataURL, trusted), false)
	}
	endpointURL, rootURL := protectedMetadataURLs(resourceURL)
	if endpointURL != rootURL {
		metadata, err := resolver.fetchProtected(ctx, endpointURL, input.Resource, true, true)
		if err == nil {
			return metadata, nil
		}
		if !errors.Is(err, errNotFound) {
			return protectedMetadata{}, err
		}
	}
	return resolver.fetchProtected(ctx, rootURL, origin(resourceURL), true, false)
}

var errNotFound = errors.New("OAuth metadata not found")

func (resolver *Resolver) fetchProtected(ctx context.Context, rawURL, expectedResource string, trusted, allowNotFound bool) (protectedMetadata, error) {
	status, header, body, err := resolver.fetch.Fetch(ctx, rawURL, trusted)
	if err != nil {
		return protectedMetadata{}, ErrTrustRejected
	}
	if status == http.StatusNotFound && allowNotFound {
		return protectedMetadata{}, errNotFound
	}
	if status != http.StatusOK || !jsonContentType(header) {
		return protectedMetadata{}, ErrTrustRejected
	}
	metadata, err := parseProtected(body)
	if err != nil || metadata.resource != expectedResource {
		return protectedMetadata{}, ErrTrustRejected
	}
	return metadata, nil
}

func (resolver *Resolver) discoverAuthorization(ctx context.Context, issuer *url.URL, trusted map[string]struct{}) (authorizationMetadata, error) {
	urls := authorizationMetadataURLs(issuer)
	for index, rawURL := range urls {
		status, header, body, err := resolver.fetch.Fetch(ctx, rawURL, isTrusted(issuer, trusted))
		if err != nil {
			return authorizationMetadata{}, ErrTrustRejected
		}
		if status == http.StatusNotFound && index+1 < len(urls) {
			continue
		}
		if status != http.StatusOK || !jsonContentType(header) {
			return authorizationMetadata{}, ErrTrustRejected
		}
		metadata, parseErr := parseAuthorization(body)
		if parseErr != nil {
			return authorizationMetadata{}, ErrTrustRejected
		}
		return metadata, nil
	}
	return authorizationMetadata{}, ErrTrustRejected
}

func CanonicalOrigin(raw string) (string, error) {
	canonical, err := remote.ParseOrigin(raw)
	if err != nil {
		return "", ErrTrustRejected
	}
	return canonical, nil
}

func trustedOriginSet(values []string) (map[string]struct{}, error) {
	if len(values) > 64 {
		return nil, ErrTrustRejected
	}
	result := make(map[string]struct{}, len(values)+1)
	for _, value := range values {
		canonical, err := CanonicalOrigin(value)
		if err != nil {
			return nil, err
		}
		if _, exists := result[canonical]; exists {
			return nil, ErrTrustRejected
		}
		result[canonical] = struct{}{}
	}
	return result, nil
}

func parseIdentifier(raw string, allowQuery bool) (*url.URL, error) {
	if raw == "" || !utf8.ValidString(raw) || int64(len(raw)) > limit("oauth_url_bytes") {
		return nil, ErrTrustRejected
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Opaque != "" || parsed.User != nil || parsed.Fragment != "" || (!allowQuery && parsed.RawQuery != "") || parsed.String() != raw || strings.ToLower(parsed.Hostname()) != parsed.Hostname() {
		return nil, ErrTrustRejected
	}
	if parsed.Port() == "443" {
		return nil, ErrTrustRejected
	}
	if parsed.Port() != "" {
		port, portErr := strconv.ParseUint(parsed.Port(), 10, 16)
		if portErr != nil || port == 0 {
			return nil, ErrTrustRejected
		}
	}
	if parsed.RawQuery != "" && int64(len(parsed.RawQuery)) > limit("oauth_query_bytes") {
		return nil, ErrTrustRejected
	}
	if parsed.Path != "" && path.Clean(parsed.Path) != parsed.Path {
		return nil, ErrTrustRejected
	}
	validationURL := *parsed
	if validationURL.Path == "" {
		validationURL.Path = "/"
	}
	if _, err := remote.Parse(validationURL.String(), remote.Policy{AllowQuery: allowQuery}); err != nil {
		return nil, ErrTrustRejected
	}
	return parsed, nil
}

func protectedMetadataURLs(resource *url.URL) (string, string) {
	base := origin(resource) + "/.well-known/oauth-protected-resource"
	root := base
	if escaped := resource.EscapedPath(); escaped != "" && escaped != "/" {
		return base + escaped, root
	}
	return root, root
}

func authorizationMetadataURL(issuer string) string {
	parsed, err := parseIdentifier(issuer, false)
	if err != nil {
		return ""
	}
	return authorizationMetadataURLs(parsed)[0]
}

func authorizationMetadataURLs(issuer *url.URL) []string {
	issuerPath := issuer.EscapedPath()
	base := origin(issuer)
	rfc := base + "/.well-known/oauth-authorization-server"
	openid := base
	if issuerPath == "" || issuerPath == "/" {
		openid += "/.well-known/openid-configuration"
	} else {
		rfc += issuerPath
		openid += issuerPath + "/.well-known/openid-configuration"
	}
	return []string{rfc, openid}
}

func origin(parsed *url.URL) string {
	return "https://" + parsed.Host
}

func isTrusted(parsed *url.URL, trusted map[string]struct{}) bool {
	_, ok := trusted[origin(parsed)]
	return ok
}

func selectIssuer(values []string, desired *string, resourceOrigin string) (string, error) {
	if len(values) == 0 {
		return "", ErrTrustRejected
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		parsed, err := parseIdentifier(value, false)
		if err != nil {
			return "", ErrTrustRejected
		}
		if _, duplicate := seen[value]; duplicate {
			return "", ErrTrustRejected
		}
		seen[value] = struct{}{}
		_ = parsed
	}
	if desired != nil {
		if _, err := parseIdentifier(*desired, false); err != nil {
			return "", ErrTrustRejected
		}
		if _, advertised := seen[*desired]; !advertised {
			return "", ErrTrustRejected
		}
		return *desired, nil
	}
	if len(values) != 1 {
		return "", ErrTrustRejected
	}
	issuer, _ := parseIdentifier(values[0], false)
	if origin(issuer) != resourceOrigin {
		return "", ErrTrustRejected
	}
	return values[0], nil
}

func parseProtected(body []byte) (protectedMetadata, error) {
	object, err := decodeObject(body)
	if err != nil {
		return protectedMetadata{}, err
	}
	resource, err := requiredString(object, "resource")
	if err != nil {
		return protectedMetadata{}, err
	}
	servers, err := requiredStrings(object, "authorization_servers")
	if err != nil || len(servers) == 0 || len(servers) > 64 {
		return protectedMetadata{}, ErrTrustRejected
	}
	bearer, present, err := optionalStrings(object, "bearer_methods_supported")
	if err != nil || present && !contains(bearer, "header") {
		return protectedMetadata{}, ErrTrustRejected
	}
	scopes, _, err := optionalStrings(object, "scopes_supported")
	if err != nil || !validScopes(scopes) {
		return protectedMetadata{}, ErrTrustRejected
	}
	return protectedMetadata{resource: resource, authorizationServers: servers, scopesSupported: scopes}, nil
}

func parseAuthorization(body []byte) (authorizationMetadata, error) {
	object, err := decodeObject(body)
	if err != nil {
		return authorizationMetadata{}, err
	}
	issuer, err := requiredString(object, "issuer")
	if err != nil {
		return authorizationMetadata{}, err
	}
	authorizationEndpoint, err := requiredString(object, "authorization_endpoint")
	if err != nil {
		return authorizationMetadata{}, err
	}
	tokenEndpoint, err := requiredString(object, "token_endpoint")
	if err != nil {
		return authorizationMetadata{}, err
	}
	responseTypes, err := requiredStrings(object, "response_types_supported")
	if err != nil {
		return authorizationMetadata{}, err
	}
	grantTypes, err := requiredStrings(object, "grant_types_supported")
	if err != nil {
		return authorizationMetadata{}, err
	}
	challengeMethods, err := requiredStrings(object, "code_challenge_methods_supported")
	if err != nil {
		return authorizationMetadata{}, err
	}
	registrationEndpoint, registrationPresent, err := optionalString(object, "registration_endpoint")
	if err != nil || registrationPresent && registrationEndpoint == "" {
		return authorizationMetadata{}, ErrTrustRejected
	}
	revocationEndpoint, revocationPresent, err := optionalString(object, "revocation_endpoint")
	if err != nil || revocationPresent && revocationEndpoint == "" {
		return authorizationMetadata{}, ErrTrustRejected
	}
	scopes, _, err := optionalStrings(object, "scopes_supported")
	if err != nil || !validScopes(scopes) {
		return authorizationMetadata{}, ErrTrustRejected
	}
	tokenMethods, _, err := optionalStrings(object, "token_endpoint_auth_methods_supported")
	if err != nil {
		return authorizationMetadata{}, err
	}
	revocationMethods, _, err := optionalStrings(object, "revocation_endpoint_auth_methods_supported")
	if err != nil {
		return authorizationMetadata{}, err
	}
	issuerParameter, _, err := optionalBool(object, "authorization_response_iss_parameter_supported")
	if err != nil {
		return authorizationMetadata{}, err
	}
	return authorizationMetadata{issuer: issuer, authorizationEndpoint: authorizationEndpoint, tokenEndpoint: tokenEndpoint, registrationEndpoint: registrationEndpoint, revocationEndpoint: revocationEndpoint, responseTypes: responseTypes, grantTypes: grantTypes, codeChallengeMethods: challengeMethods, scopesSupported: scopes, tokenEndpointAuthMethods: tokenMethods, revocationEndpointAuthMethods: revocationMethods, authorizationResponseIssuerParameterUsed: issuerParameter}, nil
}

func decodeObject(body []byte) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := strictjson.Decode(body, &object, strictjson.Options{MaxBytes: limit("oauth_metadata_body_bytes"), MaxDepth: int(limit("oauth_json_depth"))}); err != nil || object == nil {
		return nil, ErrTrustRejected
	}
	return object, nil
}

func requiredString(object map[string]json.RawMessage, name string) (string, error) {
	value, present, err := optionalString(object, name)
	if err != nil || !present || value == "" {
		return "", ErrTrustRejected
	}
	return value, nil
}

func optionalString(object map[string]json.RawMessage, name string) (string, bool, error) {
	raw, present := object[name]
	if !present {
		return "", false, nil
	}
	var value string
	if string(raw) == "null" || json.Unmarshal(raw, &value) != nil || !utf8.ValidString(value) {
		return "", true, ErrTrustRejected
	}
	return value, true, nil
}

func requiredStrings(object map[string]json.RawMessage, name string) ([]string, error) {
	values, present, err := optionalStrings(object, name)
	if err != nil || !present {
		return nil, ErrTrustRejected
	}
	return values, nil
}

func optionalStrings(object map[string]json.RawMessage, name string) ([]string, bool, error) {
	raw, present := object[name]
	if !present {
		return nil, false, nil
	}
	var values []string
	if string(raw) == "null" || json.Unmarshal(raw, &values) != nil {
		return nil, true, ErrTrustRejected
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" || !utf8.ValidString(value) {
			return nil, true, ErrTrustRejected
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, true, ErrTrustRejected
		}
		seen[value] = struct{}{}
	}
	return values, true, nil
}

func optionalBool(object map[string]json.RawMessage, name string) (bool, bool, error) {
	raw, present := object[name]
	if !present {
		return false, false, nil
	}
	var value bool
	if string(raw) == "null" || json.Unmarshal(raw, &value) != nil {
		return false, true, ErrTrustRejected
	}
	return value, true, nil
}

func validScopes(values []string) bool {
	if len(values) > int(limit("oauth_scope_count")) {
		return false
	}
	serialized := 0
	for _, value := range values {
		if len(value) > int(limit("oauth_scope_token_bytes")) {
			return false
		}
		serialized += len(value)
		for _, character := range []byte(value) {
			if character != 0x21 && (character < 0x23 || character > 0x5b) && (character < 0x5d || character > 0x7e) {
				return false
			}
		}
	}
	return int64(serialized) <= limit("oauth_scope_bytes")
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func jsonContentType(header http.Header) bool {
	mediaType, _, err := mime.ParseMediaType(header.Get("Content-Type"))
	return err == nil && mediaType == contract.MediaTypeJSON
}

func limit(name string) int64 {
	value, ok := contract.FixedLimitByName(name)
	if !ok {
		panic("missing OAuth limit " + name)
	}
	return value.Maximum
}
