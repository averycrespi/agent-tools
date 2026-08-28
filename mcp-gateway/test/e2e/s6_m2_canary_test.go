//go:build e2e

package e2e

import (
	"net/http"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS6E2EM2Canary(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()

	response := harness.adminSnapshot(http.MethodGet, "/api/v1/invocations?limit=1", nil)
	var page contract.InvocationPage
	decodeSnapshot(t, response, http.StatusOK, &page)
	assert.Empty(t, page.Items)
	assert.Nil(t, page.NextCursor)
	assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	assert.Equal(t, contract.MediaTypeJSON, response.Header.Get("Content-Type"))
	assert.Empty(t, response.Header.Get("Access-Control-Allow-Origin"))

	method := harness.adminSnapshot(http.MethodPost, "/api/v1/invocations", nil)
	require.Equal(t, http.StatusMethodNotAllowed, method.StatusCode, string(method.Body))
	assert.Equal(t, http.MethodGet, method.Header.Get("Allow"))
	missing := harness.adminSnapshot(http.MethodGet, "/api/v1/invocations/01ARZ3NDEKTSV4RRFFQ69G5FAV/replay", nil)
	assertProblem(t, missing, http.StatusNotFound, "not_found", "The resource was not found.", false)

	harness.Stop(syscall.SIGTERM)
}
