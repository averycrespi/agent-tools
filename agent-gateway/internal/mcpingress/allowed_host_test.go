package mcpingress

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/discovery"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpboundary"
	"github.com/stretchr/testify/require"
)

// Recorders alone omit the listener context that activates SDK localhost protection.
func loopbackRequest(request *http.Request, host string) *http.Request {
	request.Host = host
	return request.WithContext(context.WithValue(request.Context(), http.LocalAddrContextKey,
		&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8210}))
}

func TestAllowedHostSDKTransports(t *testing.T) {
	authority := newTestAuthority(t)
	authority.add(t, "valid", contract.VisibilityRequestable)
	list := listToolsFunc(func(ctx context.Context, lease *authorization.Lease, cursor string, encode ToolsListEncoder) ([]byte, error) {
		require.True(t, lease.Current())
		require.Empty(t, cursor)
		return encode(ctx, []*discovery.Tool{{Name: "fixture.read", InputSchema: json.RawMessage(`{"type":"object"}`)}}, "")
	})
	ingress := New(Options{Authenticator: authority, ListTools: list})
	t.Cleanup(ingress.Shutdown)
	boundary, err := httpboundary.New(httpboundary.Options{
		Authority: contract.DefaultAuthority, AllowedHosts: []string{"host.docker.internal"},
		Authenticate: ingress.Authenticate,
		Next:         ingress,
	})
	require.NoError(t, err)
	for _, host := range []string{contract.DefaultAuthority, "host.docker.internal:8210"} {
		t.Run(host, func(t *testing.T) {
			t.Run("modern SDK discovery", func(t *testing.T) {
				request := loopbackRequest(modernRequest(http.MethodPost, strings.Replace(modernPing, `"ping"`, `"server/discover"`, 1)), host)
				request.Header.Set("Mcp-Protocol-Version", contract.ModernProtocolVersion)
				response := httptest.NewRecorder()
				boundary.ServeHTTP(response, request)
				require.Equal(t, http.StatusOK, response.Code, response.Body.String())
				var envelope struct {
					JSONRPC string          `json:"jsonrpc"`
					ID      int             `json:"id"`
					Error   json.RawMessage `json:"error"`
					Result  struct {
						ResultType        string                     `json:"resultType"`
						SupportedVersions []string                   `json:"supportedVersions"`
						Capabilities      map[string]json.RawMessage `json:"capabilities"`
					} `json:"result"`
				}
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
				require.Equal(t, "2.0", envelope.JSONRPC)
				require.Equal(t, 1, envelope.ID)
				require.Empty(t, envelope.Error)
				require.Equal(t, "complete", envelope.Result.ResultType)
				require.Contains(t, envelope.Result.SupportedVersions, contract.ModernProtocolVersion)
				require.JSONEq(t, `{}`, string(envelope.Result.Capabilities["tools"]))
				require.Equal(t, host, request.Host)
			})
			t.Run("legacy initialization and discovery", func(t *testing.T) {
				request := loopbackRequest(legacyRequest(http.MethodPost, legacyInitialize, "valid", ""), host)
				response := httptest.NewRecorder()
				boundary.ServeHTTP(response, request)
				require.Equal(t, http.StatusOK, response.Code, response.Body.String())
				require.JSONEq(t, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"mcp-gateway","version":"s1"}}}`, response.Body.String())
				session := response.Header().Get("Mcp-Session-Id")
				require.NotEmpty(t, session)
				request = loopbackRequest(legacyRequest(http.MethodPost, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, "valid", session), host)
				response = httptest.NewRecorder()
				boundary.ServeHTTP(response, request)
				require.Equal(t, http.StatusOK, response.Code, response.Body.String())
				require.JSONEq(t, `{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"fixture.read","inputSchema":{"type":"object"}}]}}`, response.Body.String())
				require.Equal(t, host, request.Host)
			})
		})
	}
}

func TestAllowedHostPreservesEarlyBoundaryAndAuthentication(t *testing.T) {
	authority := newTestAuthority(t)
	authority.add(t, "valid", contract.VisibilityRequestable)
	ingress := New(Options{Authenticator: authority})
	t.Cleanup(ingress.Shutdown)
	authCalls := 0
	boundary, err := httpboundary.New(httpboundary.Options{
		Authority: contract.DefaultAuthority, AllowedHosts: []string{"host.docker.internal"},
		Authenticate: func(ctx context.Context, r *http.Request, domain contract.CredentialAuthority) (context.Context, error) {
			authCalls++
			return ingress.Authenticate(ctx, r, domain)
		},
		Next: ingress,
	})
	require.NoError(t, err)
	for _, test := range []struct {
		name, host, header, value, bearer string
		status, authentications           int
	}{
		{name: "unlisted", host: "evil.example:8210", status: 421},
		{name: "subdomain", host: "sub.host.docker.internal:8210", status: 421},
		{name: "lookalike", host: "host.docker.internal.evil:8210", status: 421},
		{name: "trailing dot", host: "host.docker.internal.:8210", status: 421},
		{name: "malformed port", host: "host.docker.internal:bad", status: 421},
		{name: "origin", header: "Origin", value: "http://host.docker.internal:8210", status: 403},
		{name: "forwarded", header: "Forwarded", value: "host=127.0.0.1:8210", status: 400},
		{name: "x forwarded", header: "X-Forwarded-Host", value: "127.0.0.1:8210", status: 400},
		{name: "absent credential", status: 401, authentications: 1},
		{name: "invalid credential", bearer: contract.AgentBearerPrefix + "unknown", status: 401, authentications: 1},
		{name: "admin credential", bearer: contract.AdminBearerPrefix + "fixture", status: 403, authentications: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := test.host
			if host == "" {
				host = "host.docker.internal:8210"
			}
			body := &readSpy{}
			request := loopbackRequest(httptest.NewRequest(http.MethodPost, "/mcp", body), host)
			if test.header != "" {
				request.Header.Set(test.header, test.value)
			}
			if test.bearer != "" {
				request.Header.Set("Authorization", "Bearer "+test.bearer)
			}
			before := authCalls
			response := httptest.NewRecorder()
			boundary.ServeHTTP(response, request)
			require.Equal(t, test.status, response.Code, response.Body.String())
			require.Equal(t, test.authentications, authCalls-before)
			require.Zero(t, body.reads.Load())
		})
	}
}
