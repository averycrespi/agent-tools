package httpboundary

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAllowedHostsUseEveryNormalRoute(t *testing.T) {
	hosts := []string{"HOST.LIMA.INTERNAL", "container.internal"}
	lookups := 0
	var authenticated contract.CredentialAuthority
	boundary, err := New(Options{
		AllowedHosts: hosts, Ready: func() bool { return true },
		Authenticate: func(ctx context.Context, _ *http.Request, authority contract.CredentialAuthority) (context.Context, error) {
			lookups++
			authenticated = authority
			return ctx, nil
		},
		Next: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.NotEqual(t, contract.DefaultAuthority, r.Host, "Host must not be rewritten")
			w.WriteHeader(http.StatusNoContent)
		}),
	})
	require.NoError(t, err)
	hosts[0] = "unlisted.internal"
	for _, route := range contract.Routes() {
		path := strings.ReplaceAll(route.Pattern, "*", "app.js")
		for _, method := range route.Methods {
			for _, host := range []string{"host.lima.internal", "Host.Lima.Internal:1", "host.lima.internal:65535", "host.lima.internal:08211", "container.internal:8211"} {
				r := httptest.NewRequest(method, path, http.NoBody)
				r.Host = host
				before := lookups
				w := httptest.NewRecorder()
				boundary.ServeHTTP(w, r)
				want := http.StatusNoContent
				if path == "/livez" || path == "/readyz" {
					want = http.StatusOK
				}
				require.Equal(t, want, w.Code, "%s %s %s: %s", method, path, host, w.Body.String())
				authority := contract.AuthorityForMethod(route, method)
				if requiresAuthentication(authority) {
					assert.Equal(t, before+1, lookups)
					assert.Equal(t, authority, authenticated)
				} else {
					assert.Equal(t, before, lookups)
				}
				assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
			}
		}
	}
}

func TestAllowedHostsRejectBeforeAuthenticationAndBody(t *testing.T) {
	boundary, err := New(Options{AllowedHosts: []string{"host.lima.internal"}, Authenticate: func(context.Context, *http.Request, contract.CredentialAuthority) (context.Context, error) {
		t.Fatal("early rejection reached authentication")
		return nil, nil
	}})
	require.NoError(t, err)
	for _, host := range []string{"", "evil.internal", "host.lima.internal.evil", "sub.host.lima.internal", "host.lima.internal.", "host.lima.internal:", "host.lima.internal:0", "host.lima.internal:65536", "host.lima.internal:+1", "host.lima.internal:-1", "host.lima.internal: 1", "host.lima.internal:one", "host.lima.internal:1:2", "[host.lima.internal]:1", "user@host.lima.internal", "host.lima.internal/path", "host.lima.internal\n", "127.0.0.1:8211"} {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/admin-sessions", http.NoBody)
		r.Host = host
		w := httptest.NewRecorder()
		boundary.ServeHTTP(w, r)
		assert.Equal(t, http.StatusMisdirectedRequest, w.Code, "%q", host)
	}
	for _, test := range []struct {
		header, value string
		status        int
	}{
		{"Forwarded", "host=host.lima.internal", 400}, {"X-Forwarded-Host", "host.lima.internal", 400},
		{"Origin", "http://host.lima.internal:8210", 403}, {"Origin", "http://127.0.0.1:8211", 403},
	} {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/admin-sessions", http.NoBody)
		r.Host = "host.lima.internal:8211"
		r.Header.Set(test.header, test.value)
		w := httptest.NewRecorder()
		boundary.ServeHTTP(w, r)
		assert.Equal(t, test.status, w.Code)
		assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
	}
	for _, test := range []struct {
		method, path string
		status       int
	}{
		{http.MethodPut, "/mcp", 405}, {http.MethodGet, "/missing", 404},
	} {
		r := httptest.NewRequest(test.method, test.path, http.NoBody)
		r.Host = "host.lima.internal:8211"
		w := httptest.NewRecorder()
		boundary.ServeHTTP(w, r)
		assert.Equal(t, test.status, w.Code)
	}
}

func TestAllowedHostsConfigurationAndDefault(t *testing.T) {
	for _, host := range []string{"", "http://host", "host:8210", "host/path", "*.example", "bad..host", "host\r\n", "127.0.0.1"} {
		_, err := New(Options{AllowedHosts: []string{host}})
		assert.Error(t, err, "%q", host)
	}
	boundary, err := New(Options{})
	require.NoError(t, err)
	assert.True(t, boundary.acceptsHost(contract.DefaultAuthority))
	assert.False(t, boundary.acceptsHost("host.lima.internal:8210"))
	assert.False(t, boundary.acceptsHost("localhost:8210"))
}
