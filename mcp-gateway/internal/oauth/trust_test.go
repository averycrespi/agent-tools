package oauth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type scriptedFetch struct {
	responses map[string]fetchResponse
	requests  []fetchRequest
}

type fetchRequest struct {
	url     string
	trusted bool
}

type fetchResponse struct {
	status      int
	contentType string
	body        string
	err         error
}

func (fetch *scriptedFetch) Fetch(_ context.Context, rawURL string, trusted bool) (int, http.Header, []byte, error) {
	fetch.requests = append(fetch.requests, fetchRequest{url: rawURL, trusted: trusted})
	response, ok := fetch.responses[rawURL]
	if !ok {
		return 0, nil, nil, errors.New("unexpected URL")
	}
	contentType := response.contentType
	if contentType == "" {
		contentType = "application/json"
	}
	return response.status, http.Header{"Content-Type": []string{contentType}}, []byte(response.body), response.err
}

func TestResolverProductionConstructorRequiresHardenedRemoteFactory(t *testing.T) {
	_, err := NewResolver(nil)
	assert.ErrorIs(t, err, ErrTrustRejected)
	resolver, err := NewResolver(remote.New(remote.Options{}))
	require.NoError(t, err)
	assert.NotNil(t, resolver)
}

func TestDiscoverUsesExactChallengeMetadataWithoutFallback(t *testing.T) {
	fetch := &scriptedFetch{responses: map[string]fetchResponse{
		"https://metadata.example/protected?tenant=one":                        {status: 200, body: protected(`https://resource.example/mcp`, `https://issuer.example/tenant`)},
		"https://issuer.example/.well-known/oauth-authorization-server/tenant": {status: 200, body: authorization(`https://issuer.example/tenant`)},
	}}
	resolver := newResolver(fetch)
	desired := "https://issuer.example/tenant"
	graph, err := resolver.Discover(context.Background(), Input{
		Resource:          "https://resource.example/mcp",
		ChallengeMetadata: []string{"https://metadata.example/protected?tenant=one"},
		DesiredIssuer:     &desired,
		TrustedOrigins:    []string{"https://metadata.example"},
	})
	require.NoError(t, err)
	assert.Equal(t, "https://resource.example/mcp", graph.Resource)
	assert.Equal(t, desired, graph.Issuer)
	assert.Equal(t, "https://issuer.example/authorize", graph.AuthorizationEndpoint)
	assert.Equal(t, "https://issuer.example/token", graph.TokenEndpoint)
	assert.True(t, graph.AllowsRestrictedEndpoint("https://resource.example/other"))
	assert.True(t, graph.AllowsRestrictedEndpoint("https://metadata.example/other"))
	assert.False(t, graph.AllowsRestrictedEndpoint(graph.TokenEndpoint))
	assert.False(t, graph.AllowsRestrictedEndpoint(graph.RegistrationEndpoint))
	assert.Equal(t, []fetchRequest{
		{url: "https://metadata.example/protected?tenant=one", trusted: true},
		{url: "https://issuer.example/.well-known/oauth-authorization-server/tenant", trusted: false},
	}, fetch.requests)
}

func TestDiscoverFallsBackFromEndpointSpecific404ToRootMetadata(t *testing.T) {
	fetch := &scriptedFetch{responses: map[string]fetchResponse{
		"https://resource.example/.well-known/oauth-protected-resource/mcp":      {status: 404},
		"https://resource.example/.well-known/oauth-protected-resource":          {status: 200, body: protected(`https://resource.example`, `https://resource.example/issuer`)},
		"https://resource.example/.well-known/oauth-authorization-server/issuer": {status: 404},
		"https://resource.example/issuer/.well-known/openid-configuration":       {status: 200, body: authorization(`https://resource.example/issuer`)},
	}}
	graph, err := newResolver(fetch).Discover(context.Background(), Input{Resource: "https://resource.example/mcp"})
	require.NoError(t, err)
	assert.Equal(t, "https://resource.example/issuer", graph.Issuer)
	assert.Equal(t, []fetchRequest{
		{url: "https://resource.example/.well-known/oauth-protected-resource/mcp", trusted: true},
		{url: "https://resource.example/.well-known/oauth-protected-resource", trusted: true},
		{url: "https://resource.example/.well-known/oauth-authorization-server/issuer", trusted: true},
		{url: "https://resource.example/issuer/.well-known/openid-configuration", trusted: true},
	}, fetch.requests)
}

