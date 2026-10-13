package httpboundary

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestBrowserOriginConfiguration(t *testing.T) {
	for _, bind := range []string{"127.0.0.1:8210", "0.0.0.0:8210", "10.0.0.2:8210"} {
		for _, origin := range []string{"https://gateway.example", "https://gateway.example:8443"} {
			got, err := BrowserOrigin(bind, origin)
			require.NoError(t, err)
			require.Equal(t, origin, got)
		}
	}
	got, err := BrowserOrigin("127.0.0.2:9000", "")
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.2:9000", got)
	for _, origin := range []string{"", "http://gateway.example", "https://Gateway.example", "https://gateway.example/", "https://user@gateway.example", "https://gateway.example?", "https://gateway.example#", "https://gateway.example:443", "https://gateway.example:08443", "https://gateway.example:0", "https://gateway.example:65536", "https://gateway.example:", "https://*.example", "https://gateway.example/path", "https://gateway.example?x=1", "https://gateway.example#x"} {
		_, err := BrowserOrigin("0.0.0.0:8210", origin)
		require.Error(t, err, origin)
	}
}

func TestExternalBoundaryTrustAndLocalAccess(t *testing.T) {
	const origin = "https://gateway.example:8443"
	calls := 0
	boundary, err := New(Options{Authority: "0.0.0.0:8210", ExternalOrigin: origin, AllowedHosts: []string{"inside.example", "gateway.example"}, Ready: func() bool { return true },
		Authenticate: func(ctx context.Context, r *http.Request, _ contract.CredentialAuthority) (context.Context, error) {
			calls++
			if r.Header.Get("Authorization") != "Bearer fixture" {
				return ctx, Error{Code: contract.ProblemAuthenticationRequired}
			}
			return ctx, nil
		}, Next: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })})
	require.NoError(t, err)
	for _, tc := range []struct {
		name, host, path, origin, cookie, forwarding, bearer string
		status                                               int
	}{
		{name: "public", host: "gateway.example:8443", path: "/api/v2/system-status", origin: origin, bearer: "fixture", status: 204},
		{name: "public missing port", host: "gateway.example", path: "/api/v2/system-status", bearer: "fixture", status: 421},
		{name: "public wrong port", host: "gateway.example:8444", path: "/api/v2/system-status", bearer: "fixture", status: 421},
		{name: "wrong origin", host: "gateway.example:8443", path: "/api/v2/system-status", origin: "https://gateway.example", bearer: "fixture", status: 403},
		{name: "forwarded", host: "gateway.example:8443", path: "/api/v2/system-status", forwarding: "host=gateway.example:8443;proto=https", bearer: "fixture", status: 400},
		{name: "spoofed host", host: "evil.example", path: "/api/v2/system-status", forwarding: "host=gateway.example:8443", bearer: "fixture", status: 421},
		{name: "local health", host: "127.0.0.1:8210", path: "/readyz", status: 200},
		{name: "local cli", host: "127.0.0.1:8210", path: "/api/v2/system-status", bearer: "fixture", status: 204},
		{name: "local unauthenticated", host: "127.0.0.1:8210", path: "/api/v2/system-status", status: 401},
		{name: "local browser", host: "127.0.0.1:8210", path: "/api/v2/system-status", cookie: "agent_gateway_session=fixture", bearer: "fixture", status: 421},
		{name: "local sign in", host: "127.0.0.1:8210", path: "/api/v2/admin-sessions", origin: origin, bearer: "fixture", status: 421},
		{name: "wildcard not host", host: "0.0.0.0:8210", path: "/readyz", status: 421},
		{name: "local wrong port", host: "127.0.0.1:8211", path: "/readyz", status: 421},
		{name: "alias API", host: "inside.example", path: "/api/v2/system-status", bearer: "fixture", status: 204},
		{name: "alias API origin", host: "inside.example", path: "/api/v2/system-status", origin: "http://inside.example", bearer: "fixture", status: 421},
		{name: "alias MCP", host: "inside.example:1234", path: "/mcp", bearer: "fixture", status: 204},
		{name: "alias origin", host: "inside.example:1234", path: "/mcp", origin: "https://inside.example:1234", bearer: "fixture", status: 421},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := calls
			r := httptest.NewRequest("GET", tc.path, nil)
			r.Host = tc.host
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.cookie != "" {
				r.Header.Set("Cookie", tc.cookie)
			}
			if tc.forwarding != "" {
				r.Header.Set("Forwarded", tc.forwarding)
			}
			if tc.bearer != "" {
				r.Header.Set("Authorization", "Bearer "+tc.bearer)
			}
			w := httptest.NewRecorder()
			boundary.ServeHTTP(w, r)
			require.Equal(t, tc.status, w.Code, w.Body.String())
			require.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
			if tc.status == 400 || tc.status == 403 || tc.status == 421 {
				require.Equal(t, before, calls)
			}
		})
	}
	r := httptest.NewRequest("POST", "/api/v2/admin-sessions", nil)
	r.Host = "gateway.example:8443"
	r.Header["Origin"] = []string{origin, origin}
	w := httptest.NewRecorder()
	boundary.ServeHTTP(w, r)
	require.Equal(t, 403, w.Code)
}
