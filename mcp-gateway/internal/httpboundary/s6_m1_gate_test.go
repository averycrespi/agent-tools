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

func TestS6M1Gate(t *testing.T) {
	var authorities []contract.CredentialAuthority
	boundary, err := New(Options{
		Authority: contract.DefaultAuthority,
		Authenticate: func(ctx context.Context, _ *http.Request, authority contract.CredentialAuthority) (context.Context, error) {
			authorities = append(authorities, authority)
			return ctx, nil
		},
		Next: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) }),
	})
	require.NoError(t, err)

	wrongHost := httptest.NewRequest(http.MethodPost, "/api/v1/events", http.NoBody)
	wrongHost.Host = "localhost:8210"
	wrongHostResponse := httptest.NewRecorder()
	boundary.ServeHTTP(wrongHostResponse, wrongHost)
	assert.Equal(t, http.StatusMisdirectedRequest, wrongHostResponse.Code)
	assert.Empty(t, authorities)

	for _, test := range []struct {
		method    string
		path      string
		authority contract.CredentialAuthority
	}{
		{method: http.MethodPost, path: "/api/v1/admin-sessions/current", authority: contract.AuthorityAdminSession},
		{method: http.MethodGet, path: "/api/v1/events", authority: contract.AuthorityAdmin},
		{method: http.MethodPost, path: "/api/v1/events", authority: contract.AuthorityAdminSession},
	} {
		request := httptest.NewRequest(test.method, test.path, http.NoBody)
		request.Host = contract.DefaultAuthority
		response := httptest.NewRecorder()
		boundary.ServeHTTP(response, request)
		assert.Equal(t, http.StatusNoContent, response.Code)
		assert.Equal(t, test.authority, authorities[len(authorities)-1])
	}
}
