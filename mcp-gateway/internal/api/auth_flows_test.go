package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/httpboundary"
	serverdomain "github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeAuthFlows struct {
	creation  contract.AuthFlowCreation
	items     []contract.ServerAuthFlow
	created   bool
	cancelled bool
	err       error
}

func (flows *fakeAuthFlows) CreateInitial(context.Context, string, string) (contract.AuthFlowCreation, error) {
	flows.created = true
	return flows.creation, flows.err
}
func (flows *fakeAuthFlows) Get(context.Context, string, string) (contract.ServerAuthFlow, error) {
	if len(flows.items) == 0 {
		return contract.ServerAuthFlow{}, serverdomain.ErrNotFound
	}
	return flows.items[0], flows.err
}
func (flows *fakeAuthFlows) List(context.Context, string, *serverdomain.SnapshotCursor, int) ([]contract.ServerAuthFlow, *serverdomain.SnapshotCursor, error) {
	return append([]contract.ServerAuthFlow(nil), flows.items...), nil, flows.err
}
func (flows *fakeAuthFlows) Cancel(context.Context, string, string) (contract.ServerAuthFlow, error) {
	flows.cancelled = true
	if len(flows.items) == 0 {
		return contract.ServerAuthFlow{}, serverdomain.ErrNotFound
	}
	return flows.items[0], flows.err
}
func (flows *fakeAuthFlows) FenceServer(string) {}

func TestAuthFlowAPIExactCreateReadListDeleteAndOneTimeURL(t *testing.T) {
	flow := contract.ServerAuthFlow{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAA", ServerID: testID, FlowState: contract.AuthFlowAwaitingCallback, TargetDesiredRevision: "1", RegistrationRevision: "1", CreatedAt: "2026-08-22T14:00:00Z", ExpiresAt: "2026-08-22T14:05:00Z"}
	flows := &fakeAuthFlows{creation: contract.AuthFlowCreation{Flow: flow, AuthorizationURL: "https://issuer.example/authorize?state=one-time-secret"}, items: []contract.ServerAuthFlow{flow}}
	handler := newAuthFlowHandler(t, flows)
	headers := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "If-Match": contract.ServerETag(testID, "1")}
	created := perform(handler, http.MethodPost, "/api/v1/servers/"+testID+"/auth-flows", `{}`, headers)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	assert.Contains(t, created.Body.String(), `"authorization_url":"https://issuer.example/authorize?state=one-time-secret"`)
	assert.True(t, flows.created)

	listed := perform(handler, http.MethodGet, "/api/v1/servers/"+testID+"/auth-flows", "", map[string]string{"Authorization": "Bearer " + testBearer})
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	assert.NotContains(t, listed.Body.String(), "one-time-secret")
	assert.Contains(t, listed.Body.String(), `"next_cursor":null`)
	got := perform(handler, http.MethodGet, "/api/v1/servers/"+testID+"/auth-flows/"+flow.ID, "", map[string]string{"Authorization": "Bearer " + testBearer})
	require.Equal(t, http.StatusOK, got.Code, got.Body.String())
	assert.NotContains(t, got.Body.String(), "authorization_url")

	deleted := perform(handler, http.MethodDelete, "/api/v1/servers/"+testID+"/auth-flows/"+flow.ID, `{}`, map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON})
	require.Equal(t, http.StatusNoContent, deleted.Code, deleted.Body.String())
	assert.Empty(t, deleted.Body.String())
	assert.True(t, flows.cancelled)
}

func TestAuthFlowAPIStrictBodiesQueriesPreconditionsAndSafeErrors(t *testing.T) {
	flow := contract.ServerAuthFlow{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAA", ServerID: testID}
	tests := []struct {
		name    string
		method  string
		path    string
		body    string
		headers map[string]string
		status  int
		code    contract.ProblemCode
	}{
		{name: "precondition required", method: http.MethodPost, path: "/api/v1/servers/" + testID + "/auth-flows", body: `{}`, headers: map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON}, status: 428, code: contract.ProblemPreconditionRequired},
		{name: "unknown member", method: http.MethodPost, path: "/api/v1/servers/" + testID + "/auth-flows", body: `{"extra":true}`, headers: map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "If-Match": contract.ServerETag(testID, "1")}, status: 400, code: contract.ProblemInvalidJSON},
		{name: "list query", method: http.MethodGet, path: "/api/v1/servers/" + testID + "/auth-flows?extra=x", headers: map[string]string{"Authorization": "Bearer " + testBearer}, status: 400, code: contract.ProblemMalformedRequest},
		{name: "member query", method: http.MethodGet, path: "/api/v1/servers/" + testID + "/auth-flows/" + flow.ID + "?x=1", headers: map[string]string{"Authorization": "Bearer " + testBearer}, status: 400, code: contract.ProblemMalformedRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			flows := &fakeAuthFlows{creation: contract.AuthFlowCreation{Flow: flow}, items: []contract.ServerAuthFlow{flow}}
			response := perform(newAuthFlowHandler(t, flows), test.method, test.path, test.body, test.headers)
			require.Equal(t, test.status, response.Code, response.Body.String())
			assert.Contains(t, response.Body.String(), `"code":"`+string(test.code)+`"`)
		})
	}

	flows := &fakeAuthFlows{err: serverdomain.ErrOAuthFlowActive}
	response := perform(newAuthFlowHandler(t, flows), http.MethodPost, "/api/v1/servers/"+testID+"/auth-flows", `{}`, map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "If-Match": contract.ServerETag(testID, "1")})
	require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), `"code":"oauth_flow_active"`)
}

func newAuthFlowHandler(t *testing.T, flows AuthFlowService) http.Handler {
	t.Helper()
	service := new(fakeServerService)
	service.server = storedServer(serverdomain.Definition{Namespace: "alpha", DisplayName: "Alpha", Transport: contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: "https://resource.example/mcp", ProtocolMode: contract.ProtocolModern, Authentication: contract.OAuthAuthentication{Mode: contract.AuthenticationOAuth, Registration: contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic}, TrustedOrigins: []string{}}}})
	handler := New(Options{Credentials: &fakeCredentials{items: []contract.AdminCredential{credential()}}, Sessions: fakeSessions{}, Servers: service, AuthFlows: flows})
	boundary, err := httpboundary.New(httpboundary.Options{Authority: contract.DefaultAuthority, Authenticate: handler.Authenticate, Next: handler})
	require.NoError(t, err)
	return boundary
}
