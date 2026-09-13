package api

import (
	"net/http"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpboundary"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionCookieMigrationBootstrap(t *testing.T) {
	for _, test := range []struct {
		name, cookies   string
		status, lookups int
		canonicalExpiry bool
	}{
		{"old only even with a live value", "mcp_gateway_session=" + canonicalSessionValue, 401, 0, false},
		{"new only", "agent_gateway_session=" + canonicalSessionValue, 200, 1, false},
		{"mixed new wins", "mcp_gateway_session=stale; agent_gateway_session=" + canonicalSessionValue, 200, 1, false},
		{"mixed cannot fall back to old", "mcp_gateway_session=" + canonicalSessionValue + "; agent_gateway_session=bad", 401, 0, true},
		{"duplicate old is not authority", "mcp_gateway_session=a; mcp_gateway_session=b; agent_gateway_session=" + canonicalSessionValue, 200, 1, false},
		{"duplicate canonical rejected", "mcp_gateway_session=old; agent_gateway_session=" + canonicalSessionValue + "; agent_gateway_session=" + canonicalSessionValue, 400, 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			sessions := new(bootstrapSessions)
			handler := New(Options{Credentials: &fakeCredentials{items: []contract.AdminCredential{credential()}}, Sessions: sessions})
			boundary, err := httpboundary.New(httpboundary.Options{Authenticate: handler.Authenticate, Next: handler})
			require.NoError(t, err)
			response := perform(boundary, http.MethodPost, "/api/v2/admin-sessions/current", `{}`, map[string]string{
				"Origin": contract.CanonicalOrigin, "Cookie": test.cookies, "Content-Type": contract.MediaTypeJSON,
			})
			assert.Equal(t, test.status, response.Code)
			assert.Equal(t, test.lookups, sessions.calls)
			assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
			cookies := response.Result().Cookies()
			oldPresent := false
			for _, cookie := range cookies {
				if cookie.Name == "mcp_gateway_session" {
					oldPresent = true
					assertRetiredCookieScope(t, cookie)
				} else {
					assert.Equal(t, "agent_gateway_session", cookie.Name)
					assert.True(t, test.canonicalExpiry)
					assertRetiredCookieScope(t, cookie)
				}
			}
			assert.Equal(t, test.name != "new only", oldPresent)
			if test.canonicalExpiry {
				assert.Len(t, cookies, 2)
			}
		})
	}
}

func TestSessionCookieMigrationExchangeAndLogout(t *testing.T) {
	handler := newTestHandler(t)
	for _, test := range []struct {
		name, method, path, cookies, bearer, csrf, origin string
		status                                            int
	}{
		{"fresh sign in ignores old authority", "POST", "/api/v2/admin-sessions", "mcp_gateway_session=session", testBearer, "", contract.CanonicalOrigin, 201},
		{"failed sign in retires old", "POST", "/api/v2/admin-sessions", "mcp_gateway_session=session", "bad", "", contract.CanonicalOrigin, 401},
		{"old only cannot logout", "DELETE", "/api/v2/admin-sessions/current", "mcp_gateway_session=session", "", "csrf", contract.CanonicalOrigin, 401},
		{"mixed cannot bypass csrf", "DELETE", "/api/v2/admin-sessions/current", "mcp_gateway_session=session; agent_gateway_session=session", "", "wrong", contract.CanonicalOrigin, 401},
		{"mixed logout", "DELETE", "/api/v2/admin-sessions/current", "mcp_gateway_session=other; agent_gateway_session=session", "", "csrf", contract.CanonicalOrigin, 204},
		{"missing origin", "DELETE", "/api/v2/admin-sessions/current", "mcp_gateway_session=session; agent_gateway_session=session", "", "csrf", "", 403},
		{"wrong origin", "DELETE", "/api/v2/admin-sessions/current", "mcp_gateway_session=session; agent_gateway_session=session", "", "csrf", "https://example.test", 403},
		{"bearer canonical remains ambiguous", "POST", "/api/v2/admin-sessions", "mcp_gateway_session=old; agent_gateway_session=session", testBearer, "", contract.CanonicalOrigin, 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			headers := map[string]string{"Cookie": test.cookies, "X-CSRF-Token": test.csrf, "Origin": test.origin, "Content-Type": contract.MediaTypeJSON}
			if test.bearer != "" {
				headers["Authorization"] = "Bearer " + test.bearer
			}
			response := perform(handler, test.method, test.path, `{}`, headers)
			assert.Equal(t, test.status, response.Code, response.Body.String())
			cookies := response.Result().Cookies()
			if test.status == 403 {
				assert.Empty(t, cookies)
				return
			}
			require.NotEmpty(t, cookies)
			assert.Equal(t, "mcp_gateway_session", cookies[0].Name)
			assertRetiredCookieScope(t, cookies[0])
			if test.status == 201 || test.status == 204 {
				require.Len(t, cookies, 2)
				assert.Equal(t, "agent_gateway_session", cookies[1].Name)
			}
		})
	}
}

func assertRetiredCookieScope(t *testing.T, cookie *http.Cookie) {
	t.Helper()
	assert.Empty(t, cookie.Value)
	assert.Empty(t, cookie.Domain)
	assert.Equal(t, "/", cookie.Path)
	assert.True(t, cookie.HttpOnly)
	assert.False(t, cookie.Secure)
	assert.Equal(t, http.SameSiteStrictMode, cookie.SameSite)
	assert.Equal(t, -1, cookie.MaxAge)
	assert.Equal(t, int64(1), cookie.Expires.Unix())
}
