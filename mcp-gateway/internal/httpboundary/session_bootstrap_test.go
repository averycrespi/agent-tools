package httpboundary

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdminSessionBootstrapBoundary(t *testing.T) {
	lookups := 0
	boundary, err := New(Options{
		Authority: contract.DefaultAuthority,
		Authenticate: func(ctx context.Context, request *http.Request, authority contract.CredentialAuthority) (context.Context, error) {
			lookups++
			if request.Header.Get("Origin") == "" {
				return ctx, Error{Code: contract.ProblemForbiddenOrigin}
			}
			return ctx, Error{Code: contract.ProblemAuthenticationRequired, SetCookie: "mcp_gateway_session=; Path=/; Max-Age=0; HttpOnly; SameSite=Strict"}
		},
		Next: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected handler call") }),
	})
	require.NoError(t, err)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin-sessions/current", http.NoBody)
	request.Host = contract.DefaultAuthority
	request.Header.Set("Origin", contract.CanonicalOrigin)
	response := httptest.NewRecorder()
	boundary.ServeHTTP(response, request)
	assert.Equal(t, http.StatusUnauthorized, response.Code)
	assert.Equal(t, 1, lookups)
	assert.Equal(t, "mcp_gateway_session=; Path=/; Max-Age=0; HttpOnly; SameSite=Strict", response.Header().Get("Set-Cookie"))

	missingOrigin := httptest.NewRequest(http.MethodPost, "/api/v1/admin-sessions/current", http.NoBody)
	missingOrigin.Host = contract.DefaultAuthority
	missingResponse := httptest.NewRecorder()
	boundary.ServeHTTP(missingResponse, missingOrigin)
	assert.Equal(t, http.StatusForbidden, missingResponse.Code)
	assert.Equal(t, 2, lookups)
}
