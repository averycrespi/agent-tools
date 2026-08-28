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

func TestS6PostEvents(t *testing.T) {
	authorities := make([]contract.CredentialAuthority, 0, 2)
	boundary, err := New(Options{
		Authority: contract.DefaultAuthority,
		Authenticate: func(ctx context.Context, _ *http.Request, authority contract.CredentialAuthority) (context.Context, error) {
			authorities = append(authorities, authority)
			return ctx, nil
		},
		Next: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) }),
	})
	require.NoError(t, err)

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		request := httptest.NewRequest(method, "/api/v1/events", http.NoBody)
		request.Host = contract.DefaultAuthority
		response := httptest.NewRecorder()
		boundary.ServeHTTP(response, request)
		assert.Equal(t, http.StatusNoContent, response.Code)
	}
	assert.Equal(t, []contract.CredentialAuthority{contract.AuthorityAdmin, contract.AuthorityAdminSession}, authorities)

	unsupported := httptest.NewRequest(http.MethodPut, "/api/v1/events", http.NoBody)
	unsupported.Host = contract.DefaultAuthority
	response := httptest.NewRecorder()
	boundary.ServeHTTP(response, unsupported)
	assert.Equal(t, http.StatusMethodNotAllowed, response.Code)
	assert.Equal(t, "GET, POST", response.Header().Get("Allow"))
}
