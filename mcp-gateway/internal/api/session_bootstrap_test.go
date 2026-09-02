package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/httpboundary"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const canonicalSessionValue = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

type bootstrapSessions struct{ calls int }

func (*bootstrapSessions) Exchange(context.Context, string) (admin.CreatedSession, error) {
	return admin.CreatedSession{}, admin.ErrAuthenticationRequired
}
func (sessions *bootstrapSessions) Bootstrap(_ context.Context, session string) (admin.CreatedSession, error) {
	sessions.calls++
	if session != canonicalSessionValue {
		return admin.CreatedSession{}, admin.ErrAuthenticationRequired
	}
	return admin.CreatedSession{
		ID: session, CSRFToken: "csrf", IdleExpiresAt: time.Date(2026, 8, 28, 16, 30, 0, 0, time.UTC),
		AbsoluteExpiresAt: time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC),
	}, nil
}
func (*bootstrapSessions) Authenticate(context.Context, string, string, string, bool) (contract.AdminCredential, error) {
	return contract.AdminCredential{}, admin.ErrAuthenticationRequired
}
func (*bootstrapSessions) Logout(string) error { return nil }
func (*bootstrapSessions) Subscribe(string) (<-chan struct{}, error) {
	return nil, admin.ErrAuthenticationRequired
}
func (*bootstrapSessions) Status() contract.LimitStatus { return contract.LimitStatus{Limit: 128} }

func TestAdminSessionBootstrapAPI(t *testing.T) {
	sessions := new(bootstrapSessions)
	handler := New(Options{Credentials: &fakeCredentials{items: []contract.AdminCredential{credential()}}, Sessions: sessions})
	boundary, err := httpboundary.New(httpboundary.Options{Authority: contract.DefaultAuthority, Authenticate: handler.Authenticate, Next: handler})
	require.NoError(t, err)

	cookie := contract.SessionCookieName + "=" + canonicalSessionValue
	expiry := func(responseHeader http.Header) {
		value := responseHeader.Get("Set-Cookie")
		assert.Contains(t, value, contract.SessionCookieName+"=")
		assert.Contains(t, value, "Path=/")
		assert.Contains(t, value, "Max-Age=0")
		assert.Contains(t, value, "HttpOnly")
		assert.Contains(t, value, "SameSite=Strict")
		assert.NotContains(t, value, "Domain=")
		assert.Contains(t, value, "Expires=")
	}

	missingOrigin := perform(boundary, http.MethodPost, "/api/v1/admin-sessions/current", `{}`, map[string]string{"Cookie": cookie + "; " + cookie, "Content-Type": contract.MediaTypeJSON})
	assert.Equal(t, http.StatusForbidden, missingOrigin.Code)
	assert.Contains(t, missingOrigin.Body.String(), "forbidden_origin")
	assert.Empty(t, missingOrigin.Header().Get("Set-Cookie"))
	assert.Zero(t, sessions.calls)

	ambiguous := perform(boundary, http.MethodPost, "/api/v1/admin-sessions/current", `{}`, map[string]string{"Origin": contract.CanonicalOrigin, "Authorization": "anything", "Cookie": contract.SessionCookieName + "=bad", "Content-Type": contract.MediaTypeJSON})
	assert.Equal(t, http.StatusBadRequest, ambiguous.Code)
	assert.Contains(t, ambiguous.Body.String(), "ambiguous_credentials")
	assert.Empty(t, ambiguous.Header().Get("Set-Cookie"))
	assert.Zero(t, sessions.calls)

	for name, headers := range map[string]map[string]string{
		"authorization only": {"Origin": contract.CanonicalOrigin, "Authorization": "Bearer anything", "Content-Type": contract.MediaTypeJSON},
		"no credentials":     {"Origin": contract.CanonicalOrigin, "Content-Type": contract.MediaTypeJSON},
	} {
		t.Run(name, func(t *testing.T) {
			response := perform(boundary, http.MethodPost, "/api/v1/admin-sessions/current", `{}`, headers)
			assert.Equal(t, http.StatusUnauthorized, response.Code)
			assert.Empty(t, response.Header().Get("Set-Cookie"))
		})
	}

	duplicate := perform(boundary, http.MethodPost, "/api/v1/admin-sessions/current", `{}`, map[string]string{"Origin": contract.CanonicalOrigin, "Cookie": cookie + "; " + cookie, "Content-Type": contract.MediaTypeJSON})
	assert.Equal(t, http.StatusBadRequest, duplicate.Code)
	expiry(duplicate.Header())
	assert.Zero(t, sessions.calls)

	for name, value := range map[string]string{"empty": "", "malformed": "short", "noncanonical": canonicalSessionValue + "="} {
		t.Run(name, func(t *testing.T) {
			response := perform(boundary, http.MethodPost, "/api/v1/admin-sessions/current", `{}`, map[string]string{"Origin": contract.CanonicalOrigin, "Cookie": contract.SessionCookieName + "=" + value, "Content-Type": contract.MediaTypeJSON})
			assert.Equal(t, http.StatusUnauthorized, response.Code)
			expiry(response.Header())
		})
	}
	assert.Zero(t, sessions.calls)

	unknown := "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE"
	unknownResponse := perform(boundary, http.MethodPost, "/api/v1/admin-sessions/current", `{}`, map[string]string{"Origin": contract.CanonicalOrigin, "Cookie": contract.SessionCookieName + "=" + unknown, "Content-Type": contract.MediaTypeJSON})
	assert.Equal(t, http.StatusUnauthorized, unknownResponse.Code)
	expiry(unknownResponse.Header())
	assert.Equal(t, 1, sessions.calls)

	current := perform(boundary, http.MethodPost, "/api/v1/admin-sessions/current", `{}`, map[string]string{"Origin": contract.CanonicalOrigin, "Cookie": cookie, "Content-Type": contract.MediaTypeJSON})
	assert.Equal(t, http.StatusOK, current.Code)
	assert.JSONEq(t, `{"csrf_token":"csrf","idle_expires_at":"2026-08-28T16:30:00Z","absolute_expires_at":"2026-08-29T00:00:00Z"}`, current.Body.String())
	assert.Empty(t, current.Header().Get("Set-Cookie"))
	assert.Equal(t, 2, sessions.calls)
}
