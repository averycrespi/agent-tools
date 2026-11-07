package api

import (
	"net/http"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestMCPGrantCreateAcceptsOptionalKeyWithoutChangingIDRoutes(t *testing.T) {
	service := &fakePrincipalService{}
	handler := newGrantHandler(t, service, allowGrantTarget, nil)
	headers := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON}
	const body = `"description":null,"principal_id":"01ARZ3NDEKTSV4RRFFQ69G5FAV","effect":"allow","server_id":"00000000000000000000000000","upstream_name":null,"constraint":null,"expires_at":null}`
	for _, key := range []string{`null`, `""`, `"Upper"`, `42`} {
		response := perform(handler, http.MethodPost, "/api/v2/mcp/grants", `{"key":`+key+`,`+body, headers)
		require.Equal(t, 400, response.Code, response.Body.String())
	}
	response := perform(handler, http.MethodPost, "/api/v2/mcp/grants", `{"key":"build-access",`+body, headers)
	require.Equal(t, 201, response.Code, response.Body.String())
	require.Equal(t, "build-access", service.grantCreate.Key)
	response = perform(handler, http.MethodPost, "/api/v2/mcp/grants", `{`+body, headers)
	require.Equal(t, 201, response.Code, response.Body.String())
	require.Empty(t, service.grantCreate.Key, "legacy omission reaches domain generation")
}

func TestPrincipalCreateKeyValidationIsClosedAndOptional(t *testing.T) {
	service := &fakePrincipalService{}
	handler := newPrincipalHandler(t, service, nil)
	headers := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON}
	for _, key := range []string{`null`, `""`, `"Upper"`, `42`} {
		response := perform(handler, http.MethodPost, "/api/v2/principals", `{"key":`+key+`,"display_name":"Agent","visibility":"all"}`, headers)
		require.Equal(t, 400, response.Code)
	}
	response := perform(handler, http.MethodPost, "/api/v2/principals", `{"key":"build-agent","display_name":"Agent","visibility":"all"}`, headers)
	require.Equal(t, 201, response.Code)
	require.Equal(t, "build-agent", service.create.Key)
}