func TestDiscoverRejectsInvalidChallengeAndNeverFallsBack(t *testing.T) {
	tests := [][]string{{"http://metadata.example/value"}, {"https://one.example", "https://two.example"}, {"https://metadata.example/#fragment"}}
	for _, challenge := range tests {
		fetch := &scriptedFetch{responses: map[string]fetchResponse{}}
		_, err := newResolver(fetch).Discover(context.Background(), Input{Resource: "https://resource.example/mcp", ChallengeMetadata: challenge})
		assert.ErrorIs(t, err, ErrTrustRejected)
		assert.Empty(t, fetch.requests)
	}
}

func TestDiscoverEnforcesIssuerSelection(t *testing.T) {
	tests := []struct {
		name      string
		issuers   string
		desired   *string
		wantError bool
	}{
		{name: "sole same origin", issuers: `"https://resource.example/issuer"`},
		{name: "sole cross origin needs pin", issuers: `"https://issuer.example"`, wantError: true},
		{name: "multiple need pin", issuers: `"https://resource.example/one","https://resource.example/two"`, wantError: true},
		{name: "exact pin", issuers: `"https://issuer.example"`, desired: stringPointer("https://issuer.example")},
		{name: "mismatched pin", issuers: `"https://issuer.example"`, desired: stringPointer("https://other.example"), wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metadataURL := "https://resource.example/.well-known/oauth-protected-resource/mcp"
			fetch := &scriptedFetch{responses: map[string]fetchResponse{metadataURL: {status: 200, body: `{"resource":"https://resource.example/mcp","authorization_servers":[` + test.issuers + `]}`}}}
			if !test.wantError {
				issuer := "https://resource.example/issuer"
				if test.desired != nil {
					issuer = *test.desired
				}
				fetch.responses[authorizationMetadataURL(issuer)] = fetchResponse{status: 200, body: authorization(issuer)}
			}
			_, err := newResolver(fetch).Discover(context.Background(), Input{Resource: "https://resource.example/mcp", DesiredIssuer: test.desired})
			if test.wantError {
				assert.ErrorIs(t, err, ErrTrustRejected)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestDiscoverRejectsMalformedOrMismatchedMetadata(t *testing.T) {
	tests := []string{
		`{"resource":null,"authorization_servers":["https://resource.example/issuer"]}`,
		`{"resource":"https://wrong.example/mcp","authorization_servers":["https://resource.example/issuer"]}`,
		`{"resource":"https://resource.example/mcp","authorization_servers":null}`,
		`{"resource":"https://resource.example/mcp","authorization_servers":[]}`,
		`{"resource":"https://resource.example/mcp","authorization_servers":["https://resource.example/issuer"],"bearer_methods_supported":["body"]}`,
		`{"resource":"https://resource.example/mcp","resource":"https://resource.example/mcp","authorization_servers":["https://resource.example/issuer"]}`,
	}
	for _, body := range tests {
		fetch := &scriptedFetch{responses: map[string]fetchResponse{"https://resource.example/.well-known/oauth-protected-resource/mcp": {status: 200, body: body}}}
		_, err := newResolver(fetch).Discover(context.Background(), Input{Resource: "https://resource.example/mcp"})
		assert.ErrorIs(t, err, ErrTrustRejected)
	}
}

func TestDiscoverValidatesAuthorizationMetadataAndDelegatedEndpoints(t *testing.T) {
	issuer := "https://issuer.example/tenant"
	desired := issuer
	protectedURL := "https://resource.example/.well-known/oauth-protected-resource/mcp"
	authorizationURL := authorizationMetadataURL(issuer)
	tests := []string{
		`{"issuer":"https://wrong.example","authorization_endpoint":"https://issuer.example/authorize","token_endpoint":"https://issuer.example/token","response_types_supported":["code"],"grant_types_supported":["authorization_code"],"code_challenge_methods_supported":["S256"]}`,
		`{"issuer":"https://issuer.example/tenant","authorization_endpoint":null,"token_endpoint":"https://issuer.example/token","response_types_supported":["code"],"grant_types_supported":["authorization_code"],"code_challenge_methods_supported":["S256"]}`,
		`{"issuer":"https://issuer.example/tenant","authorization_endpoint":"http://issuer.example/authorize","token_endpoint":"https://issuer.example/token","response_types_supported":["code"],"grant_types_supported":["authorization_code"],"code_challenge_methods_supported":["S256"]}`,
		`{"issuer":"https://issuer.example/tenant","authorization_endpoint":"https://issuer.example/authorize","token_endpoint":"https://issuer.example/token","response_types_supported":["token"],"grant_types_supported":["authorization_code"],"code_challenge_methods_supported":["S256"]}`,
		`{"issuer":"https://issuer.example/tenant","authorization_endpoint":"https://issuer.example/authorize","token_endpoint":"https://issuer.example/token","response_types_supported":["code"],"grant_types_supported":["implicit"],"code_challenge_methods_supported":["S256"]}`,
		`{"issuer":"https://issuer.example/tenant","authorization_endpoint":"https://issuer.example/authorize","token_endpoint":"https://issuer.example/token","response_types_supported":["code"],"grant_types_supported":["authorization_code"],"code_challenge_methods_supported":["plain"]}`,
	}
	for _, body := range tests {
		fetch := &scriptedFetch{responses: map[string]fetchResponse{
			protectedURL:     {status: 200, body: protected(`https://resource.example/mcp`, issuer)},
			authorizationURL: {status: 200, body: body},
		}}
		_, err := newResolver(fetch).Discover(context.Background(), Input{Resource: "https://resource.example/mcp", DesiredIssuer: &desired})
		assert.ErrorIs(t, err, ErrTrustRejected)
	}
}

func TestMetadataBoundsMediaTypeAndFallbackPolicy(t *testing.T) {
	endpoint := "https://resource.example/.well-known/oauth-protected-resource/mcp"
	root := "https://resource.example/.well-known/oauth-protected-resource"
	tests := []fetchResponse{
		{status: 500},
		{status: 200, contentType: "text/plain", body: protected(`https://resource.example/mcp`, `https://resource.example/issuer`)},
		{status: 200, body: strings.Repeat(" ", int(limit("oauth_metadata_body_bytes"))+1)},
		{status: 200, body: strings.Repeat(`{"x":`, int(limit("oauth_json_depth"))+1) + `null` + strings.Repeat(`}`, int(limit("oauth_json_depth"))+1)},
	}
	for _, response := range tests {
		fetch := &scriptedFetch{responses: map[string]fetchResponse{endpoint: response, root: {status: 200, body: protected(`https://resource.example`, `https://resource.example/issuer`)}}}
		_, err := newResolver(fetch).Discover(context.Background(), Input{Resource: "https://resource.example/mcp"})
		assert.ErrorIs(t, err, ErrTrustRejected)
		assert.Len(t, fetch.requests, 1, "only 404 may fall back to root metadata")
	}
	_, err := parseIdentifier("https://metadata.example/value?"+strings.Repeat("q", int(limit("oauth_query_bytes"))+1), true)
	assert.ErrorIs(t, err, ErrTrustRejected)
}

func TestCanonicalOriginAndWellKnownDerivation(t *testing.T) {
	valid := []string{"https://example.com", "https://example.com:8443"}
	for _, value := range valid {
		origin, err := CanonicalOrigin(value)
		require.NoError(t, err)
		assert.Equal(t, value, origin)
	}
	invalid := []string{"HTTPs://example.com", "https://EXAMPLE.com", "https://example.com:443", "https://example.com/", "https://example.com/path", "https://user@example.com", "https://127.0.0.1", "https://example.com?x=1"}
	for _, value := range invalid {
		_, err := CanonicalOrigin(value)
		assert.ErrorIs(t, err, ErrTrustRejected, value)
	}
	assert.Equal(t, "https://issuer.example/.well-known/oauth-authorization-server/tenant", authorizationMetadataURL("https://issuer.example/tenant"))
}

func protected(resource, issuer string) string {
	return `{"resource":"` + resource + `","authorization_servers":["` + issuer + `"],"bearer_methods_supported":["header"],"unknown_extension":{"ignored":true}}`
}

func authorization(issuer string) string {
	return `{"issuer":"` + issuer + `","authorization_endpoint":"https://issuer.example/authorize","token_endpoint":"https://issuer.example/token","registration_endpoint":"https://register.example/clients","revocation_endpoint":"https://revoke.example/token","response_types_supported":["code"],"grant_types_supported":["authorization_code","refresh_token"],"code_challenge_methods_supported":["S256"],"token_endpoint_auth_methods_supported":["client_secret_basic","client_secret_post","none"],"scopes_supported":["openid","offline_access"],"authorization_response_iss_parameter_supported":true,"unknown_extension":true}`
}

func stringPointer(value string) *string { return &value }
