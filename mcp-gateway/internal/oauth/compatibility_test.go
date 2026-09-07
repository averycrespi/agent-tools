package oauth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetadataOverrideKeepsIssuerAndLocationAuthoritySeparate(t *testing.T) {
	const resource = "https://resource.example/mcp"
	const issuer = "https://resource.example/issuer"
	const location = "https://metadata.example/custom/config?tenant=fixture"
	for _, trusted := range []bool{false, true} {
		fetch := &scriptedFetch{responses: map[string]fetchResponse{
			"https://resource.example/.well-known/oauth-protected-resource/mcp": {status: 200, body: protected(resource, issuer)},
			location: {status: 200, body: authorization(issuer)},
		}}
		input := Input{Resource: resource, AuthServerMetadataURL: stringPointer(location)}
		if trusted {
			input.TrustedOrigins = []string{"https://metadata.example"}
		}
		graph, err := newResolver(fetch).Discover(context.Background(), input)
		require.NoError(t, err)
		assert.Equal(t, issuer, graph.Issuer)
		assert.Equal(t, resource, graph.Resource)
		assert.Equal(t, []fetchRequest{{url: "https://resource.example/.well-known/oauth-protected-resource/mcp", trusted: true}, {url: location, trusted: trusted}}, fetch.requests)
	}
	for _, response := range []fetchResponse{
		{status: 404}, {status: 302}, {status: 200, body: authorization("https://wrong.example")},
		{status: 200, body: `{ "issuer": 1 }`}, {status: 200, contentType: "text/plain", body: authorization(issuer)},
	} {
		fetch := &scriptedFetch{responses: map[string]fetchResponse{
			"https://resource.example/.well-known/oauth-protected-resource/mcp": {status: 200, body: protected(resource, issuer)},
			location:                         response,
			authorizationMetadataURL(issuer): {status: 200, body: authorization(issuer)},
		}}
		_, err := newResolver(fetch).Discover(context.Background(), Input{Resource: resource, AuthServerMetadataURL: stringPointer(location)})
		require.Error(t, err)
		require.Len(t, fetch.requests, 2)
		assert.Equal(t, location, fetch.requests[1].url)
	}
}

func TestRefreshRediscoversExactCompatibilityMetadata(t *testing.T) {
	const location = "https://metadata.example/custom/config?revision=2"
	prepared := refreshPrepared(contract.TokenEndpointAuthNone)
	prepared.Registration.CallbackURL = "http://localhost:3118/callback"
	prepared.Configuration.Authentication.AuthServerMetadataURL = stringPointer(location)
	old := refreshGeneration(contract.TokenEndpointAuthNone)
	old.Scopes = []string{"fixture.read", "fixture.write"}
	prepared.Configuration.Authentication.Scopes = &old.Scopes
	operation := &refreshOperationFake{token: mustTokenBytes(t, old)}
	fetch := &scriptedFetch{responses: map[string]fetchResponse{
		"https://resource.example/.well-known/oauth-protected-resource/mcp": {status: 200, body: protected(old.Resource, old.Issuer)},
		location: {status: 200, body: authorization(old.Issuer)},
	}}
	requester := &refreshRequesterFake{status: 200, header: http.Header{"Content-Type": {contract.MediaTypeJSON}}, body: []byte(`{"access_token":"fixture-refreshed","token_type":"Bearer","refresh_token":"fixture-refresh-next","scope":"fixture.read"}`)}
	service := newRefreshService(refreshStoreFake{prepared: prepared}, &refreshCoordinatorFake{operation: operation}, newResolver(fetch), requester, refreshServerID, func() time.Time { return refreshNow }, nil, nil)
	result, err := service.Refresh(context.Background(), RefreshRequest{ServerID: refreshServerID})
	require.NoError(t, err)
	assert.True(t, result.Refreshed)
	assert.Equal(t, []fetchRequest{{url: "https://resource.example/.well-known/oauth-protected-resource/mcp", trusted: true}, {url: location, trusted: false}}, fetch.requests)
	installed, err := DecodeTokenGeneration(operation.installed)
	require.NoError(t, err)
	assert.Equal(t, old.Issuer, installed.Issuer)
	assert.Equal(t, old.Resource, installed.Resource)
	assert.Equal(t, []string{"fixture.read"}, installed.Scopes)
	assert.Equal(t, "fixture-refresh-next", *installed.RefreshToken)
}

func TestExplicitInitialScopeSemantics(t *testing.T) {
	graph := Graph{ProtectedScopesSupported: []string{"metadata-default"}, ScopesSupported: []string{"offline_access"}}
	empty := []string{}
	configured := []string{"write", "read", "write"}
	for _, test := range []struct {
		name       string
		configured *[]string
		offline    bool
		challenge  []string
		prior      []string
		expected   []string
	}{
		{name: "omitted", expected: []string{"metadata-default"}},
		{name: "empty", configured: &empty, expected: []string{}},
		{name: "explicit", configured: &configured, expected: []string{"read", "write"}},
		{name: "explicit offline", configured: &configured, offline: true, expected: []string{"offline_access", "read", "write"}},
		{name: "empty offline", configured: &empty, offline: true, expected: []string{"offline_access"}},
		{name: "foreground union", configured: &configured, challenge: []string{"admin"}, expected: []string{"admin", "read", "write"}},
		{name: "narrowed prior union", configured: &configured, prior: []string{"read"}, challenge: []string{"admin"}, expected: []string{"admin", "read", "write"}},
		{name: "prior preserves configured baseline", configured: &configured, prior: []string{"read"}, expected: []string{"read", "write"}},
		{name: "omitted baseline preserves prior behavior", prior: []string{"read"}, challenge: []string{"admin"}, expected: []string{"admin", "read"}},
		{name: "empty baseline with prior", configured: &empty, prior: []string{"read"}, challenge: []string{"admin"}, expected: []string{"admin", "read"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			scopes, err := requestedScopes(FlowRequest{InitialScopes: test.configured, PriorRequestedScopes: test.prior, ChallengeScopes: test.challenge}, test.offline, graph)
			require.NoError(t, err)
			assert.Equal(t, test.expected, scopes)
		})
	}
	now := time.Now().UTC()
	header := http.Header{"Content-Type": {contract.MediaTypeJSON}}
	_, err := parseTokenResponse(200, header, []byte(`{"access_token":"fixture-token","token_type":"Bearer","scope":"unexpected"}`), empty, now)
	require.ErrorIs(t, err, ErrTokenRejected)
	token, err := parseTokenResponse(200, header, []byte(`{"access_token":"fixture-token","token_type":"Bearer"}`), empty, now)
	require.NoError(t, err)
	encoded, err := encodeBoundTokenGeneration(TokenGeneration{ServerID: "fixture-server", Issuer: "https://issuer.example", Resource: "https://resource.example/mcp", RegistrationRevision: "1"}, token, now)
	require.NoError(t, err)
	decoded, err := DecodeTokenGeneration(encoded)
	require.NoError(t, err)
	assert.True(t, decoded.ScopeSpecified)
	assert.NotNil(t, decoded.Scopes)
	assert.Empty(t, decoded.Scopes)
}
